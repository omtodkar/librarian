package pdf

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
)

// The Handler.Parse contract: convert the PDF to markdown, run it through
// the markdown handler's Parse, then stamp Format/DocType = "pdf" so
// downstream filters distinguish PDF-sourced chunks. Metadata records
// which cascade tier produced the markdown.
func TestHandler_Parse_OverridesFormatAndRecordsSource(t *testing.T) {
	h := NewPDF(DefaultConfig())
	doc, err := h.Parse("docs/sample.pdf", loadFixture(t, "plain.pdf"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "pdf" {
		t.Errorf("Format = %q, want %q", doc.Format, "pdf")
	}
	if doc.DocType != "pdf" {
		t.Errorf("DocType = %q, want %q", doc.DocType, "pdf")
	}
	src, _ := doc.Metadata["pdf.structure_source"].(string)
	if src == "" {
		t.Errorf("pdf.structure_source missing from Metadata: %+v", doc.Metadata)
	}
}

// Chunking must produce at least one chunk for a PDF with body text —
// this validates that the markdown delegation chain actually passes
// through ChunkSections.
func TestHandler_Chunk_ProducesAtLeastOneChunk(t *testing.T) {
	h := NewPDF(DefaultConfig())
	doc, err := h.Parse("docs/multi.pdf", loadFixture(t, "multipage.pdf"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.ChunkOpts{MaxTokens: 512, MinTokens: 10, OverlapLines: 0})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected at least one chunk, got 0")
	}
	// At least one chunk should carry body text — confirms per-page text
	// reached the chunker intact.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "Page") || strings.Contains(c.Content, "content") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected body text in at least one chunk; got:\n%+v", chunks)
	}
}

// Registration sanity: blank-import via defaults must wire the handler.
func TestHandler_RegisteredByDefault(t *testing.T) {
	if h := indexer.DefaultRegistry().HandlerFor("x.pdf"); h == nil {
		t.Error(".pdf extension not registered in DefaultRegistry")
	}
}
