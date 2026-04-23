package indexer

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
//	"section"    — markdown H1-H6 section
//	"paragraph"  — standalone paragraph in formats without sections
//	"class"      — class/struct/interface/enum declaration
//	"method"     — method/function declaration
//	"field"      — class field / top-level variable
//	"key-path"   — YAML/JSON/TOML/properties key path
//	"page"       — PDF page or slide
//	"row"        — CSV row (opt-in, rare)
//	"table"      — tabular data summary
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

	// Metadata is unit-specific extras (method parameter list, YAML value type).
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
//	"extends"     — class extending another class
//	"implements"  — class implementing an interface
//	"config-key"  — document or config referencing another config key
//	"doc-link"    — markdown linking to another document
type Reference struct {
	// Kind categorizes the reference (see type doc).
	Kind string

	// Target identifies the referenced entity. Shape depends on Kind:
	//
	//	"code-file":  file path (e.g., "internal/auth/service.go")
	//	"import":     fully-qualified symbol (e.g., "com.acme.util.Logger")
	//	"call":       symbol name
	//	"config-key": dotted key path
	Target string

	// Context is a surrounding snippet (typically the line where the reference appears).
	Context string

	// Loc is where the reference appears.
	Loc Location

	// Metadata is reference-specific extras (call arguments, import alias).
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
