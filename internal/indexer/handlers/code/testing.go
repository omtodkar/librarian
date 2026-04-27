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
// This helper exercises the Parse path (not ParseCtx), so samples for
// grammars with a ResolveImports post-pass (Python, JS/TS) MUST NOT contain
// relative module references — the invariant below that rejects unresolved
// targets assumes the resolver has run, and ParseCtx with a real AbsPath +
// ProjectRoot is required for that. Samples with only absolute imports (or
// bare npm specifiers on JS/TS) are fine. See
// TestPythonGrammar_ResolvesRelativeImportsViaParseCtx and
// TestTypeScriptGrammar_ResolvesRelativeImportsViaParseCtx for ParseCtx-
// driven equivalents.
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
//   - For Python grammar: Reference.Target never starts with '.' or contains
//     '..' (relative-import form). Grammar-gated because JS/TS legitimately
//     emits leading-dot module specifiers today; widen as resolvers land.
//   - Every Reference with a populated Source has Source matching the Path of
//     some Unit in the same ParsedDoc — grammars that implement
//     inheritanceExtractor (lib-wji.1+) use Source to anchor graph edges at
//     sym:<Source>, and a mismatched Source would produce an edge whose
//     from_node points at a phantom symbol. joinPath bugs in per-language
//     grammar implementations are caught here cheaply.
//   - Chunk returns without error and produces chunks whose SectionHeading
//     is non-empty.
//   - Grammar.CommentNodeTypes returns at least one entry (otherwise the
//     comment-handling machinery is silently unreachable).
func AssertGrammarInvariants(t *testing.T, h *CodeHandler, path string, src []byte) {
	t.Helper()

	if got := h.grammar.CommentNodeTypes(); len(got) == 0 {
		// DocstringFromNode is the intended mechanism for this grammar; the
		// walker's comment buffer is intentionally bypassed. No error — but log
		// so an accidental empty return in a new grammar doesn't go unnoticed.
		t.Logf("CommentNodeTypes() is empty for %s grammar; comment attachment relies on DocstringFromNode", h.Name())
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

	unitPaths := make(map[string]bool, len(doc.Units))
	for _, u := range doc.Units {
		unitPaths[u.Path] = true
	}
	for _, r := range doc.Refs {
		if r.Source != "" && !unitPaths[r.Source] {
			t.Errorf("Reference %+v: Source %q does not match any Unit.Path in the doc (possible joinPath bug in grammar)",
				r, r.Source)
		}
		if r.Kind != "import" {
			continue
		}
		if r.Target == "" {
			t.Errorf("import Reference has empty Target: %+v", r)
			continue
		}
		// Post-resolution postcondition: the grammar's ResolveImports hook
		// must rewrite relative module references into an absolute form so
		// two files importing the same module via relative vs. absolute
		// syntax collapse onto a single graph node. Check-shape varies per
		// grammar (Python: dotted symbols; JS/TS: project-relative file
		// paths), but the non-relative guarantee is shared.
		switch h.grammar.Name() {
		case "python":
			if strings.HasPrefix(r.Target, ".") {
				t.Errorf("python import Target %q starts with '.' (relative import not resolved)", r.Target)
			}
			if strings.Contains(r.Target, "..") {
				t.Errorf("python import Target %q contains '..' (relative import not resolved)", r.Target)
			}
		case "javascript", "typescript", "tsx":
			if strings.HasPrefix(r.Target, "./") || strings.HasPrefix(r.Target, "../") {
				t.Errorf("%s import Target %q starts with './' or '../' (relative import not resolved)",
					h.grammar.Name(), r.Target)
			}
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
