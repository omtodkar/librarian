package mcpserver

import (
	"fmt"
	"strings"
	"testing"

	"librarian/internal/store"
)

// TestSearchDocsFormatNonDuplicatedSection is a golden-output table test that
// guards against the regression where section headings appeared twice in
// search_docs / get_context responses (e.g. "Stage 2: DispatchStage 2: Dispatch").
//
// It calls the production formatChunkResult helper so any change to the
// format string in search_docs.go will cause this test to fail visibly.
func TestSearchDocsFormatNonDuplicatedSection(t *testing.T) {
	cases := []struct {
		name      string
		chunk     store.DocChunk
		wantLabel string // exact section heading expected in the rendered block
	}{
		{
			name: "plain text heading",
			chunk: store.DocChunk{
				FilePath:       "docs/architecture.md",
				SectionHeading: "Stage 2: Dispatch",
				Content:        "Dispatch routes the parsed unit to the correct handler.",
			},
			wantLabel: "Stage 2: Dispatch",
		},
		{
			name: "inline-code heading",
			chunk: store.DocChunk{
				FilePath:       "docs/skill.md",
				SectionHeading: "search_docs",
				Content:        "Semantic search across all indexed documentation.",
			},
			wantLabel: "search_docs",
		},
		{
			name: "multi-word heading",
			chunk: store.DocChunk{
				FilePath:       "docs/setup.md",
				SectionHeading: "Point Librarian at it",
				Content:        "Run librarian init to bootstrap the workspace.",
			},
			wantLabel: "Point Librarian at it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered := formatChunkResult(tc.chunk, true)

			// The section label must appear exactly once.
			if count := strings.Count(rendered, tc.wantLabel); count != 1 {
				t.Errorf("section label %q appears %d times in rendered block (want 1):\n%s", tc.wantLabel, count, rendered)
			}

			// Golden check: the **Section:** line must contain the label verbatim.
			wantLine := fmt.Sprintf("**Section:** %s\n", tc.wantLabel)
			if !strings.Contains(rendered, wantLine) {
				t.Errorf("rendered block does not contain expected section line %q:\n%s", wantLine, rendered)
			}
		})
	}
}

// TestGetContextFormatNonDuplicatedSection guards the get_context header line
// format ("### filepath > section") against section-label duplication.
func TestGetContextFormatNonDuplicatedSection(t *testing.T) {
	cases := []struct {
		name           string
		chunk          store.DocChunk
		wantSection    string
	}{
		{
			name: "plain heading",
			chunk: store.DocChunk{
				FilePath:       "docs/indexing.md",
				SectionHeading: "Indexing Pipeline",
				Content:        "The indexer walks the docs directory.",
			},
			wantSection: "Indexing Pipeline",
		},
		{
			name: "dispatch heading",
			chunk: store.DocChunk{
				FilePath:       "docs/architecture.md",
				SectionHeading: "Stage 2: Dispatch",
				Content:        "Dispatch description.",
			},
			wantSection: "Stage 2: Dispatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := formatContextChunkHeader(tc.chunk)

			if count := strings.Count(line, tc.wantSection); count != 1 {
				t.Errorf("section label %q appears %d times in output line %q (want 1)", tc.wantSection, count, line)
			}
		})
	}
}
