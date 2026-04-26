package code

import (
	"strconv"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code/tree_sitter_dart"
)

// DartGrammar indexes .dart source files.
//
// Dart's tree-sitter grammar (UserNobody14/tree-sitter-dart, ABI 15 at
// HEAD — vendored in-tree because its go.mod path is broken) covers
// Dart 3 thoroughly: sealed/base/final/interface class modifiers,
// records, patterns, enhanced enums, extension types, and Dart 3.10
// dot shorthand. Declaration-level extraction (what librarian's graph
// uses) is stable; expression-level quirks remain in a few upstream
// issues but don't affect symbol output.
//
// Inheritance model:
//
//   - `class X extends A implements B with M`:
//     extends A  → inherits relation=extends
//     implements B → inherits relation=implements
//     with M     → inherits relation=mixes
//     All three relations emit under the single "inherits" edge kind;
//     Metadata.relation distinguishes them — same convention as other
//     grammars.
//
//   - `mixin M on Base`:
//     on Base → Reference.Kind="requires" (NOT "inherits"). The `on`
//     clause is a use-site constraint (mixin M can only be applied to
//     subtypes of Base), not an inheritance parent. Carried under a
//     distinct EdgeKindRequires so "all parents of X" queries stay
//     clean. See lib-wji.3 bead for the design rationale.
//
//   - `mixin M` (no on): no parent refs, like a plain interface.
//
// File-membership (Dart's library-part system):
//
//   - `part 'foo.dart'`    → Reference.Kind="part", Metadata empty
//   - `part of 'bar.dart'` → Reference.Kind="part", Metadata["direction"]="of"
//   - These are distinct from imports — a Dart library lives across
//     multiple files joined via part directives.
//
// Known upstream-grammar quirks (tracked separately):
//
//   - `interfaces` node merges implements + with clauses into one
//     children list, with the first mixin after `with` sometimes wrapped
//     in an ERROR node. Workaround: Utf8Text-based position splitting
//     plus ERROR-inner identifier recovery. Tracked upstream.
type DartGrammar struct{}

// NewDartGrammar returns the Dart grammar implementation.
func NewDartGrammar() *DartGrammar { return &DartGrammar{} }

// dartClassModifiers is the set of class-level modifier keywords that
// surface as label Signals. These are direct named children of
// class_definition whose node type matches the keyword itself.
var dartClassModifiers = map[string]bool{
	"abstract":  true,
	"sealed":    true,
	"base":      true,
	"interface": true,
	"final":     true,
	"mixin":     true, // `mixin class` form
}

// dartFieldModifiers — keyword node types on field declarations (inside
// a `declaration` wrapper). Dart writes `final`/`const` as dedicated
// node types (final_builtin / const_builtin), not generic identifiers.
var dartFieldModifiers = map[string]string{
	"final_builtin":  "final",
	"const_builtin":  "const",
	"static_builtin": "static",
	"late_builtin":   "late",
}

func (*DartGrammar) Name() string               { return "dart" }
func (*DartGrammar) Extensions() []string       { return []string{".dart"} }
func (*DartGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_dart.Language()) }

func (*DartGrammar) CommentNodeTypes() []string {
	return []string{"comment", "documentation_comment"}
}

func (*DartGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Dart AST node types to generic Unit.Kind values.
//
// The tree-sitter-dart grammar wraps members in several layers:
//
//	class_body → declaration → constructor_signature | initialized_identifier
//	class_body → method_signature → function_signature | getter_signature | setter_signature
//
// We register the INNER signature nodes as symbols and the outer wrappers
// (declaration, method_signature, initialized_identifier_list) as containers.
// This keeps Unit.Kind precise (getter vs method vs field) without a
// symbolKindResolver pass.
//
// Note: `function_signature` maps to "function" at the file level; the shared
// walker rewrites it to "method" when the symbol's container is class-family.
func (*DartGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"class_definition":              "class",
		"mixin_declaration":             "mixin",
		"extension_declaration":         "extension",
		"extension_type_declaration":    "extension_type",
		"enum_declaration":              "enum",
		"type_alias":                    "type",
		"function_signature":            "function",
		"constructor_signature":         "constructor",
		"factory_constructor_signature": "constructor",
		"getter_signature":              "property",
		"setter_signature":              "property",
		"initialized_identifier":        "field",
	}
}

// ContainerKinds enumerates the intermediate wrapper nodes the walker must
// descend through to reach member symbols. Hybrid nodes (emit a Unit AND
// descend into their body) appear in both SymbolKinds and here.
func (*DartGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		// Hybrid declaration containers — emit a Unit for the type and
		// descend into its body.
		"class_definition":           true,
		"mixin_declaration":          true,
		"extension_declaration":      true,
		"extension_type_declaration": true,
		"enum_declaration":           true,
		// Body wrappers.
		"class_body":     true,
		"extension_body": true,
		"enum_body":      true,
		// Inner wrappers — descend to reach signatures / initialized_identifiers.
		"declaration":                 true,
		"initialized_identifier_list": true,
		"method_signature":            true,
	}
}

// SymbolName returns the display name for a Dart declaration node.
//
// Dart's AST quirks that need WHY-level attention:
//   - Named constructors (`Foo.named()`) have two identifier children.
//     Title is set to the second (the named segment) so Unit.Path
//     reads `pkg.Foo.named` — not `pkg.Foo.Foo.named`, which would
//     double the class name.
//   - factory_constructor_signature follows the same two-identifier
//     shape.
//   - extension_declaration: first identifier child is the extension's
//     OWN name (e.g., "StringX"), NOT its target type — the target lives
//     separately as a type_identifier and is exposed via SymbolMetadata
//     as extends_type.
func (*DartGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "class_definition", "mixin_declaration", "enum_declaration",
		"extension_declaration", "extension_type_declaration":
		return firstIdentifierChild(n, source)
	case "type_alias":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type_identifier" {
				return strings.TrimSpace(c.Utf8Text(source))
			}
		}
	case "function_signature":
		return firstIdentifierChild(n, source)
	case "constructor_signature", "factory_constructor_signature":
		return dartConstructorName(n, source)
	case "getter_signature", "setter_signature":
		return firstIdentifierChild(n, source)
	case "initialized_identifier":
		return firstIdentifierChild(n, source)
	}
	return ""
}

// firstIdentifierChild returns the text of the first named `identifier`
// child, trimmed. Many Dart declaration types carry their name here.
func firstIdentifierChild(n *sitter.Node, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			return strings.TrimSpace(c.Utf8Text(source))
		}
	}
	return ""
}

// dartConstructorName returns the Unit.Title for a Dart constructor.
// Default ctors (one identifier, same as class name) → class name —
// matches Java's `Class.Class` Path convention. Named ctors (two
// identifiers) → just the named segment so Unit.Path doesn't double
// the class name (`pkg.Store.fromConfig`, not `pkg.Store.Store.fromConfig`).
func dartConstructorName(n *sitter.Node, source []byte) string {
	var ids []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			ids = append(ids, strings.TrimSpace(c.Utf8Text(source)))
		}
	}
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	default:
		return ids[1]
	}
}

// PackageName returns the dotted library name from `library foo.bar;`
// when present, otherwise "". With no library directive the shared
// Parse falls back to the file stem (e.g. `main.dart` → `main`).
//
// The tree-sitter-dart node for this is `library_name`, which wraps a
// `dotted_identifier_list` → one identifier per dotted segment.
func (*DartGrammar) PackageName(root *sitter.Node, source []byte) string {
	var out string
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "library_name" {
			return true
		}
		list := findDescendant(n, "dotted_identifier_list")
		if list == nil {
			// Single-segment library name lands as a direct identifier.
			for i := uint(0); i < n.NamedChildCount(); i++ {
				c := n.NamedChild(i)
				if c != nil && c.Kind() == "identifier" {
					out = strings.TrimSpace(c.Utf8Text(source))
					return false
				}
			}
			return false
		}
		var parts []string
		for i := uint(0); i < list.NamedChildCount(); i++ {
			c := list.NamedChild(i)
			if c != nil && c.Kind() == "identifier" {
				parts = append(parts, strings.TrimSpace(c.Utf8Text(source)))
			}
		}
		out = strings.Join(parts, ".")
		return false
	})
	return out
}

// Imports walks every import_or_export, part_directive, and
// part_of_directive. Three shapes:
//
//   - `import 'package:foo/foo.dart'`        → ImportRef{Path, Kind:"import"}
//   - `import 'x.dart' as p show A hide B`   → Path + Alias + Metadata{show,hide}
//   - `part 'other.dart'`                    → ImportRef{Path, Kind:"part"}
//   - `part of 'main.dart'`                  → ImportRef{Path, Kind:"part", Metadata["direction"]:"of"}
//
// `show` and `hide` land as []string in Metadata — the future resolver
// (lib-0t4) will use show-list entries as file-local type bindings.
func (*DartGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		switch n.Kind() {
		case "import_or_export":
			if ref := dartParseImport(n, source); ref != nil {
				out = append(out, *ref)
			}
			return false
		case "part_directive":
			if path := dartFirstURIString(n, source); path != "" {
				out = append(out, ImportRef{Path: path, Kind: "part"})
			}
			return false
		case "part_of_directive":
			if path := dartFirstURIString(n, source); path != "" {
				out = append(out, ImportRef{
					Path:     path,
					Kind:     "part",
					Metadata: map[string]any{"direction": "of"},
				})
			}
			return false
		}
		return true
	})
	return out
}

// dartParseImport extracts ImportRef from an import_or_export node.
// Inner shape: library_import > import_specification > configurable_uri
// + [bare identifier for `as`]  + combinator* (for `show`/`hide`).
//
// tree-sitter-dart AST quirk: `as X` is NOT wrapped in a combinator node
// — it emits a bare `identifier` direct child of import_specification
// after the configurable_uri. Only `show`/`hide` use combinator wrappers.
func dartParseImport(n *sitter.Node, source []byte) *ImportRef {
	spec := findDescendant(n, "import_specification")
	if spec == nil {
		return nil
	}
	path := dartFirstURIString(spec, source)
	if path == "" {
		return nil
	}
	ref := &ImportRef{Path: path}
	var show, hide []string
	for i := uint(0); i < spec.NamedChildCount(); i++ {
		c := spec.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "identifier":
			// Bare identifier sibling of configurable_uri is the `as` alias.
			if ref.Alias == "" {
				ref.Alias = strings.TrimSpace(c.Utf8Text(source))
			}
		case "combinator":
			text := strings.TrimSpace(c.Utf8Text(source))
			var bucket *[]string
			switch {
			case strings.HasPrefix(text, "show"):
				bucket = &show
			case strings.HasPrefix(text, "hide"):
				bucket = &hide
			default:
				continue
			}
			for j := uint(0); j < c.NamedChildCount(); j++ {
				cc := c.NamedChild(j)
				if cc != nil && cc.Kind() == "identifier" {
					*bucket = append(*bucket, strings.TrimSpace(cc.Utf8Text(source)))
				}
			}
		}
	}
	if len(show) > 0 || len(hide) > 0 {
		ref.Metadata = map[string]any{}
		if len(show) > 0 {
			ref.Metadata["show"] = show
		}
		if len(hide) > 0 {
			ref.Metadata["hide"] = hide
		}
	}
	return ref
}

// dartFirstURIString returns the text of the first uri→string_literal
// descendant, with surrounding quotes stripped. Used for both imports
// and part directives.
func dartFirstURIString(n *sitter.Node, source []byte) string {
	uri := findDescendant(n, "uri")
	if uri == nil {
		return ""
	}
	lit := findDescendant(uri, "string_literal")
	if lit == nil {
		return ""
	}
	raw := strings.TrimSpace(lit.Utf8Text(source))
	return strings.Trim(raw, `'"`)
}

// SymbolAnnotations implements annotationExtractor for Dart. Annotations
// (`@immutable`, `@override`, `@Deprecated(...)`) appear as direct
// `annotation` children of the decorated node — for class_definition,
// they sit BEFORE the class modifier keywords; inside a class_body,
// they precede each method_signature / declaration as siblings.
//
// Method-level annotations are surfaced via the walker's comment
// buffering — they sit as siblings inside class_body, not as children
// of the method_signature. So we only return annotations that are
// direct children of the symbol node itself.
func (*DartGrammar) SymbolAnnotations(n *sitter.Node, source []byte) []string {
	var out []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "annotation" {
			continue
		}
		if name := dartAnnotationName(c, source); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// dartAnnotationName extracts the identifier text from an annotation
// node. `@Deprecated(...)` → "Deprecated"; `@foo.Bar` → "foo.Bar".
func dartAnnotationName(n *sitter.Node, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "identifier", "scoped_identifier":
			return strings.TrimSpace(c.Utf8Text(source))
		}
	}
	return ""
}

// SymbolExtraSignals implements extraSignalsExtractor. Emits:
//
//   - class-level modifier labels (abstract / sealed / base /
//     interface / final / mixin class) as Signal{Kind="label"}
//   - field-level final / const / static / late labels on declarations
//     that wrap a final_builtin / const_builtin / static_builtin /
//     late_builtin keyword
//   - factory marker on factory_constructor_signature
func (*DartGrammar) SymbolExtraSignals(n *sitter.Node, source []byte) []indexer.Signal {
	var out []indexer.Signal
	if n.Kind() == "class_definition" {
		// Modifier keywords split between two AST shapes:
		//  - Named children: `abstract`, `sealed`, `base`, `interface`,
		//    `mixin` (when used as a class modifier).
		//  - Anonymous tokens: `final` — tree-sitter-dart doesn't wrap
		//    the `final class` keyword as a distinct named node, so we
		//    walk ALL children (including anonymous) and match the
		//    allow-list against Kind().
		for i := uint(0); i < n.ChildCount(); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if dartClassModifiers[c.Kind()] {
				out = append(out, indexer.Signal{Kind: "label", Value: c.Kind()})
			}
		}
	}
	if n.Kind() == "factory_constructor_signature" {
		out = append(out, indexer.Signal{Kind: "label", Value: "factory"})
	}
	// Field modifiers — the symbol node here is initialized_identifier,
	// but the modifiers live on its grandparent `declaration`. Walk up.
	if n.Kind() == "initialized_identifier" {
		decl := findAncestor(n, "declaration")
		if decl != nil {
			for i := uint(0); i < decl.NamedChildCount(); i++ {
				c := decl.NamedChild(i)
				if c == nil {
					continue
				}
				if label, ok := dartFieldModifiers[c.Kind()]; ok {
					out = append(out, indexer.Signal{Kind: "label", Value: label})
				}
			}
		}
	}
	return out
}

// SymbolParents implements inheritanceExtractor.
//
// Shapes handled:
//
//   - class_definition: `superclass` child → extends; `interfaces` child
//     → implements + mixes split by `with` keyword position.
//   - mixin_declaration: direct `type_identifier` children → requires
//     (the `on Base` constraint).
//   - enum_declaration: `interfaces` child is implements-only.
//   - extension_type_declaration: `interfaces` child is implements-only.
//
// Generics on parents are stripped before emission (Map<K,V> → "Map");
// the raw type-argument strings land in Metadata["type_args"] so
// downstream code can still render "Comparable<Animal<T>>" faithfully
// while graph lookups use the bare name.
func (*DartGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	switch n.Kind() {
	case "class_definition":
		return dartClassParents(n, source)
	case "mixin_declaration":
		return dartMixinParents(n, source)
	case "enum_declaration", "extension_type_declaration":
		return dartImplementsOnlyParents(n, source)
	}
	return nil
}

func dartClassParents(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "superclass":
			if p := dartFirstTypeIdentifierRef(c, source, "extends"); p != nil {
				out = append(out, *p)
			}
		case "interfaces":
			out = append(out, dartInterfacesParents(c, source)...)
		case "ERROR":
			// tree-sitter-dart grammar quirk: `class X implements A with M`
			// sometimes puts the `with M` clause as an ERROR SIBLING of the
			// interfaces node (rather than as an ERROR inside it). Recover
			// mixin identifiers from the ERROR's inner children.
			if strings.HasPrefix(strings.TrimSpace(c.Utf8Text(source)), "with") {
				out = append(out, dartErrorWithClauseMixins(c, source)...)
			}
		}
	}
	return out
}

// dartErrorWithClauseMixins extracts mixin identifiers from an ERROR
// node whose text starts with "with". Used for the sibling-ERROR form
// of the upstream grammar quirk.
func dartErrorWithClauseMixins(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() != "identifier" && c.Kind() != "type_identifier" {
			continue
		}
		out = append(out, ParentRef{
			Name:     strings.TrimSpace(c.Utf8Text(source)),
			Relation: "mixes",
			Loc: nodeLocation(c),
		})
	}
	return out
}

// dartInterfacesParents walks an `interfaces` node, splitting children
// into "implements" (before ` with `) and "mixes" (after) using a
// byte-offset cut derived from Utf8Text position of the `with` keyword.
// The grammar wraps the first mixin in an ERROR node; we recover its
// inner identifier.
func dartInterfacesParents(n *sitter.Node, source []byte) []ParentRef {
	text := n.Utf8Text(source)
	// withOffset is the file-byte position where the `with` keyword sits,
	// used by the relation split (before → implements, after → mixes).
	// -1 means no `with` — all names are implements.
	withOffset := -1
	switch {
	case strings.HasPrefix(strings.TrimSpace(text), "with "):
		// When the grammar emits an interfaces node that is exclusively
		// the with-clause (no `implements` preamble), StartByte() is
		// already at `with`. Without this branch, withOffset stays -1
		// and every mixin would be silently misclassified as implements.
		withOffset = int(n.StartByte())
	default:
		if idx := strings.Index(text, " with "); idx >= 0 {
			withOffset = int(n.StartByte()) + idx + 1 // skip the leading space
		}
	}

	// Track the pending target when type_arguments trails a type_identifier.
	// seen keys on "<byteOffset>|<name>" — dedupe in case the grammar
	// ever emits the same identifier both as a regular type_identifier
	// AND inside an ERROR wrapper (defensive; hasn't been observed in
	// current vendor output, but the ERROR-recovery shape is grammar-
	// version-sensitive).
	var pending *ParentRef
	var out []ParentRef
	seen := map[string]bool{}
	emit := func(p ParentRef) {
		key := strconv.Itoa(p.Loc.ByteOffset) + "|" + p.Name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}
	flush := func() {
		if pending == nil {
			return
		}
		emit(*pending)
		pending = nil
	}

	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "type_identifier":
			flush()
			relation := "implements"
			if withOffset >= 0 && int(c.StartByte()) > withOffset {
				relation = "mixes"
			}
			pending = &ParentRef{
				Name:     strings.TrimSpace(c.Utf8Text(source)),
				Relation: relation,
				Loc: nodeLocation(c),
			}
		case "type_arguments":
			if pending != nil {
				args := dartTypeArgumentStrings(c, source)
				if len(args) > 0 {
					if pending.Metadata == nil {
						pending.Metadata = map[string]any{}
					}
					pending.Metadata["type_args"] = args
				}
			}
		case "ERROR":
			// Upstream grammar quirk: tree-sitter-dart wraps some
			// identifier(s) adjacent to the `with` keyword in an ERROR
			// node. Observed shapes:
			//   ERROR ["with M"]                → inner identifier "M" is a mixin
			//   ERROR ["Disposable with"]       → inner type_identifier
			//                                     "Disposable" is STILL an
			//                                     implements target
			// Use position relative to withOffset to assign the relation,
			// same as the regular type_identifier path.
			flush()
			for j := uint(0); j < c.NamedChildCount(); j++ {
				id := c.NamedChild(j)
				if id == nil {
					continue
				}
				if id.Kind() != "identifier" && id.Kind() != "type_identifier" {
					continue
				}
				relation := "implements"
				if withOffset >= 0 && int(id.StartByte()) > withOffset {
					relation = "mixes"
				}
				emit(ParentRef{
					Name:     strings.TrimSpace(id.Utf8Text(source)),
					Relation: relation,
					Loc: nodeLocation(id),
				})
			}
		}
	}
	flush()
	return out
}

func dartMixinParents(n *sitter.Node, source []byte) []ParentRef {
	// Direct type_identifier children are the `on T` constraint targets.
	// Emit them as ParentRef{Kind:"requires"} so they land as a distinct
	// EdgeKindRequires, not conflated with inheritance.
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "type_identifier" {
			continue
		}
		out = append(out, ParentRef{
			Name:     strings.TrimSpace(c.Utf8Text(source)),
			Relation: "requires",
			Kind:     "requires",
			Loc: nodeLocation(c),
		})
	}
	return out
}

// dartImplementsOnlyParents is the enum / extension_type variant of
// dartInterfacesParents. Delegates to the full walker because the
// shared `interfaces` node shape is identical — the no-with guard
// happens automatically (withOffset stays -1).
func dartImplementsOnlyParents(n *sitter.Node, source []byte) []ParentRef {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "interfaces" {
			return dartInterfacesParents(c, source)
		}
	}
	return nil
}

// dartFirstTypeIdentifierRef returns a ParentRef for the first
// type_identifier descendant under wrapper (e.g., `superclass`
// wrapping `extends Foo`). type_arguments siblings within the wrapper
// become Metadata["type_args"].
func dartFirstTypeIdentifierRef(wrapper *sitter.Node, source []byte, relation string) *ParentRef {
	var nameNode *sitter.Node
	var args []string
	for i := uint(0); i < wrapper.NamedChildCount(); i++ {
		c := wrapper.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "type_identifier":
			if nameNode == nil {
				nameNode = c
			}
		case "type_arguments":
			args = dartTypeArgumentStrings(c, source)
		}
	}
	if nameNode == nil {
		return nil
	}
	ref := &ParentRef{
		Name:     strings.TrimSpace(nameNode.Utf8Text(source)),
		Relation: relation,
		Loc: nodeLocation(nameNode),
	}
	if len(args) > 0 {
		ref.Metadata = map[string]any{"type_args": args}
	}
	return ref
}

// dartTypeArgumentStrings flattens a `type_arguments` node to string
// forms of each direct named child (typically type_identifier), for
// ParentRef.Metadata["type_args"].
func dartTypeArgumentStrings(n *sitter.Node, source []byte) []string {
	var out []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if t := strings.TrimSpace(c.Utf8Text(source)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ResolveParents implements inheritanceResolver. Dart imports are
// file-level (like Swift), so the shared local-type-bindings table is
// mostly empty — `import 'package:foo/foo.dart'` binds no type names.
// The one exception is `show Bar` which binds `Bar` locally, but that
// resolution is lib-0t4's work (defer for now). As a result most bare
// parent names will be marked Metadata["unresolved"]=true, matching
// Swift's behavior.
func (*DartGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	return resolveInheritsRefs(refs, localTypeBindings(refs, false /* skipStatic */))
}

// SymbolMetadata implements symbolMetadataExtractor. Emits:
//
//   - Metadata["extends_type"] on extension_declaration and
//     extension_type_declaration Units — the type the extension
//     extends (cross-language parallel of Swift's convention).
//   - Metadata["representation_name"] on extension_type_declaration
//     Units — the parameter name bound to the representation value
//     (e.g., `extension type UserId(int id)` → "id"). Callers must use
//     this name when constructing or destructuring the extension type.
func (*DartGrammar) SymbolMetadata(n *sitter.Node, source []byte) map[string]any {
	switch n.Kind() {
	case "extension_declaration":
		// extension StringX on String { ... } — first type_identifier is
		// the target type. (First identifier is the extension's own name.)
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c != nil && c.Kind() == "type_identifier" {
				if name := strings.TrimSpace(c.Utf8Text(source)); name != "" {
					return map[string]any{"extends_type": name}
				}
				return nil
			}
		}
	case "extension_type_declaration":
		return dartExtensionTypeMetadata(n, source)
	}
	return nil
}

// dartExtensionTypeMetadata walks an extension_type_declaration's
// representation_declaration child and returns both the representation
// type and the parameter name. `extension type UserId(int id)` →
// {extends_type: "int", representation_name: "id"}.
//
// Handles the AST shapes tree-sitter-dart emits for the representation
// type:
//   - bare `type_identifier` → "int"
//   - consecutive `type_identifier`s (dotted name `foo.Bar`) → "foo.Bar"
//   - `type_identifier` + `nullable_type "?"` → "int" (nullable stripped,
//     matches Swift/Kotlin convention)
//   - `type_identifier` + `type_arguments` (generic `List<String>`) →
//     "List" (generic args stripped, matches convention)
//   - `function_type` → raw Utf8Text with whitespace collapsed
//
// Either key is omitted if the corresponding node is absent (grammar
// version drift guard); the whole map is nil if nothing was found.
func dartExtensionTypeMetadata(n *sitter.Node, source []byte) map[string]any {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "representation_declaration" {
			continue
		}
		return dartRepresentationMetadata(c, source)
	}
	return nil
}

func dartRepresentationMetadata(rep *sitter.Node, source []byte) map[string]any {
	var typeParts []string
	reprName := ""
	// Dotted qualified names like `foo.Bar` parse as consecutive
	// `type_identifier` siblings — tree-sitter-dart's `_type_name` rule
	// is hidden, so each segment is hoisted as a direct named child.
	// Concatenate with "." to reconstruct the FQN. `type_arguments` and
	// `nullable_type` siblings are ignored (generics + nullability
	// stripped, matching Swift/Kotlin convention).
	//
	// `function_type` is structurally exclusive with `type_identifier`
	// in the representation slot — the grammar emits one OR the other,
	// never both — so the fallback appends unconditionally when seen.
	// The len(typeParts) guard is defensive against a future grammar
	// version that might ever emit them in sequence.
	//
	// For `reprName`, we overwrite on every `identifier` we see. Named
	// constructor variants like `extension type X.named(int id)` would
	// emit an identifier for `named` before the param-name identifier;
	// overwriting ensures the last one (the param name) wins.
	for j := uint(0); j < rep.NamedChildCount(); j++ {
		inner := rep.NamedChild(j)
		if inner == nil {
			continue
		}
		switch inner.Kind() {
		case "type_identifier":
			typeParts = append(typeParts, strings.TrimSpace(inner.Utf8Text(source)))
		case "function_type":
			if len(typeParts) == 0 {
				typeParts = append(typeParts, strings.Join(strings.Fields(inner.Utf8Text(source)), " "))
			}
		case "identifier":
			reprName = strings.TrimSpace(inner.Utf8Text(source))
		}
	}
	out := map[string]any{}
	if len(typeParts) > 0 {
		out["extends_type"] = strings.Join(typeParts, ".")
	}
	if reprName != "" {
		out["representation_name"] = reprName
	}
	if len(out) > 0 {
		return out
	}
	return nil
}
