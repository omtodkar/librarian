package code

import (
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/tree-sitter/tree-sitter-javascript/bindings/go"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code/tree_sitter_typescript/tsx"
	"librarian/internal/indexer/handlers/code/tree_sitter_typescript/typescript"
)

// jsLikeGrammar is the shared implementation for JavaScript, TypeScript, and
// TSX. The three grammars differ only in three dimensions: the tree-sitter
// language, the file extensions, and whether TypeScript-only declaration
// kinds (interface / type / enum / abstract class / method signatures) are
// recognised. Everything else — imports, arrow-as-function extraction, export
// labelling, method / class / const handling — is identical across the
// family.
//
// Using one struct with three constructors keeps the generated symbol set
// stable across the three grammars while still routing each file to the
// language that actually parses its extension (TSX to the JSX-aware parser,
// pure TS to the stricter non-JSX parser, JS to its own grammar).
type jsLikeGrammar struct {
	name          string
	extensions    []string
	lang          *sitter.Language
	supportsTypes bool // TypeScript-only declaration kinds
}

// tsOnlySymbolKinds lists node types that only the TypeScript family
// recognises as symbols. Defined once and consumed by SymbolKinds when
// supportsTypes is true so the list doesn't drift between SymbolKinds and
// ContainerKinds.
var tsOnlySymbolKinds = map[string]string{
	"interface_declaration":      "interface",
	"type_alias_declaration":     "type",
	"enum_declaration":           "enum",
	"abstract_class_declaration": "class",
	"method_signature":           "method",   // interface member
	"abstract_method_signature":  "method",   // abstract class member
	"property_signature":         "field",    // interface / type member
}

// tsOnlyContainerKinds lists pure and hybrid container nodes that only the
// TypeScript family descends through. Hybrid nodes also appear in
// tsOnlySymbolKinds; pure bodies (interface_body, enum_body) only here.
var tsOnlyContainerKinds = map[string]bool{
	"interface_declaration":      true, // hybrid
	"enum_declaration":           true, // hybrid
	"abstract_class_declaration": true, // hybrid
	"interface_body":             true, // pure
	"enum_body":                  true, // pure
}

// NewJavaScriptGrammar returns the JavaScript grammar covering .js, .jsx,
// .mjs, and .cjs. JSX syntax is handled by the javascript tree-sitter language
// in all these cases.
func NewJavaScriptGrammar() Grammar {
	return &jsLikeGrammar{
		name:       "javascript",
		extensions: []string{".js", ".jsx", ".mjs", ".cjs"},
		lang:       sitter.NewLanguage(tree_sitter_javascript.Language()),
	}
}

// NewTypeScriptGrammar returns the TypeScript grammar covering .ts, .mts,
// .cts. TSX files use NewTSXGrammar — the tree-sitter-typescript project
// ships two separate grammars for that reason.
func NewTypeScriptGrammar() Grammar {
	return &jsLikeGrammar{
		name:          "typescript",
		extensions:    []string{".ts", ".mts", ".cts"},
		lang:          sitter.NewLanguage(tree_sitter_typescript.Language()),
		supportsTypes: true,
	}
}

// NewTSXGrammar returns the TSX grammar for .tsx. Identical to TypeScript
// except for the underlying tree-sitter language which accepts JSX syntax.
func NewTSXGrammar() Grammar {
	return &jsLikeGrammar{
		name:          "tsx",
		extensions:    []string{".tsx"},
		lang:          sitter.NewLanguage(tree_sitter_tsx.Language()),
		supportsTypes: true,
	}
}

func (g *jsLikeGrammar) Name() string               { return g.name }
func (g *jsLikeGrammar) Extensions() []string       { return g.extensions }
func (g *jsLikeGrammar) Language() *sitter.Language { return g.lang }

// CommentNodeTypes returns "comment" — tree-sitter-javascript and the
// TypeScript grammars both use a single "comment" node type that covers
// `//`, `/* */`, and `/** */` (JSDoc). stripCommentMarkers already handles
// leading-asterisk continuation lines for JSDoc rendering.
func (*jsLikeGrammar) CommentNodeTypes() []string { return []string{"comment"} }

// DocstringFromNode returns "" — JSDoc blocks precede the declaration as a
// `comment` sibling and are captured by the walker's preceding-comment buffer.
func (*jsLikeGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps AST node types to Unit.Kind values. variable_declarator
// appears as a "function" here, but SymbolName returns "" for any declarator
// whose value is NOT an arrow_function — so only `const useAuth = () => {}`
// style declarations emit Units, not `const PI = 3.14`.
func (g *jsLikeGrammar) SymbolKinds() map[string]string {
	m := map[string]string{
		"function_declaration": "function",
		"class_declaration":    "class",
		"method_definition":    "method",
		// variable_declarator is filtered by SymbolName: only arrow-function
		// values become Units. Kind="function" matches how module authors
		// think of `const useAuth = () => {...}`.
		"variable_declarator": "function",
	}
	if g.supportsTypes {
		for k, v := range tsOnlySymbolKinds {
			m[k] = v
		}
	}
	return m
}

// ContainerKinds covers the hybrid type declarations (emit Unit + descend
// into body) and the pure containers the walker must traverse to reach
// member symbols.
//
// export_statement and lexical_declaration are pure containers: the walker
// descends into them, forwards pending comments, but doesn't emit a Unit.
// This lets `/** doc */ export function X() {}` attach the JSDoc to X, and
// `/** doc */ export const useX = () => {}` attach the JSDoc to useX.
func (g *jsLikeGrammar) ContainerKinds() map[string]bool {
	m := map[string]bool{
		"class_declaration":   true, // hybrid
		"class_body":          true, // pure
		"export_statement":    true, // pure
		"lexical_declaration": true, // pure
	}
	if g.supportsTypes {
		for k := range tsOnlyContainerKinds {
			m[k] = true
		}
	}
	return m
}

// SymbolName returns the identifier for a symbol node. All type-declaration
// and callable kinds expose their name as the "name" field; variable_declarator
// additionally gates on the value being an arrow_function so `const x = 5`
// doesn't mint a spurious Unit at module scope.
func (*jsLikeGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "function_declaration",
		"class_declaration", "abstract_class_declaration",
		"interface_declaration", "enum_declaration", "type_alias_declaration",
		"method_definition", "method_signature", "abstract_method_signature",
		"property_signature":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "variable_declarator":
		value := n.ChildByFieldName("value")
		if value == nil || value.Kind() != "arrow_function" {
			return ""
		}
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	}
	return ""
}

// PackageName returns "" — JS/TS modules have no package clause. The shared
// Parse falls back to the file stem (basename without the final extension).
func (*jsLikeGrammar) PackageName(*sitter.Node, []byte) string { return "" }

// Imports walks import_statement nodes and emits one ImportRef per imported
// binding. Five shapes are handled:
//
//   - `import "side-effects";`        → one ref, Path=module, no name.
//   - `import foo from "m";`          → Path=m, Alias=foo, Metadata["default"].
//   - `import { a, b as c } from "m";` → Path=m, Metadata["member"]=a;
//                                        Path=m, Metadata["member"]=b, Alias=c.
//   - `import * as ns from "m";`      → Path=m, Alias=ns, Metadata["namespace"].
//   - `import type { X } from "m";`   → any of the above, Metadata["type_only"].
//
// Named imports keep the module and the named member on separate fields so
// the JS/TS resolver (javascript_resolve.go) can rewrite Path to a
// project-relative file path without having to disentangle "./utils.foo"
// (member `foo` of module `./utils`) from "./utils.foo" (file literally
// named `utils.foo`). Mixed forms like `import def, { named } from "m"`
// emit one ref per binding.
func (*jsLikeGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "import_statement" {
			return true
		}
		out = append(out, extractJSImports(n, source)...)
		return false
	})
	return out
}

// extractJSImports handles one import_statement. Walked separately because
// tree-sitter exposes the `type` keyword as an anonymous child — named-child
// iteration would miss it — and the module path lives inside a string node.
func extractJSImports(n *sitter.Node, source []byte) []ImportRef {
	typeOnly := hasAnonymousChild(n, "type")

	var module string
	var clause *sitter.Node
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "import_clause":
			clause = c
		case "string":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				cc := c.NamedChild(j)
				if cc != nil && cc.Kind() == "string_fragment" {
					module = cc.Utf8Text(source)
					break
				}
			}
		}
	}
	if module == "" {
		return nil
	}

	var out []ImportRef
	if clause == nil {
		// Side-effect import: `import "side-effects";`.
		out = append(out, ImportRef{Path: module})
	} else {
		for i := uint(0); i < clause.NamedChildCount(); i++ {
			c := clause.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "identifier":
				// Default import: `import foo from "m"`.
				out = append(out, ImportRef{
					Path:     module,
					Alias:    c.Utf8Text(source),
					Metadata: map[string]any{"default": true},
				})
			case "namespace_import":
				alias := ""
				for j := uint(0); j < c.NamedChildCount(); j++ {
					id := c.NamedChild(j)
					if id != nil && id.Kind() == "identifier" {
						alias = id.Utf8Text(source)
						break
					}
				}
				out = append(out, ImportRef{
					Path:     module,
					Alias:    alias,
					Metadata: map[string]any{"namespace": true},
				})
			case "named_imports":
				for j := uint(0); j < c.NamedChildCount(); j++ {
					spec := c.NamedChild(j)
					if spec == nil || spec.Kind() != "import_specifier" {
						continue
					}
					var name, alias string
					if nm := spec.ChildByFieldName("name"); nm != nil {
						name = nm.Utf8Text(source)
					}
					if al := spec.ChildByFieldName("alias"); al != nil {
						alias = al.Utf8Text(source)
					}
					if name == "" {
						continue
					}
					out = append(out, ImportRef{
						Path:     module,
						Alias:    alias,
						Metadata: map[string]any{"member": name},
					})
				}
			}
		}
	}

	// Apply type_only flag in a single post-loop pass so the mutation point
	// is obvious and doesn't hide inside a closure.
	if typeOnly {
		for i := range out {
			if out[i].Metadata == nil {
				out[i].Metadata = map[string]any{}
			}
			out[i].Metadata["type_only"] = true
		}
	}
	return out
}

// SymbolParents implements inheritanceExtractor. Covers:
//   - JS class_declaration:       `extends` (class_heritage node)
//   - TS class_declaration:       `extends` + `implements` (implements_clause)
//   - TS abstract_class_declaration: same shape as TS class_declaration
//   - TS interface_declaration:   `extends` (extends_type_clause)
//
// In JS, `class_heritage` wraps a single _expression child — the parent can
// be an identifier, a member_expression (`pkg.Foo`), a call_expression
// (mixin-application like `Mixin(Base)`), or a generic_type instantiation in
// TS. The helper jsExtractParent handles each shape, falling back to the
// callee identifier with unresolved_expression=true when the parent is a
// call (full mixin handling is deferred to lib-ap8).
func (g *jsLikeGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	switch n.Kind() {
	case "class_declaration", "abstract_class_declaration":
		return jsClassParents(n, source)
	case "interface_declaration":
		if !g.supportsTypes {
			return nil
		}
		return jsInterfaceParents(n, source)
	}
	return nil
}

// jsClassParents walks a class_declaration / abstract_class_declaration for
// inheritance information. Shape differs between JS and TS:
//
//   - tree-sitter-javascript: class_declaration has a class_heritage child
//     whose named children are the `extends` expressions directly.
//   - tree-sitter-typescript: class_declaration has a class_heritage child
//     that WRAPS an extends_clause and/or implements_clause. The actual
//     parent types live inside those clauses.
//
// Unified walker: descend one level into class_heritage; for each child,
// either recognise extends_clause / implements_clause (TS case) or treat
// the child itself as an extends parent (JS case).
func jsClassParents(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "class_heritage":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				inner := c.NamedChild(j)
				if inner == nil {
					continue
				}
				switch inner.Kind() {
				case "extends_clause":
					for k := uint(0); k < inner.NamedChildCount(); k++ {
						if p := jsMakeParent(inner.NamedChild(k), "extends", source); p != nil {
							out = append(out, *p)
						}
					}
				case "implements_clause":
					for k := uint(0); k < inner.NamedChildCount(); k++ {
						if p := jsMakeParent(inner.NamedChild(k), "implements", source); p != nil {
							out = append(out, *p)
						}
					}
				default:
					// JS: class_heritage's named child is the extends
					// expression directly (identifier / member_expression /
					// call_expression).
					if p := jsMakeParent(inner, "extends", source); p != nil {
						out = append(out, *p)
					}
				}
			}
		case "implements_clause":
			// Defensive: some grammar variants expose implements_clause as
			// a sibling of class_heritage rather than a child. Handling both
			// shapes keeps us robust across tree-sitter-typescript versions.
			for j := uint(0); j < c.NamedChildCount(); j++ {
				if p := jsMakeParent(c.NamedChild(j), "implements", source); p != nil {
					out = append(out, *p)
				}
			}
		}
	}
	return out
}

// jsInterfaceParents walks a TS interface_declaration for its extends clause
// (`interface Foo extends A, B`). The container node type is
// extends_type_clause in the tree-sitter-typescript grammar.
func jsInterfaceParents(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() != "extends_type_clause" && c.Kind() != "extends_clause" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			if p := jsMakeParent(c.NamedChild(j), "extends", source); p != nil {
				out = append(out, *p)
			}
		}
	}
	return out
}

// jsMakeParent turns a single parent-type node into a ParentRef. Returns nil
// when the node's shape doesn't yield a usable name (e.g., a bare `extends`
// keyword child picked up by NamedChild, or an unsupported expression form).
func jsMakeParent(c *sitter.Node, relation string, source []byte) *ParentRef {
	if c == nil {
		return nil
	}
	name, typeArgs, unresolvedExpr := jsExtractParent(c, source)
	if name == "" {
		return nil
	}
	loc := indexer.Location{
		Line:       int(c.StartPosition().Row) + 1,
		Column:     int(c.StartPosition().Column) + 1,
		ByteOffset: int(c.StartByte()),
	}
	meta := map[string]any{}
	if len(typeArgs) > 0 {
		meta["type_args"] = typeArgs
	}
	if unresolvedExpr {
		meta["unresolved_expression"] = true
	}
	return &ParentRef{Name: name, Relation: relation, Loc: loc, Metadata: meta}
}

// jsExtractParent teases a JS/TS parent-type node into (name, typeArgs,
// unresolvedExpr). Recognises identifier / member_expression for JS class
// extends; type_identifier / nested_type_identifier / generic_type for TS
// implements / interface-extends clauses; and call_expression for the
// mixin-application fallback.
func jsExtractParent(n *sitter.Node, source []byte) (string, []string, bool) {
	switch n.Kind() {
	case "identifier", "type_identifier", "nested_type_identifier":
		return strings.TrimSpace(n.Utf8Text(source)), nil, false
	case "member_expression":
		// JS `extends pkg.Foo` — dotted identifier chain. Content includes
		// the full chain; consumers treat dotted targets as already-qualified.
		return strings.TrimSpace(n.Utf8Text(source)), nil, false
	case "generic_type":
		// TS `Foo<T, U>` — named "name" field carries the type identifier,
		// and type_arguments hold the args.
		var name string
		var args []string
		if nm := n.ChildByFieldName("name"); nm != nil {
			name = strings.TrimSpace(nm.Utf8Text(source))
		}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil || c.Kind() != "type_arguments" {
				continue
			}
			for j := uint(0); j < c.NamedChildCount(); j++ {
				if a := c.NamedChild(j); a != nil {
					if t := strings.TrimSpace(a.Utf8Text(source)); t != "" {
						args = append(args, t)
					}
				}
			}
		}
		if name == "" {
			// Grammar variant: name may not be a field. Fall back to first
			// type_identifier / nested_type_identifier child.
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Kind() == "type_identifier" || c.Kind() == "nested_type_identifier" {
					name = strings.TrimSpace(c.Utf8Text(source))
					break
				}
			}
		}
		return name, args, false
	case "call_expression":
		// Mixin-application pattern: `class Foo extends Mixin(Base)`. Full
		// handling lives in lib-ap8; here we fall back to the callee
		// identifier so SOMETHING lands in the graph with a clear marker.
		fn := n.ChildByFieldName("function")
		if fn == nil {
			return "", nil, true
		}
		return strings.TrimSpace(fn.Utf8Text(source)), nil, true
	}
	return "", nil, false
}

// ResolveParents implements inheritanceResolver for JS/TS. Mirrors the Java
// same-file-import strategy: bare parent names get rewritten to
// `<module_stem>.<member>` when the file imports the matching symbol, and
// otherwise land with Metadata["unresolved"]=true.
//
// JS imports are varied; this resolver handles the common cases:
//   - Named import  (`import { Foo } from './utils'`)        → local "Foo"  → canonical "utils.Foo"
//   - Named aliased (`import { Foo as F } from './utils'`)   → local "F"    → canonical "utils.Foo"
//   - Default       (`import foo from './utils'`)            → left unresolved (the default export's real name is unknown at parse time — lib-38i will handle this).
//   - Namespace     (`import * as ns from './utils'`)        → reference would be `ns.Foo` (dotted, skipped).
//
// npm packages (external node_kind) don't participate: the imported symbol
// lives outside the workspace and has no sym: node to point at.
func (*jsLikeGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	// jsLocalNamedBindings builds its own map (JS uses module-stem +
	// member canonical paths, unlike Java/Python/Kotlin which take the
	// Target's leaf). The ResolveInheritsRefs body is shared.
	return resolveInheritsRefs(refs, jsLocalNamedBindings(refs))
}

// jsLocalNamedBindings builds the local-name → canonical-symbol map from JS
// import Refs. Only named imports participate (default/namespace/external
// bindings don't map cleanly to sym: ids — see ResolveParents godoc).
//
// For a ref with Metadata["member"]="Foo" and Path="src/utils.ts":
//   - local name = alias (if set) else member ("Foo" or "F" for `Foo as F`)
//   - canonical  = stem("src/utils.ts") + "." + member = "utils.Foo"
//
// The stem comes from the final path segment minus its extension, matching
// the Unit.Path prefix the stem-fallback path uses for files without a
// package clause (see code.go ParseCtx).
func jsLocalNamedBindings(refs []indexer.Reference) map[string]string {
	out := map[string]string{}
	for _, r := range refs {
		if r.Kind != "import" || r.Target == "" || r.Metadata == nil {
			continue
		}
		member, ok := r.Metadata["member"].(string)
		if !ok || member == "" {
			continue
		}
		// Skip npm-external named imports — canonical target for those is
		// ext:<pkg>, not a sym: path, so a bare parent reference to them
		// should stay unresolved anyway.
		if k, _ := r.Metadata["node_kind"].(string); k == "external" {
			continue
		}
		local := member
		if alias, _ := r.Metadata["alias"].(string); alias != "" {
			local = alias
		}
		stem := jsModuleStem(r.Target)
		if stem == "" {
			continue
		}
		if _, seen := out[local]; seen {
			continue
		}
		out[local] = stem + "." + member
	}
	return out
}

// jsModuleStem returns the basename-without-extension of a resolved module
// path: "src/utils.ts" → "utils", "index.ts" → "index",
// "./foo/bar.tsx" → "bar". Empty input returns empty.
func jsModuleStem(modulePath string) string {
	if modulePath == "" {
		return ""
	}
	base := filepath.Base(modulePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// SymbolExtraSignals emits label signals for exported symbols. Top-level
// symbols wrapped in `export_statement` carry Kind="label" Value="exported";
// those with a `default` anonymous token also emit Value="default-export".
// Walks one level up through `lexical_declaration` because
// `export const useAuth = () => {}` parses as
// `export_statement > lexical_declaration > variable_declarator` and the
// variable_declarator's immediate parent is the lexical_declaration.
func (*jsLikeGrammar) SymbolExtraSignals(n *sitter.Node, _ []byte) []indexer.Signal {
	exportStmt := n.Parent()
	if exportStmt == nil {
		return nil
	}
	if exportStmt.Kind() == "lexical_declaration" {
		exportStmt = exportStmt.Parent()
	}
	if exportStmt == nil || exportStmt.Kind() != "export_statement" {
		return nil
	}
	signals := []indexer.Signal{{Kind: "label", Value: "exported"}}
	if hasAnonymousChild(exportStmt, "default") {
		signals = append(signals, indexer.Signal{Kind: "label", Value: "default-export"})
	}
	return signals
}

// hasAnonymousChild reports whether n has a direct child whose Type matches
// typ. Used to detect tree-sitter anonymous keyword tokens (`default`,
// `type`, `static`, …) that don't appear in NamedChild iteration.
func hasAnonymousChild(n *sitter.Node, typ string) bool {
	for i := uint(0); i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c != nil && c.Kind() == typ {
			return true
		}
	}
	return false
}
