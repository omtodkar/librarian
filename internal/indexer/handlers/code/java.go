package code

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/tree-sitter/tree-sitter-java/bindings/go"

	"librarian/internal/indexer"
)

// JavaGrammar indexes .java source files.
//
// Symbols:
//   - class_declaration       → "class"
//   - interface_declaration   → "interface"
//   - enum_declaration        → "enum"
//   - record_declaration      → "record"       (Java 14+)
//   - method_declaration      → "method"
//   - constructor_declaration → "constructor"
//   - field_declaration       → "field"
//
// Classes, interfaces, enums, and records are all hybrid — they emit a Unit
// for the container itself AND the walker descends into their body to emit
// Units for members. Nested classes therefore produce Units at every level
// of enclosure (`com.example.Outer.Inner.method`).
//
// Fields: a `private int a, b;` declaration compiles to one field_declaration
// with multiple variable_declarator children. The v1 grammar emits a single
// Unit named after the first declarator — the full declaration is visible in
// Unit.Content so siblings remain searchable. A dedicated follow-up tracks
// multi-declarator fan-out.
//
// Imports: static imports carry Static=true via Reference.Metadata. tree-
// sitter-java emits the `static` keyword as an anonymous child of
// import_declaration; we scan all children (not just named) to detect it.
//
// Annotations: `@Deprecated`, `@Transactional(...)`, etc. from each symbol's
// `modifiers` child are surfaced as Signal{Kind: "annotation"} via the
// optional annotationExtractor interface. Annotations also appear inline in
// Unit.Content naturally, so textual search for `@Transactional` works too.
type JavaGrammar struct{}

// NewJavaGrammar returns the Java grammar implementation.
func NewJavaGrammar() *JavaGrammar { return &JavaGrammar{} }

func (*JavaGrammar) Name() string               { return "java" }
func (*JavaGrammar) Extensions() []string       { return []string{".java"} }
func (*JavaGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_java.Language()) }

// CommentNodeTypes covers both line comments and block comments; Javadoc
// (`/** ... */`) is represented as a block_comment, so the existing
// stripCommentMarkers handling (which strips `/*`, `*/`, and leading `*`)
// renders Javadoc correctly without a separate node type.
func (*JavaGrammar) CommentNodeTypes() []string { return []string{"line_comment", "block_comment"} }

// DocstringFromNode returns "" — Java's docstrings (Javadoc) precede the
// declaration as block_comment siblings, which the walker's preceding-comment
// buffer already captures.
func (*JavaGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Java AST node types to Unit.Kind values.
func (*JavaGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"class_declaration":       "class",
		"interface_declaration":   "interface",
		"enum_declaration":        "enum",
		"record_declaration":      "record",
		"method_declaration":      "method",
		"constructor_declaration": "constructor",
		"field_declaration":       "field",
	}
}

// ContainerKinds lists nodes the walker descends into. The four type
// declarations are hybrid (also in SymbolKinds) so nested members become
// Units. Their body node types are pure containers — the walker must
// descend through them to reach the member declarations.
func (*JavaGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{
		"class_declaration":     true,
		"interface_declaration": true,
		"enum_declaration":      true,
		"record_declaration":    true,
		"class_body":            true,
		"interface_body":        true,
		"enum_body":             true,
		"record_body":           true,
	}
}

// SymbolName returns the display name for each Java symbol node.
//
// For field_declaration, Java permits `private int a, b;` — one declaration
// with multiple variable_declarator children. The grammar returns the first
// declarator's name so exactly one Unit is emitted per declaration; the full
// declaration text (including all variables) is captured in Unit.Content.
func (*JavaGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Kind() {
	case "class_declaration", "interface_declaration",
		"enum_declaration", "record_declaration",
		"method_declaration", "constructor_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Utf8Text(source)
		}
	case "field_declaration":
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil || c.Kind() != "variable_declarator" {
				continue
			}
			if name := c.ChildByFieldName("name"); name != nil {
				return name.Utf8Text(source)
			}
		}
	}
	return ""
}

// PackageName extracts the dotted package name from the `package foo.bar;`
// declaration. Returns "" for files without a package (default package / test
// snippets), which falls back to the file stem like Python.
func (*JavaGrammar) PackageName(root *sitter.Node, source []byte) string {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Kind() != "package_declaration" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			if cc.Kind() == "scoped_identifier" || cc.Kind() == "identifier" {
				return cc.Utf8Text(source)
			}
		}
	}
	return ""
}

// Imports walks every import_declaration in the file. The `static` keyword
// (as in `import static java.util.Collections.emptyList;`) is an anonymous
// child token — we iterate all children (not just named) to detect it, then
// record Static=true on the emitted ImportRef.
func (*JavaGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Kind() != "import_declaration" {
			return true
		}
		ref := ImportRef{}
		wildcard := false
		// All children (not just named) so the anonymous `static` and
		// `asterisk` tokens are both visible. The scoped_identifier /
		// identifier carries the dotted path; the asterisk marks `.*`.
		for i := uint(0); i < n.ChildCount(); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "static":
				ref.Static = true
			case "scoped_identifier", "identifier":
				if ref.Path == "" {
					ref.Path = c.Utf8Text(source)
				}
			case "asterisk":
				wildcard = true
			}
		}
		if wildcard && ref.Path != "" {
			ref.Path += ".*"
		}
		if ref.Path != "" {
			out = append(out, ref)
		}
		return false
	})
	return out
}

// SymbolAnnotations implements annotationExtractor. It walks the declaration's
// `modifiers` child for marker_annotation (@Foo) and annotation (@Foo(x=1))
// nodes and returns each annotation's identifier without the leading `@`.
// Arguments on parameterised annotations are discarded from the signal Value
// — they survive in Unit.Content if the caller needs the full form.
func (*JavaGrammar) SymbolAnnotations(n *sitter.Node, source []byte) []string {
	var mods *sitter.Node
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "modifiers" {
			mods = c
			break
		}
	}
	if mods == nil {
		return nil
	}

	var out []string
	for i := uint(0); i < mods.NamedChildCount(); i++ {
		c := mods.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "marker_annotation", "annotation":
			if name := javaAnnotationName(c, source); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// SymbolParents implements inheritanceExtractor. It surfaces the inheritance
// relationships declared on a Java class-family symbol:
//
//   - class X extends Y               → Relation="extends" for Y
//   - class X implements A, B         → Relation="implements" for A, B
//   - interface I extends J, K        → Relation="extends" for J, K
//   - record R(...) implements I, J   → Relation="implements" for I, J
//   - enum  E implements I            → Relation="implements" for I
//
// Generics are stripped: `Map<K, V>` lands as Name="Map" with
// Metadata["type_args"]=["K", "V"]. Nested generics collapse to their outer
// text ("List<Map<K, V>>" → type_arg "Map<K, V>"); later passes can normalise
// further if needed.
//
// Resolution of bare type names to fully-qualified form (`Base` →
// `com.example.Base` via the same-file import list) happens in
// ResolveParents, not here — SymbolParents returns what the source literally
// declares.
func (*JavaGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "superclass":
			out = append(out, javaParentsFromTypeContainer(c, "extends", source)...)
		case "super_interfaces":
			out = append(out, javaParentsFromTypeContainer(c, "implements", source)...)
		case "extends_interfaces":
			// interface_declaration's extends clause — interface A extends B
			// is semantically inheritance ("extends" relation), not
			// implements.
			out = append(out, javaParentsFromTypeContainer(c, "extends", source)...)
		}
	}
	return out
}

// javaParentsFromTypeContainer walks a node that wraps one or more parent
// types (superclass holds a single _type; super_interfaces /
// extends_interfaces hold a type_list) and emits one ParentRef per type.
func javaParentsFromTypeContainer(container *sitter.Node, relation string, source []byte) []ParentRef {
	var out []ParentRef
	add := func(t *sitter.Node) {
		name, args := extractJavaTypeName(t, source)
		if name == "" {
			return
		}
		meta := map[string]any{}
		if len(args) > 0 {
			meta["type_args"] = args
		}
		out = append(out, ParentRef{
			Name:     name,
			Relation: relation,
			Loc: indexer.Location{
				Line:       int(t.StartPosition().Row) + 1,
				Column:     int(t.StartPosition().Column) + 1,
				ByteOffset: int(t.StartByte()),
			},
			Metadata: meta,
		})
	}
	for i := uint(0); i < container.NamedChildCount(); i++ {
		c := container.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "type_list" {
			for j := uint(0); j < c.NamedChildCount(); j++ {
				if cc := c.NamedChild(j); cc != nil {
					add(cc)
				}
			}
			continue
		}
		add(c)
	}
	return out
}

// extractJavaTypeName teases a type-name node into (bareName, typeArgs),
// handling the three shapes tree-sitter-java produces for a type reference:
//   - type_identifier         → `Foo`
//   - scoped_type_identifier  → `pkg.Foo` (possibly multi-segment)
//   - generic_type            → `Foo<T>` wrapping one of the above + type_arguments
//
// Other shapes (array_type, primitive_type) are valid _type positions in
// general Java syntax but never legal in an extends/implements list, so
// returning "" there silently skips them.
func extractJavaTypeName(n *sitter.Node, source []byte) (string, []string) {
	switch n.Kind() {
	case "type_identifier", "scoped_type_identifier":
		return strings.TrimSpace(n.Utf8Text(source)), nil
	case "generic_type":
		var name string
		var args []string
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			switch c.Kind() {
			case "type_identifier", "scoped_type_identifier":
				if name == "" {
					name = strings.TrimSpace(c.Utf8Text(source))
				}
			case "type_arguments":
				for j := uint(0); j < c.NamedChildCount(); j++ {
					cc := c.NamedChild(j)
					if cc == nil {
						continue
					}
					if t := strings.TrimSpace(cc.Utf8Text(source)); t != "" {
						args = append(args, t)
					}
				}
			}
		}
		return name, args
	}
	return "", nil
}

// ResolveParents implements inheritanceResolver. Java parent references that
// appear as bare class names (`extends BaseService`) are rewritten to their
// fully-qualified form using the file's own import list — the one that
// Imports() already populated and extractImports() appended to doc.Refs.
// Bare names that aren't matched by any single-class import land with
// Metadata["unresolved"]=true so downstream consumers can tell best-effort
// edges apart from confident ones.
//
// Limitations scoped to lib-wji.1:
//   - Wildcard imports (`import com.example.*;`) offer no binding for bare
//     names and are ignored. A later resolver pass across grammars (see the
//     filed follow-up bead) will use the workspace symbol index for this.
//   - Same-package siblings (no import needed in Java) are not resolved —
//     same follow-up.
//   - Static imports bind methods, not types; they cannot satisfy an
//     inheritance target and are skipped.
//
// Parent targets that already contain '.' are treated as fully-qualified and
// left alone (the source wrote `extends com.lib.Base` directly).
func (*JavaGrammar) ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	return resolveInheritsRefs(refs, localTypeBindings(refs, true /* skipStatic */))
}

// javaAnnotationName pulls the identifier (or dotted scoped_identifier) out
// of an annotation node, ignoring the `@` token and any argument list.
// `@Deprecated` → "Deprecated"; `@lombok.Data` → "lombok.Data";
// `@Transactional(readOnly=true)` → "Transactional".
func javaAnnotationName(n *sitter.Node, source []byte) string {
	name := n.ChildByFieldName("name")
	if name == nil {
		// Some tree-sitter versions expose the name as an un-fielded child.
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Kind() == "identifier" || c.Kind() == "scoped_identifier" {
				name = c
				break
			}
		}
	}
	if name == nil {
		return ""
	}
	return strings.TrimSpace(name.Utf8Text(source))
}
