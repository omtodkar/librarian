package code

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
)

// RustGrammar indexes .rs source files.
//
// Symbol extraction:
//   - Top-level and nested fn items → function Unit.
//   - fn items inside impl blocks → method Unit (detected via SymbolKindFor).
//   - struct items → class Unit.
//   - enum items → enum Unit.
//   - trait items → interface Unit (hybrid: also container so trait method
//     signatures emit separate method Units inside the trait path).
//   - type aliases (type_item) → type Unit.
//   - function_signature_item (method signature inside trait body) → method Unit.
//
// Containers (no Unit emitted, path prefix extended):
//   - impl_item: extends path with the type name so methods land under
//     "pkg.TypeName.method". Trait name is ignored for path scoping.
//   - mod_item: extends path with the module name.
//   - declaration_list: body of impl / trait / mod — pure pass-through.
//
// Imports: use_declaration arguments are recursively expanded — handles
// grouped ({ A, B }), aliased (as X), wildcard (*), and scoped paths.
//
// Comments: line_comment covers both // and /// doc-comments;
// block_comment covers /* ... */ and /** ... */ forms.
type RustGrammar struct{}

// NewRustGrammar returns the Rust grammar implementation.
func NewRustGrammar() *RustGrammar { return &RustGrammar{} }

func (*RustGrammar) Name() string               { return "rust" }
func (*RustGrammar) Extensions() []string       { return []string{".rs"} }
func (*RustGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_rust.Language()) }

func (*RustGrammar) CommentNodeTypes() []string {
	return []string{"line_comment", "block_comment"}
}

func (*RustGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Rust AST node types to generic Unit.Kind values.
//
// impl_item is intentionally absent — it is a pure container whose methods
// are detected as Kind="method" by SymbolKindFor (which checks for an
// impl_item ancestor). mod_item is also a pure container.
func (*RustGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"function_item":           "function",
		"function_signature_item": "method",
		"struct_item":             "class",
		"enum_item":               "enum",
		"trait_item":              "interface",
		"type_item":               "type",
	}
}

// ContainerKinds lists nodes the walker descends into.
//
//   - trait_item is hybrid (emits a Unit AND descends with containerKind=
//     "interface" so member function_signature_items become method Units).
//   - impl_item is a pure container: no Unit emitted; path extended by the
//     type name so methods land at "pkg.TypeName.method".
//   - mod_item is a pure container: path extended by the module name.
//   - declaration_list is the body node shared by impl/trait/mod blocks.
func (*RustGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"trait_item":       true, // hybrid
		"impl_item":        true, // pure
		"mod_item":         true, // pure
		"declaration_list": true, // pure body
	}
}

// SymbolName returns the declaration name for symbol and container nodes.
//
// Symbol nodes (in SymbolKinds) use their "name" field.
// Container-only nodes (impl_item, mod_item, declaration_list) return the
// type/module name when one exists, or "" to leave pathPrefix unchanged.
func (*RustGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "function_item", "function_signature_item",
		"struct_item", "enum_item", "trait_item", "type_item":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "impl_item":
		// Use the "type" field (the type being implemented), not the optional
		// "trait" field, so path is scoped by the struct/enum name. Strip
		// generic type parameters so `impl<T> Container<T>` scopes methods
		// under "Container" rather than "Container<T>" — angle brackets in
		// graph node IDs break lookups and corrupt symbol paths.
		if typeNode := n.ChildByFieldName("type"); typeNode != nil {
			return rustBareTypeName(typeNode, source)
		}
	case "mod_item":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "declaration_list":
		// Pure body — no name contribution.
	}
	return ""
}

// PackageName returns "" — Rust uses mod declarations for namespacing; there
// is no file-level package clause. The shared CodeHandler falls back to the
// file stem as the Unit.Path prefix (e.g., "lib.rs" → "lib").
func (*RustGrammar) PackageName(*sitter.Node, []byte) string { return "" }

// SymbolKindFor implements symbolKindResolver: promotes function_item nodes
// that live inside an impl_item from "function" to "method". This is needed
// because impl_item is a pure container (not in SymbolKinds), so the walker's
// normal container-kind-based rewrite (function→method when containerKind is
// a class-family kind) cannot fire for impl blocks.
//
// function_signature_item is already mapped to "method" in SymbolKinds and
// needs no promotion here.
func (*RustGrammar) SymbolKindFor(node *sitter.Node, _ []byte, declaredKind string) string {
	if declaredKind == "function" && findAncestor(node, "impl_item") != nil {
		return "method"
	}
	return ""
}

// Imports walks every use_declaration in the file and expands its argument
// into one ImportRef per imported binding. Grouped imports, aliases, and
// wildcards are all handled; see extractRustUse for the full shape matrix.
func (*RustGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "use_declaration" {
			return true
		}
		arg := n.ChildByFieldName("argument")
		if arg != nil {
			out = append(out, extractRustUse(arg, "", source)...)
		}
		return false
	})
	return out
}

// extractRustUse recursively expands a use_declaration argument node (or any
// inner node of a use tree) into ImportRefs. prefix is the path accumulated
// from outer scoped_use_list ancestors; empty at the top level.
//
// Handled shapes:
//   - identifier / self / super / crate     → {prefix::name}
//   - scoped_identifier                     → uses full text (already ::‑joined)
//   - use_as_clause                         → {path, alias}
//   - use_wildcard                          → {prefix::*}
//   - scoped_use_list                       → recurse into list with extended prefix
//   - use_list                              → recurse into each child
func extractRustUse(n *sitter.Node, prefix string, source []byte) []ImportRef {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case "identifier", "self", "super", "crate":
		name := n.Utf8Text(source)
		path := name
		if prefix != "" {
			path = prefix + "::" + name
		}
		return []ImportRef{{Path: path}}

	case "scoped_identifier":
		// Full path text like "std::io::Write" already contains "::" separators.
		path := n.Utf8Text(source)
		if prefix != "" {
			path = prefix + "::" + path
		}
		return []ImportRef{{Path: path}}

	case "use_as_clause":
		pathNode := n.ChildByFieldName("path")
		aliasNode := n.ChildByFieldName("alias")
		if pathNode == nil {
			return nil
		}
		path := pathNode.Utf8Text(source)
		if prefix != "" {
			path = prefix + "::" + path
		}
		ref := ImportRef{Path: path}
		if aliasNode != nil {
			ref.Alias = aliasNode.Utf8Text(source)
		}
		return []ImportRef{ref}

	case "use_wildcard":
		// The full text of use_wildcard already contains the path: "io::*",
		// "std::collections::*", "crate::*", etc. Prepend prefix only when
		// the wildcard lives inside a scoped_use_list (prefix is non-empty).
		text := n.Utf8Text(source)
		if prefix != "" {
			text = prefix + "::" + text
		}
		return []ImportRef{{Path: text}}

	case "scoped_use_list":
		// path field = the scoped prefix; list field = the use_list body.
		var newPrefix string
		if pathNode := n.ChildByFieldName("path"); pathNode != nil {
			p := pathNode.Utf8Text(source)
			if prefix != "" {
				newPrefix = prefix + "::" + p
			} else {
				newPrefix = p
			}
		} else {
			newPrefix = prefix
		}
		listNode := n.ChildByFieldName("list")
		if listNode == nil {
			return nil
		}
		return extractRustUse(listNode, newPrefix, source)

	case "use_list":
		var out []ImportRef
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			out = append(out, extractRustUse(c, prefix, source)...)
		}
		return out
	}
	return nil
}

// SymbolParents implements inheritanceExtractor for Rust trait items. Rust
// trait declarations can specify super-trait bounds: `trait Foo: Bar + Baz`.
// These bounds live in the "bounds" field of trait_item.
//
// Struct and enum items do not have direct parent declarations — trait
// implementations (impl Trait for Type) carry that relationship but are
// not surfaced via this extractor (impl_item is a pure container).
// Trait super-bounds are filed under Relation="extends" by convention
// matching the edge semantics of interface-extends-interface in other
// grammars.
func (*RustGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	if n.Kind() != "trait_item" {
		return nil
	}
	bounds := n.ChildByFieldName("bounds")
	if bounds == nil {
		return nil
	}
	var out []ParentRef
	for i := uint(0); i < bounds.NamedChildCount(); i++ {
		c := bounds.NamedChild(i)
		if c == nil {
			continue
		}
		name := rustBoundName(c, source)
		if name == "" {
			continue
		}
		out = append(out, ParentRef{
			Name:     name,
			Relation: "extends",
			Loc:      nodeLocation(c),
		})
	}
	return out
}

// rustBareTypeName returns the bare type identifier from a type node,
// stripping any generic parameters. Used by SymbolName for impl_item so that
// `impl<T> Container<T>` scopes under "Container" rather than "Container<T>".
//
// Handled forms:
//   - type_identifier          → "Container"  (bare)
//   - scoped_type_identifier   → "mod::Container" (qualified, no generics)
//   - generic_type             → extract the "type" child field, then recurse
//   - reference_type / pointer_type  → ignored (impl for &T / *T; uncommon)
func rustBareTypeName(n *sitter.Node, source []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "type_identifier", "scoped_type_identifier":
		return strings.TrimSpace(n.Utf8Text(source))
	case "generic_type":
		// generic_type has a "type" field for the base type identifier and a
		// "type_arguments" child for the parameter list. Recurse on the base.
		if base := n.ChildByFieldName("type"); base != nil {
			return rustBareTypeName(base, source)
		}
		// Fallback: first type_identifier named child.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && (c.Kind() == "type_identifier" || c.Kind() == "scoped_type_identifier") {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	}
	return ""
}

// rustBoundName extracts the trait name from a trait_bound node. Handles
// simple identifiers, scoped identifiers, and generic types (stripping the
// type parameter list). Lifetime bounds ('a) and for<> higher-ranked bounds
// are ignored (they carry no useful inheritance information).
func rustBoundName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "type_identifier", "scoped_type_identifier":
		return strings.TrimSpace(n.Utf8Text(source))
	case "generic_type":
		// Extract just the base type name from `Iterator<Item=T>`.
		if name := n.ChildByFieldName("type"); name != nil {
			return strings.TrimSpace(name.Utf8Text(source))
		}
		// Fallback: first type_identifier child.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type_identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	}
	return ""
}

// Compile-time interface checks.
var (
	_ Grammar              = (*RustGrammar)(nil)
	_ symbolKindResolver   = (*RustGrammar)(nil)
	_ inheritanceExtractor = (*RustGrammar)(nil)
)
