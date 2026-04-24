// Package code indexes source files via tree-sitter. A Grammar plugs in
// per-language AST knowledge (node types, name extraction, import syntax);
// the shared CodeHandler walks the parsed tree and produces format-agnostic
// ParsedDoc output.
//
// This package ships the Grammar interface + CodeHandler wiring. Concrete
// languages live in sibling files (golang.go, python.go; typescript and java
// to follow via bd issues lib-46j and lib-x91). Each grammar registers through
// the package-level init() so the extension → handler mapping stays in one
// place.
package code

import (
	"context"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"librarian/internal/indexer"
)

// ImportRef captures a single import declaration. Path is the module / file
// string; Alias is the local name the import is bound to (empty when absent);
// Static is Java's `import static` form and false for other languages.
type ImportRef struct {
	Path   string
	Alias  string
	Static bool
}

// Grammar encapsulates per-language tree-sitter knowledge. Implementations
// provide enough information for the shared walker to extract symbols,
// imports, and their docstrings without the walker knowing anything
// language-specific.
type Grammar interface {
	// Name returns a stable identifier (e.g., "go", "python"). Also used
	// as ParsedDoc.Format and for logging.
	Name() string

	// Extensions returns lowercase file extensions this grammar handles.
	Extensions() []string

	// Language returns the tree-sitter language for parsing.
	Language() *sitter.Language

	// SymbolKinds maps AST node types to generic Unit.Kind values. Nodes
	// whose Type() is not in the map are skipped (unless also listed in
	// ContainerKinds — see below).
	//
	// Example (Go): {
	//   "function_declaration": "function",
	//   "method_declaration":   "method",
	//   "type_declaration":     "type",
	// }
	SymbolKinds() map[string]string

	// ContainerKinds lists AST node types that are not symbols themselves
	// but contain nested symbols the walker should descend into. Examples:
	// Python class_definition contains method function_definitions; Java
	// class_declaration's class_body contains methods and inner classes.
	//
	// Go returns an empty map — all Go symbols are at the file root.
	ContainerKinds() map[string]bool

	// SymbolName returns the display name of a symbol node (e.g., the
	// function identifier). Return "" to skip the node — useful when the
	// same AST node type is a symbol in some contexts and not others
	// (the grammar can inspect Node.Parent() etc. to decide).
	SymbolName(node *sitter.Node, source []byte) string

	// PackageName returns the file's package / namespace, or "" if none.
	// Used as the document title and prepended to Unit.Path.
	PackageName(root *sitter.Node, source []byte) string

	// Imports returns every import in the file. Each one becomes a
	// ParsedDoc.Reference with Kind="import" and any Alias / Static
	// metadata stashed in Reference.Metadata.
	Imports(root *sitter.Node, source []byte) []ImportRef

	// CommentNodeTypes returns AST node type names whose content the walker
	// should (a) buffer as "preceding context" for the next symbol's docstring
	// and (b) scan for rationale markers (TODO/FIXME/...).
	//
	// Most grammars return just the grammar's comment node name — ["comment"]
	// for Go, ["line_comment", "block_comment"] for Java. Grammars may also
	// include genuinely-not-comment nodes that should behave like preceding
	// context from the walker's perspective: Python adds "decorator" so
	// `@dataclass` text lands in the following symbol's docstring and is
	// searchable. Any node listed here is also scanned by the rationale-
	// signal pass, so only include nodes whose text is unlikely to contain
	// false TODO/FIXME hits.
	CommentNodeTypes() []string

	// DocstringFromNode optionally extracts a language-idiomatic docstring
	// that lives INSIDE a symbol node (e.g., Python `"""..."""` as the
	// first statement of a function body). Returning "" falls back to the
	// preceding-sibling-comments path the walker maintains. Go returns "".
	DocstringFromNode(node *sitter.Node, source []byte) string
}

// CodeHandler implements indexer.FileHandler for a single Grammar.
type CodeHandler struct {
	grammar Grammar
}

// New returns a CodeHandler backed by the given Grammar.
func New(g Grammar) *CodeHandler { return &CodeHandler{grammar: g} }

var _ indexer.FileHandler = (*CodeHandler)(nil)

// Name implements indexer.FileHandler.
func (h *CodeHandler) Name() string { return h.grammar.Name() }

// Extensions implements indexer.FileHandler.
func (h *CodeHandler) Extensions() []string { return h.grammar.Extensions() }

// Parse implements indexer.FileHandler. Builds the AST via tree-sitter then
// walks it extracting symbol Units (with docstrings), import References, and
// rationale Signals.
func (h *CodeHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(h.grammar.Language())

	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return nil, err
	}
	defer tree.Close()

	root := tree.RootNode()
	pkg := h.grammar.PackageName(root, content)
	title := pkg
	if title == "" {
		// Fallback for languages without an explicit package clause (Python,
		// JS, etc.): use the file stem so Unit.Path reads as a proper dotted
		// identifier — `service.Service.validate` rather than
		// `service.py.Service.validate`. Only the final extension is
		// stripped, so `foo.tar.gz.py` becomes `foo.tar.gz` — fine for search,
		// but Unit.Paths for such files will have embedded dots that don't
		// correspond to package boundaries.
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     h.grammar.Name(),
		Title:      title,
		DocType:    "code",
		RawContent: string(content),
		Metadata:   map[string]any{"package": pkg},
	}

	commentSet := setFromSlice(h.grammar.CommentNodeTypes())
	symbolKinds := h.grammar.SymbolKinds()
	containerKinds := h.grammar.ContainerKinds()

	// pathPrefix starts at title (not pkg) so stem-based languages get the
	// stem in every Unit.Path. For Go, title == pkg; behaviour unchanged.
	h.extractUnits(root, content, title, "", symbolKinds, containerKinds, commentSet, doc, nil)
	h.extractImports(root, content, doc)
	doc.Signals = extractAllCommentSignals(root, content, commentSet)

	return doc, nil
}

// Chunk implements indexer.FileHandler. Each symbol Unit becomes one
// SectionInput; ChunkSections handles token-aware splitting. Signals flow
// via SignalLineFromSignals + SignalsToJSON.
func (h *CodeHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		inputs = append(inputs, indexer.SectionInput{
			Heading:    u.Title,
			Hierarchy:  []string{doc.Title, u.Title},
			Content:    u.Content,
			SignalLine: indexer.SignalLineFromSignals(u.Signals),
			SignalMeta: indexer.SignalsToJSON(u.Signals),
		})
	}
	return indexer.ChunkSections(doc.Title, doc.RawContent, inputs, opts), nil
}

// extractUnits walks the AST recursively. Top-level children are processed
// directly; container nodes are descended into so nested symbols emit under
// the container's name as additional hierarchy. A node listed in BOTH
// symbolKinds and containerKinds is hybrid — emit a Unit for it AND descend
// into it (Python/Java classes need this so the class itself is a Unit and
// its methods are separate Units).
//
// Preceding consecutive comment siblings attach as the next symbol's
// docstring; blank-line-separated comment groups do not merge (see
// commentsAreConsecutive).
//
// pathPrefix is the dotted context for Unit.Path. At the file root it's the
// package name (or file stem); inside a container it's "pkg.Container".
//
// containerKind is the Kind of the enclosing symbol container ("" at the
// root, "class" when inside a Python/Java class). When a function_definition
// Kind resolves to "function" while containerKind == "class", it's rewritten
// to "method" — the AST can't distinguish these on its own for Python.
//
// inheritedPending carries the preceding-comment buffer across container
// boundaries, so `# doc\n@decorator\nclass X:` still attaches the comment
// and the decorator to X's Unit.
func (h *CodeHandler) extractUnits(
	node *sitter.Node,
	source []byte,
	pathPrefix string,
	containerKind string,
	symbolKinds map[string]string,
	containerKinds map[string]bool,
	commentSet map[string]bool,
	doc *indexer.ParsedDoc,
	inheritedPending []*sitter.Node,
) {
	pending := inheritedPending

	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		typ := child.Type()

		if commentSet[typ] {
			// Restart the buffer if this comment is separated from the
			// previous one by a blank line — tree-sitter emits each //
			// line as its own comment node, so consecutive docstring lines
			// have adjacent line numbers.
			if len(pending) > 0 && !commentsAreConsecutive(pending[len(pending)-1], child) {
				pending = pending[:0]
			}
			pending = append(pending, child)
			continue
		}

		declaredKind, isSymbol := symbolKinds[typ]
		isContainer := containerKinds[typ]

		if isSymbol {
			// Inside a class container, what the AST calls `function_definition`
			// is semantically a method. Rewrite the emitted kind here, but
			// preserve declaredKind for the recursion's containerKind argument
			// so future grammars that register a hybrid "function" container
			// (e.g., JS class-expression-as-value) still propagate the
			// original scope kind downstream.
			emittedKind := declaredKind
			if declaredKind == "function" && containerKind == "class" {
				emittedKind = "method"
			}
			name := h.grammar.SymbolName(child, source)
			if name != "" {
				doc.Units = append(doc.Units,
					h.buildUnit(child, source, pathPrefix, name, emittedKind, pending))
			}
			// Hybrid: a node that's also a container has its body descended
			// into with the symbol's name extending the path. pending is not
			// forwarded — it was consumed by the hybrid's own Unit.
			if isContainer && name != "" {
				h.extractUnits(child, source, joinPath(pathPrefix, name), declaredKind, symbolKinds, containerKinds, commentSet, doc, nil)
			}
			pending = nil
			continue
		}

		if isContainer {
			// Pure container (no Unit for itself). Descend, forwarding `pending`
			// so a preceding docstring-comment attaches to the first inner
			// symbol. `containerKind` passes through unchanged — this container
			// doesn't add a class scope of its own (e.g., decorated_definition
			// wraps a class without changing whether we're inside a class).
			containerName := h.grammar.SymbolName(child, source)
			next := pathPrefix
			if containerName != "" {
				next = joinPath(pathPrefix, containerName)
			}
			h.extractUnits(child, source, next, containerKind, symbolKinds, containerKinds, commentSet, doc, pending)
			pending = nil
			continue
		}

		// Non-symbol, non-container, non-comment: clear the buffer so its
		// comments don't drift to the next symbol.
		pending = nil
	}
}

// buildUnit constructs a Unit from a symbol AST node. Docstrings come from
// the preceding-comment buffer OR the grammar's DocstringFromNode hook
// (whichever the grammar implements); both paths can contribute when the
// grammar returns a non-empty docstring to augment the comment buffer.
func (h *CodeHandler) buildUnit(
	n *sitter.Node,
	source []byte,
	pathPrefix, name, kind string,
	pendingComments []*sitter.Node,
) indexer.Unit {
	path := name
	if pathPrefix != "" {
		path = pathPrefix + "." + name
	}

	var docLines []string
	for _, c := range pendingComments {
		docLines = append(docLines, stripCommentMarkers(c.Content(source)))
	}
	if inner := h.grammar.DocstringFromNode(n, source); inner != "" {
		docLines = append(docLines, inner)
	}
	docstring := strings.TrimSpace(strings.Join(docLines, "\n"))

	body := n.Content(source)
	content := body
	if docstring != "" {
		content = docstring + "\n\n" + body
	}

	unit := indexer.Unit{
		Kind:    kind,
		Title:   name,
		Path:    path,
		Content: content,
		Loc: indexer.Location{
			Line:       int(n.StartPoint().Row) + 1,
			Column:     int(n.StartPoint().Column) + 1,
			ByteOffset: int(n.StartByte()),
		},
	}
	if docstring != "" {
		unit.Signals = indexer.ExtractRationaleSignals(docstring)
	}
	return unit
}

// extractImports runs the grammar's import extractor and appends each as a
// ParsedDoc.Reference with Kind="import". Alias / Static metadata rides on
// Reference.Metadata so the indexer pipeline can surface it if it wants.
func (h *CodeHandler) extractImports(root *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	for _, imp := range h.grammar.Imports(root, source) {
		ref := indexer.Reference{Kind: "import", Target: imp.Path}
		if imp.Alias != "" || imp.Static {
			ref.Metadata = map[string]any{}
			if imp.Alias != "" {
				ref.Metadata["alias"] = imp.Alias
			}
			if imp.Static {
				ref.Metadata["static"] = true
			}
		}
		doc.Refs = append(doc.Refs, ref)
	}
}

// extractAllCommentSignals walks every comment in the file and runs the
// shared rationale-marker extractor over the combined text. Per-unit
// docstring signals are handled by buildUnit; this catches TODO/FIXME/etc.
// sprinkled inside function bodies.
func extractAllCommentSignals(root *sitter.Node, source []byte, commentSet map[string]bool) []indexer.Signal {
	var all strings.Builder
	walk(root, func(n *sitter.Node) bool {
		if commentSet[n.Type()] {
			all.WriteString(stripCommentMarkers(n.Content(source)))
			all.WriteByte('\n')
			return false
		}
		return true
	})
	return indexer.ExtractRationaleSignals(all.String())
}

// commentsAreConsecutive reports whether two comment nodes are on adjacent
// source lines, i.e., not separated by a blank line. tree-sitter does not
// emit whitespace nodes, so this is the only way to detect blank-line breaks
// between comment groups.
func commentsAreConsecutive(a, b *sitter.Node) bool {
	return int(b.StartPoint().Row) <= int(a.EndPoint().Row)+1
}

// stripCommentMarkers removes common comment leaders from a raw comment
// token so the rationale regex sees the comment body itself. Handles //,
// /* */, #, --, and Javadoc / JSDoc * continuation leaders.
func stripCommentMarkers(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/*") {
		s = strings.TrimPrefix(s, "/*")
		s = strings.TrimSuffix(s, "*/")
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		t = strings.TrimPrefix(t, "//")
		t = strings.TrimPrefix(t, "#")
		t = strings.TrimPrefix(t, "--")
		t = strings.TrimPrefix(t, "*")
		lines[i] = strings.TrimSpace(t)
	}
	return strings.Join(lines, "\n")
}

// walk visits every node in the tree (DFS). The visitor returns true to
// descend into the node's children, false to skip its subtree.
func walk(root *sitter.Node, visit func(*sitter.Node) bool) {
	if root == nil || !visit(root) {
		return
	}
	for i := 0; i < int(root.ChildCount()); i++ {
		walk(root.Child(i), visit)
	}
}

// joinPath composes a dotted Unit.Path from a prefix and a name, handling the
// empty-prefix case so the result never starts with '.'. Used by extractUnits
// at two recursion points that previously open-coded the same conditional.
func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// setFromSlice returns a membership-test map for a slice of strings.
func setFromSlice(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

// init wires all code grammars into the default registry. Consolidated at the
// package level (mirroring internal/indexer/handlers/config/config.go) so
// adding a new grammar only requires authoring its sibling file and appending
// its constructor to this list — no scattered per-file init()s.
func init() {
	for _, g := range []Grammar{
		NewGoGrammar(),
		NewPythonGrammar(),
	} {
		indexer.RegisterDefault(New(g))
	}
}
