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
// Extension functions: `fun String.toSlug()` — the receiver type lands
// on Unit.Metadata["receiver"] via SymbolMetadata, so "all extensions of
// String" is a cheap metadata filter. Generic receivers strip the type
// arguments (`fun List<T>.head()` → receiver="List", type args drop).
//
// Imports: `import foo.bar.Baz`, `import foo.bar.Baz as B`, `import foo.*`.
// Wildcards land with `metadata.wildcard=true`; aliases populate Alias.
//
// Known tree-sitter-kotlin gaps (upstream grammar limitations, NOT
// librarian bugs):
//
//   - `fun interface` (Kotlin 1.5 SAM) parses as an ERROR node containing
//     a stray user_type for `interface` and a lambda_literal wrapping the
//     body. No class_declaration is emitted, so there's no Unit to attach
//     a `fun_interface` label to.
//   - `context(Scope)` receivers (Kotlin 1.6 experimental) parse as a
//     top-level call_expression rather than a distinct context_receivers
//     node. The walker treats that as a non-symbol / non-container /
//     non-comment child, which clears the pending-comment buffer — so a
//     KDoc preceding `context(...)` fails to attach to the following
//     function. Working around this would require teaching the walker to
//     peek at call_expression bodies, which is language-specific pollution.
//
// Both unblock once the upstream smacker/go-tree-sitter kotlin grammar
// adds support. Tracked in lib-ljn.
type KotlinGrammar struct{}

// NewKotlinGrammar returns the Kotlin grammar implementation.
func NewKotlinGrammar() *KotlinGrammar { return &KotlinGrammar{} }

// kotlinLabelModifiers is the allow-list of Kotlin modifier keywords that
// surface as label Signals via SymbolExtraSignals. See that function's
// godoc for the category breakdown. Visibility modifiers (public / private
// / internal / protected) are deliberately omitted — they apply to every
// declaration and add noise rather than signal.
var kotlinLabelModifiers = map[string]bool{
	// class_modifier
	"data":       true,
	"sealed":     true,
	"annotation": true,
	"inner":      true,
	"value":      true,
	"companion":  true,
	// inheritance_modifier
	"open":     true,
	"abstract": true,
	// member_modifier
	"override": true,
	"lateinit": true,
	// function_modifier
	"inline":   true,
	"tailrec":  true,
	"suspend":  true,
	"external": true,
	"operator": true,
	"infix":    true,
	// property_modifier
	"const": true,
	// parameter_modifier
	"noinline":    true,
	"crossinline": true,
	// platform_modifier (Kotlin Multiplatform `expect` / `actual`; note
	// the tree-sitter node name is platform_modifier, not the
	// spec-colloquial multiplatform_modifier)
	"expect": true,
	"actual": true,
}

func (*KotlinGrammar) Name() string               { return "kotlin" }
func (*KotlinGrammar) Extensions() []string       { return []string{".kt"} }
func (*KotlinGrammar) Language() *sitter.Language { return kotlin.GetLanguage() }

func (*KotlinGrammar) CommentNodeTypes() []string {
	return []string{"line_comment", "multiline_comment"}
}
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
		"companion_object":      "object", // `companion object { ... }` — emit a Unit AND descend into its class_body
		"function_declaration":  "function",
		"property_declaration":  "property",
		"class_parameter":       "property", // `class Foo(val id: String)` — val/var params are properties; SymbolName returns "" for plain params without binding_pattern_kind
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
		"class_declaration":   true,
		"object_declaration":  true,
		"companion_object":    true, // hybrid — emit Unit (via SymbolKinds above) AND descend into class_body
		"class_body":          true,
		"enum_class_body":     true,
		"primary_constructor": true, // descend into class_parameter children so val/var properties get Units
		"package_header":      true,
		"import_list":         true,
		"import_header":       true,
		"identifier":          true,
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
	case "secondary_constructor":
		// Kotlin's secondary_constructor has no `name` field and no
		// simple_identifier name — `constructor` is a keyword token. The
		// Unit.Title should be the enclosing class's name, mirroring
		// Java's constructor convention (Unit.Path ends with
		// `<package>.<Class>.<Class>`).
		p := findAncestor(n, "class_declaration", "object_declaration")
		if p == nil {
			return ""
		}
		if name := p.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
		for i := 0; i < int(p.NamedChildCount()); i++ {
			c := p.NamedChild(i)
			if c != nil && (c.Type() == "simple_identifier" || c.Type() == "type_identifier") {
				return c.Content(source)
			}
		}
		return ""
	case "class_declaration", "object_declaration",
		"function_declaration", "type_alias":
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
	case "class_parameter":
		// Primary-constructor parameters are properties ONLY when they
		// carry `val` or `var`: `class Foo(val id: String, plain: Int)`
		// → id becomes a property, plain is just a ctor arg. Filter by
		// presence of binding_pattern_kind — return "" to skip plain
		// params (the walker treats empty as "not a symbol to emit").
		hasBinding := false
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type() == "binding_pattern_kind" {
				hasBinding = true
				break
			}
		}
		if !hasBinding {
			return ""
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Type() == "simple_identifier" {
				return c.Content(source)
			}
		}
	case "companion_object":
		// `companion object` without a name defaults to "Companion" in
		// Kotlin; `companion object Factory` has an explicit identifier.
		// Return the explicit name if present, else the implicit default.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c != nil && (c.Type() == "simple_identifier" || c.Type() == "type_identifier") {
				return c.Content(source)
			}
		}
		return "Companion"
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
// modifiers that aren't annotations but carry semantic meaning, as
// Signal{Kind="label"} entries. Querying "find all data classes" /
// "find all lateinit properties" / "find all suspend functions" becomes
// a cheap label filter rather than re-parsing source.
//
// Modifier coverage (organised by tree-sitter-kotlin modifier category):
//
//   - class_modifier:           data, sealed, annotation, inner, value
//   - inheritance_modifier:     open, abstract
//   - member_modifier:          override, lateinit
//   - function_modifier:        inline, tailrec, suspend, external,
//                               operator, infix
//   - property_modifier:        const
//   - parameter_modifier:       noinline, crossinline
//   - reification_modifier:     reified
//   - platform_modifier:        expect, actual (the tree-sitter node is
//                                 named platform_modifier, not the
//                                 spec-colloquial multiplatform_modifier)
//   - visibility_modifier:      (not emitted — `private` / `internal` etc.
//                                are runtime-visible on every declaration
//                                and add noise rather than signal)
//
// Extra class_declaration-level labels (derived from the anonymous keyword
// child rather than a named modifier):
//
//   - interface      — keyword == "interface"
//   - fun_interface  — keyword == "interface" AND "fun" anon child present
//   - enum           — keyword == "enum"
//
// `companion` is also a class_modifier but applies to companion-object
// declarations. Emitted as its keyword name so the companion's Unit carries
// a `companion` label.
func (*KotlinGrammar) SymbolExtraSignals(n *sitter.Node, source []byte) []indexer.Signal {
	var out []indexer.Signal
	if n.Type() == "class_declaration" {
		if hasAnonymousChild(n, "interface") {
			out = append(out, indexer.Signal{Kind: "label", Value: "interface"})
		}
		if hasAnonymousChild(n, "enum") {
			out = append(out, indexer.Signal{Kind: "label", Value: "enum"})
		}
	}
	// `companion object { ... }` is a distinct node type in tree-sitter-kotlin
	// (not an object_declaration with a modifier). The `companion` keyword is
	// intrinsic to the node, so emit the label by node-type check rather than
	// by scanning modifiers.
	if n.Type() == "companion_object" {
		out = append(out, indexer.Signal{Kind: "label", Value: "companion"})
	}
	// Reified type parameters live in type_parameter_modifiers inside
	// type_parameters (sibling of `modifiers`), not under the function's
	// main modifiers block. Use the shared walker to find any reification
	// marker without stepping into the function body — emit at most one
	// `reified` label regardless of how many type parameters are reified,
	// matching how `data` / `sealed` dedupe on class-level modifiers.
	//
	// Gated to `function_declaration` because only functions carry reified
	// type parameters. Running this on a class_declaration would walk the
	// class_body into member functions and leak their `reified` up to the
	// class itself (classes can't be reified). No test caught that leak
	// because the modifier-coverage test only asserts positive labels.
	if n.Type() == "function_declaration" {
		reified := false
		walk(n, func(c *sitter.Node) bool {
			switch c.Type() {
			case "function_body":
				// Don't descend into bodies — a `reified` keyword inside
				// the body (in a nested lambda's type params, say) isn't
				// a modifier of the outer function.
				return false
			case "reification_modifier":
				reified = true
				return false
			}
			return true
		})
		if reified {
			out = append(out, indexer.Signal{Kind: "label", Value: "reified"})
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
		// Each modifier wrapper node contains a single keyword child
		// whose text is the modifier name. Descend one level and filter
		// against the allow-list so stray / visibility / junk keywords
		// don't leak into signals. visibility_modifier is deliberately
		// NOT in the list (see function godoc).
		switch c.Type() {
		// reification_modifier is NOT listed here — it lives under
		// type_parameter_modifiers inside type_parameters (a sibling of
		// the function's `modifiers` node), not under `modifiers` itself.
		// The dedicated walk() above handles reified.
		case "class_modifier", "function_modifier", "member_modifier",
			"inheritance_modifier", "property_modifier",
			"parameter_modifier", "platform_modifier":
			keyword := strings.TrimSpace(c.Content(source))
			if keyword == "" {
				continue
			}
			if kotlinLabelModifiers[keyword] {
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
// import lookup via the shared helpers: bare names in delegation_specifiers
// get rewritten to their fully-qualified form using non-wildcard imports
// in the file. Unresolved names land with metadata["unresolved"]=true.
func (*KotlinGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	return resolveInheritsRefs(refs, localTypeBindings(refs, false /* skipStatic */))
}

// SymbolMetadata implements symbolMetadataExtractor. Currently surfaces the
// receiver type of extension functions as Metadata["receiver"]. Kotlin's
// extension syntax puts the receiver as an unfielded type node BEFORE the
// function's simple_identifier inside function_declaration:
//
//	fun String.toSlug(): String = ...    → user_type=String, then simple_identifier=toSlug
//	fun List<Int>.sum(): Int = 0          → user_type=List<Int>, kotlinUserTypeName strips generics
//	fun String?.safeLowercase(): String   → nullable_type wrapping user_type=String
//
// Non-extension functions (`fun plain()`) don't have a preceding type
// child; SymbolMetadata returns nil. Generic type arguments on the receiver
// are dropped from the metadata value (only the base type name lands, so
// "List" not "List<Int>") to keep the filter predicate consistent, and the
// nullable marker is stripped (`String?` receivers and `String` receivers
// both land as Metadata["receiver"]="String") so downstream queries don't
// have to normalise.
func (*KotlinGrammar) SymbolMetadata(n *sitter.Node, source []byte) map[string]any {
	if n.Type() != "function_declaration" {
		return nil
	}
	// Walk named children looking for a receiver type that appears BEFORE
	// the simple_identifier (the function name). The only valid
	// type-before-name shape is an extension receiver. nullable_type
	// wraps a user_type for `String?`-style nullable receivers; unwrap
	// one level before handing to kotlinUserTypeName.
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "user_type":
			name, _ := kotlinUserTypeName(c, source)
			if name == "" {
				return nil
			}
			return map[string]any{"receiver": name}
		case "nullable_type":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				inner := c.NamedChild(j)
				if inner != nil && inner.Type() == "user_type" {
					name, _ := kotlinUserTypeName(inner, source)
					if name == "" {
						return nil
					}
					return map[string]any{"receiver": name}
				}
			}
			return nil
		case "simple_identifier":
			// Reached the name without seeing a receiver type — this is
			// a plain (non-extension) function.
			return nil
		}
	}
	return nil
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
