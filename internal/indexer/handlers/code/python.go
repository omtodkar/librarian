package code

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
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
func (*PythonGrammar) Language() *sitter.Language { return python.GetLanguage() }

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
	switch n.Type() {
	case "function_definition", "class_definition":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
	case "type_alias_statement":
		// tree-sitter-python models this as two unnamed `type` children: the
		// first is the alias LHS, the second the RHS. The LHS wraps either
		// an identifier (`type Matrix = ...`) or a generic_type
		// (`type Vec[T] = ...`). Guard the `type` wrapper so a future
		// grammar change that inserts a new first named child doesn't get
		// mis-interpreted as the LHS.
		lhs := n.NamedChild(0)
		if lhs == nil || lhs.Type() != "type" {
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
	if assign == nil || assign.Type() != "assignment" {
		return ""
	}
	ann := assign.ChildByFieldName("type")
	left := assign.ChildByFieldName("left")
	if ann == nil || left == nil || left.Type() != "identifier" {
		return ""
	}
	annText := strings.Trim(ann.Content(source), `"' `)
	if annText != "TypeAlias" && !strings.HasSuffix(annText, ".TypeAlias") {
		return ""
	}
	return left.Content(source)
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
	if inner.Type() == "identifier" {
		return inner.Content(source)
	}
	if name := inner.NamedChild(0); name != nil && name.Type() == "identifier" {
		return name.Content(source)
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
	switch n.Type() {
	case "function_definition", "class_definition":
	default:
		return ""
	}
	body := n.ChildByFieldName("body")
	if body == nil || body.NamedChildCount() == 0 {
		return ""
	}
	first := body.NamedChild(0)
	if first == nil || first.Type() != "expression_statement" || first.NamedChildCount() == 0 {
		return ""
	}
	str := first.NamedChild(0)
	if str == nil || str.Type() != "string" {
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
	for i := 0; i < int(str.NamedChildCount()); i++ {
		c := str.NamedChild(i)
		if c == nil || c.Type() != "string_start" {
			continue
		}
		prefix := strings.ToLower(c.Content(source))
		return strings.ContainsAny(prefix, "bB") ||
			strings.HasPrefix(prefix, "b") ||
			strings.HasPrefix(prefix, "rb") ||
			strings.HasPrefix(prefix, "br")
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
	for i := 0; i < int(str.NamedChildCount()); i++ {
		c := str.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "string_start", "string_end":
			// Delimiters — skip.
		default:
			out.WriteString(c.Content(source))
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
		switch n.Type() {
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
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "dotted_name":
			out = append(out, ImportRef{Path: c.Content(source)})
		case "aliased_import":
			ref := ImportRef{}
			if p := c.ChildByFieldName("name"); p != nil {
				ref.Path = p.Content(source)
			}
			if a := c.ChildByFieldName("alias"); a != nil {
				ref.Alias = a.Content(source)
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
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		typ := c.Type()
		if !sawImportKeyword {
			// Before "import" keyword we're in the FROM clause.
			switch typ {
			case "import":
				sawImportKeyword = true
			case "dotted_name":
				module = c.Content(source)
			case "relative_import":
				isRelative = true
				// A relative_import has import_prefix (the dots) and
				// optionally a dotted_name (the package after the dots).
				for j := 0; j < int(c.NamedChildCount()); j++ {
					cc := c.NamedChild(j)
					if cc == nil {
						continue
					}
					switch cc.Type() {
					case "import_prefix":
						dots = cc.Content(source)
					case "dotted_name":
						module = cc.Content(source)
					}
				}
			}
			continue
		}
		// After "import": collect imported names + aliases (or the wildcard).
		switch typ {
		case "dotted_name":
			names = append(names, struct{ name, alias string }{name: c.Content(source)})
		case "aliased_import":
			pair := struct{ name, alias string }{}
			if p := c.ChildByFieldName("name"); p != nil {
				pair.name = p.Content(source)
			}
			if a := c.ChildByFieldName("alias"); a != nil {
				pair.alias = a.Content(source)
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
