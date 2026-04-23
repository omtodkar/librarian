package markdown_test

import (
	"reflect"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/markdown"
)

func TestHandler_SatisfiesInterface(t *testing.T) {
	var _ indexer.FileHandler = markdown.New()
}

func TestHandler_NameAndExtensions(t *testing.T) {
	h := markdown.New()

	if h.Name() != "markdown" {
		t.Errorf("Name() = %q, want %q", h.Name(), "markdown")
	}

	want := map[string]bool{".md": true, ".markdown": true}
	exts := h.Extensions()
	if len(exts) != len(want) {
		t.Errorf("Extensions() returned %d entries, want %d: %v", len(exts), len(want), exts)
	}
	for _, e := range exts {
		if !want[e] {
			t.Errorf("Extensions() includes unexpected entry %q", e)
		}
	}
}

func TestHandler_ParseBasicFields(t *testing.T) {
	content := []byte(`---
title: Sample
type: guide
description: demo
---

# Top

Intro.

## Section A

Body A.
`)

	h := markdown.New()
	doc, err := h.Parse("sample.md", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if doc.Format != "markdown" {
		t.Errorf("Format = %q, want %q", doc.Format, "markdown")
	}
	if doc.Path != "sample.md" {
		t.Errorf("Path = %q, want %q", doc.Path, "sample.md")
	}
	if doc.Title != "Sample" {
		t.Errorf("Title = %q, want %q", doc.Title, "Sample")
	}
	if doc.DocType != "guide" {
		t.Errorf("DocType = %q, want %q", doc.DocType, "guide")
	}
	if doc.Summary != "demo" {
		t.Errorf("Summary = %q, want %q", doc.Summary, "demo")
	}
	if len(doc.Units) == 0 {
		t.Fatalf("expected at least one Unit, got 0")
	}
	for _, u := range doc.Units {
		if u.Kind != "section" {
			t.Errorf("Unit.Kind = %q, want %q", u.Kind, "section")
		}
	}
	if doc.Metadata == nil || doc.Metadata["frontmatter"] == nil {
		t.Error("expected frontmatter in metadata")
	}
}

// TestHandler_ChunkParityWithLegacy is the byte-identical-output gate from the
// lib-20v acceptance criteria. It verifies that running content through the new
// FileHandler path produces exactly the same Chunks as the legacy
// ParseMarkdown + ChunkDocument path.
func TestHandler_ChunkParityWithLegacy(t *testing.T) {
	content := []byte(`---
title: Parity Doc
type: adr
description: verifies round-trip equivalence
---

# Parity Doc

Intro paragraph with some context.

## First Section

First section body with multiple sentences. This one has **WARNING:** inline.

Another paragraph in the first section.

## Second Section

Second body paragraph.

**NOTE:** a secondary note.

**deprecated**: marked-risk value.

### Subsection

Nested subsection content.

## Third Section

Third body with just text.
`)

	cfg := indexer.DefaultChunkConfig()

	// Legacy path.
	pd, err := indexer.ParseMarkdownBytes("parity.md", content)
	if err != nil {
		t.Fatalf("legacy parse: %v", err)
	}
	legacyChunks := indexer.ChunkDocument(pd, cfg)

	// Handler path.
	h := markdown.New()
	doc, err := h.Parse("parity.md", content)
	if err != nil {
		t.Fatalf("handler parse: %v", err)
	}
	handlerChunks, err := h.Chunk(doc, cfg)
	if err != nil {
		t.Fatalf("handler chunk: %v", err)
	}

	if len(legacyChunks) != len(handlerChunks) {
		t.Fatalf("chunk count mismatch: legacy=%d handler=%d", len(legacyChunks), len(handlerChunks))
	}
	for i := range legacyChunks {
		if !reflect.DeepEqual(legacyChunks[i], handlerChunks[i]) {
			t.Errorf("chunk %d differs:\nlegacy:  %+v\nhandler: %+v", i, legacyChunks[i], handlerChunks[i])
		}
	}
}

// TestHandler_ChunkParity_NoSections exercises the fallback path where the
// document has no headings. The legacy chunker treats the whole RawContent as a
// single chunk; the handler must do the same after round-tripping.
func TestHandler_ChunkParity_NoSections(t *testing.T) {
	content := []byte("Plain text with no headings at all. Just some words in a paragraph. And another paragraph, too, so the thing reaches minimum token count and actually generates a chunk from the raw content fallback path used by the chunker when there are no sections to split on.")

	cfg := indexer.DefaultChunkConfig()

	pd, err := indexer.ParseMarkdownBytes("plain.md", content)
	if err != nil {
		t.Fatalf("legacy parse: %v", err)
	}
	legacyChunks := indexer.ChunkDocument(pd, cfg)

	h := markdown.New()
	doc, err := h.Parse("plain.md", content)
	if err != nil {
		t.Fatalf("handler parse: %v", err)
	}
	handlerChunks, err := h.Chunk(doc, cfg)
	if err != nil {
		t.Fatalf("handler chunk: %v", err)
	}

	if !reflect.DeepEqual(legacyChunks, handlerChunks) {
		t.Errorf("no-sections fallback diverges:\nlegacy:  %+v\nhandler: %+v", legacyChunks, handlerChunks)
	}
}
