package code

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/kotlin"

	"librarian/internal/indexer"
)

// KotlinGrammar indexes .kt source files.
//
// Kotlin uses a single `class_declaration` node for classes, interfaces, and
// enum classes — the keyword (class / interface / enum class) is an anonymous
// child token, not a distinct AST type. This means every declaration emits
// Unit.Kind="class"; the interface-ness / data-ness / sealed-ness surfaces as
// label Signals via SymbolExtraSignals so consumers can still filter
// ("find all data classes", "find all sealed hierarchies") without an
// explosion of Unit.Kind strings.
//
// Inheritance (via `delegation_specifier` children of a class_declaration):
// Kotlin doesn't syntactically distinguish extends vs implements — both sit
// after a single `:`. We apply the idiomatic heuristic:
//   - interface X : A, B          → all targets land as relation="extends"
//   - class X : A(), B, C         → A() (constructor_invocation) is the
//                                   superclass (relation="extends"); B / C
//                                   (plain user_type) are implemented
//                                   interfaces (relation="implements").
//   - enum class X : I            → relation="implements" (enums can't extend).
// This is the standard reading of Kotlin inheritance syntax and matches what
// a human reviewer would write.
//
// Annotations: `@Composable`, `@Inject`, `@Serializable` etc. from the
// `modifiers` child are emitted as Signal{Kind="annotation"} via
// annotationExtractor. Class / function modifiers (data, sealed, inline,
// open, abstract, suspend) emit as Signal{Kind="label"} via
// extraSignalsExtractor.
//
// Extension functions: `fun String.toSlug()` — the receiver type surfaces in
// Unit.Content and Title but is NOT extracted as structured metadata today.
// Receiver-type extraction (so "all extensions of String" becomes a cheap
// label/metadata filter) is deferred to a follow-up bead — the Grammar
// interface would need a symbolMetadataExtractor hook since buildUnit in
// code.go does not populate Unit.Metadata from any existing extractor.
//
// Imports: `import foo.bar.Baz`, `import foo.bar.Baz as B`, `import foo.*`.
// Wildcards land with `metadata.wildcard=true`; aliases populate Alias.
type KotlinGrammar struct{}

// NewKotlinGrammar returns the Kotlin grammar implementation.
func NewKotlinGrammar() *KotlinGrammar { return &KotlinGrammar{} }

func (*KotlinGrammar) Name() string                             { return "kotlin" }
func (*KotlinGrammar) Extensions() []string                     { return []string{".kt"} }
func (*KotlinGrammar) Language() *sitter.Language               { return kotlin.GetLanguage() }
func (*KotlinGrammar) CommentNodeTypes() []string               { return []string{"line_comment", "multiline_comment"} }
func (*KotlinGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Kotlin AST node types to Unit.Kind values.
//
// class_declaration → "class" covers Kotlin's class / interface / enum class /
// annotation class. The interface / data / sealed / enum distinctions come
// through as label Signals rather than through Unit.Kind, so downstream code
// filtering by Kind stays simple and the "find all classes including
// interfaces" common query just works.
//
// object_declaration → "object" is Kotlin-specific (no analog in Java / JS
// today). Companion objects are a nested object_declaration inside a
// class_body with the `companion` modifier — the walker descends through
// class_body and emits the companion as its own Unit.
func (*KotlinGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"class_declaration":     "class",
		"object_declaration":    "object",
		"function_declaration":  "function",
		"property_declaration":  "property",
		"type_alias":            "type",
		"secondary_constructor": "constructor",
	}
}

// ContainerKinds descends into class / object bodies for nested member
// extraction. class_declaration and object_declaration are hybrid — emit a
// Unit for the container itself AND descend into their bodies.
//
// package_header / import_list / import_header / identifier are included
// because tree-sitter-kotlin's extent rules greedily absorb the trailing
// file-level KDoc into whichever of these nodes precedes the first class
// declaration. The trap nests: a file with no imports puts the KDoc in
// package_header; a file with imports can place it inside the last
// import_header; either form can nest one more level into the identifier
// child. Making all four pure containers lets the walker descend through
// the chain, buffer the trapped multiline_comment at whichever depth it
// landed, and — via the walker's tail-pending bubble — attach it to the
// class that follows at the file level.
//
// Kotlin is the only current grammar to register `identifier` as a
// container. That's a per-grammar choice: any future grammar that needs
// to descend into identifier (say, to rescue a trapped comment) is free
// to do the same without affecting the shared walker's cost for grammars
// that don't.
func (*KotlinGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"class_declaration":  true,
		"object_declaration": true,
		"class_body":         true,
		"enum_class_body":    true,
		"package_header":     true,
		"import_list":        true,
		"import_header":      true,
		"identifier":         true,
	}
}

// SymbolName returns the identifier for a Kotlin symbol node.
//
// property_declaration's name lives one level down in a variable_declaration
// (Kotlin separates the mutability / type-annotation structure from the
// name). Functions and classes use the standard "name" field but sometimes
// fall back to scanning for `simple_identifier` when the grammar version
// varies.
func (*KotlinGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Type() {
	case "class_declaration", "object_declaration",
		"function_declaration", "secondary_constructor", "type_alias":
		// Named field first; fall back to any direct simple_identifier /
		// type_identifier child (the typealias grammar variant exposes the
		// alias name as type_identifier without a field tag).
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Type() == "simple_identifier" || c.Type() == "type_identifier" {
				return c.Content(source)
			}
		}
	case "property_declaration":
		// A property wraps one or more variable_declaration children; take
		// the first. Kotlin allows `val (a, b) = pair` which produces
		// multi_variable_declaration — skip those for v1 (users can still
		// grep Unit.Content for the destructuring pattern).
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil || c.Type() != "variable_declaration" {
				continue
			}
			for j := 0; j < int(c.NamedChildCount()); j++ {
				cc := c.NamedChild(j)
				if cc != nil && cc.Type() == "simple_identifier" {
					return cc.Content(source)
				}
			}
		}
	}
	return ""
}

// PackageName extracts the dotted package from `package foo.bar`. Kotlin's
// package_header is the first statement in a file when present; return "" if
// absent (matches Python / JS stem fallback).
func (*KotlinGrammar) PackageName(root *sitter.Node, source []byte) string {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Type() != "package_header" {
			continue
		}
		// package_header's child is an identifier (possibly dotted — Kotlin's
		// grammar flattens `foo.bar.baz` into a single identifier node in
		// this context).
		for j := 0; j < int(c.NamedChildCount()); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			if cc.Type() == "identifier" || cc.Type() == "simple_identifier" {
				return strings.TrimSpace(cc.Content(source))
			}
		}
		// Fallback: content minus the leading "package " keyword.
		txt := strings.TrimSpace(c.Content(source))
		txt = strings.TrimPrefix(txt, "package")
		return strings.TrimSpace(txt)
	}
	return ""
}

// Imports walks every import_header. Handles three shapes:
//   - `import foo.bar.Baz`          → Path="foo.bar.Baz"
//   - `import foo.bar.Baz as B`     → Path="foo.bar.Baz", Alias="B"
//   - `import foo.bar.*`            → Path="foo.bar.*", Metadata["wildcard"]=true
func (*KotlinGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Type() != "import_header" {
			return true
		}
		ref := ImportRef{}
		wildcard := false
		var path string
		var alias string
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "identifier", "simple_identifier":
				if path == "" {
					path = strings.TrimSpace(c.Content(source))
				}
			case "import_alias":
				for j := 0; j < int(c.NamedChildCount()); j++ {
					cc := c.NamedChild(j)
					if cc != nil && (cc.Type() == "simple_identifier" || cc.Type() == "type_identifier") {
						alias = cc.Content(source)
						break
					}
				}
			case "wildcard_import":
				wildcard = true
			}
		}
		if path == "" {
			return false
		}
		if wildcard {
			path += ".*"
		}
		ref.Path = path
		ref.Alias = alias
		if wildcard {
			ref.Metadata = map[string]any{"wildcard": true}
		}
		out = append(out, ref)
		return false
	})
	return out
}

// SymbolAnnotations implements annotationExtractor. Walks the symbol's
// `modifiers` child for `annotation` nodes and returns each annotation's
// name without the leading `@`. Argument lists (`@Deprecated("msg")`) are
// dropped; the name alone is the signal value.
func (*KotlinGrammar) SymbolAnnotations(n *sitter.Node, source []byte) []string {
	mods := kotlinModifiersNode(n)
	if mods == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(mods.NamedChildCount()); i++ {
		c := mods.NamedChild(i)
		if c == nil || c.Type() != "annotation" {
			continue
		}
		if name := kotlinAnnotationName(c, source); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// SymbolExtraSignals implements extraSignalsExtractor. Surfaces Kotlin
// modifiers that aren't annotations but carry semantic meaning:
//
//   - data / sealed / open / abstract / inline (class modifiers)
//   - override (member modifier) — signals "this overrides a parent"
//   - suspend (function modifier) — coroutine-aware function
//   - enum — emitted when the class_declaration's keyword is `enum class`
//   - interface — emitted when the keyword is `interface`
//
// Each becomes a Signal{Kind="label"} so a retrieval query like "find all
// data classes" hits a cheap label filter rather than re-parsing the file.
func (*KotlinGrammar) SymbolExtraSignals(n *sitter.Node, source []byte) []indexer.Signal {
	var out []indexer.Signal
	if n.Type() == "class_declaration" {
		if hasAnonymousChild(n, "interface") {
			out = append(out, indexer.Signal{Kind: "label", Value: "interface"})
			// `fun interface Foo` (Kotlin 1.5 SAM) — surfaces as an
			// extra label so callers can distinguish functional
			// interfaces from plain ones without re-parsing.
			if hasAnonymousChild(n, "fun") {
				out = append(out, indexer.Signal{Kind: "label", Value: "fun_interface"})
			}
		}
		if hasAnonymousChild(n, "enum") {
			out = append(out, indexer.Signal{Kind: "label", Value: "enum"})
		}
	}
	mods := kotlinModifiersNode(n)
	if mods == nil {
		return out
	}
	for i := 0; i < int(mods.NamedChildCount()); i++ {
		c := mods.NamedChild(i)
		if c == nil {
			continue
		}
		// class_modifier / function_modifier / member_modifier wrap a
		// single keyword child; inheritance_modifier wraps `open` /
		// `abstract` / `final` / `sealed`. Descend one level.
		switch c.Type() {
		case "class_modifier", "function_modifier", "member_modifier",
			"inheritance_modifier", "visibility_modifier":
			keyword := strings.TrimSpace(c.Content(source))
			if keyword == "" {
				continue
			}
			switch keyword {
			case "data", "sealed", "open", "abstract", "inline", "value",
				"override", "suspend", "companion":
				// `value` is the Kotlin 1.5+ successor to `inline class`
				// — both emit as labels so "find all value classes"
				// matches either syntax form.
				out = append(out, indexer.Signal{Kind: "label", Value: keyword})
			}
		}
	}
	return out
}

// SymbolParents implements inheritanceExtractor. Returns the class header's
// delegation_specifier targets, mapped to extends/implements by the Kotlin
// heuristic (constructor_invocation = extends, bare user_type = implements,
// with interface / enum class declarations overriding the default mapping).
func (*KotlinGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	if n.Type() != "class_declaration" && n.Type() != "object_declaration" {
		return nil
	}

	isInterface := hasAnonymousChild(n, "interface")
	isEnum := hasAnonymousChild(n, "enum")

	var out []ParentRef
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Type() != "delegation_specifier" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			target := c.NamedChild(j)
			if target == nil {
				continue
			}
			name, args := kotlinExtractParentType(target, source)
			if name == "" {
				continue
			}
			// Default: bare user_type on a class → implemented interface;
			// enum class targets → implemented interface (enums can't
			// extend). Only two cases override that default.
			relation := "implements"
			switch {
			case isInterface:
				// `interface X : Y, Z` — every target is a parent interface.
				relation = "extends"
			case !isEnum && target.Type() == "constructor_invocation":
				// Class with a concrete superclass (`: Base()`). Excluded
				// for enums because an `enum class X : I()` would be a
				// syntax error anyway.
				relation = "extends"
			}
			meta := map[string]any{}
			if len(args) > 0 {
				meta["type_args"] = args
			}
			out = append(out, ParentRef{
				Name:     name,
				Relation: relation,
				Loc: indexer.Location{
					Line:       int(target.StartPoint().Row) + 1,
					Column:     int(target.StartPoint().Column) + 1,
					ByteOffset: int(target.StartByte()),
				},
				Metadata: meta,
			})
		}
	}
	return out
}

// kotlinExtractParentType teases a delegation_specifier child into
// (baseName, typeArgs). Three shapes arise:
//   - user_type                  → `Foo`, possibly with type_arguments child
//   - constructor_invocation     → `Foo()` — wraps a user_type with args
//   - explicit_delegation        → `Foo by bar` — interface delegation; the
//                                  user_type is the interface being
//                                  delegated
func kotlinExtractParentType(n *sitter.Node, source []byte) (string, []string) {
	switch n.Type() {
	case "user_type":
		return kotlinUserTypeName(n, source)
	case "constructor_invocation", "explicit_delegation":
		// constructor_invocation wraps a user_type + value_arguments
		// (`Base(arg)`); explicit_delegation wraps a user_type + `by`
		// expression (`Foo by bar`). Both expose the target type as a
		// direct user_type child — the rest is arguments / delegate we
		// don't need for inheritance edges.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type() == "user_type" {
				return kotlinUserTypeName(c, source)
			}
		}
	}
	return "", nil
}

// kotlinUserTypeName returns (name, typeArgs) from a user_type. Kotlin
// user_types nest for dotted names (`foo.Bar` has structure user_type >
// simple_user_type (foo) + simple_user_type (Bar)). Content of the full
// node is the dotted form.
func kotlinUserTypeName(n *sitter.Node, source []byte) (string, []string) {
	// Collect the identifier components (simple_identifier children of
	// nested simple_user_type nodes) for the base name.
	var ids []string
	var args []string
	var collect func(*sitter.Node)
	collect = func(node *sitter.Node) {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			c := node.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "simple_user_type":
				collect(c)
			case "simple_identifier", "type_identifier":
				if len(args) == 0 { // only collect ids before a type_arguments block
					ids = append(ids, c.Content(source))
				}
			case "type_arguments":
				for j := 0; j < int(c.NamedChildCount()); j++ {
					if a := c.NamedChild(j); a != nil {
						if t := strings.TrimSpace(a.Content(source)); t != "" {
							args = append(args, t)
						}
					}
				}
			}
		}
	}
	collect(n)
	name := strings.Join(ids, ".")
	if name == "" {
		// Fallback: use the raw content; strip any `<...>` generics.
		name = strings.TrimSpace(n.Content(source))
		if idx := strings.Index(name, "<"); idx > 0 {
			name = name[:idx]
		}
	}
	return name, args
}

// ResolveParents implements inheritanceResolver. Mirrors Java's same-file
// import lookup: bare names in delegation_specifiers get rewritten to their
// fully-qualified form using non-wildcard imports in the file. Unresolved
// names land with metadata["unresolved"]=true.
func (*KotlinGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	local := kotlinLocalTypeImports(refs)
	for i, r := range refs {
		if r.Kind != "inherits" {
			continue
		}
		if strings.Contains(r.Target, ".") {
			continue
		}
		if full, ok := local[r.Target]; ok {
			refs[i].Target = full
			continue
		}
		if refs[i].Metadata == nil {
			refs[i].Metadata = map[string]any{}
		}
		refs[i].Metadata["unresolved"] = true
	}
	return refs
}

// kotlinLocalTypeImports maps a Kotlin file's non-wildcard imports to
// short-name → fully-qualified-name. Aliased imports (`import foo.Bar as B`)
// bind the alias as the local name; wildcard imports (`import foo.*`)
// provide no binding.
func kotlinLocalTypeImports(refs []indexer.Reference) map[string]string {
	out := map[string]string{}
	for _, r := range refs {
		if r.Kind != "import" || r.Target == "" {
			continue
		}
		if strings.HasSuffix(r.Target, ".*") {
			continue
		}
		local := ""
		if r.Metadata != nil {
			if a, ok := r.Metadata["alias"].(string); ok {
				local = a
			}
		}
		if local == "" {
			if dot := strings.LastIndex(r.Target, "."); dot >= 0 && dot < len(r.Target)-1 {
				local = r.Target[dot+1:]
			} else {
				local = r.Target
			}
		}
		if _, seen := out[local]; seen {
			continue
		}
		out[local] = r.Target
	}
	return out
}

// kotlinModifiersNode returns the `modifiers` child of a declaration node,
// or nil. Kotlin puts annotations + modifiers in a shared wrapper.
func kotlinModifiersNode(n *sitter.Node) *sitter.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type() == "modifiers" {
			return c
		}
	}
	return nil
}

// kotlinAnnotationName extracts the identifier from an annotation node.
// `@Deprecated(...)` → "Deprecated"; `@foo.Bar` → "foo.Bar".
func kotlinAnnotationName(n *sitter.Node, source []byte) string {
	// Structure varies: annotation > (single_annotation | multi_annotation) >
	// user_type. Walk one level of children looking for user_type /
	// constructor_invocation / simple_identifier.
	var probe func(*sitter.Node) string
	probe = func(node *sitter.Node) string {
		for i := 0; i < int(node.NamedChildCount()); i++ {
			c := node.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "user_type":
				name, _ := kotlinUserTypeName(c, source)
				if name != "" {
					return name
				}
			case "constructor_invocation":
				for j := 0; j < int(c.NamedChildCount()); j++ {
					if cc := c.NamedChild(j); cc != nil && cc.Type() == "user_type" {
						name, _ := kotlinUserTypeName(cc, source)
						if name != "" {
							return name
						}
					}
				}
			case "simple_identifier", "type_identifier":
				return c.Content(source)
			case "single_annotation", "multi_annotation", "annotation_use_site_target":
				if r := probe(c); r != "" {
					return r
				}
			}
		}
		return ""
	}
	return probe(n)
}
