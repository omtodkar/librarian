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
| `Reference` | Outbound link from this doc: `import`, `code-file`, `call`, `inherits`, `doc-link`, `config-key`. `inherits` carries `Metadata.relation ∈ {extends, implements, mixes, conforms, embeds}` and a populated `Source` field so the graph pass anchors the edge at `sym:<Source>` rather than the file. Legacy `extends`/`implements` Kind values remain backward-compatible aliases in the graph-pass switches |

### Unit kinds

Handlers emit these kinds into `Unit.Kind`:

| Kind | Source |
|---|---|
| `section` | Markdown H1–H6 section, Office heading, PDF tagged heading |
| `paragraph` | Standalone paragraph in formats without sections |
| `class` | Class declaration (Go, Python, Java, TS, Kotlin, Swift) |
| `struct` | Swift struct declaration |
| `interface` | Interface declaration (Java, TS) |
| `protocol` | Swift protocol declaration |
| `extension` | Swift extension declaration — `Title` is the target type (`extension String {}` → Title=`String`) |
| `enum` | Enumeration (Java, TS, Kotlin — `enum class` emits Kind=class + label=enum; Swift) |
| `record` | Java record |
| `object` | Kotlin `object` / `companion object` declaration |
| `function` | Standalone function (Go, Python, JS, TS, Kotlin, Swift) |
| `method` | Function inside a class/protocol container (all code grammars) |
| `constructor` | Java / Kotlin constructor (primary and secondary); Swift `init` |
| `field` | Class field / top-level variable (Java field, JS/TS property_signature) |
| `property` | Kotlin `val` / `var` property (including primary-constructor `val`/`var` params); Swift `var` / `let` |
| `type` | Type alias or type definition (Go, Python PEP 695/613, TS, Kotlin `typealias`, Swift `typealias`) |
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
| Go | `.go` | function, method, type, const/var decl, package, imports, **interface embedding** (`inherits` with `relation=embeds`) |
| Python | `.py` | function, method (incl. async), class, type (PEP 695 + PEP 613), decorator signals, **class bases** (`inherits` with `relation=extends`; metaclass/total kwargs filtered; dot-relative resolver) |
| Java | `.java` | class, interface, enum, record, method, constructor, field, `@annotation` signals, **extends/implements** (`inherits`; same-file-import bare-name resolution) |
| JavaScript | `.js`, `.jsx`, `.mjs`, `.cjs` | function, arrow-fn, class, method, export, **class extends** (`inherits`; call-expression mixin fallback with `unresolved_expression=true`) |
| TypeScript | `.ts` | everything above + interface, enum, type, abstract class, method signature, **class/interface extends + implements** |
| TSX | `.tsx` | TypeScript + JSX |
| Kotlin | `.kt` | class, interface, enum class, object, companion object, function, property, typealias, secondary constructor, `@annotation` signals, **22 modifier label signals** (data/sealed/open/abstract/inline/value/inner/lateinit/const/override/suspend/operator/infix/tailrec/external/noinline/crossinline/reified/expect/actual/annotation/companion + interface/enum keyword labels), **extends/implements** (`inherits`; heuristic: `constructor_invocation` target → extends, bare `user_type` → implements, interface-extends-interface → extends); explicit delegation (`: Bar by d`); same-file-import bare-name resolution including aliases; **extension-function receiver** via `Unit.Metadata["receiver"]` (nullable + generics stripped). Known upstream-grammar gaps: `fun interface` parses as ERROR, `context(Scope)` receivers parse as `call_expression` — tracked in lib-ljn |
| Swift | `.swift` | class, struct, enum, protocol, extension (first-class Kind="extension" with target as Title), function, method, init, property, typealias, `@attribute` signals (@MainActor, @Published, @State, @available, @objc, @IBOutlet, etc.), **modifier label signals** (final, open, static, override, required, convenience, mutating, nonmutating, isolated, nonisolated, weak, unowned, lazy, dynamic, indirect + struct/enum/extension flavor labels), **inheritance** via `inheritance_specifier` children with per-flavor heuristic: `class X: A, B` → A=extends, B=conforms; `struct/enum X: A, B` → all conforms; `extension X: A, B` → all conforms; `protocol X: A, B` → all extends; `@testable import` sets `Reference.Metadata["testable"]=true` on the import ref (not a search signal); **extension-member receiver** via `Unit.Metadata["receiver"]` + extension Unit.Metadata["extends_type"] for the target type; same-file-import bare-name resolution |

Each grammar implements the `Grammar` interface: AST node kinds mapped to Unit kinds, symbol name extraction, docstring extraction, import shapes, optional annotation + extra-signal extractors. The shared walker handles comment buffering (docstrings), container descent (class bodies), and rationale signal extraction (TODO/FIXME/HACK/XXX).

Optional extractor interfaces a grammar may satisfy (type-asserted by the walker; grammars that don't implement them are unaffected):

- `annotationExtractor` — surfaces Java `@Deprecated`, TS decorators, Swift `@MainActor` / `@Published` / `@available`, etc. as `Signal{Kind="annotation"}`.
- `extraSignalsExtractor` — adds per-symbol signals of any Kind (JS/TS uses this for `exported` / `default-export` labels; Kotlin + Swift use it for `data`/`sealed`/`suspend`/`final`/`mutating` etc.).
- `importResolver` — post-parse rewrite of import References (Python relative-import resolution, JS/TS module path resolution).
- `inheritanceExtractor` — surfaces class-family parent relationships; the walker converts returned `ParentRef`s into `Reference{Kind="inherits"}` with `Source=<child Unit.Path>`, `Target=<parent name>`, and `Metadata.relation`.
- `inheritanceResolver` — post-parse rewrite of inherits References (Java/JS/TS/Kotlin/Swift same-file-import bare-name lookup; Python dot-relative + from-import binding lookup). Runs AFTER `importResolver` so import targets are already in canonical form.
- `symbolKindResolver` — overrides `Unit.Kind` at symbol-emission time. Used by Swift where a single `class_declaration` AST node covers four semantically-distinct flavors (class / struct / enum / extension) differentiated only by an anonymous keyword child.
- `symbolMetadataExtractor` — contributes structured per-symbol metadata (key/value map) merged into `Unit.Metadata`. Kotlin and Swift use this for extension-function/member receiver types (`fun String.toSlug()` or `extension String { func slug() }` → `Metadata["receiver"]="String"`), keeping "all extensions of String" a cheap metadata filter.

#### Python relative-import resolver

Python's `from . import utils` and `from ..pkg import Thing` preserve leading dots at the grammar layer but are rewritten to absolute dotted form (`mypkg.utils`, `pkg.Thing`) before they reach the store — so a module imported via both relative and absolute syntax lands on a single `sym:` graph node, giving "who imports X?" queries the full fan-in.

The rewrite is a `ParseCtx` post-pass: the grammar's optional `importResolver` method (`python.go:ResolveImports`) runs after `Imports()` using the file's absolute path and `config.Python.SrcRoots`. Three-tier package detection (`python_resolve.go:containingPackage`):

1. `python.src_roots` match — file sits under a configured root → anchor at the root boundary (PEP 420 / src-layout friendly, no `__init__.py` required).
2. `__init__.py` walk — upward traversal from the file's directory, collecting contiguous package markers.
3. Virtual directory fallback — project-relative directory as implicit package when the first two yield nothing; rejected if any component isn't a valid Python identifier.

`AssertGrammarInvariants` (shared grammar test helper) enforces the postcondition via a grammar-gated check: Python `Reference.Target` must never start with `.` or contain `..`; JS/TS `Reference.Target` must never start with `./` or `../`.

#### JS/TS relative-specifier resolver

ES modules map 1:1 to files, so `import x from "./utils"` in `src/a.ts` resolves to the on-disk path `src/utils.ts` (or `.tsx`, `.mts`, `.cts`, `.js`, `.jsx`, `.mjs`, `.cjs`, or `utils/index.*` — TS-family first, index-file fallback). `NodeNext`-style explicit-`.js` imports on TS sources are rewritten to the `.ts` sibling so graph nodes land on the canonical source.

Resolved relative specifiers emit `file:src/utils.ts` graph nodes (matching `store.CodeFileNodeID`) — "who imports file X?" becomes an incoming-edge lookup on the existing code-file node. Bare npm specifiers (`lodash`, `@scope/pkg`) stay untouched but get tagged `node_kind=external` in `Reference.Metadata` so `graphTargetID` routes them to `ext:` nodes, keeping `sym:` exclusive to in-project symbols. Named imports (`import { foo, bar as b } from "./utils"`) are split at grammar-extraction time: `Path=./utils` with `Metadata["member"]=foo` + `Alias=b` as applicable — no dot-heuristics needed in the resolver.

Config knobs are intentionally absent — the priority order matches ts-node / esbuild / Vite conventions. `tsconfig.json` paths aliases (`@/components/*`) fall through to the bare-specifier branch and currently land as `ext:@/components/Button`; proper path resolution is a follow-up.

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
