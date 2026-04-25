package indexer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register handlers
	"librarian/internal/store"
)

// projectFixture writes a small Go + Python + Markdown project rooted at
// dir. Returns paths the tests use to assert against.
func projectFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"cmd/main.go": `package main

func Run() string { return "hi" }

func main() { _ = Run() }
`,
		"internal/auth/service.go": `package auth

type Service struct{ db string }

func (s *Service) Login(user string) bool { return true }

func (s *Service) Logout() {}
`,
		"scripts/build.py": `def build():
    return "built"
`,
		"docs/guide.md": `# Guide

See internal/auth/service.go for details.
`,
	}
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
}

// newGraphTestIndexer builds an Indexer rooted at projectDir with the graph
// pass enabled (docs default honor_gitignore false to keep fixtures simple).
func newGraphTestIndexer(t *testing.T, projectDir string) (*indexer.Indexer, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(projectDir, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		DocsDir:     filepath.Join(projectDir, "docs"),
		DBPath:      dbPath,
		ProjectRoot: projectDir,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1,
		},
		Graph: config.GraphConfig{
			HonorGitignore:  false, // tests set this explicitly when needed
			DetectGenerated: true,  // match config.Load()'s default
		},
	}
	return indexer.New(s, cfg, fakeEmbedder{dim: 4}), s
}

// countNodesByKind returns how many graph_nodes exist for each kind.
func countNodesByKind(t *testing.T, s *store.Store) map[string]int {
	t.Helper()
	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	out := map[string]int{}
	for _, n := range nodes {
		out[n.Kind]++
	}
	return out
}

// TestIntegration_Graph_DocsPassAloneProducesNoSymbols pins the invariant
// from the lib-6j7 issue: when the docs pass runs without the graph pass,
// graph_nodes{kind='symbol'} stays empty. Regression guard for "docs_dir
// leaks into symbol projection".
func TestIntegration_Graph_DocsPassAloneProducesNoSymbols(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexDirectory(filepath.Join(root, "docs"), true); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	counts := countNodesByKind(t, s)
	if counts["symbol"] != 0 {
		t.Errorf("docs-only pass produced %d symbol nodes; want 0", counts["symbol"])
	}
}

// TestIntegration_Graph_GraphPassProducesSymbols pins that IndexProjectGraph
// actually projects code Units into symbol nodes with contains edges.
func TestIntegration_Graph_GraphPassProducesSymbols(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)

	idx, s := newGraphTestIndexer(t, root)
	res, err := idx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}
	if res.FilesScanned < 3 {
		t.Errorf("FilesScanned = %d; want >= 3 (main.go, service.go, build.py)", res.FilesScanned)
	}
	if res.SymbolsAdded < 3 {
		t.Errorf("SymbolsAdded = %d; want >= 3 (Run, main, Service, Login, Logout, build)", res.SymbolsAdded)
	}

	counts := countNodesByKind(t, s)
	if counts["symbol"] < 3 {
		t.Errorf("symbol nodes = %d; want >= 3", counts["symbol"])
	}
	if counts["code_file"] < 3 {
		t.Errorf("code_file nodes = %d; want >= 3", counts["code_file"])
	}
}

// TestIntegration_Graph_SkipsMarkdownInGraphPass pins that markdown files
// under the project root are NOT visited by the graph pass, even though a
// markdown handler is registered. Prevents double-indexing when docs_dir
// sits under the project root (the common case).
func TestIntegration_Graph_SkipsMarkdownInGraphPass(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// docs/guide.md must not produce a code_file node via the graph pass.
	// (It may appear later via the docs pass's own `mentions` pass; here we
	// only ran the graph pass, so any code_file rooted at a .md path is a
	// bug.)
	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	for _, n := range nodes {
		if n.Kind == "code_file" && filepath.Ext(n.SourcePath) == ".md" {
			t.Errorf("graph pass produced a code_file node for markdown: %s", n.SourcePath)
		}
	}
}

// TestIntegration_Graph_HardExcludeLibrarian pins that .librarian/ is never
// walked by the graph pass regardless of config.
func TestIntegration_Graph_HardExcludeLibrarian(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)
	// Plant a .go file inside .librarian/ that, if indexed, would show up
	// as a symbol — this path must be hard-excluded.
	librarianDir := filepath.Join(root, ".librarian")
	if err := os.MkdirAll(librarianDir, 0o755); err != nil {
		t.Fatal(err)
	}
	trap := filepath.Join(librarianDir, "leak.go")
	if err := os.WriteFile(trap, []byte("package leak\nfunc Leak() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	nodes, _ := s.ListNodes()
	for _, n := range nodes {
		if n.SourcePath == ".librarian/leak.go" {
			t.Errorf(".librarian/ contents should never be graph-indexed; found %+v", n)
		}
	}
}

// TestIntegration_Graph_Roots pins that cfg.Graph.Roots restricts the walk
// to declared subdirectories.
func TestIntegration_Graph_Roots(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)

	idx, s := newGraphTestIndexer(t, root)
	// reach inside the Indexer via a fresh one with Graph.Roots set
	cfg := &config.Config{
		DocsDir:     filepath.Join(root, "docs"),
		ProjectRoot: root,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph: config.GraphConfig{
			HonorGitignore: false,
			Roots:          []string{"internal/auth"},
		},
	}
	_ = idx
	scopedIdx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	res, err := scopedIdx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Only service.go under internal/auth should have been walked.
	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d; want 1 (internal/auth/service.go only)", res.FilesScanned)
	}

	// cmd/main.go and scripts/build.py are outside Roots; no code_file
	// nodes for them.
	nodes, _ := s.ListNodes()
	for _, n := range nodes {
		if n.Kind == "code_file" {
			if n.SourcePath == "cmd/main.go" || n.SourcePath == "scripts/build.py" {
				t.Errorf("graph.roots did not restrict walk: found %s", n.SourcePath)
			}
		}
	}
}

// TestIntegration_Graph_SkipsGeneratedFiles pins that a file bearing the
// Go toolchain's generated-file banner is skipped entirely by the graph
// pass — no symbols projected, no code_file row added — and the skip is
// counted on FilesSkippedGenerated.
func TestIntegration_Graph_SkipsGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	// Hand-written file: real work, should be indexed.
	if err := os.WriteFile(filepath.Join(root, "handwritten.go"), []byte(
		"package x\n\nfunc Real() {}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	// Generated file: banner present.
	if err := os.WriteFile(filepath.Join(root, "auto.pb.go"), []byte(
		"// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage x\n\nfunc Generated() {}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, s := newGraphTestIndexer(t, root)
	res, err := idx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	if res.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d; want 1 (only handwritten.go)", res.FilesScanned)
	}
	if res.FilesSkippedGenerated != 1 {
		t.Errorf("FilesSkippedGenerated = %d; want 1", res.FilesSkippedGenerated)
	}

	// auto.pb.go must not have produced a code_file or any symbols.
	nodes, _ := s.ListNodes()
	for _, n := range nodes {
		if n.SourcePath == "auto.pb.go" {
			t.Errorf("generated file leaked into graph: %+v", n)
		}
	}
}

// TestIntegration_Graph_CleansUpGeneratedFileThatWasPreviouslyIndexed pins
// the lifecycle contract: a file that was once hand-written (and got
// symbols projected) then acquired a generator banner gets its stale
// symbols and code_files row cleaned up.
func TestIntegration_Graph_CleansUpGeneratedFileThatWasPreviouslyIndexed(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "oauth.go")

	// Step 1: hand-written file gets indexed.
	if err := os.WriteFile(f, []byte("package auth\n\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("first IndexProjectGraph: %v", err)
	}
	nodes, _ := s.ListNodes()
	var beforeSym int
	for _, n := range nodes {
		if n.Kind == "symbol" && n.SourcePath == "oauth.go" {
			beforeSym++
		}
	}
	if beforeSym == 0 {
		t.Fatal("fixture bug: no symbols projected for hand-written oauth.go")
	}

	// Step 2: same file gains a generator banner.
	if err := os.WriteFile(f, []byte(
		"// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage auth\n\nfunc Login() {}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := idx.IndexProjectGraph(root, false)
	if err != nil {
		t.Fatalf("second IndexProjectGraph: %v", err)
	}
	if res.FilesSkippedGenerated != 1 {
		t.Errorf("FilesSkippedGenerated = %d; want 1", res.FilesSkippedGenerated)
	}

	// Cleanup: no symbols, no code_files row, no code_file graph_node.
	nodes, _ = s.ListNodes()
	for _, n := range nodes {
		if n.SourcePath == "oauth.go" {
			t.Errorf("stale node for now-generated file: %+v", n)
		}
	}
	if cf, _ := s.GetCodeFileByPath("oauth.go"); cf != nil {
		t.Errorf("code_files row should be cleaned up; got %+v", cf)
	}
}

// TestIntegration_Graph_AdaptiveMatchesSerial pins the adaptive auto-mode
// path: running with MaxWorkers=0 (auto, triggers runAdaptiveGraphPass on
// a >20-file fixture) must produce the same counters and node shape as
// MaxWorkers=1. The actual worker count chosen depends on host CPU timing
// but correctness is invariant.
func TestIntegration_Graph_AdaptiveMatchesSerial(t *testing.T) {
	root := t.TempDir()
	parallelMonorepoFixture(t, root)

	serialIdx, serialStore := newGraphTestIndexerWithWorkers(t, root, 1)
	if _, err := serialIdx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("serial IndexProjectGraph: %v", err)
	}
	serialCounts := countNodesByKind(t, serialStore)

	adaptiveIdx, adaptiveStore := newGraphTestIndexerWithWorkers(t, root, 0) // 0 = auto
	if _, err := adaptiveIdx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("adaptive IndexProjectGraph: %v", err)
	}
	adaptiveCounts := countNodesByKind(t, adaptiveStore)

	for kind := range serialCounts {
		if serialCounts[kind] != adaptiveCounts[kind] {
			t.Errorf("node kind %q: serial=%d adaptive=%d",
				kind, serialCounts[kind], adaptiveCounts[kind])
		}
	}
	for kind := range adaptiveCounts {
		if _, ok := serialCounts[kind]; !ok {
			t.Errorf("adaptive produced node kind %q not in serial (count=%d)",
				kind, adaptiveCounts[kind])
		}
	}
}

// TestIntegration_Graph_ParallelMatchesSerial pins the thread-safety
// contract of the worker-pool path: running with MaxWorkers=4 against a
// mixed-language fixture must produce the same symbol + edge + file counts
// as running with MaxWorkers=1 (idempotent UpsertNode + UpsertEdge +
// per-worker-local GraphResult + final merge).
//
// The fixture spreads ~30 files across Go / Python / Java so the graph
// pass actually has tree-sitter work to parallelize — a fixture with only
// a handful of files would hit the serial fallback inside pickGraphWorkers.
func TestIntegration_Graph_ParallelMatchesSerial(t *testing.T) {
	root := t.TempDir()
	parallelMonorepoFixture(t, root)

	// Serial baseline.
	serialIdx, serialStore := newGraphTestIndexerWithWorkers(t, root, 1)
	serialRes, err := serialIdx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("serial IndexProjectGraph: %v", err)
	}
	serialCounts := countNodesByKind(t, serialStore)

	// Parallel run on a fresh store (different tempdir under test.db path
	// in newGraphTestIndexerWithWorkers so the two don't collide).
	parallelIdx, parallelStore := newGraphTestIndexerWithWorkers(t, root, 4)
	parallelRes, err := parallelIdx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("parallel IndexProjectGraph: %v", err)
	}
	parallelCounts := countNodesByKind(t, parallelStore)

	// Summary counters must match exactly.
	if serialRes.FilesScanned != parallelRes.FilesScanned {
		t.Errorf("FilesScanned: serial=%d parallel=%d", serialRes.FilesScanned, parallelRes.FilesScanned)
	}
	if serialRes.SymbolsAdded != parallelRes.SymbolsAdded {
		t.Errorf("SymbolsAdded: serial=%d parallel=%d", serialRes.SymbolsAdded, parallelRes.SymbolsAdded)
	}
	if serialRes.EdgesAdded != parallelRes.EdgesAdded {
		t.Errorf("EdgesAdded: serial=%d parallel=%d", serialRes.EdgesAdded, parallelRes.EdgesAdded)
	}

	// Persisted node counts per kind must match exactly.
	for kind := range serialCounts {
		if serialCounts[kind] != parallelCounts[kind] {
			t.Errorf("node kind %q: serial=%d parallel=%d",
				kind, serialCounts[kind], parallelCounts[kind])
		}
	}
	for kind := range parallelCounts {
		if _, ok := serialCounts[kind]; !ok {
			t.Errorf("parallel produced node kind %q not seen in serial (count=%d)",
				kind, parallelCounts[kind])
		}
	}
}

// parallelMonorepoFixture writes ~30 files across three grammars so the
// graph pass has enough material to exercise the worker pool (the
// parallelism threshold is 20).
func parallelMonorepoFixture(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{}
	for i := 0; i < 12; i++ {
		files[fmt.Sprintf("cmd/tool%d/main.go", i)] =
			fmt.Sprintf("package main\nfunc Run%d() string { return \"x\" }\nfunc main() {}\n", i)
	}
	for i := 0; i < 10; i++ {
		files[fmt.Sprintf("services/svc%d/handler.py", i)] =
			fmt.Sprintf("class Handler%d:\n    def run(self):\n        return %d\n", i, i)
	}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("libs/core/Thing%d.java", i)] =
			fmt.Sprintf("package core;\npublic class Thing%d {\n  public void go() {}\n}\n", i)
	}
	for rel, content := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
}

// newGraphTestIndexerWithWorkers mirrors newGraphTestIndexer but parameterises
// MaxWorkers so the parallel integration test can compare runs side-by-side.
func newGraphTestIndexerWithWorkers(t *testing.T, projectDir string, workers int) (*indexer.Indexer, *store.Store) {
	t.Helper()
	// Isolate each run in its own subdir so the two stores don't collide.
	dbDir := filepath.Join(t.TempDir(), fmt.Sprintf("workers-%d", workers))
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		DocsDir:     filepath.Join(projectDir, "docs"),
		DBPath:      dbPath,
		ProjectRoot: projectDir,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph: config.GraphConfig{
			HonorGitignore:  false,
			DetectGenerated: true,
			MaxWorkers:      workers,
		},
	}
	return indexer.New(s, cfg, fakeEmbedder{dim: 4}), s
}

// TestIntegration_Graph_IncrementalHashGate pins the content-hash gate:
// re-running without --force on an unchanged file counts as skipped.
func TestIntegration_Graph_IncrementalHashGate(t *testing.T) {
	root := t.TempDir()
	projectFixture(t, root)

	idx, _ := newGraphTestIndexer(t, root)

	first, err := idx.IndexProjectGraph(root, true)
	if err != nil {
		t.Fatalf("first IndexProjectGraph: %v", err)
	}
	if first.FilesScanned == 0 {
		t.Fatal("first run scanned zero files; fixture issue")
	}

	second, err := idx.IndexProjectGraph(root, false)
	if err != nil {
		t.Fatalf("second IndexProjectGraph: %v", err)
	}
	if second.FilesSkipped == 0 {
		t.Errorf("second run should have skipped unchanged files; FilesSkipped=%d", second.FilesSkipped)
	}
	if second.FilesScanned != 0 {
		t.Errorf("second run FilesScanned=%d; want 0 (all unchanged)", second.FilesScanned)
	}
}

// TestIntegration_Graph_PythonRelativeImportsResolveToSingleSymNode pins the
// lib-o8m guarantee: a Python module imported via `from . import utils` and
// `from mypkg import utils` from separate files produces a single
// `sym:mypkg.utils` graph node — not one per syntax form — so "who imports X?"
// queries see the full fan-in.
func TestIntegration_Graph_PythonRelativeImportsResolveToSingleSymNode(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mypkg/__init__.py": "",
		"mypkg/utils.py":    "def helper():\n    return 1\n",
		"mypkg/a.py":        "from . import utils\n\ndef a():\n    return utils.helper()\n",
		"mypkg/b.py":        "from mypkg import utils\n\ndef b():\n    return utils.helper()\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	dbPath := filepath.Join(root, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{
		DocsDir:     filepath.Join(root, "docs"),
		DBPath:      dbPath,
		ProjectRoot: root,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph:       config.GraphConfig{HonorGitignore: false, DetectGenerated: true, MaxWorkers: 1},
	}
	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	nodes, err := s.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var utilsSyms []string
	for _, n := range nodes {
		if n.Kind == "symbol" && (n.ID == "sym:mypkg.utils" || n.ID == "sym:.utils" || n.ID == "sym:utils") {
			utilsSyms = append(utilsSyms, n.ID)
		}
	}
	if len(utilsSyms) != 1 || utilsSyms[0] != "sym:mypkg.utils" {
		t.Errorf("utils symbol nodes = %v; want exactly [sym:mypkg.utils]", utilsSyms)
	}

	// Both files should have an import edge to sym:mypkg.utils. Relies on
	// UpsertNode using ON CONFLICT DO UPDATE (see lib-idi store fix) — with
	// INSERT OR REPLACE, b.py's re-upsert would cascade-delete a.py's edge.
	// filepath.Walk traverses alphabetically, so a.py runs before b.py —
	// that's the exact ordering the cascade-delete bug needed to surface.
	edges, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	importersOfUtils := map[string]bool{}
	for _, e := range edges {
		if e.To == "sym:mypkg.utils" && e.Kind == "import" {
			importersOfUtils[e.From] = true
		}
	}
	for _, want := range []string{"file:mypkg/a.py", "file:mypkg/b.py"} {
		if !importersOfUtils[want] {
			t.Errorf("missing import edge from %s to sym:mypkg.utils; have %v", want, importersOfUtils)
		}
	}
}

// TestIntegration_Graph_JSTSRelativeImportsResolveToSingleFileNode pins
// the JS/TS analog of lib-o8m's Python guarantee: two files importing the
// same module via different relative specifiers (`./utils` from src/ vs
// `../src/utils` from pkg/) produce a single `file:src/utils.ts` graph node
// with edges from both importers. Bare npm specifiers land on `ext:` nodes.
func TestIntegration_Graph_JSTSRelativeImportsResolveToSingleFileNode(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/utils.ts": "export const helper = 1;\n",
		"src/a.ts":     `import { helper } from "./utils"; import _ from "lodash"; export const a = helper;` + "\n",
		"src/b.ts":     `import { helper } from "./utils"; export const b = helper;` + "\n",
		"pkg/c.ts":     `import { helper } from "../src/utils"; export const c = helper;` + "\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	edges, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	importersOfUtils := map[string]bool{}
	for _, e := range edges {
		if e.Kind == "import" && e.To == "file:src/utils.ts" {
			importersOfUtils[e.From] = true
		}
	}
	for _, want := range []string{"file:src/a.ts", "file:src/b.ts", "file:pkg/c.ts"} {
		if !importersOfUtils[want] {
			t.Errorf("missing import edge from %s to file:src/utils.ts; have %v", want, importersOfUtils)
		}
	}

	// lodash (bare specifier) should land on an ext: node, not sym:.
	lodash, err := s.GetNode("ext:lodash")
	if err != nil {
		t.Fatalf("GetNode ext:lodash: %v", err)
	}
	if lodash == nil {
		t.Error("expected ext:lodash node for bare npm import")
	}
	if stale, _ := s.GetNode("sym:lodash"); stale != nil {
		t.Error("bare npm specifier leaked onto sym: namespace")
	}
}

// TestIntegration_Graph_ForceIndexCompatibleWithOrphanSweep constructs a
// seeded orphan (simulating a pre-lib-o8m leftover node), runs
// IndexProjectGraph with force=true, then invokes the sweep directly on the
// store. Pins that the two stages compose: forced re-index leaves the
// canonical nodes intact, and the subsequent sweep only removes the stale
// orphan. The CLI's cmd/index.go auto-trigger is built on this contract.
func TestIntegration_Graph_ForceIndexCompatibleWithOrphanSweep(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mypkg/__init__.py": "",
		"mypkg/utils.py":    "def helper():\n    return 1\n",
		"mypkg/a.py":        "from . import utils\n\ndef a():\n    return utils.helper()\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)

	// Seed a stale orphan node manually — mimics a leftover from a prior
	// indexer run with a different Reference.Target shape.
	if err := s.UpsertNode(store.Node{ID: "sym:.utils", Kind: store.NodeKindSymbol}); err != nil {
		t.Fatalf("UpsertNode seed: %v", err)
	}

	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// The stale orphan should still exist pre-sweep.
	if n, _ := s.GetNode("sym:.utils"); n == nil {
		t.Fatal("orphan sym:.utils unexpectedly removed by IndexProjectGraph alone")
	}

	deleted, err := s.DeleteOrphanNodes([]string{store.NodeKindSymbol})
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	foundStale := false
	for _, id := range deleted {
		if id == "sym:.utils" {
			foundStale = true
		}
	}
	if !foundStale {
		t.Errorf("stale orphan sym:.utils was not swept; deleted=%v", deleted)
	}
	// Canonical node must survive — it has inbound edges from a.py + utils.py.
	if n, _ := s.GetNode("sym:mypkg.utils"); n == nil {
		t.Error("canonical sym:mypkg.utils was incorrectly swept")
	}
}

// TestIntegration_Graph_PythonPyprojectAutoDetectsSrcRoot exercises the
// full auto-detect chain: a PEP 420 namespace package under src/ with no
// __init__.py anywhere, declared via pyproject.toml setuptools config, is
// detected by the indexer and anchors the resolver correctly. Without
// pyproject.toml detection the resolver would fall to the virtual
// directory fallback and land on sym:src.ns.b instead of sym:ns.b.
func TestIntegration_Graph_PythonPyprojectAutoDetectsSrcRoot(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pyproject.toml": "[tool.setuptools.packages.find]\nwhere = [\"src\"]\n",
		"src/ns/utils.py": "def helper():\n    return 1\n",
		"src/ns/a.py":     "from . import utils\n\ndef a():\n    return utils.helper()\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// newGraphTestIndexer builds its cfg without Python.SrcRoots; the
	// pyproject.toml must supply the anchor by itself.
	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	if n, _ := s.GetNode("sym:ns.utils"); n == nil {
		nodes, _ := s.ListNodes()
		var syms []string
		for _, nd := range nodes {
			if nd.Kind == "symbol" {
				syms = append(syms, nd.ID)
			}
		}
		t.Errorf("sym:ns.utils not found; symbol nodes: %v", syms)
	}
	if stale, _ := s.GetNode("sym:src.ns.utils"); stale != nil {
		t.Error("virtual-package fallback fired despite pyproject.toml auto-detect")
	}
}

// TestIntegration_Graph_PythonSrcRootsResolvesWithoutInitPy pins that
// configuring python.src_roots lets PEP 420 namespace packages (no
// __init__.py) resolve too. Without src_roots, a file under src/ns/ has no
// ancestor __init__.py and would fall to the virtual-package tier — with
// src_roots: [src], the file anchors cleanly at ns.
func TestIntegration_Graph_PythonSrcRootsResolvesWithoutInitPy(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/ns/utils.py": "def helper():\n    return 1\n",
		"src/ns/a.py":     "from . import utils\n\ndef a():\n    return utils.helper()\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	dbPath := filepath.Join(root, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		DocsDir:     filepath.Join(root, "docs"),
		DBPath:      dbPath,
		ProjectRoot: root,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph:       config.GraphConfig{HonorGitignore: false, DetectGenerated: true},
		Python:      config.PythonConfig{SrcRoots: []string{"src"}},
	}
	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	node, err := s.GetNode("sym:ns.utils")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node == nil {
		nodes, _ := s.ListNodes()
		var syms []string
		for _, n := range nodes {
			if n.Kind == "symbol" {
				syms = append(syms, n.ID)
			}
		}
		t.Errorf("sym:ns.utils not found; symbol nodes: %v", syms)
	}
}
