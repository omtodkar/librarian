// Package code indexes source files via tree-sitter. A Grammar plugs in
// per-language AST knowledge (node types, name extraction, import syntax);
// the shared CodeHandler walks the parsed tree and produces format-agnostic
// ParsedDoc output.
//
// This package ships the Grammar interface + CodeHandler wiring. Concrete
// languages live in sibling files (golang.go, python.go, java.go,
// javascript.go — the last ships TypeScript, TSX, and JavaScript grammars
// backed by a shared base — kotlin.go, swift.go, and dart.go). Each grammar
// registers through the package-level init() so the extension → handler
// mapping stays in one place.
package code

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
	"librarian/internal/store"
)

// ImportRef captures a single import declaration. Path is the module / file
// string; Alias is the local name the import is bound to (empty when absent);
// Static is Java's `import static` form and false for other languages.
// Metadata carries grammar-specific tags — used by TypeScript for
// type_only/default/namespace markers — merged into Reference.Metadata.
//
// Kind overrides the emitted Reference.Kind. Default "" → "import". Dart
// uses Kind="part" to distinguish `part 'foo.dart'` / `part of 'bar.dart'`
// file-joins from ordinary imports — emitting them as Reference.Kind="part"
// so `neighbors --edge-kind=import` stays clean.
type ImportRef struct {
	Path     string
	Alias    string
	Static   bool
	Kind     string
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
//
// Kind overrides the emitted Reference.Kind. Default "" → "inherits". Dart
// uses Kind="requires" for `mixin M on Base` — a use-site constraint, not
// an inheritance parent — so "all parents of X" queries stay clean.
type ParentRef struct {
	Name     string
	Relation string
	Loc      indexer.Location
	Kind     string
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
	"enum":           true, // Java enum implements, Swift enum conformance
	"record":         true, // Java records can implement
	"protocol":       true, // Swift protocol declarations (lib-wji.4)
	"mixin":          true, // reserved for lib-wji.3 Dart mixins
	"struct":         true, // Swift struct declarations (lib-wji.4)
	"extension":      true, // Swift extension declarations (lib-wji.4) — inheritance = adds conformances
	"extension_type": true, // Dart extension_type_declaration (lib-wji.3) — implements clause
	"object":         true, // Kotlin object / companion object (lib-wji.2)
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

// symbolPathElementResolver is an optional interface a Grammar implements
// to customise the Unit.Path segment of a just-matched symbol while keeping
// Unit.Title = the bare name from SymbolName.
//
// The walker default joins pathPrefix + SymbolName for both Unit.Path and
// Unit.Title. This works for most grammars because the symbol's display
// name and its place in the path hierarchy are the same string. Go methods
// break that: `func (s *AuthServiceServer) Login()` has display name "Login"
// but belongs under "AuthServiceServer.Login" in the path so that two
// methods with the same name on different receivers in the same package
// project to distinct sym: nodes. The resolver returns "" to fall through
// to the default (pathElement == name); a non-empty return is used as the
// path element only.
//
// Invocation happens inside extractUnits (symbol-match path), AFTER
// SymbolKindFor has settled the Unit.Kind, because the decision may depend
// on the final kind (Go only rewrites the path for kind="method").
// Grammars that don't implement this are unaffected.
type symbolPathElementResolver interface {
	SymbolPathElement(node *sitter.Node, source []byte, name, kind string) string
}

// symbolKindResolver is an optional interface a Grammar implements to
// override the Unit.Kind of a just-matched symbol node at emission time.
// The walker starts with the kind from SymbolKinds() and applies the
// function→method rewrite (for functions inside class containers), then
// gives grammars one more opportunity to refine. Invocation happens
// inside extractUnits (symbol-match path), earlier than the other hooks
// below which fire during buildUnit — this is why it appears first.
//
// Use case: Swift's tree-sitter node `class_declaration` covers four
// semantically-distinct declarations (class / struct / enum / extension),
// differentiated only by an anonymous keyword child. The static
// SymbolKinds() map can't express this — all four would land as one Kind.
// Swift implements SymbolKindFor to inspect the keyword and emit the
// precise Kind ("class", "struct", "enum", "extension"). Grammars that
// don't implement this are unaffected.
type symbolKindResolver interface {
	SymbolKindFor(node *sitter.Node, source []byte, declaredKind string) string
}

// symbolMetadataExtractor is an optional interface a Grammar implements to
// contribute structured per-symbol metadata (beyond Signals and References).
// The walker calls SymbolMetadata for every emitted Unit and merges the
// returned map into Unit.Metadata; returning nil or an empty map is a no-op.
//
// Examples of what this surface is for:
//
//   - Kotlin: `fun String.toSlug()` → {"receiver": "String"} so queries
//     like "all extensions of String" become a cheap metadata filter.
//   - Swift: members inside `extension String { ... }` → {"receiver":
//     "String"}; the extension declaration itself → {"extends_type":
//     "String"}.
//
// Candidate future users (not yet implemented, no beads filed):
//
//   - Go method receivers — the pointer/value distinction on
//     `func (s *Service) Foo()` vs `func (s Service) Foo()`. The AST
//     already exposes the receiver; only the hook implementation is
//     missing.
//   - C# / Rust extension / impl target types — similar shape.
//
// Distinguished from extraSignalsExtractor by shape: signals are
// append-oriented observations (labels, annotations, TODOs); metadata is a
// structured key-value map that downstream retrieval uses as a filter
// predicate. Grammars that don't implement this are unaffected.
type symbolMetadataExtractor interface {
	SymbolMetadata(node *sitter.Node, source []byte) map[string]any
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

// parsedDocPostProcessor is an optional interface a Grammar implements when
// it needs to stash file-level information onto the ParsedDoc AFTER the
// shared walker finishes — not per-symbol Metadata, which has its own
// symbolMetadataExtractor hook. Today the only user is the Protobuf grammar,
// which projects `option go_package` / `option java_package` etc. onto
// ParsedDoc.Metadata["options"] (feeds lib-4kb's buf.gen.yaml awareness).
// ParseCtx invokes it last, after imports and inheritance have been emitted
// so the grammar can read (but not mutate) the final Refs list.
type parsedDocPostProcessor interface {
	PostProcess(doc *indexer.ParsedDoc, root *sitter.Node, source []byte)
}

// CallRef captures one call expression identified during AST walking.
// CallerPath is the Unit.Path of the enclosing function/method as computed
// by the grammar. CalleeName is the bare callee identifier extracted from the
// call expression node.
//
// Grammars that cannot determine the full package-qualified path (e.g. Python,
// whose PackageName returns "") return a local path — "ClassName.method" rather
// than "module.ClassName.method". extractCallRefs resolves it via suffix
// matching against the file's parsed Units.
type CallRef struct {
	CallerPath string
	CalleeName string
	Loc        indexer.Location
}

// callExtractor is an optional interface a Grammar implements to surface
// function-to-function call relationships during the graph pass.
// CallExpressions is called once per file after all Units have been built.
// Each returned CallRef becomes a ParsedDoc.Reference with Kind="call",
// Source resolved against doc.Units, Target resolved or bare, and
// Metadata["confidence"] of "resolved" (same-file callee) or "unresolved".
//
// Grammars that don't implement this are unaffected; ParseCtx type-asserts
// and only calls it when present.
type callExtractor interface {
	CallExpressions(root *sitter.Node, source []byte) []CallRef
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
	if err := parser.SetLanguage(h.grammar.Language()); err != nil {
		return nil, err
	}

	tree := parser.ParseCtx(context.Background(), content, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse returned nil tree (language load or ABI mismatch)")
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
	if ce, ok := h.grammar.(callExtractor); ok {
		h.extractCallRefs(ce, root, content, doc)
	}
	if pp, ok := h.grammar.(parsedDocPostProcessor); ok {
		pp.PostProcess(doc, root, content)
	}

	return doc, nil
}

// extractCallRefs invokes the grammar's callExtractor and appends one
// Reference{Kind="call"} per returned CallRef to doc.Refs.
//
// Caller resolution: both the full Unit.Path and the path-without-first-segment
// are indexed so Python/Java grammars that omit the module/package prefix still
// match (e.g. "Service.validate" resolves to "module.Service.validate").
// CallRefs whose CallerPath matches no Unit are silently dropped — a call
// outside any known symbol boundary has no graph node to anchor the edge.
//
// Callee resolution: the callee name is looked up by Unit.Title. A match sets
// confidence="resolved" and uses the full Unit.Path as the target; no match
// keeps the bare CalleeName and sets confidence="unresolved" so downstream
// queries can tell precise edges from speculative ones.
func (h *CodeHandler) extractCallRefs(ce callExtractor, root *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	// callerMap: full-path and path-without-first-segment → canonical Unit.Path.
	callerMap := make(map[string]string, len(doc.Units)*2)
	// calleeMap: bare Title → Unit.Path for same-file callee resolution.
	calleeMap := make(map[string]string, len(doc.Units))
	for _, u := range doc.Units {
		callerMap[u.Path] = u.Path
		if dot := strings.Index(u.Path, "."); dot >= 0 {
			local := u.Path[dot+1:]
			if _, exists := callerMap[local]; !exists {
				callerMap[local] = u.Path
			}
		}
		if _, exists := calleeMap[u.Title]; !exists {
			calleeMap[u.Title] = u.Path
		}
	}

	for _, cr := range ce.CallExpressions(root, source) {
		if cr.CallerPath == "" || cr.CalleeName == "" {
			continue
		}
		callerPath, ok := callerMap[cr.CallerPath]
		if !ok {
			continue
		}
		target := cr.CalleeName
		confidence := "unresolved"
		if path, ok := calleeMap[cr.CalleeName]; ok {
			target = path
			confidence = "resolved"
		}
		doc.Refs = append(doc.Refs, indexer.Reference{
			Kind:   store.EdgeKindCall,
			Source: callerPath,
			Target: target,
			Loc:    cr.Loc,
			Metadata: map[string]any{
				"confidence": confidence,
			},
		})
	}
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

	for i := uint(0); i < node.NamedChildCount(); i++ {
		child := node.NamedChild(i)
		if child == nil {
			continue
		}
		typ := child.Kind()

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
			if declaredKind == "function" && classFamilyUnitKinds[containerKind] {
				emittedKind = "method"
			}
			// Grammar-specific Kind override runs AFTER the function→method
			// rewrite so the Swift resolver (which only cares about
			// class_declaration flavors) doesn't need to re-implement the
			// container-scope rewrite.
			if r, ok := h.grammar.(symbolKindResolver); ok {
				if override := r.SymbolKindFor(child, source, emittedKind); override != "" {
					emittedKind = override
				}
			}
			name := h.grammar.SymbolName(child, source)
			if name != "" {
				// Unit.Title stays the bare name; Unit.Path may be extended
				// by a grammar that implements symbolPathElementResolver
				// (Go uses this to include the receiver type on methods).
				// Empty / unimplemented falls through to pathElement == name,
				// preserving every grammar's existing path shape.
				pathElement := name
				if r, ok := h.grammar.(symbolPathElementResolver); ok {
					if override := r.SymbolPathElement(child, source, name, emittedKind); override != "" {
						pathElement = override
					}
				}
				unit := h.buildUnit(child, source, pathPrefix, pathElement, name, emittedKind, pending)
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
//
// Optional-interface hook call order (each is a no-op for grammars that
// don't implement):
//
//  1. ExtractRationaleSignals on the docstring (shared, not an extractor)
//  2. annotationExtractor.SymbolAnnotations      → Signal{Kind="annotation"}
//  3. extraSignalsExtractor.SymbolExtraSignals   → append verbatim
//  4. symbolMetadataExtractor.SymbolMetadata     → merged into Unit.Metadata
//
// No current grammar depends on state produced by an earlier hook, so the
// order is purely additive. If a future hook needs cross-hook state, the
// ordering contract should be made explicit at that point.
func (h *CodeHandler) buildUnit(
	n *sitter.Node,
	source []byte,
	pathPrefix, pathElement, title, kind string,
	pendingComments []*sitter.Node,
) indexer.Unit {
	path := joinPath(pathPrefix, pathElement)

	var docLines []string
	for _, c := range pendingComments {
		docLines = append(docLines, stripCommentMarkers(c.Utf8Text(source)))
	}
	if inner := h.grammar.DocstringFromNode(n, source); inner != "" {
		docLines = append(docLines, inner)
	}
	docstring := strings.TrimSpace(strings.Join(docLines, "\n"))

	body := n.Utf8Text(source)
	content := body
	if docstring != "" {
		content = docstring + "\n\n" + body
	}

	unit := indexer.Unit{
		Kind:    kind,
		Title:   title,
		Path:    path,
		Content: content,
		Loc: indexer.Location{
			Line:       int(n.StartPosition().Row) + 1,
			Column:     int(n.StartPosition().Column) + 1,
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
	if me, ok := h.grammar.(symbolMetadataExtractor); ok {
		for k, v := range me.SymbolMetadata(n, source) {
			if unit.Metadata == nil {
				unit.Metadata = map[string]any{}
			}
			unit.Metadata[k] = v
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
		kind := p.Kind
		if kind == "" {
			kind = "inherits"
		}
		doc.Refs = append(doc.Refs, indexer.Reference{
			Kind:     kind,
			Source:   unitPath,
			Target:   p.Name,
			Loc:      p.Loc,
			Metadata: meta,
		})
	}
}

// localTypeBindings builds the short-name → canonical-target map used by
// inheritance resolvers to rewrite bare parent names (`extends BaseService`)
// into fully-qualified form (`com.example.BaseService`). Shared across
// Java, Python, and Kotlin — all three languages' import shapes agree on:
//
//   - Wildcards (Target suffixed with `.*`) provide no bindings.
//   - Aliases, when present in Metadata["alias"], become the local name.
//   - Otherwise the local name is the leaf of the dotted Target.
//   - On duplicate local names, first-wins (AST order = source order).
//
// skipStatic controls whether imports with Metadata["static"]=true are
// dropped. Java sets this true — `import static Foo.method` binds a method
// name, not a type, and can't satisfy an inheritance target. Python and
// Kotlin pass false (neither emits a static-flagged import).
//
// JS/TS has a different canonical form (module stem + "." + member) and
// keeps its own jsLocalNamedBindings in javascript.go.
func localTypeBindings(refs []indexer.Reference, skipStatic bool) map[string]string {
	out := map[string]string{}
	for _, r := range refs {
		if r.Kind != "import" || r.Target == "" {
			continue
		}
		if strings.HasSuffix(r.Target, ".*") {
			continue
		}
		if skipStatic && r.Metadata != nil {
			if v, ok := r.Metadata["static"].(bool); ok && v {
				continue
			}
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

// resolveInheritsRefs is the shared body of every grammar's ResolveParents
// method. Rewrites bare (non-dotted) Reference.Target values of Kind="inherits"
// using the supplied local map, and marks the rest as unresolved. Skips:
//
//   - Non-inherits refs.
//   - Refs already in dotted form (either source wrote an FQN or an earlier
//     pass canonicalised them).
//   - Refs with Metadata["unresolved_expression"]=true (Python call bases,
//     JS/TS mixin-application fallbacks) — these aren't identifier targets
//     the resolver can reason about.
//
// Any ref not rewritten gets Metadata["unresolved"]=true so downstream
// queries can distinguish confident edges from placeholder ones.
//
// Mutation contract: modifies elements in the backing array of `refs`
// (sets Target or populates Metadata["unresolved"]). Callers must pass
// the canonical `doc.Refs` slice, not an aliasing sub-slice. Mirrors the
// in-place contract of Python's ResolveImports; the returned slice is
// the same backing array for convenience.
func resolveInheritsRefs(refs []indexer.Reference, local map[string]string) []indexer.Reference {
	for i, r := range refs {
		if r.Kind != "inherits" {
			continue
		}
		if r.Metadata != nil {
			if v, _ := r.Metadata["unresolved_expression"].(bool); v {
				continue
			}
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

// modifiersNode returns the first named `modifiers` child of a declaration
// node, or nil. Used by Kotlin and Swift — both grammars wrap attributes,
// annotations, and access/mutation modifiers in a shared `modifiers` node
// whose presence is optional.
func modifiersNode(n *sitter.Node) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "modifiers" {
			return c
		}
	}
	return nil
}

// extractImports runs the grammar's import extractor and appends each as a
// ParsedDoc.Reference with Kind="import". Alias / Static / grammar-provided
// Metadata all land on Reference.Metadata so the indexer pipeline can
// surface them.
func (h *CodeHandler) extractImports(root *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	for _, imp := range h.grammar.Imports(root, source) {
		kind := imp.Kind
		if kind == "" {
			kind = "import"
		}
		ref := indexer.Reference{Kind: kind, Target: imp.Path}
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
		if commentSet[n.Kind()] {
			all.WriteString(stripCommentMarkers(n.Utf8Text(source)))
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
	return int(b.StartPosition().Row) <= int(a.EndPosition().Row)+1
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
	for i := uint(0); i < root.ChildCount(); i++ {
		walk(root.Child(i), visit)
	}
}

// findDescendant returns the first named descendant (DFS) matching kind,
// or nil. Bails on first match — cheaper than walk() when only one
// match is needed.
func findDescendant(n *sitter.Node, kind string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == kind {
			return c
		}
		if found := findDescendant(c, kind); found != nil {
			return found
		}
	}
	return nil
}

// findAncestor walks n.Parent() upward until it finds a node whose Type is
// in types, or returns nil if no match is reached before the tree root.
// Mirror of walk() for the upward direction. Package-shared since multiple
// grammars may need to walk up to a scope-carrying ancestor (Kotlin's
// secondary_constructor → class_declaration to borrow the class's name;
// Swift extension members → enclosing extension target).
func findAncestor(n *sitter.Node, types ...string) *sitter.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		for _, t := range types {
			if p.Kind() == t {
				return p
			}
		}
	}
	return nil
}

// nodeLocation returns an indexer.Location populated from a tree-sitter
// Node's start position and byte offset. Package-shared because multiple
// grammars open-code the same three-field struct literal when recording
// Unit / Reference locations.
func nodeLocation(n *sitter.Node) indexer.Location {
	return indexer.Location{
		Line:       int(n.StartPosition().Row) + 1,
		Column:     int(n.StartPosition().Column) + 1,
		ByteOffset: int(n.StartByte()),
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
		NewSwiftGrammar(),
		NewDartGrammar(),
		NewProtoGrammar(),
	} {
		indexer.RegisterDefault(New(g))
	}
}
