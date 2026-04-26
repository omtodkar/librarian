package code

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code/tree_sitter_swift"
)

// SwiftGrammar indexes .swift source files.
//
// Tree-sitter-swift unifies class / struct / enum / extension under one
// `class_declaration` node, differentiated only by an anonymous keyword
// child — symbolKindResolver refines Unit.Kind at symbol-emission time
// so downstream queries ("find all structs", "find all extensions of
// String") stay first-class. `protocol_declaration` is a distinct node
// and maps to Unit.Kind="protocol".
//
// Inheritance heuristic — Swift's single `:` doesn't syntactically
// distinguish superclass from protocol conformance, so per-flavor rules
// apply:
//
//   - class:            first inheritance_specifier = extends, rest = conforms
//   - struct / enum:    all = conforms (neither can have a superclass)
//   - extension:        all = conforms
//   - protocol:         all = extends (interface-extends-interface)
//
// `class Foo: SomeProtocol` (class with only a protocol conformance,
// no superclass) is a known misclassification the heuristic can't catch
// without resolving the target's kind — a future resolver pass,
// tracked alongside lib-38i's cross-file name resolution.
//
// Extensions are a first-class Unit kind: `extension String {}` emits
// Unit{Kind:"extension", Title:"String", Metadata["extends_type"]:"String"},
// and members inside carry Metadata["receiver"]="String" — parallels
// Kotlin's extension-function receiver convention.
type SwiftGrammar struct{}

// NewSwiftGrammar returns the Swift grammar implementation.
func NewSwiftGrammar() *SwiftGrammar { return &SwiftGrammar{} }

func (*SwiftGrammar) Name() string               { return "swift" }
func (*SwiftGrammar) Extensions() []string       { return []string{".swift"} }
func (*SwiftGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_swift.Language()) }

func (*SwiftGrammar) CommentNodeTypes() []string {
	return []string{"comment", "multiline_comment"}
}

func (*SwiftGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

func (*SwiftGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"class_declaration":             "class", // refined to class/struct/enum/extension via SymbolKindFor
		"protocol_declaration":          "protocol",
		"function_declaration":          "function",
		"protocol_function_declaration": "method", // protocol requirement — always a method-shape
		"init_declaration":              "constructor",
		"property_declaration":          "property",
		"protocol_property_declaration": "property",
		"typealias_declaration":         "type",
		"subscript_declaration":         "method", // `subscript(i: Int) -> T` — callable member, shape matches a method
		"associatedtype_declaration":    "type",   // protocol `associatedtype Element` — a deferred type binding
	}
}

func (*SwiftGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"class_declaration":    true, // hybrid
		"protocol_declaration": true, // hybrid
		"class_body":           true, // struct / class / extension body
		"enum_class_body":      true, // enum body (naming is a tree-sitter-swift quirk — enums aren't classes)
		"protocol_body":        true,
	}
}

// SymbolKindFor refines Kind for Swift's lumped class_declaration via the
// anonymous keyword child. Returns "" for other node types to leave the
// walker's existing Kind intact.
func (g *SwiftGrammar) SymbolKindFor(n *sitter.Node, _ []byte, _ string) string {
	if n.Kind() != "class_declaration" {
		return ""
	}
	return swiftClassFlavor(n)
}

// swiftClassFlavor dispatches on the anonymous keyword child; "class" is
// the default because class_declaration without an explicit
// extension/struct/enum token IS a class.
func swiftClassFlavor(n *sitter.Node) string {
	switch {
	case hasAnonymousChild(n, "extension"):
		return "extension"
	case hasAnonymousChild(n, "struct"):
		return "struct"
	case hasAnonymousChild(n, "enum"):
		return "enum"
	default:
		return "class"
	}
}

// SymbolName returns the Unit.Title for a Swift declaration.
//
// For extensions: the target type comes BEFORE the colon as a user_type
// child (no type_identifier for the extension's own name, since there
// isn't one). Returning the target type's base name lets "extensions of
// String" be grep-able by Unit.Title.
//
// For class / struct / enum / protocol / typealias: the name is a
// type_identifier or simple_identifier direct child.
//
// For function / init / property / protocol requirements: standard
// simple_identifier.
func (*SwiftGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "class_declaration":
		if swiftClassFlavor(n) == "extension" {
			return swiftExtensionTargetName(n, source)
		}
		// Regular class / struct / enum — name is a type_identifier.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type_identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	case "protocol_declaration", "typealias_declaration",
		"associatedtype_declaration":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type_identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	case "subscript_declaration":
		// No name — Swift subscripts are addressed by the "subscript"
		// keyword, similar in spirit to init_declaration. Multiple
		// subscript overloads in one type will collide at the Path
		// level; overload disambiguation is deferred (same gap tracked
		// for Java/Kotlin in lib-7wz).
		return "subscript"
	case "function_declaration", "protocol_function_declaration",
		"protocol_property_declaration":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "simple_identifier" {
				return c.Utf8Text(source)
			}
		}
	case "init_declaration":
		// init carries no name — Swift's constructor keyword is
		// semantically "init". Return "init" as the Title so the Unit is
		// queryable; Path will be class.init (similar in spirit to
		// Java's Class.Class).
		return "init"
	case "property_declaration":
		// A property's name lives inside a `pattern` node, or directly
		// as a simple_identifier child depending on the declaration
		// shape. Dig one level: pattern → simple_identifier. If no
		// pattern, look at direct simple_identifier.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Kind() == "pattern" {
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc != nil && cc.Kind() == "simple_identifier" {
						return cc.Utf8Text(source)
					}
				}
			}
			if c.Kind() == "simple_identifier" {
				return c.Utf8Text(source)
			}
		}
	}
	return ""
}

// PackageName — Swift modules aren't declared in source (they come from
// the build config / file's module membership, which tree-sitter can't
// see). Return "" so the shared Parse path falls back to the file stem
// for Unit.Path prefixes.
func (*SwiftGrammar) PackageName(*sitter.Node, []byte) string { return "" }

// Imports walks every import_declaration. Four shapes:
//
//   - `import UIKit`                    → Path="UIKit"
//   - `import class Foo.Bar`            → Path="Foo.Bar" (import kind declaration — the `class` keyword is an anonymous token; we don't track it in v1)
//   - `@testable import MyCore`         → Path="MyCore", Metadata["testable"]=true
//   - `import Foo.Sub`                  → Path="Foo.Sub" (submodule)
func (*SwiftGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "import_declaration" {
			return true
		}
		ref := ImportRef{}
		testable := false
		// Scan children for:
		//   - modifiers → attribute → user_type → type_identifier "testable"
		//   - identifier (the module path), possibly dotted via
		//     nested simple_identifier children
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "modifiers":
				if swiftHasAttribute(c, source, "testable") {
					testable = true
				}
			case "identifier":
				// Collect dotted path from simple_identifier children.
				var parts []string
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc != nil && cc.Kind() == "simple_identifier" {
						parts = append(parts, cc.Utf8Text(source))
					}
				}
				if len(parts) > 0 {
					ref.Path = strings.Join(parts, ".")
				} else {
					ref.Path = strings.TrimSpace(c.Utf8Text(source))
				}
			}
		}
		if ref.Path == "" {
			return false
		}
		if testable {
			ref.Metadata = map[string]any{"testable": true}
		}
		out = append(out, ref)
		return false
	})
	return out
}

// SymbolAnnotations implements annotationExtractor for Swift. Attributes
// sit inside the `modifiers` child (mirroring Kotlin). Each attribute
// wraps a user_type whose type_identifier carries the attribute name —
// return it without the `@` prefix, matching the Java / Kotlin
// annotation convention.
func (*SwiftGrammar) SymbolAnnotations(n *sitter.Node, source []byte) []string {
	mods := modifiersNode(n)
	if mods == nil {
		return nil
	}
	var out []string
	for i := uint(0); i < mods.NamedChildCount(); i++ {
		c := mods.NamedChild(i)
		if c == nil || c.Kind() != "attribute" {
			continue
		}
		if name := swiftAttributeName(c, source); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// SymbolExtraSignals implements extraSignalsExtractor. Surfaces Swift
// modifier keywords and class-flavor markers as Signal{Kind="label"}
// entries. Visibility modifiers (public / private / fileprivate /
// internal) are deliberately omitted — they're present on most
// declarations and add noise rather than signal, matching Kotlin's rule.
//
// Covered modifiers:
//
//   - mutating / nonmutating / isolated / nonisolated       (Swift concurrency + value-type)
//   - final / open / dynamic                                 (inheritance / dispatch)
//   - override / required / convenience                      (inheritance / init kind)
//   - static / class                                         (type-level members; `class` keyword on a method means "overridable static")
//   - weak / unowned / lazy                                  (property attributes)
//   - indirect                                               (recursive enum cases)
//
// class-flavor labels for class_declaration: "struct" / "enum" /
// "extension" when the keyword is not "class" (the default Kind for a
// class-keyword declaration is "class", no extra label needed). This
// mirrors Kotlin emitting "interface" / "enum" labels for its unified
// class_declaration.
func (g *SwiftGrammar) SymbolExtraSignals(n *sitter.Node, source []byte) []indexer.Signal {
	var out []indexer.Signal
	if n.Kind() == "class_declaration" {
		switch swiftClassFlavor(n) {
		case "struct":
			out = append(out, indexer.Signal{Kind: "label", Value: "struct"})
		case "enum":
			out = append(out, indexer.Signal{Kind: "label", Value: "enum"})
		case "extension":
			out = append(out, indexer.Signal{Kind: "label", Value: "extension"})
		}
		// `indirect enum Tree` marks a recursive enum. tree-sitter-swift
		// emits the `indirect` keyword as an anonymous token of
		// class_declaration rather than as a modifier node.
		if hasAnonymousChild(n, "indirect") {
			out = append(out, indexer.Signal{Kind: "label", Value: "indirect"})
		}
	}
	mods := modifiersNode(n)
	if mods == nil {
		return out
	}
	for i := uint(0); i < mods.NamedChildCount(); i++ {
		c := mods.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		// Swift's tree-sitter splits modifiers across several node
		// categories. We include `visibility_modifier` here specifically
		// so `open` (which Swift treats as a stricter-than-public visibility)
		// surfaces as a label; other visibility values (public / private /
		// internal / fileprivate / package) are filtered by the
		// swiftLabelModifiers allow-list below.
		case "property_modifier", "property_behavior_modifier",
			"member_modifier", "function_modifier",
			"inheritance_modifier", "mutation_modifier",
			"ownership_modifier", "parameter_modifier",
			"visibility_modifier":
			keyword := strings.TrimSpace(c.Utf8Text(source))
			if keyword == "" {
				continue
			}
			if swiftLabelModifiers[keyword] {
				out = append(out, indexer.Signal{Kind: "label", Value: keyword})
			}
		}
	}
	return out
}

// swiftLabelModifiers is the allow-list of Swift modifier keywords that
// surface as label Signals. Visibility modifiers (public / private /
// fileprivate / internal / package) are deliberately omitted — they
// apply to nearly every declaration and add noise rather than signal.
var swiftLabelModifiers = map[string]bool{
	// inheritance / dispatch
	"final":   true,
	"open":    true,
	"dynamic": true,
	// member / inheritance
	"override":    true,
	"required":    true,
	"convenience": true,
	// type-level
	"static": true,
	// Note: `class` (as in `class func overridable()`) is a Swift
	// type-member modifier but tree-sitter-swift discards the keyword
	// during parsing — no `modifiers` child is emitted — so we have no
	// way to label it without scanning raw source text. Tracked upstream.
	// value-type / concurrency
	"mutating":    true,
	"nonmutating": true,
	"isolated":    true,
	"nonisolated": true,
	// property attributes
	"weak":    true,
	"unowned": true,
	"lazy":    true,
}

// SymbolParents implements inheritanceExtractor. Emits one ParentRef per
// inheritance_specifier child of class_declaration / protocol_declaration,
// with Relation set per the per-flavor heuristic described in the file's
// package doc.
func (*SwiftGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	var flavor string
	switch n.Kind() {
	case "class_declaration":
		flavor = swiftClassFlavor(n)
	case "protocol_declaration":
		flavor = "protocol"
	default:
		return nil
	}

	var out []ParentRef
	seen := 0 // index of inheritance_specifier encountered so far (0 = first)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "inheritance_specifier" {
			continue
		}
		// Extract the user_type inside the specifier; ignore generic
		// constraints / attributes attached to the conformance.
		var typeNode *sitter.Node
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			if cc.Kind() == "user_type" {
				typeNode = cc
				break
			}
		}
		if typeNode == nil {
			continue
		}
		name, args := swiftUserTypeWithGenerics(typeNode, source)
		if name == "" {
			continue
		}
		relation := swiftRelationFor(flavor, seen)
		meta := map[string]any{}
		if len(args) > 0 {
			meta["type_args"] = args
		}
		out = append(out, ParentRef{
			Name:     name,
			Relation: relation,
			Loc: indexer.Location{
				Line:       int(typeNode.StartPosition().Row) + 1,
				Column:     int(typeNode.StartPosition().Column) + 1,
				ByteOffset: int(typeNode.StartByte()),
			},
			Metadata: meta,
		})
		seen++
	}
	return out
}

// swiftRelationFor applies the per-flavor heuristic from the package
// doc: class flavor's first specifier = extends (usually the superclass)
// and the rest = conforms (protocols); struct / enum / extension treat
// every specifier as a conformance; protocol's specifiers are all
// "extends" (interface-extends-interface).
func swiftRelationFor(flavor string, index int) string {
	switch flavor {
	case "class":
		if index == 0 {
			return "extends"
		}
		return "conforms"
	case "protocol":
		return "extends"
	default: // struct, enum, extension
		return "conforms"
	}
}

// ResolveParents implements inheritanceResolver. Swift module imports
// don't bind type names at the file level the way Java's
// `import com.foo.Bar` does — `import Foundation` says nothing about
// which types from Foundation are referenced — so localTypeBindings
// almost always returns an empty map for Swift and bare parents stay
// unresolved. The one case that does bind is `import class Foo.Bar`
// (and `import struct`/`enum`/`protocol` variants), which surface
// "Foo.Bar" as a dotted import Target; localTypeBindings's tail-segment
// heuristic picks up "Bar" from those.
func (*SwiftGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	return resolveInheritsRefs(refs, localTypeBindings(refs, false /* skipStatic */))
}

// SymbolMetadata implements symbolMetadataExtractor. Kotlin-parallel: for
// members defined inside an `extension Foo { ... }`, emit
// Metadata["receiver"]="Foo" so cross-language queries like "all
// extensions of String" work uniformly. Detected by walking up to the
// enclosing class_declaration with `extension` flavor.
func (*SwiftGrammar) SymbolMetadata(n *sitter.Node, source []byte) map[string]any {
	switch n.Kind() {
	case "function_declaration", "property_declaration",
		"init_declaration", "protocol_function_declaration",
		"protocol_property_declaration", "subscript_declaration",
		"typealias_declaration":
		ext := findAncestor(n, "class_declaration")
		if ext == nil || swiftClassFlavor(ext) != "extension" {
			return nil
		}
		if name := swiftExtensionTargetName(ext, source); name != "" {
			return map[string]any{"receiver": name}
		}
	case "class_declaration":
		if swiftClassFlavor(n) != "extension" {
			return nil
		}
		if name := swiftExtensionTargetName(n, source); name != "" {
			return map[string]any{"extends_type": name}
		}
	}
	return nil
}

// swiftExtensionTargetName returns the base name of the type an
// extension/class_declaration extends — the first user_type direct
// child. Returns "" if the node is not an extension or no user_type
// child is present.
func swiftExtensionTargetName(ext *sitter.Node, source []byte) string {
	if ext == nil {
		return ""
	}
	for i := uint(0); i < ext.NamedChildCount(); i++ {
		c := ext.NamedChild(i)
		if c != nil && c.Kind() == "user_type" {
			return swiftUserTypeName(c, source)
		}
	}
	return ""
}

func swiftUserTypeName(n *sitter.Node, source []byte) string {
	name, _ := swiftUserTypeWithGenerics(n, source)
	return name
}

// swiftUserTypeWithGenerics returns the base name + stripped type
// arguments. A user_type either wraps a single type_identifier
// (possibly nested_type_identifier for dotted access) or a
// generic_type / type_identifier + type_arguments pair.
func swiftUserTypeWithGenerics(n *sitter.Node, source []byte) (string, []string) {
	if n == nil {
		return "", nil
	}
	var name string
	var args []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "type_identifier":
			if name == "" {
				name = strings.TrimSpace(c.Utf8Text(source))
			}
		case "nested_type_identifier":
			if name == "" {
				name = strings.TrimSpace(c.Utf8Text(source))
			}
		case "type_arguments":
			for j := uint(0); j < c.NamedChildCount(); j++ {
				a := c.NamedChild(j)
				if a == nil {
					continue
				}
				if t := strings.TrimSpace(a.Utf8Text(source)); t != "" {
					args = append(args, t)
				}
			}
		}
	}
	if name == "" {
		// No type_identifier / nested_type_identifier child was matched —
		// a grammar-version gap (e.g., tree-sitter-swift emits an unfamiliar
		// inner node type). Fall back to the raw bytes with generics stripped
		// so the caller still gets a usable name rather than "".
		raw := strings.TrimSpace(n.Utf8Text(source))
		if idx := strings.Index(raw, "<"); idx > 0 {
			raw = raw[:idx]
		}
		name = raw
	}
	return name, args
}

// swiftAttributeName extracts the identifier from an attribute node.
// `@Published` → "Published"; `@available(iOS 14, *)` → "available";
// `@objc(Foo)` → "objc".
func swiftAttributeName(n *sitter.Node, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "user_type":
			return swiftUserTypeName(c, source)
		case "simple_identifier", "type_identifier":
			return strings.TrimSpace(c.Utf8Text(source))
		}
	}
	return ""
}

func swiftHasAttribute(mods *sitter.Node, source []byte, target string) bool {
	for i := uint(0); i < mods.NamedChildCount(); i++ {
		c := mods.NamedChild(i)
		if c == nil || c.Kind() != "attribute" {
			continue
		}
		if swiftAttributeName(c, source) == target {
			return true
		}
	}
	return false
}
