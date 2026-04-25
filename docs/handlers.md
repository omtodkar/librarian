---
title: File Handlers
type: reference
description: The FileHandler abstraction that lets Librarian index markdown, code, config, Office documents, and PDFs through one pipeline.
---

# File Handlers

Librarian indexes nine+ file formats through a single abstraction: the `FileHandler` interface in `internal/indexer/handler.go`. Each format-specific package registers a handler for its extensions; the core pipeline (walker → dispatch → chunker → store) is format-agnostic.

## The interface

```go
type FileHandler interface {
    Name() string
    Extensions() []string
    Parse(path string, content []byte) (*ParsedDoc, error)
    Chunk(doc *ParsedDoc, opts ChunkOpts) ([]Chunk, error)
}
```

`Parse` converts raw bytes to a `ParsedDoc` — a format-agnostic tree of `Unit`s with structural metadata. `Chunk` splits that tree into embedding-ready chunks. The walker doesn't care which handler does the work; it just looks up the handler for a given extension.

## ParsedDoc / Unit / Signal / Reference

The four shapes every handler produces:

| Shape | Role |
|---|---|
| `ParsedDoc` | Document-level: title, format, doc type, headings list, frontmatter, units, references, signals |
| `Unit` | Hierarchical content node: kind, title, path, content, signals, children. Kinds are listed below |
| `Signal` | Typed marker on a doc or unit: warnings, decisions, TODOs, rationale, code annotations, emphasis |
| `Reference` | Outbound link from this doc: `import`, `code-file`, `call`, `extends`, `doc-link`, `config-key` |

### Unit kinds

Handlers emit these kinds into `Unit.Kind`:

| Kind | Source |
|---|---|
| `section` | Markdown H1–H6 section, Office heading, PDF tagged heading |
| `paragraph` | Standalone paragraph in formats without sections |
| `class` | Class/struct declaration (Go, Python, Java, TS) |
| `interface` | Interface declaration (Java, TS) |
| `enum` | Enumeration (Java, TS) |
| `record` | Java record |
| `function` | Standalone function (Go, Python, JS, TS) |
| `method` | Function inside a class container |
| `constructor` | Java constructor |
| `field` | Class field / top-level variable |
| `type` | Type alias or type definition (Go, Python PEP 695/613, TS) |
| `key-path` | YAML/JSON/TOML/properties/XML key path |
| `page` | PDF page fallback |
| `table` | Tabular summary (XLSX) |

## Registry

`indexer.RegisterDefault(h)` registers a handler with the default registry (`internal/indexer/registry.go`). Extensions are last-writer-wins, so two handlers claiming the same extension is a silent collision — each handler package asserts disjoint extension sets.

Blank-importing `internal/indexer/handlers/defaults` wires every built-in handler in one line:

```go
import _ "librarian/internal/indexer/handlers/defaults"
```

`cmd/root.go` then re-registers format-specific handlers after `config.Load()` so user-configured caps / toggles flow through without a global singleton.

## Shipped handlers

### Markdown — `internal/indexer/handlers/markdown/`

The baseline. Uses goldmark + goldmark-meta for frontmatter, AST walk, heading hierarchy, diagrams, tables, and emphasis signals. Extensions: `.md`, `.markdown`.

Every other handler ultimately delegates `Parse`/`Chunk` to the markdown handler after converting its source format to markdown — this keeps chunking logic in one place.

### Code grammars — `internal/indexer/handlers/code/`

Tree-sitter powered. Six languages share one `CodeHandler` wrapping per-language `Grammar` structs:

| Grammar | Extensions | Emits |
|---|---|---|
| Go | `.go` | function, method, type, const/var decl, package, imports |
| Python | `.py` | function, method (incl. async), class, type (PEP 695 + PEP 613), decorator signals |
| Java | `.java` | class, interface, enum, record, method, constructor, field, `@annotation` signals |
| JavaScript | `.js`, `.jsx`, `.mjs`, `.cjs` | function, arrow-fn, class, method, export |
| TypeScript | `.ts` | everything above + interface, enum, type, abstract class, method signature |
| TSX | `.tsx` | TypeScript + JSX |

Each grammar implements the `Grammar` interface: AST node kinds mapped to Unit kinds, symbol name extraction, docstring extraction, import shapes, optional annotation + extra-signal extractors. The shared walker handles comment buffering (docstrings), container descent (class bodies), and rationale signal extraction (TODO/FIXME/HACK/XXX).

#### Python relative-import resolver

Python's `from . import utils` and `from ..pkg import Thing` preserve leading dots at the grammar layer but are rewritten to absolute dotted form (`mypkg.utils`, `pkg.Thing`) before they reach the store — so a module imported via both relative and absolute syntax lands on a single `sym:` graph node, giving "who imports X?" queries the full fan-in.

The rewrite is a `ParseCtx` post-pass: the grammar's optional `importResolver` method (`python.go:ResolveImports`) runs after `Imports()` using the file's absolute path and `config.Python.SrcRoots`. Three-tier package detection (`python_resolve.go:containingPackage`):

1. `python.src_roots` match — file sits under a configured root → anchor at the root boundary (PEP 420 / src-layout friendly, no `__init__.py` required).
2. `__init__.py` walk — upward traversal from the file's directory, collecting contiguous package markers.
3. Virtual directory fallback — project-relative directory as implicit package when the first two yield nothing; rejected if any component isn't a valid Python identifier.

`AssertGrammarInvariants` (shared grammar test helper) enforces the postcondition via a Python-gated check: `Reference.Target` must never start with `.` or contain `..` on Python grammar output. The gate widens grammar-by-grammar as their own resolvers land.

### Config files — `internal/indexer/handlers/config/`

Six handlers, one file each:

| Handler | Extensions |
|---|---|
| YAML | `.yaml`, `.yml` |
| JSON | `.json` |
| TOML | `.toml` |
| XML | `.xml` |
| Properties | `.properties` |
| Env | `.env` |

Each produces `Unit{Kind: "key-path", Path: "a.b.c", Content: <value + comment>}` so every leaf key is independently searchable. Leading comments on a key become its docstring.

### Office formats — `internal/indexer/handlers/office/`

Three formats, one `Handler` struct with three constructors:

| Handler | Extension | Parser |
|---|---|---|
| DOCX | `.docx` | pure-Go `encoding/xml` over `archive/zip` (AGPL-free) |
| XLSX | `.xlsx` | `github.com/xuri/excelize/v2` (BSD-3) |
| PPTX | `.pptx` | pure-Go XML |

Each converts the Office document to markdown preserving heading levels, lists, tables, hyperlinks (DOCX); slide titles, bullet depth, speaker notes (PPTX); per-sheet GFM tables with row/col caps (XLSX). The generated markdown is then fed to the markdown handler so chunking and signal extraction run identically. Format/DocType on the returned `ParsedDoc` are stamped to `"docx"` / `"xlsx"` / `"pptx"` so downstream filters can tell where a chunk came from.

ZIP-bomb guard: `openZip` rejects archives exceeding 200 MB uncompressed or 10 000 entries before any format-specific parser runs.

### PDF — `internal/indexer/handlers/pdf/`

`.pdf` via `github.com/klippa-app/go-pdfium` running in its WebAssembly mode (wazero, pure-Go, no CGo). The ~5 MB `pdfium.wasm` is embedded via `//go:embed`. One shared instance lives behind a `sync.Mutex` + `inFlight` WaitGroup so `cmd/index.go` can `defer pdf.Shutdown()` without racing in-flight Parse calls.

Structure cascade (first viable tier wins):

1. **Tagged-PDF struct tree** — semantic H1/H2/P/L/LI/Table from `/StructTreeRoot`.
2. **Bookmarks/outline** — author-curated TOC; heading level = bookmark depth, body text bounded by the next bookmark's page.
3. **Font-size heuristic** — cluster rects by rendered size; largest sizes above the body mode become H2/H3/H4.
4. **Flat per-page fallback** — `## Page N` for each page with `GetPageText` body.

The chosen tier is recorded on `Metadata["pdf.structure_source"]` for diagnosability. OCR for scanned pages is deferred (tracked as a follow-up).

## Where to add a new format

1. Create `internal/indexer/handlers/<format>/` with `Name()`, `Extensions()`, `Parse`, `Chunk`, and an `init()` that calls `indexer.RegisterDefault(New(...))`.
2. Blank-import it in `internal/indexer/handlers/defaults/defaults.go`.
3. If the format needs user config, add a struct to `internal/config/config.go`, wire defaults in `Load()`, and have `cmd/root.go` re-register after `config.Load()` with the user-scoped instance (matches the Office + PDF precedent).

The walker, store, MCP server, and every downstream consumer need no changes.
