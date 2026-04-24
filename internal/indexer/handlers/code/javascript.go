package code

import (
	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"librarian/internal/indexer"
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
		lang:       javascript.GetLanguage(),
	}
}

// NewTypeScriptGrammar returns the TypeScript grammar covering .ts, .mts,
// .cts. TSX files use NewTSXGrammar — the tree-sitter-typescript project
// ships two separate grammars for that reason.
func NewTypeScriptGrammar() Grammar {
	return &jsLikeGrammar{
		name:          "typescript",
		extensions:    []string{".ts", ".mts", ".cts"},
		lang:          typescript.GetLanguage(),
		supportsTypes: true,
	}
}

// NewTSXGrammar returns the TSX grammar for .tsx. Identical to TypeScript
// except for the underlying tree-sitter language which accepts JSX syntax.
func NewTSXGrammar() Grammar {
	return &jsLikeGrammar{
		name:          "tsx",
		extensions:    []string{".tsx"},
		lang:          tsx.GetLanguage(),
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
	switch n.Type() {
	case "function_declaration",
		"class_declaration", "abstract_class_declaration",
		"interface_declaration", "enum_declaration", "type_alias_declaration",
		"method_definition", "method_signature", "abstract_method_signature",
		"property_signature":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
	case "variable_declarator":
		value := n.ChildByFieldName("value")
		if value == nil || value.Type() != "arrow_function" {
			return ""
		}
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
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
//   - `import "side-effects";`       → one ref, Path=module, no name.
//   - `import foo from "m";`          → Path=m, Alias=foo, Metadata["default"].
//   - `import { a, b as c } from "m";` → Path=m.a; Path=m.b (Alias="c").
//   - `import * as ns from "m";`      → Path=m, Alias=ns, Metadata["namespace"].
//   - `import type { X } from "m";`   → any of the above, Metadata["type_only"].
//
// Mixed forms like `import def, { named } from "m"` emit one ref per binding.
func (*jsLikeGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Type() != "import_statement" {
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
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "import_clause":
			clause = c
		case "string":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				cc := c.NamedChild(j)
				if cc != nil && cc.Type() == "string_fragment" {
					module = cc.Content(source)
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
		for i := 0; i < int(clause.NamedChildCount()); i++ {
			c := clause.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "identifier":
				// Default import: `import foo from "m"`.
				out = append(out, ImportRef{
					Path:     module,
					Alias:    c.Content(source),
					Metadata: map[string]any{"default": true},
				})
			case "namespace_import":
				alias := ""
				for j := 0; j < int(c.NamedChildCount()); j++ {
					id := c.NamedChild(j)
					if id != nil && id.Type() == "identifier" {
						alias = id.Content(source)
						break
					}
				}
				out = append(out, ImportRef{
					Path:     module,
					Alias:    alias,
					Metadata: map[string]any{"namespace": true},
				})
			case "named_imports":
				for j := 0; j < int(c.NamedChildCount()); j++ {
					spec := c.NamedChild(j)
					if spec == nil || spec.Type() != "import_specifier" {
						continue
					}
					var name, alias string
					if nm := spec.ChildByFieldName("name"); nm != nil {
						name = nm.Content(source)
					}
					if al := spec.ChildByFieldName("alias"); al != nil {
						alias = al.Content(source)
					}
					if name == "" {
						continue
					}
					out = append(out, ImportRef{Path: module + "." + name, Alias: alias})
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
	if exportStmt.Type() == "lexical_declaration" {
		exportStmt = exportStmt.Parent()
	}
	if exportStmt == nil || exportStmt.Type() != "export_statement" {
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
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == typ {
			return true
		}
	}
	return false
}
