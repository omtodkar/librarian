// Package code indexes source files via tree-sitter. A Grammar plugs in
// per-language AST knowledge (node types, name extraction, import syntax);
// the shared CodeHandler walks the parsed tree and produces format-agnostic
// ParsedDoc output.
//
// This package ships the Grammar interface + CodeHandler wiring. Concrete
// languages live in sibling files (golang.go, python.go, java.go,
// javascript.go — the last ships TypeScript, TSX, and JavaScript grammars
// backed by a shared base). Each grammar registers through the package-level
// init() so the extension → handler mapping stays in one place.
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
// Metadata carries grammar-specific tags — used by TypeScript for
// type_only/default/namespace markers — merged into Reference.Metadata.
type ImportRef struct {
	Path     string
	Alias    string
	Static   bool
	Metadata map[string]any
}

// ParentRef captures one inheritance relationship declared on a class-family
// symbol (Java class extends/implements, Python class bases, TS class/interface
// heritage, Go interface embedding). One symbol with multiple parents emits
// multiple ParentRefs.
//
// Name is the parent identifier as it appears in source, post-generic-stripping
// (e.g., `Map<K,V>` → "Map", with ["K","V"] stashed in Metadata["type_args"]).
//
// Relation carries the semantic flavor of the inheritance edge. Reserved
// values: "extends" (single-parent class / interface-extends-interface),
// "implements" (Java-style interface conformance), "mixes" (Dart `with`
// mixins — reserved for lib-wji.3), "conforms" (Swift protocol conformance —
// reserved for lib-wji.4), "embeds" (Go interface embedding; Go struct
// embedding deferred to a separate bead).
//
// Loc points at the parent identifier within the source, not the class header
// — so multiple parents on separate lines each carry their own line number.
//
// Metadata conventions (optional): "type_args" ([]string of generic parameters),
// "unresolved" (bool — parent name could not be mapped to a canonical form by
// same-file import lookup), "unresolved_expression" (bool — base was a call /
// non-identifier expression, like Python `class Foo(factory()):` or JS
// `class Foo extends Mixin(Base)`; identifier-only fallback captured).
type ParentRef struct {
	Name     string
	Relation string
	Loc      indexer.Location
	Metadata map[string]any
}

// classFamilyUnitKinds gates the inheritanceExtractor hook: parent extraction
// is only meaningful for these Unit.Kind values, and skipping the rest saves
// a tree-sitter subtree walk per method / field / function in the file. Kept
// here because the walker is the sole invocation site.
//
// "type" is included because Go's Unit.Kind for `type X interface {...}` /
// `type X struct {...}` is "type" (type_spec), and interface embedding is a
// real inheritance-adjacent construct. Non-interface Go type_specs return nil
// from GoGrammar.SymbolParents, and other grammars' type-kind aliases
// (TS type_alias_declaration, Python TypeAlias) also return nil — so the
// extra gate opens cost nothing for them.
var classFamilyUnitKinds = map[string]bool{
	"class":          true,
	"interface":      true,
	"abstract_class": true, // reserved — TS abstract_class_declaration currently maps to Unit.Kind="class" via tsOnlySymbolKinds, so this entry doesn't fire today. Kept so a future grammar can emit the distinct kind without gate changes.
	"enum":           true, // Java enum implements, and future languages
	"record":         true, // Java records can implement
	"protocol":       true, // reserved for lib-wji.4 Swift protocols
	"mixin":          true, // reserved for lib-wji.3 Dart mixins
	"struct":         true, // reserved for lib-wji.4 Swift structs / future others
	"object":         true, // reserved for lib-wji.2 Kotlin objects
	"type":           true, // Go type_spec wrapping interface_type (embedding)
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

// annotationExtractor is an optional interface a Grammar may implement to
// emit annotation-kind signals for a symbol (Java @Deprecated, C# attributes,
// etc.). Values are annotation names WITHOUT the leading `@` — the walker
// wraps each in a Signal{Kind: "annotation", Value: <name>} and merges it
// into the symbol's Unit.Signals alongside rationale markers.
//
// Grammars that don't implement this are unaffected; the walker type-asserts
// and only calls it when present.
type annotationExtractor interface {
	SymbolAnnotations(node *sitter.Node, source []byte) []string
}

// extraSignalsExtractor is an optional interface for grammars that emit
// per-symbol Signals of arbitrary Kind beyond annotations — used by the
// JS/TS grammar to flag exported / default-export symbols with label
// signals. Returned signals are appended verbatim to Unit.Signals; the
// walker only fills in Loc if the grammar left it zero.
type extraSignalsExtractor interface {
	SymbolExtraSignals(node *sitter.Node, source []byte) []indexer.Signal
}

// importResolver is an optional interface a Grammar implements when its
// extracted import References need a post-parse rewrite informed by the
// file's on-disk location — today only Python, to resolve relative imports
// against the containing package. code.ParseCtx invokes it after
// grammar.Imports() has produced bare References and before they're finalised
// on the ParsedDoc. Grammars that don't implement it are unaffected.
type importResolver interface {
	ResolveImports(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference
}

// inheritanceExtractor is an optional interface a Grammar implements to
// surface parent-type relationships declared on a class-family symbol. The
// walker invokes SymbolParents in buildUnit whenever the symbol's Kind is in
// classFamilyUnitKinds (class / interface / abstract_class / enum / record /
// protocol / mixin / struct / object). Each returned ParentRef becomes a
// ParsedDoc.Reference with Kind="inherits", Source=<containing Unit.Path>,
// Target=<ParentRef.Name>, and Metadata carrying relation + any
// grammar-provided keys (type_args, unresolved, unresolved_expression).
//
// Grammars that don't implement this are unaffected; the walker type-asserts
// and only calls it when present.
type inheritanceExtractor interface {
	SymbolParents(node *sitter.Node, source []byte) []ParentRef
}

// inheritanceResolver is an optional interface a Grammar implements when its
// extracted inherits References need a post-parse rewrite informed by file
// location or in-file imports. Mirrors importResolver. ParseCtx invokes it
// after grammar-level inheritance extraction and after ResolveImports (so
// resolvers can cross-reference the file's import list when available).
// Grammars that don't implement it are unaffected.
type inheritanceResolver interface {
	ResolveParents(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference
}

// CodeHandler implements indexer.FileHandler for a single Grammar.
type CodeHandler struct {
	grammar Grammar
}

// New returns a CodeHandler backed by the given Grammar.
func New(g Grammar) *CodeHandler { return &CodeHandler{grammar: g} }

var (
	_ indexer.FileHandler    = (*CodeHandler)(nil)
	_ indexer.FileHandlerCtx = (*CodeHandler)(nil)
)

// Name implements indexer.FileHandler.
func (h *CodeHandler) Name() string { return h.grammar.Name() }

// Extensions implements indexer.FileHandler.
func (h *CodeHandler) Extensions() []string { return h.grammar.Extensions() }

// Parse implements indexer.FileHandler. Thin wrapper over ParseCtx with an
// empty ParseContext — grammars that implement importResolver (Python)
// therefore skip resolution when callers use the legacy Parse path. Used by
// grammar-level tests that want to inspect raw AST output without standing up
// a filesystem fixture; production code paths go through the indexer's
// FileHandlerCtx dispatch.
func (h *CodeHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	return h.ParseCtx(path, content, indexer.ParseContext{})
}

// ParseCtx implements indexer.FileHandlerCtx. Builds the AST via tree-sitter
// then walks it extracting symbol Units (with docstrings), import References,
// and rationale Signals. If the Grammar implements importResolver, import
// References are rewritten using ctx (Python uses this to resolve relative
// imports against the file's containing package).
func (h *CodeHandler) ParseCtx(path string, content []byte, ctx indexer.ParseContext) (*indexer.ParsedDoc, error) {
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
	// Tail-pending at the root level has nowhere to attach; discard.
	_ = h.extractUnits(root, content, title, "", symbolKinds, containerKinds, commentSet, doc, nil)
	h.extractImports(root, content, doc)
	if r, ok := h.grammar.(importResolver); ok {
		doc.Refs = r.ResolveImports(doc.Refs, path, ctx)
	}
	// ResolveParents runs after ResolveImports so grammars that resolve
	// inheritance targets by cross-referencing the file's imports
	// (Java/JS/TS same-file import lookup landing in lib-wji.1 Phase 3-5)
	// can read canonicalised import targets rather than raw specifiers.
	if r, ok := h.grammar.(inheritanceResolver); ok {
		doc.Refs = r.ResolveParents(doc.Refs, path, ctx)
	}
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
) []*sitter.Node {
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
				unit := h.buildUnit(child, source, pathPrefix, name, emittedKind, pending)
				doc.Units = append(doc.Units, unit)
				h.extractParentRefs(child, source, unit.Path, unit.Kind, doc)
			}
			// Hybrid: a node that's also a container has its body descended
			// into with the symbol's name extending the path. pending is not
			// forwarded — it was consumed by the hybrid's own Unit. Tail
			// from the hybrid descent is discarded so a trailing comment
			// inside one class body can't leak onto the next sibling class.
			if isContainer && name != "" {
				_ = h.extractUnits(child, source, joinPath(pathPrefix, name), declaredKind, symbolKinds, containerKinds, commentSet, doc, nil)
			}
			pending = nil
			continue
		}

		if isContainer {
			// Pure container (no Unit for itself). Descend, forwarding `pending`
			// so a preceding docstring-comment attaches to the first inner
			// symbol. Tail-pending from the recursion is re-inherited so a
			// comment that appears inside the container with no symbol
			// following it (tree-sitter-kotlin traps the file-level KDoc
			// inside package_header / import_header) can still attach to
			// the symbol that follows the container at the outer frame.
			containerName := h.grammar.SymbolName(child, source)
			next := pathPrefix
			if containerName != "" {
				next = joinPath(pathPrefix, containerName)
			}
			pending = h.extractUnits(child, source, next, containerKind, symbolKinds, containerKinds, commentSet, doc, pending)
			continue
		}

		// Non-symbol, non-container, non-comment: clear the buffer so its
		// comments don't drift to the next symbol.
		pending = nil
	}
	return pending
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
	path := joinPath(pathPrefix, name)

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
	if ae, ok := h.grammar.(annotationExtractor); ok {
		for _, a := range ae.SymbolAnnotations(n, source) {
			unit.Signals = append(unit.Signals, indexer.Signal{
				Kind:  "annotation",
				Value: a,
				Loc:   unit.Loc,
			})
		}
	}
	if ee, ok := h.grammar.(extraSignalsExtractor); ok {
		for _, s := range ee.SymbolExtraSignals(n, source) {
			if s.Loc == (indexer.Location{}) {
				s.Loc = unit.Loc
			}
			unit.Signals = append(unit.Signals, s)
		}
	}
	return unit
}

// extractParentRefs invokes the grammar's inheritanceExtractor (if any) for a
// just-emitted class-family symbol and appends one Reference per returned
// ParentRef to doc.Refs. Kind is "inherits"; Source is the symbol's Unit.Path
// so the graph pass can anchor the edge at sym:<Source>. Relation + any
// grammar-provided metadata land on Reference.Metadata.
//
// The kind-gate protects the walker from calling into grammars for symbol
// types where inheritance is meaningless (methods, fields, functions). The
// allowlist is classFamilyUnitKinds; grammars that need a new kind should add
// it there rather than overloading SymbolParents with filtering logic.
func (h *CodeHandler) extractParentRefs(n *sitter.Node, source []byte, unitPath, unitKind string, doc *indexer.ParsedDoc) {
	if !classFamilyUnitKinds[unitKind] {
		return
	}
	ie, ok := h.grammar.(inheritanceExtractor)
	if !ok {
		return
	}
	for _, p := range ie.SymbolParents(n, source) {
		if p.Name == "" {
			continue
		}
		meta := map[string]any{}
		for k, v := range p.Metadata {
			meta[k] = v
		}
		if p.Relation != "" {
			meta["relation"] = p.Relation
		}
		doc.Refs = append(doc.Refs, indexer.Reference{
			Kind:     "inherits",
			Source:   unitPath,
			Target:   p.Name,
			Loc:      p.Loc,
			Metadata: meta,
		})
	}
}

// extractImports runs the grammar's import extractor and appends each as a
// ParsedDoc.Reference with Kind="import". Alias / Static / grammar-provided
// Metadata all land on Reference.Metadata so the indexer pipeline can
// surface them.
func (h *CodeHandler) extractImports(root *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	for _, imp := range h.grammar.Imports(root, source) {
		ref := indexer.Reference{Kind: "import", Target: imp.Path}
		if imp.Alias != "" || imp.Static || len(imp.Metadata) > 0 {
			ref.Metadata = map[string]any{}
			for k, v := range imp.Metadata {
				ref.Metadata[k] = v
			}
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

// joinPath composes a dotted Unit.Path from a prefix and a name, handling
// the empty-prefix case so the result never starts with '.'. Used by
// extractUnits at the recursion points that previously open-coded the same
// conditional, and by buildUnit for the leaf Unit emission.
//
// No collision-collapse here: for Java files in the default package the
// stem equals the class name and paths look like `Orphan.Orphan.x` — mildly
// redundant but unambiguous, and the alternative (silently dropping a
// duplicate segment) broke Go `package foo` + `type foo`, Python
// `service.py` + `class service`, and nested same-name classes.
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
	// Extensions must remain disjoint across grammars. RegisterDefault
	// replaces on collision, so a duplicate `.jsx` between JavaScript and
	// TSX (say) would silently last-writer-win. Add new grammars with an
	// extension set that doesn't overlap any registered above.
	for _, g := range []Grammar{
		NewGoGrammar(),
		NewPythonGrammar(),
		NewJavaGrammar(),
		NewJavaScriptGrammar(),
		NewTypeScriptGrammar(),
		NewTSXGrammar(),
		NewKotlinGrammar(),
	} {
		indexer.RegisterDefault(New(g))
	}
}
