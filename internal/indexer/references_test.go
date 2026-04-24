package indexer

import (
	"strings"
	"testing"
)

// Default code_file_patterns the CLI writes into config.yaml — mirror them in
// tests so ExtractCodeReferences gets a realistic set of allowed extensions.
var defaultCodePatterns = []string{
	"*.go", "*.ts", "*.tsx", "*.py", "*.rs", "*.java", "*.rb",
}

func pathSet(refs []CodeReference) map[string]bool {
	out := make(map[string]bool, len(refs))
	for _, r := range refs {
		out[r.FilePath] = true
	}
	return out
}

// Markdown bold/italic emphasis syntax used to leak into the graph as
// code_file nodes (e.g. `**Diagrams**`, `**Bookmarks/outline**`) because the
// glob regex accepts `*` in the character class. Regression guard so bold
// fragments stay out of the graph.
func TestExtractCodeReferences_RejectsMarkdownEmphasis(t *testing.T) {
	content := `
Here is a **bold term** with *italic text*. A **compound/path** is still bold.
Now a real file: internal/indexer/walker.go is useful.
The glob ` + "`*.go`" + ` matches Go files.
A nested glob: ` + "`**/*.ts`" + ` should pass.
A directory: internal/store/ should pass.
`
	refs := ExtractCodeReferences(content, defaultCodePatterns)
	got := pathSet(refs)

	for _, shouldReject := range []string{
		"**bold term**", "**bold", "bold**",
		"*italic", "italic*", "*italic text*",
		"**compound/path**",
	} {
		if got[shouldReject] {
			t.Errorf("markdown emphasis %q leaked into references; got %v", shouldReject, got)
		}
	}

	for _, shouldKeep := range []string{
		"internal/indexer/walker.go",
		"*.go",
		"**/*.ts",
		"internal/store/",
	} {
		if !got[shouldKeep] {
			t.Errorf("expected %q in references; got %v", shouldKeep, got)
		}
	}
}

// Single `*` and `**` with no path content used to become graph nodes via the
// glob branch. Reject them explicitly.
func TestExtractCodeReferences_RejectsBareAsterisks(t *testing.T) {
	content := "A paragraph with * and ** in it. And the literal *** too."
	refs := ExtractCodeReferences(content, defaultCodePatterns)
	for _, r := range refs {
		if strings.Trim(r.FilePath, "*") == "" {
			t.Errorf("bare asterisks leaked as reference: %q", r.FilePath)
		}
	}
}

// Legit globs with extensions or path separators must survive the emphasis
// filter. Pins the positive case so the filter can't accidentally get too
// aggressive.
func TestExtractCodeReferences_KeepsRealGlobs(t *testing.T) {
	content := "See ``*.go`` for the Go files and ``internal/**/*.py`` for Python."
	refs := ExtractCodeReferences(content, defaultCodePatterns)
	got := pathSet(refs)
	for _, want := range []string{"*.go", "internal/**/*.py"} {
		if !got[want] {
			t.Errorf("legit glob %q missing; got %v", want, got)
		}
	}
}
