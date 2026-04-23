// Package markdown implements the FileHandler for markdown files (.md, .markdown).
//
// For the multi-format migration this is a thin adapter over the existing
// goldmark-based parser and chunker in the parent indexer package. It converts their
// outputs to the format-agnostic ParsedDoc and reuses Chunk as-is.
//
// A follow-up task (lib-31p) will consolidate the markdown-specific parsing and
// chunking code into this package.
package markdown

import (
	"librarian/internal/indexer"
)

// Handler parses and chunks markdown files.
type Handler struct{}

// New returns a markdown handler.
func New() *Handler { return &Handler{} }

// init registers the markdown handler with indexer.DefaultRegistry so a blank-import
// (`import _ "librarian/internal/indexer/handlers/markdown"`) is sufficient to enable
// markdown indexing. Callers that prefer explicit registration can use New() + a
// custom Registry via indexer.NewWithRegistry.
func init() {
	indexer.RegisterDefault(New())
}

// Compile-time assertion that Handler satisfies indexer.FileHandler.
var _ indexer.FileHandler = (*Handler)(nil)

// Name implements indexer.FileHandler.
func (*Handler) Name() string { return "markdown" }

// Extensions implements indexer.FileHandler.
func (*Handler) Extensions() []string { return []string{".md", ".markdown"} }

// Parse implements indexer.FileHandler. It delegates to the existing goldmark-based
// parser and converts the result to ParsedDoc.
func (*Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	pd, err := indexer.ParseMarkdownBytes(path, content)
	if err != nil {
		return nil, err
	}
	return toParsedDoc(pd, path), nil
}

// Chunk implements indexer.FileHandler. It walks ParsedDoc.Units, converts section
// units into the generic SectionInput shape, and delegates to indexer.ChunkSections
// for the token-aware splitting/fallback logic. No ParsedDoc -> ParsedDocument
// round-trip is required.
func (*Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		if u.Kind != "section" {
			continue
		}
		hierarchy, _ := u.Metadata["hierarchy"].([]string)

		line := ""
		meta := "{}"
		if emphasis := signalsToEmphasis(u.Signals); emphasis != nil {
			line = emphasis.SignalLine()
			meta = emphasis.ToJSON()
		}

		inputs = append(inputs, indexer.SectionInput{
			Heading:    u.Title,
			Hierarchy:  hierarchy,
			Content:    u.Content,
			SignalLine: line,
			SignalMeta: meta,
		})
	}
	return indexer.ChunkSections(doc.Title, doc.RawContent, inputs, opts), nil
}
