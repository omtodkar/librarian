package code

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/tree-sitter/tree-sitter-python/bindings/go"

	"librarian/internal/indexer"
)

// PythonGrammar indexes .py source files.
//
// Symbol extraction:
//   - Top-level `def` / `async def` → function Unit.
//   - `class` → class Unit AND a container whose methods emit separate Units
//     (Kind rewritten to "method" by the walker when inside a class).
//   - Methods, whether sync or async, carry Path = "module.ClassName.method".
//
// Docstrings: Python's triple-quoted string as the first statement of a
// function or class body is captured via DocstringFromNode and merged with any
// preceding `#` comments the walker buffered.
//
// Decorators: `decorated_definition` is a container so the walker descends
// through it; `decorator` is registered as a comment type so decorator text
// (e.g. `@dataclass`) lands in the Unit's docstring and stays searchable.
//
// Imports: plain (`import X`), aliased (`import X as Y`), from-imports
// (`from X import Y[, Z]` with optional `as` aliases), and relative
// (`from . import X`, `from ..pkg import X`) are all emitted as ImportRefs.
// The Path is module-dot-member for from-imports so consumers get a fully-
// qualified identifier to grep against.
type PythonGrammar struct{}

// NewPythonGrammar returns the Python grammar implementation.
func NewPythonGrammar() *PythonGrammar { return &PythonGrammar{} }

func (*PythonGrammar) Name() string               { return "python" }
func (*PythonGrammar) Extensions() []string       { return []string{".py"} }
func (*PythonGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_python.Language()) }

// CommentNodeTypes includes "decorator" alongside the real comment type. The
// walker treats decorators as comments so `@dataclass` text flows into the
// Unit's docstring buffer — useful for searches like "@dataclass" and for AI
// agents that use decorators as classification signals.
func (*PythonGrammar) CommentNodeTypes() []string { return []string{"comment", "decorator"} }

// SymbolKinds maps Python AST node types to generic Unit.Kind values. Note
// that function_definition resolves to "function" here but the walker rewrites
// it to "method" when descending into a class container — the AST doesn't
// distinguish methods from standalone functions.
//
// Type aliases come from two syntaxes: PEP 695's `type Matrix = ...` has a
// dedicated `type_alias_statement` node (Python 3.12+), while the older PEP
// 613 form `X: TypeAlias = ...` is a generic `expression_statement`. Both
// emit Kind="type" — SymbolName's expression_statement branch filters for
// the TypeAlias annotation so unrelated assignments don't produce Units.
func (*PythonGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"function_definition":  "function",
		"class_definition":     "class",
		"type_alias_statement": "type",
		"expression_statement": "type",
	}
}

// ContainerKinds lists nodes the walker descends into.
//
//   - class_definition is hybrid with SymbolKinds so the class itself emits a
//     Unit AND its methods become separate Units.
//   - block is the body node that wraps a class's inner statements; the walker
//     needs to descend through it to reach the methods. Blocks inside function
//     bodies are never reached because function_definition isn't a container.
//   - decorated_definition wraps `@decorator`s around a function or class;
//     descending through it lets the inner definition claim any preceding
//     comments and emit its Unit as normal.
func (*PythonGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"class_definition":     true,
		"block":                true,
		"decorated_definition": true,
	}
}

// SymbolName returns the identifier for function_definition / class_definition
// / type_alias_statement nodes, plus PEP-613 `X: TypeAlias = ...` expression
// statements. Every other node type (including `block` and
// `decorated_definition`, which are containers but not symbols) returns "" so
// the walker doesn't inject spurious segments into Unit.Path when descending
// through them.
func (*PythonGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "function_definition", "class_definition":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "type_alias_statement":
		// tree-sitter-python models this as two unnamed `type` children: the
		// first is the alias LHS, the second the RHS. The LHS wraps either
		// an identifier (`type Matrix = ...`) or a generic_type
		// (`type Vec[T] = ...`). Guard the `type` wrapper so a future
		// grammar change that inserts a new first named child doesn't get
		// mis-interpreted as the LHS.
		lhs := n.NamedChild(0)
		if lhs == nil || lhs.Kind() != "type" {
			return ""
		}
		return aliasIdentifier(lhs, source)
	case "expression_statement":
		// PEP 613 `X: TypeAlias = ...` — accept only when the annotation is
		// literally TypeAlias (or a dotted form ending in .TypeAlias,
		// including the string-forward-reference variant `"typing.TypeAlias"`).
		// Aliased imports (`from typing import TypeAlias as TA; X: TA = ...`)
		// are not detected because the annotation text is "TA" — acceptable
		// best-effort since PEP 613 is being phased out in favor of PEP 695.
		return pep613AliasName(n, source)
	}
	return ""
}

// pep613AliasName detects the `X: TypeAlias = VALUE` / `X: "typing.TypeAlias"
// = VALUE` form and returns X. Returns "" for any other expression_statement
// so the walker skips it. LHS must be a plain identifier — pattern_list or
// subscript LHSes (pathological for a type alias) would corrupt Unit.Path
// with embedded punctuation.
func pep613AliasName(n *sitter.Node, source []byte) string {
	if n.NamedChildCount() == 0 {
		return ""
	}
	assign := n.NamedChild(0)
	if assign == nil || assign.Kind() != "assignment" {
		return ""
	}
	ann := assign.ChildByFieldName("type")
	left := assign.ChildByFieldName("left")
	if ann == nil || left == nil || left.Kind() != "identifier" {
		return ""
	}
	annText := strings.Trim(ann.Utf8Text(source), `"' `)
	if annText != "TypeAlias" && !strings.HasSuffix(annText, ".TypeAlias") {
		return ""
	}
	return left.Utf8Text(source)
}

// aliasIdentifier returns the identifier buried inside a PEP 695 alias LHS.
// The LHS is either a bare identifier or a generic_type whose first named
// child is the identifier — so one hop max. Avoids an unbounded DFS that
// could surface a nested identifier (e.g. a type-parameter name) if the
// grammar ever adds children before the name slot.
func aliasIdentifier(lhs *sitter.Node, source []byte) string {
	if lhs == nil {
		return ""
	}
	inner := lhs.NamedChild(0)
	if inner == nil {
		return ""
	}
	if inner.Kind() == "identifier" {
		return inner.Utf8Text(source)
	}
	if name := inner.NamedChild(0); name != nil && name.Kind() == "identifier" {
		return name.Utf8Text(source)
	}
	return ""
}

// PackageName returns "" — Python files have no package clause. The shared
// code handler falls back to the file stem (basename without extension), which
// matches Python's own module-name convention (`service.py` → `service`).
func (*PythonGrammar) PackageName(*sitter.Node, []byte) string { return "" }

// DocstringFromNode implements Python's docstring convention: a bare string
// expression as the first statement of a function or class body is the
// docstring. Bytes literals (`b"..."`) are rejected — CPython doesn't treat
// them as docstrings either, and including them would corrupt Unit text with
// binary-looking content.
func (*PythonGrammar) DocstringFromNode(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "function_definition", "class_definition":
	default:
		return ""
	}
	body := n.ChildByFieldName("body")
	if body == nil || body.NamedChildCount() == 0 {
		return ""
	}
	first := body.NamedChild(0)
	if first == nil || first.Kind() != "expression_statement" || first.NamedChildCount() == 0 {
		return ""
	}
	str := first.NamedChild(0)
	if str == nil || str.Kind() != "string" {
		return ""
	}
	if isBytesStringLiteral(str, source) {
		return ""
	}
	return extractPythonStringContent(str, source)
}

// isBytesStringLiteral reports whether a `string` node is a Python bytes
// literal (prefix includes `b` / `B`). CPython rejects these as docstrings,
// and their raw bytes are neither searchable nor informative.
func isBytesStringLiteral(str *sitter.Node, source []byte) bool {
	for i := uint(0); i < str.NamedChildCount(); i++ {
		c := str.NamedChild(i)
		if c == nil || c.Kind() != "string_start" {
			continue
		}
		return strings.Contains(strings.ToLower(c.Utf8Text(source)), "b")
	}
	return false
}

// extractPythonStringContent concatenates the content-bearing children of a
// tree-sitter `string` node, skipping the `string_start` / `string_end`
// delimiters. tree-sitter splits a Python string into start-content-end (and
// interpolation nodes for f-strings); pulling just the content yields the
// user-visible text without the surrounding quote characters.
//
// f-string caveat: interpolation nodes are rendered as raw source text, so
// `f"x={y}"` yields `"x={y}"` rather than evaluated output. Uncommon as
// docstrings (PEP 257 discourages them) and acceptable for search.
func extractPythonStringContent(str *sitter.Node, source []byte) string {
	var out strings.Builder
	for i := uint(0); i < str.NamedChildCount(); i++ {
		c := str.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "string_start", "string_end":
			// Delimiters — skip.
		default:
			out.WriteString(c.Utf8Text(source))
		}
	}
	return strings.TrimSpace(out.String())
}

// Imports walks every import_statement / import_from_statement in the file
// and emits one ImportRef per imported name. From-imports produce dotted
// Path values — "collections.deque" for `from collections import deque` —
// so consumers have a fully-qualified identifier for grep/link purposes.
//
// Relative imports are emitted in raw form with leading dots preserved
// (`from . import X` → ".X"; `from ..pkg import X` → "..pkg.X"). The
// CodeHandler's ParseCtx dispatches to the Grammar's optional ResolveImports
// method — see python_resolve.go — which rewrites these to absolute dotted
// paths using the file's containing package. Callers on the legacy Parse
// path (grammar-level tests) see the raw dotted form; the production indexer
// routes through ParseCtx and sees resolved targets only.
func (*PythonGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		switch n.Kind() {
		case "import_statement":
			out = append(out, extractPlainImports(n, source)...)
			return false
		case "import_from_statement":
			out = append(out, extractFromImports(n, source)...)
			return false
		}
		return true
	})
	return out
}

// extractPlainImports handles `import X`, `import X.Y`, `import X as Y`, and
// comma-separated multi-imports `import X, Y`. Each imported name (or alias
// pair) becomes one ImportRef.
func extractPlainImports(n *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "dotted_name":
			out = append(out, ImportRef{Path: c.Utf8Text(source)})
		case "aliased_import":
			ref := ImportRef{}
			if p := c.ChildByFieldName("name"); p != nil {
				ref.Path = p.Utf8Text(source)
			}
			if a := c.ChildByFieldName("alias"); a != nil {
				ref.Alias = a.Utf8Text(source)
			}
			if ref.Path != "" {
				out = append(out, ref)
			}
		}
	}
	return out
}

// extractFromImports handles `from MODULE import NAME[, NAME2, ...]`,
// `from MODULE import *`, and the relative variants `from .pkg import X`,
// `from . import X`. The emitted Path is MODULE.NAME so every imported
// symbol carries its fully-qualified identifier.
//
// Relative imports keep their leading dots in the raw Path (`.utils`,
// `..pkg.Thing`); the CodeHandler's ParseCtx pipeline invokes the grammar's
// ResolveImports hook to rewrite these to absolute dotted paths before the
// References reach the store. See python_resolve.go.
//
// Wildcard imports (`from X import *`) emit a single ImportRef with Path
// `MODULE.*` — the `*` marker is preserved so consumers can distinguish
// explicit re-exports from named imports without having to parse comments.
func extractFromImports(n *sitter.Node, source []byte) []ImportRef {
	var module string
	var isRelative bool
	var dots string
	var names []struct {
		name, alias string
	}
	wildcard := false

	// Iterate ALL children (not just named) because we need to detect the
	// anonymous `import` keyword token that separates the FROM clause from
	// the imported names. A named-child iteration would skip it.
	sawImportKeyword := false
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		typ := c.Kind()
		if !sawImportKeyword {
			// Before "import" keyword we're in the FROM clause.
			switch typ {
			case "import":
				sawImportKeyword = true
			case "dotted_name":
				module = c.Utf8Text(source)
			case "relative_import":
				isRelative = true
				// A relative_import has import_prefix (the dots) and
				// optionally a dotted_name (the package after the dots).
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc == nil {
						continue
					}
					switch cc.Kind() {
					case "import_prefix":
						dots = cc.Utf8Text(source)
					case "dotted_name":
						module = cc.Utf8Text(source)
					}
				}
			}
			continue
		}
		// After "import": collect imported names + aliases (or the wildcard).
		switch typ {
		case "dotted_name":
			names = append(names, struct{ name, alias string }{name: c.Utf8Text(source)})
		case "aliased_import":
			pair := struct{ name, alias string }{}
			if p := c.ChildByFieldName("name"); p != nil {
				pair.name = p.Utf8Text(source)
			}
			if a := c.ChildByFieldName("alias"); a != nil {
				pair.alias = a.Utf8Text(source)
			}
			if pair.name != "" {
				names = append(names, pair)
			}
		case "wildcard_import":
			wildcard = true
		}
	}

	if wildcard {
		return []ImportRef{{Path: buildFromPath(module, dots, isRelative, "*")}}
	}

	out := make([]ImportRef, 0, len(names))
	for _, nm := range names {
		out = append(out, ImportRef{
			Path:  buildFromPath(module, dots, isRelative, nm.name),
			Alias: nm.alias,
		})
	}
	return out
}

// SymbolParents implements inheritanceExtractor. Python `class Foo(Base1, Base2, metaclass=Meta):`
// stores its bases in the `superclasses` field (an argument_list). Positional
// arguments are treated as parents; keyword arguments (metaclass, total, etc.)
// are filtered out — they configure class creation but are not inheritance.
//
// Four argument shapes are handled:
//   - identifier (`Base`)            → Name="Base", no metadata
//   - attribute (`pkg.mod.Base`)     → Name="Base" (leaf), Metadata["qualified_name"]="pkg.mod.Base"
//   - subscript (`Generic[T, U]`)    → Name="Generic", Metadata["type_args"]=["T","U"]
//   - call      (`factory()`)        → Name=<callee identifier best-effort>,
//                                      Metadata["unresolved_expression"]=true
//
// Every parent gets Relation="extends" — Python has no implements/conforms
// distinction; `typing.Protocol` is syntactically just another base.
//
// Fuller handling of call-expression bases (e.g., `namedtuple(...)`) is
// deferred to lib-0pa.3 per the plan's "don't silently skip; file a bead"
// directive; the best-effort identifier fallback here keeps something in the
// graph without pretending the resolution is complete.
func (*PythonGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	if n.Kind() != "class_definition" {
		return nil
	}
	supers := n.ChildByFieldName("superclasses")
	if supers == nil {
		return nil
	}
	var out []ParentRef
	for i := uint(0); i < supers.NamedChildCount(); i++ {
		c := supers.NamedChild(i)
		if c == nil {
			continue
		}
		// Keyword arguments (metaclass=, total=) are not parents.
		if c.Kind() == "keyword_argument" {
			continue
		}
		ref := extractPythonBase(c, source)
		if ref != nil {
			out = append(out, *ref)
		}
	}
	return out
}

// extractPythonBase converts a single argument_list child into a ParentRef,
// returning nil to skip shapes that aren't meaningful parents
// (dictionary_splat, list_splat, etc.).
func extractPythonBase(c *sitter.Node, source []byte) *ParentRef {
	loc := indexer.Location{
		Line:       int(c.StartPosition().Row) + 1,
		Column:     int(c.StartPosition().Column) + 1,
		ByteOffset: int(c.StartByte()),
	}
	switch c.Kind() {
	case "identifier":
		return &ParentRef{
			Name:     strings.TrimSpace(c.Utf8Text(source)),
			Relation: "extends",
			Loc:      loc,
		}
	case "attribute":
		// `pkg.mod.Base` — emit the leaf identifier as Name so the graph node
		// stays a clean short name; stash the full dotted chain in
		// qualified_name so downstream beads (lib-6wz, lib-0pa.2) can resolve
		// the FQN without re-parsing.
		attrField := c.ChildByFieldName("attribute")
		if attrField == nil {
			return nil
		}
		leaf := strings.TrimSpace(attrField.Utf8Text(source))
		if leaf == "" {
			return nil
		}
		return &ParentRef{
			Name:     leaf,
			Relation: "extends",
			Loc:      loc,
			Metadata: map[string]any{"qualified_name": strings.TrimSpace(c.Utf8Text(source))},
		}
	case "subscript":
		// `Generic[T, U]` / `typing.Generic[T]` — base name is the subscripted
		// value; slice entries are the type arguments. When the value is itself
		// an attribute chain (e.g. typing.Generic), extract the leaf identifier
		// as Name and stash the full dotted chain in qualified_name so
		// downstream resolvers (lib-0pa.2, lib-6wz) have the FQN context.
		// ResolveParents does NOT rewrite Target via qualified_name when
		// type_args are present — import-binding resolution (rule 4) is the
		// preferred path for parameterised bases like Generic/Protocol.
		value := c.ChildByFieldName("value")
		if value == nil {
			return nil
		}
		var name string
		meta := map[string]any{}
		switch value.Kind() {
		case "attribute":
			// e.g. typing.Generic[T] — leaf is "Generic", qualified_name is "typing.Generic"
			attrField := value.ChildByFieldName("attribute")
			if attrField == nil {
				return nil
			}
			name = strings.TrimSpace(attrField.Utf8Text(source))
			if name == "" {
				return nil
			}
			meta["qualified_name"] = strings.TrimSpace(value.Utf8Text(source))
		default:
			name = strings.TrimSpace(value.Utf8Text(source))
			if name == "" {
				return nil
			}
		}
		var args []string
		for i := uint(0); i < c.NamedChildCount(); i++ {
			cc := c.NamedChild(i)
			if cc == nil || cc.Equals(*value) {
				continue
			}
			if t := strings.TrimSpace(cc.Utf8Text(source)); t != "" {
				args = append(args, t)
			}
		}
		if len(args) > 0 {
			meta["type_args"] = args
		}
		return &ParentRef{
			Name:     name,
			Relation: "extends",
			Loc:      loc,
			Metadata: meta,
		}
	case "call":
		// `factory()` / `namedtuple(...)` — best-effort identifier fallback.
		// Full handling of call-expression bases lives in lib-3xh.
		fn := c.ChildByFieldName("function")
		if fn == nil {
			return nil
		}
		name := strings.TrimSpace(fn.Utf8Text(source))
		if name == "" {
			return nil
		}
		return &ParentRef{
			Name:     name,
			Relation: "extends",
			Loc:      loc,
			Metadata: map[string]any{"unresolved_expression": true},
		}
	}
	return nil
}

// ResolveParents implements inheritanceResolver for Python. Runs AFTER
// ResolveImports (per the ParseCtx ordering) so the imports list in refs is
// already in canonicalised absolute form — same-file bare-name parents can
// resolve against that canonical form directly.
//
// Resolution rules, in order:
//  1. Metadata.qualified_name set (attribute-chain base, e.g. pkg.mod.Base) →
//     rewrite Target to the qualified_name FQN; resolveInheritsRefs then skips
//     it as already-dotted.
//  2. Target already dotted (FQN in source) → leave alone.
//  3. Metadata.unresolved_expression=true (call-expression base) → leave
//     alone; lib-0pa.3 will tackle these.
//  4. Bare name matches a local binding from the file's imports → rewrite
//     Target to the full path.
//  5. Otherwise → mark Metadata.unresolved=true.
//
// Import forms that contribute a local binding:
//   - `import pkg`          → local "pkg"    → target "pkg"
//   - `import pkg as p`     → local "p"      → target "pkg"
//   - `from m import N`     → local "N"      → target (absolute post-resolve)
//   - `from m import N as A`→ local "A"      → target (absolute post-resolve)
//
// Wildcard `from m import *` has no local name binding and is skipped.
// Python-specific note for the shared LocalTypeBindings helper: `import
// pkg.subpkg` (no alias) produces Target="pkg.subpkg" after ResolveImports.
// The shared helper extracts the leaf ("subpkg"), matching Python's own
// scoping rule: a plain dotted import binds ONLY the leaf in the local
// scope, not the root. So `local["subpkg"] = "pkg.subpkg"` is correct even
// though the `class Foo(subpkg):` form is rare — the common
// `class Foo(pkg.subpkg.Base):` carries qualified_name and is handled by rule 1.
func (*PythonGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	// Rule 1: attribute-chain bases carry qualified_name — use it as the FQN
	// so resolveInheritsRefs sees a dotted target and leaves it alone cleanly.
	// Exception: subscript-of-attribute bases (e.g. typing.Generic[T]) also
	// carry qualified_name but additionally have type_args; for those, keep
	// the bare leaf as Target so import-binding resolution (rule 4) can fire
	// when the class is imported (e.g. `from typing import Generic`).
	for i, r := range refs {
		if r.Kind != "inherits" {
			continue
		}
		if qn, ok := r.Metadata["qualified_name"].(string); ok && qn != "" {
			if _, hasArgs := r.Metadata["type_args"]; !hasArgs {
				refs[i].Target = qn
			}
		}
	}
	return resolveInheritsRefs(refs, localTypeBindings(refs, false /* skipStatic */))
}

// buildFromPath composes the Path for a `from ... import NAME` entry across
// the four shapes: absolute+name, relative-with-module+name, relative-
// empty+name, and the fallback. Factored out so the normal and wildcard
// callers in extractFromImports share exactly one implementation.
func buildFromPath(module, dots string, isRelative bool, name string) string {
	switch {
	case module != "" && isRelative:
		// `from ..pkg import NAME` → "..pkg.NAME"
		return dots + module + "." + name
	case module != "":
		// `from pkg import NAME` → "pkg.NAME"
		return module + "." + name
	case isRelative:
		// `from . import NAME` → ".NAME" — no extra separator; dots end in '.'.
		return dots + name
	default:
		return name
	}
}

// CallExpressions implements callExtractor for Python. Walks the file AST for
// `call` nodes, extracts the bare callee identifier, and attributes each call
// to the innermost enclosing function_definition.
//
// Because Python's PackageName returns "" (no explicit package declaration),
// the returned CallerPath is a LOCAL path without the module prefix — e.g.
// "MyClass.process" rather than "module.MyClass.process". extractCallRefs
// resolves it via suffix matching against doc.Units.
//
// Callee extraction:
//   - identifier: foo() → "foo"
//   - attribute: obj.method() → "method"
//
// Closures and nested functions are attributed to the INNERMOST enclosing
// function_definition. Calls at module scope (no enclosing function) are
// skipped — they have no sym: node to anchor an edge.
func (*PythonGrammar) CallExpressions(root *sitter.Node, source []byte) []CallRef {
	var out []CallRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "call" {
			return true
		}
		funcNode := n.ChildByFieldName("function")
		if funcNode == nil {
			return true
		}
		callee := pyCalleeIdent(funcNode, source)
		if callee == "" {
			return true
		}
		callerPath := pyCallerLocalPath(n, source)
		if callerPath == "" {
			return true
		}
		out = append(out, CallRef{
			CallerPath: callerPath,
			CalleeName: callee,
			Loc:        nodeLocation(n),
		})
		return true
	})
	return out
}

// pyCalleeIdent extracts the bare callee identifier from a Python call's
// `function` field child. Returns "" for complex expressions.
func pyCalleeIdent(funcNode *sitter.Node, source []byte) string {
	switch funcNode.Kind() {
	case "identifier":
		return funcNode.Utf8Text(source)
	case "attribute":
		if attr := funcNode.ChildByFieldName("attribute"); attr != nil {
			return attr.Utf8Text(source)
		}
	}
	return ""
}

// pyCallerLocalPath builds the local caller path (without the module prefix)
// by walking from n upward to collect enclosing class and function names.
//
//   - top-level function: returns "myFunc"
//   - method inside class: returns "MyClass.myMethod"
//   - nested function: returns "outer.inner" (inner has no sym: node, so
//     extractCallRefs will drop the edge — correct v1 behaviour)
//
// Returns "" when no enclosing function_definition is found (module-scope call).
func pyCallerLocalPath(n *sitter.Node, source []byte) string {
	var parts []string
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Kind() {
		case "function_definition":
			if nameNode := p.ChildByFieldName("name"); nameNode != nil {
				parts = append([]string{nameNode.Utf8Text(source)}, parts...)
			}
		case "class_definition":
			if nameNode := p.ChildByFieldName("name"); nameNode != nil {
				parts = append([]string{nameNode.Utf8Text(source)}, parts...)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ".")
}
