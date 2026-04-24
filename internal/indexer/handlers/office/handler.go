// Package office ships three FileHandlers that convert Office Open XML
// documents (.docx, .xlsx, .pptx) to markdown at ingest time and delegate
// parsing and chunking to the existing markdown handler. Nothing about
// ParsedDoc / Chunk / signal extraction changes — Office files join the
// same pipeline hand-written markdown uses.
//
// DOCX and PPTX are parsed via pure-Go encoding/xml over archive/zip; the
// mature Go libraries for those formats are AGPL-3.0 or CGo-backed and
// unsuitable for this project's licensing + self-contained-binary goals.
// XLSX uses github.com/xuri/excelize/v2 (BSD-3-Clause, de-facto standard).
//
// What the v1 converters preserve:
//   - DOCX: heading levels (Heading1/2/3 → #/##/###), numbered+bulleted
//     list indentation, tables, hyperlinks, `w:br`, `w:tab`.
//   - PPTX: slide order (authoritative from presentation.xml), title
//     placeholders as `## Slide N: Title`, bullet-level indentation,
//     speaker notes as `### Notes`.
//   - XLSX: per-sheet `## <Sheet Name>` with a markdown table; row/col
//     caps configurable via OfficeConfig.
//
// What's skipped in v1 (tracked as follow-up bd issues): DOCX headers/
// footers / comments / tracked changes / footnotes / images / math, PPTX
// slide layouts beyond title/body, XLSX merged cells + formula evaluation.
//
// Configuration flows through constructor arguments rather than global
// state: cmd/root.go re-registers the handlers after config.Load() via
// `indexer.RegisterDefault(office.NewXlsx(cfg))`, which overwrites the
// init-time defaults. This keeps handlers free of package-level mutable
// state, matching the stateless-handler convention used by markdown and
// the code grammars.
package office

import (
	"fmt"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/markdown"
)

// Handler is the one-size-fits-all FileHandler for all three office
// formats. Three thin constructors (NewDocx / NewXlsx / NewPptx) stamp
// each instance with its name, format string, extension set, and a
// format-specific byte-to-markdown converter.
//
// Keeping a single struct matches the code.CodeHandler pattern and
// eliminates near-identical Name / Extensions / Parse / Chunk method
// bodies on three separate types.
type Handler struct {
	name       string
	format     string // overrides ParsedDoc.Format / DocType on delegation
	extensions []string
	convert    func([]byte, Config) ([]byte, error)
	cfg        Config
	md         *markdown.Handler
}

// NewDocx returns the DOCX handler. DOCX ignores every Config field today;
// cfg is accepted for signature uniformity across the three constructors.
func NewDocx(cfg Config) *Handler {
	return &Handler{
		name:       "docx",
		format:     "docx",
		extensions: []string{".docx"},
		convert:    func(b []byte, _ Config) ([]byte, error) { return convertDocx(b) },
		cfg:        cfg,
		md:         markdown.New(),
	}
}

// NewXlsx returns the XLSX handler wired with the given Config (row/col
// caps).
func NewXlsx(cfg Config) *Handler {
	return &Handler{
		name:       "xlsx",
		format:     "xlsx",
		extensions: []string{".xlsx"},
		convert:    convertXlsx,
		cfg:        cfg,
		md:         markdown.New(),
	}
}

// NewPptx returns the PPTX handler. Config controls speaker-notes emission.
func NewPptx(cfg Config) *Handler {
	return &Handler{
		name:       "pptx",
		format:     "pptx",
		extensions: []string{".pptx"},
		convert:    convertPptx,
		cfg:        cfg,
		md:         markdown.New(),
	}
}

// Compile-time interface assertion.
var _ indexer.FileHandler = (*Handler)(nil)

func (h *Handler) Name() string         { return h.name }
func (h *Handler) Extensions() []string { return h.extensions }

// Parse converts the office bytes to markdown with the format-specific
// converter, then delegates to the markdown handler for the Unit-tree
// construction. Format / DocType are overridden on the returned doc so
// downstream filters distinguish office-sourced content from hand-written
// markdown.
func (h *Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	md, err := h.convert(content, h.cfg)
	if err != nil {
		return nil, fmt.Errorf("converting %s: %w", h.format, err)
	}
	doc, err := h.md.Parse(path, md)
	if err != nil {
		return nil, err
	}
	doc.Format = h.format
	doc.DocType = h.format
	return doc, nil
}

// Chunk delegates verbatim to the markdown handler — office-converted
// Units are markdown section Units, no transformation needed.
func (h *Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return h.md.Chunk(doc, opts)
}

// init registers the three office handlers with DefaultConfig. cmd/root.go
// overwrites the registration with user-configured instances after
// config.Load (see the `Register` last-writer-wins semantics in
// internal/indexer/registry.go). Extensions across the three grammars
// must remain disjoint — RegisterDefault silently last-writer-wins on
// collision, so an accidental `.docx` in two handlers would hide one.
func init() {
	indexer.RegisterDefault(NewDocx(DefaultConfig()))
	indexer.RegisterDefault(NewXlsx(DefaultConfig()))
	indexer.RegisterDefault(NewPptx(DefaultConfig()))
}
