package pdf

import (
	"fmt"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/markdown"
)

// Handler is the FileHandler for .pdf files. It converts each PDF to
// markdown via a three-tier structure cascade (see package doc) then
// delegates Parse + Chunk to the markdown handler. Format and DocType
// are overridden to "pdf" so downstream filters distinguish PDF-sourced
// content from hand-written markdown.
type Handler struct {
	cfg Config
	md  *markdown.Handler
}

// NewPDF returns a handler wired with the given Config (MaxPages cap).
func NewPDF(cfg Config) *Handler {
	return &Handler{cfg: cfg, md: markdown.New()}
}

// Compile-time interface assertion.
var _ indexer.FileHandler = (*Handler)(nil)

func (*Handler) Name() string         { return "pdf" }
func (*Handler) Extensions() []string { return []string{".pdf"} }

// Parse runs the PDF→markdown conversion cascade, hands the result to the
// markdown handler for Unit-tree construction, then stamps Format /
// DocType on the returned ParsedDoc. The tier that produced the markdown
// is recorded on Metadata["pdf.structure_source"] for diagnostics.
func (h *Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	md, source, err := convertPDF(content, h.cfg)
	if err != nil {
		return nil, fmt.Errorf("converting pdf: %w", err)
	}
	doc, err := h.md.Parse(path, md)
	if err != nil {
		return nil, err
	}
	doc.Format = "pdf"
	doc.DocType = "pdf"
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	doc.Metadata["pdf.structure_source"] = source
	return doc, nil
}

// Chunk delegates verbatim to the markdown handler — PDF-generated Units
// are markdown section Units, no transformation needed.
func (h *Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return h.md.Chunk(doc, opts)
}

// init registers the PDF handler with DefaultConfig. cmd/root.go
// overwrites the registration with a user-configured instance after
// config.Load (see the last-writer-wins semantics in
// internal/indexer/registry.go). Extensions across handlers must remain
// disjoint — RegisterDefault silently last-writer-wins on collision, so
// an accidental `.pdf` in a second handler would hide this one.
func init() {
	indexer.RegisterDefault(NewPDF(DefaultConfig()))
}
