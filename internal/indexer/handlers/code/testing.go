package code

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
)

// AssertGrammarInvariants runs structural assertions that every Grammar
// implementation must satisfy regardless of language. Concrete grammar tests
// call it with a representative source sample and then layer language-
// specific assertions on top.
//
// Invariants checked:
//   - Parse succeeds and returns a non-nil doc.
//   - doc.Format equals h.Name(); doc.DocType is "code".
//   - Every Unit has non-empty Kind, Title, Content, and Path.
//   - Unit.Path never starts with '.' and never contains "..".
//   - When doc.Title is non-empty, Unit.Path either equals Unit.Title
//     (unqualified symbol) or is prefixed with doc.Title + "." (package
//     qualifier) — or is prefixed with a container name like "Title.Container".
//   - Every Reference with Kind="import" has non-empty Target.
//   - Chunk returns without error and produces chunks whose SectionHeading
//     is non-empty.
//   - Grammar.CommentNodeTypes returns at least one entry (otherwise the
//     comment-handling machinery is silently unreachable).
func AssertGrammarInvariants(t *testing.T, h *CodeHandler, path string, src []byte) {
	t.Helper()

	if got := h.grammar.CommentNodeTypes(); len(got) == 0 {
		t.Error("Grammar.CommentNodeTypes returned an empty slice; comment machinery will never fire")
	}

	doc, err := h.Parse(path, src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc == nil {
		t.Fatal("Parse returned nil doc")
	}
	if doc.Format != h.Name() {
		t.Errorf("Format = %q, want %q", doc.Format, h.Name())
	}
	if doc.DocType != "code" {
		t.Errorf("DocType = %q, want %q", doc.DocType, "code")
	}

	title := doc.Title
	for _, u := range doc.Units {
		if u.Kind == "" {
			t.Errorf("Unit %q has empty Kind", u.Title)
		}
		if u.Title == "" {
			t.Errorf("Unit at path %q has empty Title", u.Path)
		}
		if u.Content == "" {
			t.Errorf("Unit %q has empty Content", u.Title)
		}
		if u.Path == "" {
			t.Errorf("Unit %q has empty Path", u.Title)
			continue
		}
		if strings.HasPrefix(u.Path, ".") {
			t.Errorf("Unit %q Path %q starts with '.'", u.Title, u.Path)
		}
		if strings.Contains(u.Path, "..") {
			t.Errorf("Unit %q Path %q contains consecutive dots", u.Title, u.Path)
		}
		if title != "" && u.Path != u.Title && !strings.HasPrefix(u.Path, title+".") {
			t.Errorf("Unit %q Path %q neither equals Title nor is prefixed with %q+'.'", u.Title, u.Path, title)
		}
	}

	for _, r := range doc.Refs {
		if r.Kind == "import" && r.Target == "" {
			t.Errorf("import Reference has empty Target: %+v", r)
		}
	}

	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 1, OverlapLines: 0})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	for i, c := range chunks {
		if c.SectionHeading == "" {
			t.Errorf("chunk %d has empty SectionHeading", i)
		}
	}
}
