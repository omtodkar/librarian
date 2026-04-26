package indexer

import (
	"strings"
	"testing"
)

// TestParseMarkdownBytesHeadingsNonDuplicated guards against the regression
// where extractInlineText wrote both the heading's raw Lines() and its inline
// children, producing "Stage 2: DispatchStage 2: Dispatch" style doubles.
func TestParseMarkdownBytesHeadingsNonDuplicated(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string // expected SectionHeading values for each section
	}{
		{
			name: "plain text heading",
			content: `# Doc Title

## Stage 2: Dispatch

Some content here.
`,
			want: []string{"Stage 2: Dispatch"},
		},
		{
			name: "inline code heading",
			content: `# Doc Title

## ` + "`search_docs`" + `

Tool description here.
`,
			want: []string{"search_docs"},
		},
		{
			name: "multi-level hierarchy",
			content: `# Doc Title

## Indexing Pipeline

### Stage 2: Dispatch

Dispatch content here with enough words to clear the minimum token threshold for chunking.
`,
			want: []string{"Indexing Pipeline", "Stage 2: Dispatch"},
		},
		{
			name: "heading with underscore word",
			content: `# Doc Title

## search_docs

Some content here.
`,
			want: []string{"search_docs"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pd, err := ParseMarkdownBytes("test.md", []byte(tc.content))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			// Check raw parsed headings (skip the doc title heading).
			var gotHeadings []string
			for _, h := range pd.Headings {
				if h == "Doc Title" {
					continue
				}
				gotHeadings = append(gotHeadings, h)
			}
			if len(gotHeadings) != len(tc.want) {
				t.Fatalf("heading count: got %d %v, want %d %v", len(gotHeadings), gotHeadings, len(tc.want), tc.want)
			}
			for i, h := range gotHeadings {
				if h != tc.want[i] {
					t.Errorf("heading[%d]: got %q, want %q", i, h, tc.want[i])
				}
				// Paranoia: the heading text must not contain itself as a suffix.
				if strings.HasSuffix(h, h+h) || strings.Count(h, h[:len(h)/2+1]) > 1 {
					t.Errorf("heading[%d] appears to be duplicated: %q", i, h)
				}
			}

			// Section headings must also be non-duplicated.
			for _, sec := range pd.Sections {
				doubled := sec.Heading + sec.Heading
				if strings.Contains(doubled[:len(sec.Heading)], sec.Heading) &&
					sec.Heading != "" && strings.HasPrefix(doubled, sec.Heading) {
					// Real check: sec.Heading must not end with itself repeated.
					half := len(sec.Heading) / 2
					if half > 0 && strings.HasSuffix(sec.Heading, sec.Heading[len(sec.Heading)-half:]) &&
						sec.Heading == sec.Heading[:half]+sec.Heading[half:] {
						// only fails if heading literally equals doubled content
					}
				}
				// Simpler: no heading should appear as a strict repetition.
				if len(sec.Heading) > 0 && sec.Heading == strings.Repeat(sec.Heading[:len(sec.Heading)/2], 2) && len(sec.Heading)%2 == 0 {
					t.Errorf("section heading is duplicated: %q", sec.Heading)
				}
			}
		})
	}
}

// TestParseMarkdownBytesHeadingExact verifies exact heading values for
// the specific patterns that were observed to double in the original bug.
func TestParseMarkdownBytesHeadingExact(t *testing.T) {
	content := `# Librarian

## ` + "`search_docs`" + `

Semantic search tool.

## Stage 2: Dispatch

Dispatch description with enough text to clear minimum token count.

## Point Librarian at it

Setup instructions with enough content here.
`
	pd, err := ParseMarkdownBytes("skill.md", []byte(content))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	wantHeadings := []string{"Librarian", "search_docs", "Stage 2: Dispatch", "Point Librarian at it"}
	if len(pd.Headings) != len(wantHeadings) {
		t.Fatalf("want %d headings, got %d: %v", len(wantHeadings), len(pd.Headings), pd.Headings)
	}
	for i, want := range wantHeadings {
		if pd.Headings[i] != want {
			t.Errorf("heading[%d]: got %q, want %q", i, pd.Headings[i], want)
		}
	}

	// Sections (skip doc-level title section if present).
	wantSections := []string{"search_docs", "Stage 2: Dispatch", "Point Librarian at it"}
	if len(pd.Sections) != len(wantSections) {
		t.Fatalf("want %d sections, got %d: %v", len(wantSections), len(pd.Sections), func() []string {
			var s []string
			for _, sec := range pd.Sections {
				s = append(s, sec.Heading)
			}
			return s
		}())
	}
	for i, want := range wantSections {
		got := pd.Sections[i].Heading
		if got != want {
			t.Errorf("section[%d]: got %q, want %q", i, got, want)
		}
	}
}
