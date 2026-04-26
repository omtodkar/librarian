package indexer

import "sync"

// FileHandler parses and chunks a single file format. Implementations register with a
// Registry to be dispatched by file extension.
//
// Each handler is responsible for:
//   - Reporting the extensions it handles.
//   - Converting raw file bytes into a format-agnostic ParsedDoc tree.
//   - Splitting that tree into Chunks for embedding and storage.
//
// The core pipeline (walker, indexer orchestrator, store) is handler-agnostic. Format-
// specific concerns (goldmark, tree-sitter, YAML parsing, PDF text extraction) live
// entirely inside handler implementations.
type FileHandler interface {
	// Name returns a stable identifier for the handler (e.g., "markdown", "java").
	// Also used as ParsedDoc.Format.
	Name() string

	// Extensions returns lowercase file extensions with the leading dot
	// (e.g., []string{".md", ".markdown"}).
	Extensions() []string

	// Parse converts raw bytes to a ParsedDoc. The path is provided for metadata and
	// error messages; the handler should not re-read the file.
	Parse(path string, content []byte) (*ParsedDoc, error)

	// Chunk splits a ParsedDoc into chunks for embedding and storage. Handlers decide
	// chunk boundaries that make sense for their format: H2 sections for markdown,
	// methods for code, top-level keys for YAML, pages for PDF, etc.
	Chunk(doc *ParsedDoc, opts ChunkOpts) ([]Chunk, error)
}

// ParseContext carries per-file, per-parse context that some handlers need beyond
// what Parse() provides. Populated by the indexer from config + walker output.
// Grows additively as new handler-specific knobs appear.
type ParseContext struct {
	// AbsPath is the file's absolute on-disk path. Used by handlers that need
	// filesystem access to siblings or ancestors (Python relative-import
	// resolution walks for __init__.py markers).
	AbsPath string

	// ProjectRoot is the absolute path of the workspace root (cfg.ProjectRoot).
	// Used by handlers that need to emit project-relative node IDs or resolve
	// against a workspace boundary (JS/TS resolves './utils' to a path
	// relative to this root so graph nodes align with CodeFileNodeID's
	// project-relative contract). Empty when the indexer has no workspace
	// (some test paths).
	ProjectRoot string

	// PythonSrcRoots carries config.PythonConfig.SrcRoots through to the
	// Python grammar's import resolver. Absolute paths (project-root-joined,
	// filepath.Clean'd) so handlers can prefix-match without re-cleaning.
	PythonSrcRoots []string

	// PackageCache memoizes per-directory resolution results within a single
	// graph pass — the Python resolver's __init__.py walk hits O(depth) stats
	// per file, and every file in the same directory produces the same
	// package parts. The indexer allocates one sync.Map at the start of each
	// IndexProjectGraph run and passes the same pointer to every ParseCtx
	// call in that pass; nil means no memoization (tests that call ParseCtx
	// directly). Keys are absolute directory paths, values are []string
	// package parts (or a nil slice for "no package").
	PackageCache *sync.Map
}

// FileHandlerCtx is an optional extension of FileHandler for handlers that need
// context beyond path + content. The indexer prefers ParseCtx when the handler
// implements it; legacy handlers keep using Parse untouched.
type FileHandlerCtx interface {
	FileHandler
	ParseCtx(path string, content []byte, ctx ParseContext) (*ParsedDoc, error)
}

// ChunkOpts is the forward-looking name for ChunkConfig. Both names are interchangeable
// during the multi-format migration; new handler code should prefer ChunkOpts.
type ChunkOpts = ChunkConfig

// ParsedDoc is the format-agnostic output of FileHandler.Parse. It subsumes markdown
// sections, code symbols, and config key-paths via a generic Unit tree.
//
// Design note: ParsedDoc is intentionally additive to the legacy ParsedDocument during
// the multi-format migration. Once all handlers have moved to FileHandler, the legacy
// type will be removed.
type ParsedDoc struct {
	// Path is the file path relative to the workspace.
	Path string

	// Format is the handler's Name() (e.g., "markdown", "java", "yaml").
	Format string

	// Title is the document's primary label: H1 for markdown, class name for Java,
	// top-level key for YAML, filename fallback otherwise.
	Title string

	// DocType is a format-specific categorization (e.g., "guide", "adr", "class",
	// "config").
	DocType string

	// Summary is a short description derived from frontmatter, Javadoc, first
	// paragraph, etc. May be empty.
	Summary string

	// Headings is a flat list of the document's heading / section titles, in
	// document order. Populated by handlers whose format has a natural heading
	// structure (markdown, PDF with ToC). Consumers use this for quick
	// table-of-contents rendering without walking Units.
	Headings []string

	// Frontmatter is the parsed YAML / TOML frontmatter block of a markdown
	// document, or nil for formats without frontmatter.
	Frontmatter map[string]any

	// Units are hierarchical parsed content nodes. A markdown document is a tree of
	// sections; a Java file is a tree of package > class > method; a YAML document is
	// a tree of key paths.
	Units []Unit

	// Signals are document-level signals (warnings, decisions, risk markers). Unit-
	// level signals live on Unit.Signals.
	Signals []Signal

	// Refs are outbound references this document makes (markdown mentioning a code
	// file, Java import of another class, config referencing a key). References feed
	// graph edges at store time.
	Refs []Reference

	// Metadata holds format-specific extras: frontmatter map, AST root, Office
	// conversion provenance, etc. Core code should not read this; format-specific
	// consumers may.
	Metadata map[string]any

	// RawContent is the original bytes as string, for hashing and fallback operations.
	RawContent string
}

// Unit is a hierarchical content node within a ParsedDoc.
//
// Kind defines the unit's shape:
//
//	"section"     — markdown H1-H6 section
//	"paragraph"   — standalone paragraph in formats without sections
//	"class"          — class declaration (Go, Python, Java, TS, Kotlin, Swift, Dart)
//	"struct"         — Swift struct declaration
//	"interface"      — interface declaration (Java, TypeScript)
//	"enum"           — enumeration declaration (Java, TypeScript, Swift, Dart, Kotlin via label)
//	"record"         — Java record
//	"protocol"       — Swift protocol declaration
//	"mixin"          — Dart mixin declaration
//	"extension"      — Swift / Dart extension declaration (Swift: Title=target type; Dart: Title=extension's own name, target in Metadata["extends_type"])
//	"extension_type" — Dart extension type declaration (Dart 3) — wrapping type, distinct from "extension"
//	"object"         — Kotlin `object` / `companion object` declaration
//	"function"       — standalone function/procedure declaration
//	"method"         — method declaration (function inside a class); Swift protocol_function_declaration
//	"constructor"    — Java / Kotlin constructor; Swift init_declaration; Dart constructor (default / named / factory)
//	"field"          — class field / top-level variable
//	"property"       — Kotlin `val` / `var` property; Swift property_declaration; Dart getter/setter
//	"type"           — type alias / type definition (Go, Python, TypeScript, Swift typealias, Dart typedef)
//	"key-path"    — YAML/JSON/TOML/properties key path
//	"page"        — PDF page or slide
//	"row"         — CSV row (opt-in, rare)
//	"table"       — tabular data summary
type Unit struct {
	// Kind categorizes the unit (see type doc).
	Kind string

	// Path is a hierarchical locator. Examples:
	//
	//	markdown: "Installation > Prerequisites"
	//	java:     "com.acme.auth.AuthService.validate"
	//	yaml:     "spring.datasource.url"
	//	pdf:      "page 12"
	Path string

	// Title is the unit's short name: section heading, method name, key name.
	Title string

	// Content is the textual content prepared for embedding. Handlers structure this
	// so it's semantically meaningful: signatures + docstrings for code, key + value
	// + comment for config, section body for markdown.
	Content string

	// Signals scoped to this unit (warnings, decisions, TODO/FIXME, rationale
	// comments, annotations).
	Signals []Signal

	// Children permits tree structure where formats support it (markdown H1 > H2,
	// Java class > method). Flat formats (properties, CSV) leave this nil.
	Children []Unit

	// Loc is the source location of this unit for citations.
	Loc Location

	// Metadata is unit-specific extras. Conventional keys populated by one
	// or more handlers (open set — handlers may introduce new keys):
	//
	//	"receiver"    — extension-function/extension-member receiver type
	//	                name, generics and nullability stripped. Emitted by
	//	                the Kotlin grammar for `fun String.toSlug()` /
	//	                `fun String?.x()` and the Swift grammar for members
	//	                inside `extension String { ... }` — both produce
	//	                "String" so "all extensions of String" is a cheap
	//	                filter regardless of language.
	//	"extends_type" — target type a Swift extension declaration extends.
	//	                Emitted by the Swift grammar on Units of Kind
	//	                "extension" as the complement of "receiver": the
	//	                type the extension is extending rather than the
	//	                receiver of an individual member.
	//	"level"       — markdown section heading level (1-6). Emitted by
	//	                the markdown handler for section Units.
	//	"hierarchy"   — markdown section path as []string (e.g. ["Guide",
	//	                "Installation"]). Emitted by the markdown handler
	//	                alongside "level".
	//
	// Code-grammar keys are merged in by grammars implementing the optional
	// symbolMetadataExtractor interface (code.go). Non-code handlers may
	// stash format-specific values here directly during Parse.
	Metadata map[string]any
}

// Signal is a typed marker on a document or unit. Different formats emit different
// signals, but the shape is uniform so ranking and boosting code can handle them
// generically.
//
// Kind categorizes the signal:
//
//	"label"      — tag-like marker (WARNING, NOTE, DECISION, IMPORTANT)
//	"risk"       — deprecation / breaking-change / experimental / unsafe
//	"todo"       — TODO / FIXME / HACK / XXX
//	"rationale"  — WHY-style comment explaining intent
//	"annotation" — code annotation (@Deprecated, @Transactional)
//	"emphasis"   — bold/italicized text (markdown)
type Signal struct {
	// Kind categorizes the signal (see type doc).
	Kind string

	// Value is the signal's primary string ("deprecated", "TODO", "@Transactional").
	Value string

	// Detail is optional supporting text (TODO comment body, annotation arguments).
	Detail string

	// Loc is where the signal appears.
	Loc Location
}

// Reference is an outbound link from this document to another entity. References feed
// graph edges at store time.
//
// Kind categorizes the reference:
//
//	"code-file"   — markdown/doc mentioning a source file path
//	"import"      — code file importing another package or module
//	"call"        — code calling another function or method
//	"inherits"    — class/interface/protocol parent relationship; Metadata["relation"]
//	                carries the flavor ("extends", "implements", "mixes", "conforms",
//	                "embeds"). Source is populated with the containing symbol's Path.
//	"requires"    — Dart `mixin M on Base` constraint. Not an inheritance parent —
//	                a use-site type bound. Kept distinct from "inherits" so
//	                "all parents of X" queries stay clean. Symbol → symbol.
//	"part"        — Dart `part 'foo.dart'` / `part of 'bar.dart'` file-join
//	                directive. Semantically a file-membership relation, not a
//	                name binding — distinct from "import" so
//	                `neighbors --edge-kind=import` stays clean. code_file → code_file.
//	"config-key"  — document or config referencing another config key
//	"doc-link"    — markdown linking to another document
//
// "extends" and "implements" are retained as legacy aliases in graph-pass switches
// (see graphTargetID / graphNodeKindFromRef in internal/indexer/indexer.go) but are
// no longer emitted by new code — emit Kind="inherits" with Metadata["relation"] instead.
type Reference struct {
	// Kind categorizes the reference (see type doc).
	Kind string

	// Source identifies the originating entity WITHIN the file, when the reference
	// is symbol-scoped rather than file-scoped. Populated by grammars that emit
	// per-symbol references (inheritance parents, per-method calls) so the graph
	// pass can anchor the edge at sym:<Source> instead of the file node. Shape is
	// a Unit.Path (fully-qualified dotted identifier). Empty string = file-scoped
	// (default — preserves the long-standing shape for imports / code-file /
	// config-key / doc-link / mentions).
	//
	// Invariant: when populated, Source MUST equal a Unit.Path in the same
	// ParsedDoc. The grammar-layer walker enforces this because it populates
	// Source from the enclosing Unit as each Reference is emitted.
	Source string

	// Target identifies the referenced entity. Shape depends on Kind:
	//
	//	"code-file":  file path (e.g., "internal/auth/service.go")
	//	"import":     fully-qualified symbol (e.g., "com.acme.util.Logger")
	//	"call":       symbol name
	//	"inherits":   fully-qualified parent symbol name (generics stripped;
	//	              originals in Metadata["type_args"])
	//	"config-key": dotted key path
	Target string

	// Context is a surrounding snippet (typically the line where the reference appears).
	Context string

	// Loc is where the reference appears.
	Loc Location

	// Metadata is reference-specific extras. Conventional keys:
	//
	//	"alias"                 — import alias (string)
	//	"static"                — Java "import static" (bool)
	//	"node_kind"             — resolver-set target namespace override (string,
	//	                          see nodeKindFromMetadata)
	//	"relation"              — inheritance flavor for Kind="inherits"
	//	                          ("extends" | "implements" | "mixes" | "conforms" | "embeds")
	//	"type_args"             — generic type arguments stripped from Target
	//	                          (e.g., ["K", "V"] for Map<K, V>)
	//	"unresolved"            — bool, set true when a target name could not be
	//	                          mapped to a canonical form (e.g., bare Java class
	//	                          name with no matching import)
	//	"unresolved_expression" — bool, set true when the source expression was
	//	                          not an identifier (Python factory()-bases, JS
	//	                          mixin-application in extends clause)
	Metadata map[string]any
}

// Location is a source location for citations and graph edges. Zero values mean unknown.
type Location struct {
	// Line is the 1-indexed line. Zero means unknown.
	Line int

	// Column is the 1-indexed column. Zero means unknown.
	Column int

	// ByteOffset is the 0-indexed byte offset in the source. -1 means unknown.
	ByteOffset int
}
