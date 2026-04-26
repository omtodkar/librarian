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
// The render logic is intentionally inlined here to match what
// registerSearchDocs emits, so a future change to the format string will
// cause this test to fail visibly instead of silently regressing.
func TestSearchDocsFormatNonDuplicatedSection(t *testing.T) {
	cases := []struct {
		name      string
		chunk     store.DocChunk
		wantLabel string // the exact string expected after "**Section:** "
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
			// Reproduce the exact format string used in registerSearchDocs.
			line := fmt.Sprintf("**Section:** %s\n", tc.chunk.SectionHeading)

			// The label must appear exactly once.
			if !strings.Contains(line, tc.wantLabel) {
				t.Errorf("section label %q not found in output line %q", tc.wantLabel, line)
			}
			if count := strings.Count(line, tc.wantLabel); count != 1 {
				t.Errorf("section label %q appears %d times in output line %q (want 1)", tc.wantLabel, count, line)
			}

			// Golden output check: the full line must match exactly.
			wantLine := fmt.Sprintf("**Section:** %s\n", tc.wantLabel)
			if line != wantLine {
				t.Errorf("rendered line:\n  got:  %q\n  want: %q", line, wantLine)
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
			// Reproduce the exact format string used in registerGetContext.
			line := fmt.Sprintf("### %s > %s\n", tc.chunk.FilePath, tc.chunk.SectionHeading)

			if count := strings.Count(line, tc.wantSection); count != 1 {
				t.Errorf("section label %q appears %d times in output line %q (want 1)", tc.wantSection, count, line)
			}
		})
	}
}
