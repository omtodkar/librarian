package code

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"librarian/internal/indexer"
)

// GoGrammar indexes .go source files.
//
// Recognised symbols: function_declaration, method_declaration, type_spec
// (mapped to function / method / type Unit kinds). A type_declaration is
// treated as a container so every type_spec / type_alias inside a grouped
// `type ( X struct{}; Y int )` block emits its own Unit.
//
// Imports from import_spec become ParsedDoc.References; backtick and double-
// quoted path literals are both handled. Comments feed docstrings per-Unit
// and document-level rationale signals.
type GoGrammar struct{}

// NewGoGrammar returns the Go grammar implementation.
func NewGoGrammar() *GoGrammar { return &GoGrammar{} }

func (*GoGrammar) Name() string                   { return "go" }
func (*GoGrammar) Extensions() []string           { return []string{".go"} }
func (*GoGrammar) Language() *sitter.Language     { return golang.GetLanguage() }
func (*GoGrammar) CommentNodeTypes() []string     { return []string{"comment"} }
func (*GoGrammar) DocstringFromNode(*sitter.Node, []byte) string { return "" }

// SymbolKinds maps Go AST node types to generic Unit.Kind values. Note that
// type_declaration is deliberately NOT a symbol here — it's a container that
// holds one or more type_spec children. Treating it as a container lets the
// walker descend and emit one Unit per spec inside `type ( X; Y )` groups.
func (*GoGrammar) SymbolKinds() map[string]string {
	return map[string]string{
		"function_declaration": "function",
		"method_declaration":   "method",
		"type_spec":            "type",
		"type_alias":           "type",
	}
}

// ContainerKinds returns the node types the walker should descend into
// without treating the container itself as a symbol. For Go this is just
// type_declaration (so grouped type blocks yield one Unit per inner spec).
func (*GoGrammar) ContainerKinds() map[string]bool {
	return map[string]bool{"type_declaration": true}
}

// SymbolName extracts the display name of a Go symbol declaration. Functions
// and methods have an identifier under field "name"; type_spec / type_alias
// nodes have their name under "name" as well.
func (*GoGrammar) SymbolName(n *sitter.Node, source []byte) string {
	switch n.Type() {
	case "function_declaration", "method_declaration",
		"type_spec", "type_alias":
		if name := n.ChildByFieldName("name"); name != nil {
			return name.Content(source)
		}
	}
	return ""
}

// PackageName returns the file's package identifier, or "" if none.
func (*GoGrammar) PackageName(root *sitter.Node, source []byte) string {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		c := root.NamedChild(i)
		if c == nil || c.Type() != "package_clause" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			cc := c.NamedChild(j)
			if cc != nil && cc.Type() == "package_identifier" {
				return cc.Content(source)
			}
		}
	}
	return ""
}

// SymbolParents implements inheritanceExtractor for Go. Scope is deliberately
// narrow: interface embedding only — `type Reader interface { io.Reader }`
// produces an `inherits` edge with Relation="embeds" from Reader to io.Reader.
//
// Go struct embedding (anonymous fields like `type Service struct { *Handler }`)
// is composition, not inheritance; it's deferred to lib-ek3 which will decide
// whether to share `relation=embeds` under the inherits edge kind or to
// introduce a dedicated `composes` kind.
//
// Type aliases and other type_spec shapes return nil — this hook fires for
// any Unit with Kind="type" (per classFamilyUnitKinds gating) but only
// interface embedding yields parents.
func (*GoGrammar) SymbolParents(n *sitter.Node, source []byte) []ParentRef {
	if n.Type() != "type_spec" {
		return nil
	}
	typeField := n.ChildByFieldName("type")
	if typeField == nil || typeField.Type() != "interface_type" {
		return nil
	}
	return goInterfaceEmbeddings(typeField, source)
}

// goInterfaceEmbeddings walks an interface_type node's named children and
// emits one ParentRef per embedded interface. Method specs / method_elem
// nodes are skipped (those are the interface's own method set, not
// embeddings). Generic type-set / union constraints (Go 1.18+ `type_elem`
// with multiple types) are NOT inheritance and are ignored — a `type_elem`
// that happens to wrap a single bare embedded type is recognised, anything
// more complex gets skipped.
func goInterfaceEmbeddings(n *sitter.Node, source []byte) []ParentRef {
	var out []ParentRef
	add := func(name string, loc indexer.Location) {
		if name == "" {
			return
		}
		out = append(out, ParentRef{
			Name:     strings.TrimSpace(name),
			Relation: "embeds",
			Loc:      loc,
		})
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		loc := indexer.Location{
			Line:       int(c.StartPoint().Row) + 1,
			Column:     int(c.StartPoint().Column) + 1,
			ByteOffset: int(c.StartByte()),
		}
		switch c.Type() {
		case "method_elem", "method_spec":
			// Interface's own method set — not embedding.
		case "type_identifier":
			add(c.Content(source), loc)
		case "qualified_type":
			add(c.Content(source), loc)
		case "type_elem":
			// Go 1.18+ grammar variant: a type_elem wraps the embedded
			// type. Only treat it as embedding when it wraps exactly one
			// identifier/qualified_type child (i.e., not a union like
			// `int | float64`).
			if c.NamedChildCount() != 1 {
				continue
			}
			inner := c.NamedChild(0)
			if inner == nil {
				continue
			}
			innerLoc := indexer.Location{
				Line:       int(inner.StartPoint().Row) + 1,
				Column:     int(inner.StartPoint().Column) + 1,
				ByteOffset: int(inner.StartByte()),
			}
			switch inner.Type() {
			case "type_identifier", "qualified_type":
				add(inner.Content(source), innerLoc)
			}
		}
	}
	return out
}

// Imports returns ImportRefs for every import declaration in the file. Both
// `import "foo"` and `import ( "foo"; "bar" )` forms are handled via a full
// walk; blank imports (`import _ "foo"`) carry Alias="_"; aliased imports
// (`import f "foo"`) carry Alias="f".
//
// Path literals may be either interpreted_string_literal (double-quoted)
// or raw_string_literal (backtick-quoted); we strip both.
func (*GoGrammar) Imports(root *sitter.Node, source []byte) []ImportRef {
	var out []ImportRef
	walk(root, func(n *sitter.Node) bool {
		if n.Type() != "import_spec" {
			return true
		}
		ref := ImportRef{}
		if path := n.ChildByFieldName("path"); path != nil {
			ref.Path = strings.Trim(path.Content(source), "\"`")
		}
		if name := n.ChildByFieldName("name"); name != nil {
			ref.Alias = name.Content(source)
		}
		if ref.Path != "" {
			out = append(out, ref)
		}
		return false
	})
	return out
}
