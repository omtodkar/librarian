package code

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"
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
func (*JavaGrammar) Language() *sitter.Language { return java.GetLanguage() }

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
	switch n.Type() {
	case "class_declaration", "interface_declaration",
		"enum_declaration", "record_declaration",
		"method_declaration", "constructor_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
	case "field_declaration":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil || c.Type() != "variable_declarator" {
				continue
			}
			if name := c.ChildByFieldName("name"); name != nil {
				return name.Content(source)
			}
		}
	}
	return ""
}

// PackageName extracts the dotted package name from the `package foo.bar;`
// declaration. Returns "" for files without a package (default package / test
// snippets), which falls back to the file stem like Python.
func (*JavaGrammar) PackageName(root *sitter.Node, source []byte) string {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Type() != "package_declaration" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			cc := c.NamedChild(j)
			if cc == nil {
				continue
			}
			if cc.Type() == "scoped_identifier" || cc.Type() == "identifier" {
				return cc.Content(source)
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
		if n.Type() != "import_declaration" {
			return true
		}
		ref := ImportRef{}
		wildcard := false
		// All children (not just named) so the anonymous `static` and
		// `asterisk` tokens are both visible. The scoped_identifier /
		// identifier carries the dotted path; the asterisk marks `.*`.
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "static":
				ref.Static = true
			case "scoped_identifier", "identifier":
				if ref.Path == "" {
					ref.Path = c.Content(source)
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
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Type() == "modifiers" {
			mods = c
			break
		}
	}
	if mods == nil {
		return nil
	}

	var out []string
	for i := 0; i < int(mods.NamedChildCount()); i++ {
		c := mods.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "marker_annotation", "annotation":
			if name := javaAnnotationName(c, source); name != "" {
				out = append(out, name)
			}
		}
	}
	return out
}

// javaAnnotationName pulls the identifier (or dotted scoped_identifier) out
// of an annotation node, ignoring the `@` token and any argument list.
// `@Deprecated` → "Deprecated"; `@lombok.Data` → "lombok.Data";
// `@Transactional(readOnly=true)` → "Transactional".
func javaAnnotationName(n *sitter.Node, source []byte) string {
	name := n.ChildByFieldName("name")
	if name == nil {
		// Some tree-sitter versions expose the name as an un-fielded child.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c == nil {
				continue
			}
			if c.Type() == "identifier" || c.Type() == "scoped_identifier" {
				name = c
				break
			}
		}
	}
	if name == nil {
		return ""
	}
	return strings.TrimSpace(name.Content(source))
}
