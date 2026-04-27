package faq

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/config"
)

// -- isQuestionShaped --

func TestIsQuestionShaped(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"How does the embedding cache invalidate?", true},
		{"What is the difference between search and context?", true},
		{"Why is the auth middleware being rewritten?", true},
		{"Where is the config file loaded from?", true},
		{"When does the BM25 index get updated?", true},
		{"how to add a new handler", true},     // lower-case match
		{"Add a new feature", false},            // not question-shaped
		{"fix: panic at store.Open", false},     // imperative commit
		{"feat: hybrid search via FTS5", false}, // feature commit
		{"Is this a question?", true},           // contains '?'
		{"Heading contains ? somewhere", true},  // '?' anywhere
		{"", false},
	}
	for _, tc := range cases {
		got := isQuestionShaped(tc.text)
		if got != tc.want {
			t.Errorf("isQuestionShaped(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// -- parseGitLog --

func TestParseGitLog_QuestionSubject(t *testing.T) {
	input := "abc123defg\x1fHow does embedding cache work?\x1fSome body text.\x1e"
	srcs, err := parseGitLog([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("want 1 source, got %d", len(srcs))
	}
	s := srcs[0]
	if s.Kind != "git" {
		t.Errorf("Kind = %q, want \"git\"", s.Kind)
	}
	if s.ID != "abc123de" {
		t.Errorf("ID = %q, want \"abc123de\"", s.ID)
	}
	if s.Text != "How does embedding cache work?" {
		t.Errorf("Text = %q", s.Text)
	}
	if s.Detail != "Some body text." {
		t.Errorf("Detail = %q", s.Detail)
	}
}

func TestParseGitLog_QuestionHeadingInBody(t *testing.T) {
	// subject is not question-shaped, but body has a markdown heading with '?'
	input := "deadbeef1234\x1ffeat: add new indexer pass\x1f# Why is this needed?\nBecause the old one was slow.\x1e"
	srcs, err := parseGitLog([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("want 1 source, got %d", len(srcs))
	}
	if srcs[0].Text != "Why is this needed?" {
		t.Errorf("Text = %q, want \"Why is this needed?\"", srcs[0].Text)
	}
}

func TestParseGitLog_NonQuestion(t *testing.T) {
	input := "aaaa1111\x1ffix: remove unused import\x1f\x1e"
	srcs, err := parseGitLog([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 0 {
		t.Errorf("want 0 sources, got %d", len(srcs))
	}
}

func TestParseGitLog_MultipleCommits(t *testing.T) {
	input := strings.Join([]string{
		"aaa00001\x1fHow does search work?\x1fbody1",
		"bbb00002\x1ffeat: add feature\x1fbody2",
		"ccc00003\x1fWhat is the chunking strategy?\x1fbody3",
	}, "\x1e") + "\x1e"
	srcs, err := parseGitLog([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 2 {
		t.Fatalf("want 2 sources, got %d: %+v", len(srcs), srcs)
	}
	if srcs[0].Text != "How does search work?" {
		t.Errorf("srcs[0].Text = %q", srcs[0].Text)
	}
	if srcs[1].Text != "What is the chunking strategy?" {
		t.Errorf("srcs[1].Text = %q", srcs[1].Text)
	}
}

func TestParseGitLog_EmptyInput(t *testing.T) {
	srcs, err := parseGitLog([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 0 {
		t.Errorf("want 0 sources, got %d", len(srcs))
	}
}

// -- parseBDIssues --

func TestParseBDIssues_QuestionTitle(t *testing.T) {
	input := `[
		{
			"id": "lib-abc",
			"title": "How does the embedding cache invalidate?",
			"description": "Long description here",
			"close_reason": "Fixed by clearing on model change"
		},
		{
			"id": "lib-def",
			"title": "Add new handler for TOML files",
			"description": "Just a feature request",
			"close_reason": ""
		}
	]`
	srcs, err := parseBDIssues([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("want 1 source, got %d", len(srcs))
	}
	s := srcs[0]
	if s.ID != "lib-abc" {
		t.Errorf("ID = %q, want \"lib-abc\"", s.ID)
	}
	if s.Kind != "issue" {
		t.Errorf("Kind = %q, want \"issue\"", s.Kind)
	}
	if s.Detail != "Fixed by clearing on model change" {
		t.Errorf("Detail = %q", s.Detail)
	}
}

func TestParseBDIssues_FallsBackToDescription(t *testing.T) {
	input := `[
		{
			"id": "lib-xyz",
			"title": "Why is the store reopened on each command?",
			"description": "Because of single-writer mode.",
			"close_reason": ""
		}
	]`
	srcs, err := parseBDIssues([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(srcs) != 1 {
		t.Fatalf("want 1 source, got %d: %+v", len(srcs), srcs)
	}
	if srcs[0].Detail != "Because of single-writer mode." {
		t.Errorf("Detail = %q", srcs[0].Detail)
	}
}

func TestParseBDIssues_InvalidJSON(t *testing.T) {
	_, err := parseBDIssues([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// -- cosineSimilarity --

func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{1, 0}, 1.0},
		{[]float64{1, 0}, []float64{0, 1}, 0.0},
		{[]float64{1, 1}, []float64{1, 1}, 1.0},
		{[]float64{1, 0, 0}, []float64{0, 0, 0}, 0.0}, // zero vector
		{[]float64{}, []float64{}, 0.0},                // empty
		{[]float64{1}, []float64{1, 2}, 0.0},           // mismatched length
		{[]float64{3, 4}, []float64{3, 4}, 1.0},
		{[]float64{1, 0, 0}, []float64{0, 1, 0}, 0.0},
	}
	for _, tc := range cases {
		got := cosineSimilarity(tc.a, tc.b)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("cosineSimilarity(%v, %v) = %f, want %f", tc.a, tc.b, got, tc.want)
		}
	}
}

// -- Cluster --

// fakeEmbedder returns pre-set vectors for each text.
type fakeEmbedder struct {
	vecs    map[string][]float64
	batchErr error
}

func (f *fakeEmbedder) Embed(text string) ([]float64, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	if v, ok := f.vecs[text]; ok {
		return v, nil
	}
	return []float64{1, 0}, nil
}

func (f *fakeEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if v, ok := f.vecs[t]; ok {
			out[i] = v
		} else {
			out[i] = []float64{1, 0}
		}
	}
	return out, nil
}

func (f *fakeEmbedder) Model() string { return "fake" }

func TestCluster_GroupsNearDuplicates(t *testing.T) {
	sources := []Source{
		{Kind: "git", ID: "aaa", Text: "How does search work?"},
		{Kind: "git", ID: "bbb", Text: "What is BM25?"},
		{Kind: "issue", ID: "ccc", Text: "How do searches work?"},
	}
	embedder := &fakeEmbedder{vecs: map[string][]float64{
		"How does search work?": {1, 0},
		"What is BM25?":         {0, 1},
		"How do searches work?": {1, 0}, // near-identical to first
	}}
	clusters, err := Cluster(sources, embedder, 0.85)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("want 2 clusters, got %d: %+v", len(clusters), clusters)
	}
	if len(clusters[0]) != 2 {
		t.Errorf("cluster[0] length = %d, want 2", len(clusters[0]))
	}
	if len(clusters[1]) != 1 {
		t.Errorf("cluster[1] length = %d, want 1", len(clusters[1]))
	}
	if clusters[1][0].ID != "bbb" {
		t.Errorf("cluster[1][0].ID = %q, want \"bbb\"", clusters[1][0].ID)
	}
}

func TestCluster_EmptySources(t *testing.T) {
	clusters, err := Cluster(nil, &fakeEmbedder{}, 0.85)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 0 {
		t.Errorf("want 0 clusters, got %d", len(clusters))
	}
}

func TestCluster_NoNearDuplicates(t *testing.T) {
	sources := []Source{
		{Kind: "git", ID: "a", Text: "How does X work?"},
		{Kind: "git", ID: "b", Text: "Why does Y fail?"},
	}
	embedder := &fakeEmbedder{vecs: map[string][]float64{
		"How does X work?": {1, 0},
		"Why does Y fail?": {0, 1},
	}}
	clusters, err := Cluster(sources, embedder, 0.85)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(clusters) != 2 {
		t.Errorf("want 2 clusters (each of size 1), got %d", len(clusters))
	}
}

func TestCluster_EmbedBatchError(t *testing.T) {
	sources := []Source{{Kind: "git", ID: "a", Text: "How does X work?"}}
	embedder := &fakeEmbedder{batchErr: errors.New("api down")}
	_, err := Cluster(sources, embedder, 0.85)
	if err == nil {
		t.Error("expected error from EmbedBatch, got nil")
	}
	if !strings.Contains(err.Error(), "embedding sources") {
		t.Errorf("error message = %q, want to contain \"embedding sources\"", err.Error())
	}
}

// -- slugify --

func TestSlugify(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"How does search work?", "how-does-search-work"},
		{"What IS BM25?", "what-is-bm25"},
		{"  spaces  ", "spaces"},
		{"hello---world", "hello-world"},
	}
	for _, tc := range cases {
		got := slugify(tc.in)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugify_LongString(t *testing.T) {
	long := strings.Repeat("a", 80)
	got := slugify(long)
	if len(got) > 60 {
		t.Errorf("slugify of 80 chars = %d chars, want ≤60", len(got))
	}
}

// -- chooseAnswer --

func TestChooseAnswer_Truncation(t *testing.T) {
	// Build a detail > 300 chars with a '.' near char 250.
	prefix := strings.Repeat("x", 240) // 240 chars
	mid := "ends here."                 // sentence boundary at char 249
	suffix := strings.Repeat("y", 100) // more content pushed past 300
	detail := prefix + mid + suffix

	cluster := []Source{{Kind: "git", ID: "a", Text: "How?", Detail: detail}}
	got := chooseAnswer(cluster)
	if strings.Contains(got, suffix[:10]) {
		t.Errorf("chooseAnswer should truncate before suffix; got: %q", got)
	}
	if !strings.HasSuffix(got, ".") {
		t.Errorf("chooseAnswer should end at sentence boundary '.'; got: %q", got)
	}
}

func TestChooseAnswer_NoDetail(t *testing.T) {
	cluster := []Source{
		{Kind: "git", ID: "a", Text: "How?", Detail: ""},
		{Kind: "issue", ID: "b", Text: "How?", Detail: ""},
	}
	got := chooseAnswer(cluster)
	if got != "" {
		t.Errorf("chooseAnswer with no detail should return \"\", got %q", got)
	}
}

// -- EntryFromCluster / FAQEntry.Markdown --

func TestEntryFromCluster(t *testing.T) {
	cluster := []Source{
		{Kind: "git", ID: "abc12345", Text: "How does chunking work?", Detail: "Chunks are split by section."},
		{Kind: "issue", ID: "lib-xyz", Text: "How are chunks split?", Detail: ""},
	}
	entry := EntryFromCluster(cluster)
	if entry.Question != "How does chunking work?" {
		t.Errorf("Question = %q", entry.Question)
	}
	if entry.Answer != "Chunks are split by section." {
		t.Errorf("Answer = %q", entry.Answer)
	}
	if entry.SourceID != "abc12345" {
		t.Errorf("SourceID = %q", entry.SourceID)
	}
	if entry.SourceKind != "git" {
		t.Errorf("SourceKind = %q", entry.SourceKind)
	}

	md := entry.Markdown()
	if !strings.Contains(md, "# How does chunking work?") {
		t.Errorf("markdown missing heading:\n%s", md)
	}
	if !strings.Contains(md, "Chunks are split by section.") {
		t.Errorf("markdown missing answer:\n%s", md)
	}
	if !strings.Contains(md, "git commit `abc12345`") {
		t.Errorf("markdown missing source:\n%s", md)
	}
}

func TestEntryFromCluster_Empty(t *testing.T) {
	entry := EntryFromCluster(nil)
	if entry.Question != "" {
		t.Errorf("expected empty entry from nil cluster")
	}
}

func TestEntryFromCluster_NoDetail_EmptyAnswer(t *testing.T) {
	cluster := []Source{
		{Kind: "git", ID: "aaa", Text: "How does it work?", Detail: ""},
	}
	entry := EntryFromCluster(cluster)
	if entry.Answer != "" {
		t.Errorf("entry from cluster with no detail should have empty Answer, got %q", entry.Answer)
	}
}

// -- WriteEntries --

func TestWriteEntries_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	entries := []FAQEntry{
		{Question: "How does search work?", Answer: "Via vector similarity.", SourceID: "abc", SourceKind: "git"},
		{Question: "What is BM25?", Answer: "A ranking function.", SourceID: "lib-x", SourceKind: "issue"},
	}
	paths, err := WriteEntries(entries, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 paths, got %d", len(paths))
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("file %s not found: %v", p, err)
		}
	}
}

func TestWriteEntries_CreatesDirectory(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "faqs")
	entries := []FAQEntry{
		{Question: "Why is it slow?", Answer: "Because of N+1 queries.", SourceID: "a", SourceKind: "git"},
	}
	_, err := WriteEntries(entries, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("directory %s not created: %v", dir, err)
	}
}

func TestWriteEntries_SlugDedup(t *testing.T) {
	dir := t.TempDir()
	// Three entries that all produce the same base slug.
	entries := []FAQEntry{
		{Question: "How does it work?", Answer: "Answer one.", SourceID: "a", SourceKind: "git"},
		{Question: "How does it work?", Answer: "Answer two.", SourceID: "b", SourceKind: "git"},
		{Question: "How does it work?", Answer: "Answer three.", SourceID: "c", SourceKind: "git"},
	}
	paths, err := WriteEntries(entries, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("want 3 paths, got %d", len(paths))
	}
	names := map[string]bool{}
	for _, p := range paths {
		names[filepath.Base(p)] = true
	}
	for _, want := range []string{"how-does-it-work.md", "how-does-it-work-2.md", "how-does-it-work-3.md"} {
		if !names[want] {
			t.Errorf("expected file %q not found in %v", want, names)
		}
	}
}

func TestWriteEntries_SkipsEmptyAnswer(t *testing.T) {
	dir := t.TempDir()
	entries := []FAQEntry{
		{Question: "How does it work?", Answer: "", SourceID: "a", SourceKind: "git"},
		{Question: "Why is it fast?", Answer: "Because of caching.", SourceID: "b", SourceKind: "git"},
	}
	paths, err := WriteEntries(entries, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 path (empty-answer entry skipped), got %d", len(paths))
	}
}

// -- Run (pipeline) --

func TestRun_NoSources_EarlyReturn(t *testing.T) {
	// Both scanners return empty; Run should return a zero Result without
	// trying to create an embedder (which would fail without API keys).
	rc := RunConfig{
		GitCommits: 10,
		Threshold:  0.85,
		FAQDir:     t.TempDir(),
		Cfg:        &config.Config{DocsDir: "docs"},
		ScanGit:    func(n int) ([]Source, error) { return nil, nil },
		ScanIssues: func() ([]Source, error) { return nil, nil },
	}
	result, err := Run(rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Clusters != 0 {
		t.Errorf("Clusters = %d, want 0", result.Clusters)
	}
	if len(result.FilesWritten) != 0 {
		t.Errorf("FilesWritten = %v, want empty", result.FilesWritten)
	}
}

func TestRun_GitCommitsZeroDefaultsTo100(t *testing.T) {
	var capturedN int
	rc := RunConfig{
		GitCommits: 0, // should default to 100
		Threshold:  0.85,
		FAQDir:     t.TempDir(),
		Cfg:        &config.Config{DocsDir: "docs"},
		ScanGit: func(n int) ([]Source, error) {
			capturedN = n
			return nil, nil
		},
		ScanIssues: func() ([]Source, error) { return nil, nil },
	}
	if _, err := Run(rc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedN != 100 {
		t.Errorf("ScanGit called with n=%d, want 100", capturedN)
	}
}
