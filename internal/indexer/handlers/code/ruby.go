package code

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"

	"librarian/internal/indexer"
)

// RubyGrammar indexes .rb source files.
//
// Symbol extraction:
//   - class declarations → class Unit; hybrid container so methods inside
//     the class body are separate method Units.
//   - module declarations → class Unit (modules are namespace/mixin
//     containers that share the class-family role in Ruby's object model);
//     also hybrid container.
//   - def (method) → method Unit when inside a class/module body; function
//     Unit at the top level.
//   - def self.foo (singleton_method) → method Unit.
//   - attr_accessor / attr_reader / attr_writer calls inside a class/module
//     body → field Units (see PostProcess for per-attribute expansion of
//     multi-symbol calls like `attr_accessor :name, :age`).
//
// Imports: require, require_relative, and load call nodes at any nesting
// level are collected. require_relative paths are stored with a "relative:"
// prefix so the ResolveImports hook can rewrite them to absolute file paths
// anchored against the source file's directory.
//
// Inheritance:
//   - class Dog < Animal → inherits ref with Relation="extends".
//   - include/extend/prepend calls inside a class/module → inherits refs
//     with Relation="mixes".
//
// Comments: Ruby uses only the # single-line comment form.
type RubyGrammar struct{}

// NewRubyGrammar returns the Ruby grammar implementation.
func NewRubyGrammar() *RubyGrammar { return &RubyGrammar{} }

func (*RubyGrammar) Name() string               { return "ruby" }
func (*RubyGrammar) Extensions() []string       { return []string{".rb"} }
func (*RubyGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_ruby.Language()) }
func (*RubyGrammar) CommentNodeTypes() []string { return []string{"comment"} }
func (*RubyGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Ruby AST node types to generic Unit.Kind values. `call`
// is included so attr_* declarations can be detected inside class bodies;
// SymbolName returns "" for non-attr_* calls to suppress other call nodes.
func (*RubyGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"class":            "class",
		"module":           "class",
		"method":           "method",
		"singleton_method": "method",
		"call":             "field",
	}
}

// ContainerKinds lists nodes the walker descends into.
//
//   - class and module are hybrid (emit a Unit AND descend; methods inside
//     become "method" because containerKind="class" triggers the rewrite).
//   - body_statement is the pure body wrapper for class, module, method,
//     if, etc. — no Unit emitted, path stays the same.
func (*RubyGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"class":          true, // hybrid
		"module":         true, // hybrid
		"body_statement": true, // pure
	}
}

// SymbolName returns the declaration name for a symbol node.
//
// Nodes only present in ContainerKinds (body_statement) return "" so the
// path prefix is not extended through the body. call nodes only emit a Unit
// when the method name is attr_accessor / attr_reader / attr_writer; other
// call nodes return "" (skipped by the walker).
func (*RubyGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "class", "module":
		// The "name" field can be "constant" (Dog) or "scope_resolution"
		// (Pets::Dog). Using ChildByFieldName handles both forms and matches
		// the convention of every other grammar in the codebase.
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "method", "singleton_method":
		// The "name" field covers all valid Ruby method name forms: plain
		// identifier, operator (==, [], []=, <=>), setter (foo=), and
		// constants. Iterating for "identifier" would silently drop operator
		// and setter methods.
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "call":
		// Emit a Unit only for attr_* macro calls inside class/module bodies.
		// The first attr symbol becomes the Unit title; PostProcess expands
		// any additional symbols into extra field Units.
		method := rubyCallMethod(n, source)
		if method != "attr_accessor" && method != "attr_reader" && method != "attr_writer" {
			return ""
		}
		return rubyFirstAttrSymbol(n, source)
	}
	return ""
}

// PackageName returns "" — Ruby files have no package clause. The shared
// handler falls back to the file stem as the Unit.Path prefix.
func (*RubyGrammar) PackageName(*sitter.Node, []byte) string { return "" }

// Imports collects require, require_relative, and load calls anywhere in the
// file. require_relative targets carry the raw relative path so the
// ResolveImports hook can anchor them against the file's directory.
func (*RubyGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "call" {
			return true
		}
		method := rubyCallMethod(n, source)
		switch method {
		case "require", "load":
			if path := rubyCallStringArg(n, source); path != "" {
				out = append(out, ImportRef{Path: path})
			}
		case "require_relative":
			if path := rubyCallStringArg(n, source); path != "" {
				// Prefix signals relative path to the resolver.
				out = append(out, ImportRef{Path: "relative:" + path})
			}
		}
		return true
	})
	return out
}

// SymbolParents implements inheritanceExtractor. Handles two inheritance
// patterns:
//
//  1. class Dog < Animal — the "superclass" field of a class node.
//  2. include/extend/prepend calls inside the class/module body.
//
// Both produce inherits references. For (1) the Relation is "extends"; for
// (2) it is "mixes" (matching the Ruby mixin conventions and the edge
// flavors documented in docs/storage.md).
func (*RubyGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	switch n.Kind() {
	case "class":
		return rubyClassParents(n, source)
	case "module":
		return rubyModuleParents(n, source)
	}
	return nil
}

// rubyClassParents extracts parent refs from a Ruby class node: the optional
// < Superclass clause plus any include/extend/prepend calls in the body.
func rubyClassParents(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef

	// Superclass: `class Dog < Animal` — access via the named "superclass"
	// field rather than iterating named children, consistent with ChildByFieldName
	// usage throughout the codebase.
	if sc := n.ChildByFieldName("superclass"); sc != nil {
		if name := rubyConstantName(sc, source); name != "" {
			out = append(out, ParentRef{
				Name:     name,
				Relation: "extends",
				Loc:      nodeLocation(sc),
			})
		}
	}

	// Mixins from the class body.
	out = append(out, rubyMixinParents(n, source)...)
	return out
}

// rubyModuleParents extracts include/extend/prepend mixin calls from a module
// body. Modules do not have superclasses.
func rubyModuleParents(n *sitter.Node, source []byte) []ParentRef {
	return rubyMixinParents(n, source)
}

// rubyMixinParents walks the body_statement of a class or module node and
// collects include/extend/prepend call nodes, emitting one ParentRef per
// constant argument. Uses ChildByFieldName("body") for robust field access.
func rubyMixinParents(n *sitter.Node, source []byte) []ParentRef {
	body := n.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []ParentRef
	for j := uint(0); j < body.NamedChildCount(); j++ {
		call := body.NamedChild(j)
		if call == nil || call.Kind() != "call" {
			continue
		}
		method := rubyCallMethod(call, source)
		if method != "include" && method != "extend" && method != "prepend" {
			continue
		}
		args := call.ChildByFieldName("arguments")
		if args == nil {
			continue
		}
		for k := uint(0); k < args.NamedChildCount(); k++ {
			arg := args.NamedChild(k)
			if arg == nil {
				continue
			}
			name := rubyConstantName(arg, source)
			if name == "" {
				continue
			}
			out = append(out, ParentRef{
				Name:     name,
				Relation: "mixes",
				Loc:      nodeLocation(arg),
			})
		}
	}
	return out
}

// PostProcess implements parsedDocPostProcessor to expand multi-symbol
// attr_* calls into individual field Units. A call like
// `attr_accessor :name, :age` produces a single Unit from SymbolName (for
// ":name") but the remaining symbols (:age, etc.) need separate Units.
// PostProcess walks the AST, detects attr_* calls with >1 symbol, and
// appends the extra field Units to doc.Units using the same path prefix as
// the Unit already emitted.
func (*RubyGrammar) PostProcess(doc *indexer.ParsedDoc, root *sitter.Node, source []byte) {
	// Build a set of existing field Unit paths so we can match the first
	// symbol (already emitted by SymbolName) and derive the class prefix.
	unitPathSet := make(map[string]bool, len(doc.Units))
	for _, u := range doc.Units {
		unitPathSet[u.Path] = true
	}

	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "call" {
			return true
		}
		method := rubyCallMethod(n, source)
		if method != "attr_accessor" && method != "attr_reader" && method != "attr_writer" {
			return true
		}
		args := n.ChildByFieldName("arguments")
		if args == nil {
			return false
		}
		// Collect all :symbol arguments.
		var symbols []struct {
			name string
			node *sitter.Node
		}
		for i := uint(0); i < args.NamedChildCount(); i++ {
			arg := args.NamedChild(i)
			if arg == nil {
				continue
			}
			sym := rubySymbolText(arg, source)
			if sym == "" {
				continue
			}
			symbols = append(symbols, struct {
				name string
				node *sitter.Node
			}{sym, arg})
		}
		if len(symbols) <= 1 {
			return false // first symbol handled by SymbolName; nothing extra
		}

		// Find the class path prefix by looking for the first symbol's Unit
		// (emitted by the walker from SymbolName). If found, infer the
		// path prefix and emit extra Units for the remaining symbols.
		firstSym := symbols[0].name
		classPrefix := rubyEnclosingClassPath(n, source, doc)
		if classPrefix == "" {
			return false
		}
		firstPath := classPrefix + "." + firstSym
		if !unitPathSet[firstPath] {
			return false
		}

		for _, sym := range symbols[1:] {
			extraPath := classPrefix + "." + sym.name
			if unitPathSet[extraPath] {
				continue // avoid duplicates on class reopen
			}
			loc := nodeLocation(sym.node)
			doc.Units = append(doc.Units, indexer.Unit{
				Kind:    "field",
				Title:   sym.name,
				Path:    extraPath,
				Content: n.Utf8Text(source),
				Loc:     loc,
			})
			unitPathSet[extraPath] = true
		}
		return false
	})
}

// rubyEnclosingClassPath returns the Unit.Path of the innermost class or
// module that contains n. Returns "" if no enclosing class/module can be
// found in doc.Units.
func rubyEnclosingClassPath(n *sitter.Node, source []byte, doc *indexer.ParsedDoc) string {
	// Build a map from byte offset to Unit.Path for class/module Units.
	classUnits := map[int]string{}
	for _, u := range doc.Units {
		if u.Kind == "class" {
			classUnits[u.Loc.ByteOffset] = u.Path
		}
	}
	// Walk ancestors from closest to farthest, find innermost class/module.
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Kind() != "class" && p.Kind() != "module" {
			continue
		}
		if path, ok := classUnits[int(p.StartByte())]; ok {
			return path
		}
	}
	return ""
}

// rubyCallMethod returns the method name of a Ruby call node (the bare
// identifier before the argument list). Returns "" for method calls with a
// receiver (e.g., obj.foo — those have a non-nil "receiver" field).
func rubyCallMethod(n *sitter.Node, source []byte) string {
	// Reject calls with a receiver: `obj.include(...)` is not a mixin.
	if recv := n.ChildByFieldName("receiver"); recv != nil {
		return ""
	}
	method := n.ChildByFieldName("method")
	if method == nil {
		return ""
	}
	return method.Utf8Text(source)
}

// rubyCallStringArg returns the string content of the first string argument
// of a call node. Returns "" if the first argument is not a string.
func rubyCallStringArg(n *sitter.Node, source []byte) string {
	args := n.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil || c.Kind() != "string" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			sc := c.NamedChild(j)
			if sc != nil && sc.Kind() == "string_content" {
				return sc.Utf8Text(source)
			}
		}
	}
	return ""
}

// rubyFirstAttrSymbol returns the first :symbol argument name (without the
// leading colon) from an attr_* call node. Returns "" if no symbol found.
func rubyFirstAttrSymbol(n *sitter.Node, source []byte) string {
	args := n.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := uint(0); i < args.NamedChildCount(); i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		sym := rubySymbolText(c, source)
		if sym != "" {
			return sym
		}
	}
	return ""
}

// rubySymbolText extracts the symbol name from a simple_symbol or symbol node,
// stripping the leading colon. Returns "" for other node kinds.
func rubySymbolText(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "simple_symbol":
		return strings.TrimPrefix(n.Utf8Text(source), ":")
	case "symbol":
		// Quoted symbol — e.g., :"with-hyphen". Strip delimiters.
		text := n.Utf8Text(source)
		text = strings.TrimPrefix(text, ":")
		text = strings.Trim(text, `"'`)
		return text
	}
	return ""
}

// rubyConstantName returns the fully-qualified constant name from an AST node.
// Handles simple constants ("Animal") and scoped constants ("Mod::Animal").
// rubyConstantName returns the fully-qualified constant name from an AST node.
// Handles:
//   - "constant"       → bare name ("Animal")
//   - "scope_resolution" → qualified name ("Pets::Dog")
//   - "superclass"     → wrapper node for `< Parent`; delegates to its single
//                        named child (the constant or scope_resolution).
func rubyConstantName(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "constant":
		return n.Utf8Text(source)
	case "scope_resolution":
		return n.Utf8Text(source)
	case "superclass":
		// The superclass wrapper node (e.g., "< Animal") wraps the actual
		// parent constant as its first named child.
		return rubyConstantName(n.NamedChild(0), source)
	}
	return ""
}

// Compile-time interface checks.
var (
	_ Grammar                = (*RubyGrammar)(nil)
	_ inheritanceExtractor   = (*RubyGrammar)(nil)
	_ parsedDocPostProcessor = (*RubyGrammar)(nil)
	_ importResolver         = (*RubyGrammar)(nil)
)
