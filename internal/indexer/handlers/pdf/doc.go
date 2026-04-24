// Package pdf ships a FileHandler that converts PDF documents to markdown
// at ingest and delegates parsing + chunking to the existing markdown
// handler. PDFs join the same pipeline hand-written markdown uses — no
// changes to ParsedDoc, chunking, or signal extraction.
//
// The PDF backend is github.com/klippa-app/go-pdfium running in its
// WebAssembly mode (wazero pure-Go runtime). The 5 MB pdfium.wasm binary
// is embedded in the Go binary via //go:embed inside go-pdfium itself, so
// there are no system dependencies, no CGo, and no runtime downloads. This
// keeps librarian self-contained in the same spirit as the Office
// handlers.
//
// Structure cascade (first viable tier wins):
//
//  1. Tagged-PDF struct tree (FPDF_StructTree_*) — semantic ground truth.
//  2. Bookmarks / outline (GetBookmarks) — author-curated navigation tree.
//  3. Font-size heuristic (GetPageTextStructured on Rects) — cluster by
//     RenderedSize, top-N become H1/H2/H3.
//  4. Flat fallback — "## Page N" per page. Always viable.
//
// The chosen tier is recorded on Metadata["pdf.structure_source"] so bad
// extractions are diagnosable.
//
// What v1 skips (tracked as follow-ups): OCR for scanned/image-only PDFs
// (lib-dns), annotations, attachments, AcroForm fields, password prompts,
// per-file page ranges.
//
// Configuration flows through the constructor (pdf.NewPDF(cfg)) rather
// than package-level mutable state, matching the stateless-handler
// convention used by Office and the code grammars. cmd/root.go
// re-registers the handler after config.Load() so the init-time default
// is overwritten with the user's config.
package pdf
