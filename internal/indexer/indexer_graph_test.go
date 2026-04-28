package indexer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	s, err := store.Open(dbPath, nil, 0)
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

	s, err := store.Open(dbPath, nil, 0)
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
	s, err := store.Open(dbPath, nil, 0)
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
	s, err := store.Open(dbPath, nil, 0)
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

// TestIntegration_InheritsEdge_CrossLanguage is the end-to-end acceptance
// test for lib-wji.1: mixed-language workspace (Java + Python + TypeScript
// each declaring an `extends`-style parent), indexed via IndexProjectGraph,
// and queried via store.Neighbors with the new kind filter.
//
// What's asserted:
//   - Every language produces an `inherits` edge with the right relation.
//   - Edge.From anchors at sym:<child> (symbol-scoped source via
//     ref.Source), not at the file node.
//   - Same-file import resolution materialises for Java + TS (child's parent
//     resolves to the imported FQN) and bare names without imports land
//     with metadata.unresolved=true.
//   - Neighbors(..., EdgeKindInherits) filters cleanly — no edges of other
//     kinds leak through.
func TestIntegration_InheritsEdge_CrossLanguage(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		// Java: Base lives in a different package so Child imports it
		// explicitly. Same-package bare-name resolution is deferred to
		// lib-38i; this fixture exercises the lib-wji.1-scoped
		// same-file-import lookup only.
		"src/java/com/example/bases/Base.java": `package com.example.bases;

public class Base {}
`,
		"src/java/com/example/child/Child.java": `package com.example.child;

import com.example.bases.Base;

public class Child extends Base {}
`,
		// Python: distinct stem + class names so the resulting sym: ids
		// don't collide with the TypeScript ones below. A collision would
		// let the two languages' edges overwrite each other on the same
		// (from, to, kind) key, making this test a false positive — if
		// TS extraction broke, Python's edge would mask the gap.
		"src/py/pybase.py": `class PyBase:
    pass
`,
		"src/py/pychild.py": `from pybase import PyBase
class PyChild(PyBase):
    pass
`,
		// TypeScript: distinct names for the same reason.
		"src/ts/tsbase.ts": `export class TsBase {}
`,
		"src/ts/tschild.ts": `import { TsBase } from "./tsbase";
class TsChild extends TsBase {}
`,
		// Swift: exercises lib-wji.4's per-flavor heuristic (first
		// inheritance_specifier on a class is extends), extension Unit
		// Kind with target-as-Title, and the Unit.Metadata["extends_type"]
		// surface on the extension itself.
		"src/swift/SwBase.swift": `class SwBase {}
`,
		"src/swift/SwChild.swift": `class SwChild: SwBase, Codable {}
`,
		"src/swift/SwExt.swift": `extension String: Identifiable {
    public var id: String { self }
}
`,
		// Kotlin: exercises the full lib-wji.2 pipeline (SymbolParents,
		// ResolveParents, constructor_invocation → extends heuristic) +
		// the lib-wji.2-round-3 fixes (companion object walking, primary-
		// constructor val/var properties, Unit.Metadata → graph_nodes
		// serialisation for the extension-function receiver).
		"src/kt/ktbase.kt": `package com.example.kt
abstract class KtBase(val tag: String)
`,
		"src/kt/ktchild.kt": `package com.example.kt
import com.example.kt.KtBase
data class KtChild(val name: String) : KtBase("child") {
    companion object {
        const val DEFAULT = "anon"
    }
}
fun String.ktSlug(): String = this.lowercase()
`,
		// Dart: exercises lib-wji.3's per-flavor heuristic (extends +
		// implements + with on a single class, emitted as three inherits
		// edges with distinct Metadata.relation), plus the two new
		// Reference.Kinds — `requires` on mixin on-clauses and `part` on
		// part directives — that are ONLY produced by the Dart grammar.
		"src/dart/dr_base.dart": `library dr.base;
class DrBase {}
`,
		"src/dart/dr_child.dart": `library dr.child;
class DrChild extends DrBase implements Comparable with Mixable {}
`,
		"src/dart/dr_mixin.dart": `library dr.mixin;
mixin Mixable on DrBase {}
`,
		"src/dart/dr_library.dart": `library dr.library;
part 'dr_library.g.dart';
`,
		// Protobuf: exercises lib-cym's end-to-end projection — service /
		// rpc / message / field Units land as symbol nodes, rpc Metadata
		// (input_type / streaming) survives into graph_nodes.metadata, and
		// `extend Foo { ... }` emits a file-scoped inherits edge.
		"src/proto/greeter.proto": `syntax = "proto3";
package pr.v1;

option go_package = "example.com/pr/v1;prv1";

service Greeter {
  rpc SayHi (HiRequest) returns (HiReply);
  rpc StreamHi (stream HiRequest) returns (stream HiReply);
}

message HiRequest {
  string name = 1;
}

message HiReply {
  string greeting = 1;
}

extend Base {
  string extra = 100;
}
`,
	}
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}

	cfg := &config.Config{
		DocsDir:     filepath.Join(dir, "docs"),
		DBPath:      filepath.Join(dir, ".librarian", "test.db"),
		ProjectRoot: dir,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, OverlapLines: 0, MinTokens: 1},
		Graph:       config.GraphConfig{HonorGitignore: false, DetectGenerated: true},
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(cfg.DBPath, nil, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Per-language expectations: (child sym id, parent sym id, relation).
	// Java Unit paths are "<package>.<class>", Python/TS are "<stem>.<class>".
	cases := []struct {
		name                        string
		childID, parentID, relation string
	}{
		{"java", "sym:com.example.child.Child", "sym:com.example.bases.Base", "extends"},
		{"python", "sym:pychild.PyChild", "sym:pybase.PyBase", "extends"},
		{"typescript", "sym:tschild.TsChild", "sym:tsbase.TsBase", "extends"},
		// Kotlin's `: KtBase("child")` uses constructor_invocation —
		// heuristic maps to relation=extends. KtBase imported cross-file,
		// so ResolveParents rewrites the bare name to the FQN.
		{"kotlin", "sym:com.example.kt.KtChild", "sym:com.example.kt.KtBase", "extends"},
		// Swift's per-flavor heuristic: first inheritance_specifier on a
		// class-flavor declaration is relation=extends. SwBase is in the
		// same stem (no imports to resolve), so the per-file resolver
		// leaves it unresolved. The post-graph FQN resolver (lib-udam.3)
		// rewrites it to the real symbol.
		{"swift", "sym:SwChild.SwChild", "sym:SwBase.SwBase", "extends"},
		// Dart's class heritage: `class DrChild extends DrBase implements
		// Comparable with Mixable` emits three inherits edges. The
		// extends edge (asserted here) is the clean case; the implements
		// + mixes edges are verified in the dart_inheritance_all_relations
		// subtest below. DrBase is resolved by lib-udam.3 to its FQN.
		{"dart", "sym:dr.child.DrChild", "sym:dr.base.DrBase", "extends"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outEdges, err := s.Neighbors(tc.childID, "out", store.EdgeKindInherits)
			if err != nil {
				t.Fatalf("Neighbors %s: %v", tc.childID, err)
			}
			found := false
			for _, e := range outEdges {
				if e.To == tc.parentID && e.Kind == store.EdgeKindInherits {
					found = true
					if !containsJSON(e.Metadata, `"relation":"`+tc.relation+`"`) {
						t.Errorf("edge %s → %s metadata %q missing relation=%q", tc.childID, tc.parentID, e.Metadata, tc.relation)
					}
				}
			}
			if !found {
				allNodes, _ := s.ListNodes()
				var syms []string
				for _, n := range allNodes {
					if n.Kind == "symbol" {
						syms = append(syms, n.ID)
					}
				}
				t.Errorf("no inherits edge from %s to %s (symbols: %v; out edges: %+v)",
					tc.childID, tc.parentID, syms, outEdges)
			}
		})
	}

	// Kind filter sanity: Neighbors WITHOUT the filter returns >= the
	// filtered count; WITH the filter returns only inherits edges.
	allEdges, err := s.Neighbors("sym:com.example.child.Child", "out")
	if err != nil {
		t.Fatalf("Neighbors (no filter): %v", err)
	}
	inheritsOnly, err := s.Neighbors("sym:com.example.child.Child", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors (inherits): %v", err)
	}
	if len(inheritsOnly) > len(allEdges) {
		t.Errorf("filtered edges %d > total %d — filter broke", len(inheritsOnly), len(allEdges))
	}
	for _, e := range inheritsOnly {
		if e.Kind != store.EdgeKindInherits {
			t.Errorf("filter leaked %q edge through inherits filter", e.Kind)
		}
	}

	// Kotlin-specific end-to-end assertions (lib-wji.2 round-3 fixes).
	// These catch the live-flow regressions that unit-level tests miss:
	// companion-object members reaching the graph, primary-constructor
	// val properties reaching the graph, and symbolMetadataExtractor
	// output landing in graph_nodes.metadata.
	t.Run("kotlin_companion_object_members", func(t *testing.T) {
		// `companion object { const val DEFAULT = "anon" }` must surface
		// as a symbol node — proves the walker descends into
		// companion_object's class_body.
		n, _ := s.GetNode("sym:com.example.kt.KtChild.Companion.DEFAULT")
		if n == nil {
			allNodes, _ := s.ListNodes()
			var syms []string
			for _, nd := range allNodes {
				if nd.Kind == "symbol" {
					syms = append(syms, nd.ID)
				}
			}
			t.Errorf("expected sym:com.example.kt.KtChild.Companion.DEFAULT — companion-object member not projected; symbols: %v", syms)
		}
	})
	t.Run("kotlin_primary_constructor_property", func(t *testing.T) {
		// `data class KtChild(val name: String)` — `name` is a property.
		n, _ := s.GetNode("sym:com.example.kt.KtChild.name")
		if n == nil {
			t.Errorf("expected sym:com.example.kt.KtChild.name — primary-constructor val should project as property node")
		}
	})
	t.Run("kotlin_receiver_metadata_in_graph_nodes", func(t *testing.T) {
		// `fun String.ktSlug()` — SymbolMetadata emits
		// {"receiver":"String"}; the graph pass must serialise that into
		// graph_nodes.metadata so downstream queries can filter on it.
		n, err := s.GetNode("sym:com.example.kt.ktSlug")
		if err != nil {
			t.Fatalf("GetNode ktSlug: %v", err)
		}
		if n == nil {
			t.Fatalf("ktSlug symbol missing")
		}
		if !containsJSON(n.Metadata, `"receiver":"String"`) {
			t.Errorf("expected receiver metadata on ktSlug symbol node; got %q", n.Metadata)
		}
	})
	t.Run("swift_extension_target_metadata", func(t *testing.T) {
		// `extension String: Identifiable` — the extension Unit itself
		// should land as sym:SwExt.String with Kind=symbol (via the
		// "extension" isSymbolKind entry) and carry extends_type=String
		// in its metadata.
		n, err := s.GetNode("sym:SwExt.String")
		if err != nil {
			t.Fatalf("GetNode: %v", err)
		}
		if n == nil {
			t.Fatalf("extension String symbol missing")
		}
		if !containsJSON(n.Metadata, `"extends_type":"String"`) {
			t.Errorf("expected extends_type=String on extension symbol node; got %q", n.Metadata)
		}
	})
	t.Run("swift_extension_member_receiver", func(t *testing.T) {
		// `extension String { public var id: String { self } }` —
		// the property Unit gets Metadata["receiver"]="String" via
		// SymbolMetadata on the enclosing extension.
		n, err := s.GetNode("sym:SwExt.String.id")
		if err != nil {
			t.Fatalf("GetNode: %v", err)
		}
		if n == nil {
			t.Fatalf("extension property symbol missing")
		}
		if !containsJSON(n.Metadata, `"receiver":"String"`) {
			t.Errorf("expected receiver=String on extension property node; got %q", n.Metadata)
		}
	})
	t.Run("dart_inheritance_all_relations", func(t *testing.T) {
		// `class DrChild extends DrBase implements Comparable with Mixable`
		// should produce three inherits edges with distinct relation values.
		// The extends edge is already verified by the table-driven cases
		// above; here we check implements + mixes land correctly.
		edges, err := s.Neighbors("sym:dr.child.DrChild", "out", store.EdgeKindInherits)
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		// DrBase and Mixable are resolved by the post-graph FQN resolver
		// (lib-udam.3) to their fully-qualified sym: IDs. Comparable has
		// no in-workspace definition and stays as a placeholder.
		wantByTarget := map[string]string{
			"sym:dr.base.DrBase":   `"relation":"extends"`,
			"sym:Comparable":       `"relation":"implements"`,
			"sym:dr.mixin.Mixable": `"relation":"mixes"`,
		}
		seen := map[string]string{}
		for _, e := range edges {
			seen[e.To] = e.Metadata
		}
		for target, wantRelation := range wantByTarget {
			meta, ok := seen[target]
			if !ok {
				t.Errorf("missing inherits edge to %s (got targets %v)", target, keys(seen))
				continue
			}
			if !containsJSON(meta, wantRelation) {
				t.Errorf("edge to %s: expected %s, got %q", target, wantRelation, meta)
			}
		}
	})
	t.Run("dart_mixin_on_emits_requires_edge", func(t *testing.T) {
		// `mixin Mixable on DrBase` emits a Reference.Kind="requires",
		// which materialises as an EdgeKindRequires edge separately from
		// the inherits edges above. Verifies the new edge kind end-to-end.
		edges, err := s.Neighbors("sym:dr.mixin.Mixable", "out", store.EdgeKindRequires)
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		found := false
		for _, e := range edges {
			if e.To == "sym:DrBase" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected requires edge from Mixable to DrBase; got edges %+v", edges)
		}
		// Mixable must NOT appear in the inherits edge list.
		inh, err := s.Neighbors("sym:dr.mixin.Mixable", "out", store.EdgeKindInherits)
		if err != nil {
			t.Fatalf("Neighbors inherits: %v", err)
		}
		for _, e := range inh {
			if e.To == "sym:DrBase" {
				t.Errorf("Mixable's on-clause leaked into inherits edge (should be requires only): %+v", e)
			}
		}
	})
	t.Run("dart_part_directive_emits_part_edge", func(t *testing.T) {
		// `part 'dr_library.g.dart'` emits a Reference.Kind="part",
		// materialising as an EdgeKindPart edge from the library file
		// to the part file. Target routes to CodeFileNodeID.
		edges, err := s.Neighbors("file:src/dart/dr_library.dart", "out", store.EdgeKindPart)
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		found := false
		for _, e := range edges {
			if strings.Contains(e.To, "dr_library.g.dart") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected part edge to dr_library.g.dart; got %+v", edges)
		}
	})
	t.Run("proto_service_and_rpc_symbols", func(t *testing.T) {
		// service Greeter { rpc SayHi ... } produces two distinct symbol
		// nodes under the file's package path, with rpc Metadata (input
		// type, streaming booleans) serialised into graph_nodes.metadata.
		if n, err := s.GetNode("sym:pr.v1.Greeter"); err != nil {
			t.Fatalf("GetNode Greeter: %v", err)
		} else if n == nil {
			t.Fatalf("service Greeter symbol missing")
		}
		rpc, err := s.GetNode("sym:pr.v1.Greeter.SayHi")
		if err != nil {
			t.Fatalf("GetNode SayHi: %v", err)
		}
		if rpc == nil {
			t.Fatalf("rpc SayHi symbol missing")
		}
		if !containsJSON(rpc.Metadata, `"input_type":"HiRequest"`) {
			t.Errorf("expected input_type=HiRequest on SayHi metadata; got %q", rpc.Metadata)
		}
		if !containsJSON(rpc.Metadata, `"output_type":"HiReply"`) {
			t.Errorf("expected output_type=HiReply on SayHi metadata; got %q", rpc.Metadata)
		}
		// Non-streaming rpc still serialises both flags as false so the
		// downstream lib-6wz cross-language matcher doesn't have to deal
		// with "absent-key means not-streaming" ambiguity.
		if !containsJSON(rpc.Metadata, `"client_streaming":false`) {
			t.Errorf("expected client_streaming=false on SayHi metadata; got %q", rpc.Metadata)
		}
		if !containsJSON(rpc.Metadata, `"server_streaming":false`) {
			t.Errorf("expected server_streaming=false on SayHi metadata; got %q", rpc.Metadata)
		}
		streaming, err := s.GetNode("sym:pr.v1.Greeter.StreamHi")
		if err != nil {
			t.Fatalf("GetNode StreamHi: %v", err)
		}
		if streaming == nil {
			t.Fatalf("rpc StreamHi symbol missing")
		}
		if !containsJSON(streaming.Metadata, `"client_streaming":true`) {
			t.Errorf("expected client_streaming=true on StreamHi metadata; got %q", streaming.Metadata)
		}
		if !containsJSON(streaming.Metadata, `"server_streaming":true`) {
			t.Errorf("expected server_streaming=true on StreamHi metadata; got %q", streaming.Metadata)
		}
	})
	t.Run("proto_field_carries_field_number", func(t *testing.T) {
		// Message fields project as symbol nodes with field_number
		// serialised into graph_nodes.metadata — available to downstream
		// cross-language (lib-6wz) and buf.gen.yaml (lib-4kb) logic.
		n, err := s.GetNode("sym:pr.v1.HiRequest.name")
		if err != nil {
			t.Fatalf("GetNode name: %v", err)
		}
		if n == nil {
			t.Fatalf("field HiRequest.name symbol missing")
		}
		// Substring terminators guard against false positives like
		// `"field_number":10` matching a bare `"field_number":1` prefix.
		if !containsJSON(n.Metadata, `"field_number":1,`) && !containsJSON(n.Metadata, `"field_number":1}`) {
			t.Errorf("expected field_number=1 on name metadata; got %q", n.Metadata)
		}
	})
	t.Run("proto_extend_emits_file_scoped_inherits_edge", func(t *testing.T) {
		// `extend Base { ... }` has no surrounding symbol (file-scoped),
		// so the inherits edge originates at the file node and targets a
		// placeholder sym:Base node with relation=extends in metadata.
		edges, err := s.Neighbors("file:src/proto/greeter.proto", "out", store.EdgeKindInherits)
		if err != nil {
			t.Fatalf("Neighbors: %v", err)
		}
		found := false
		for _, e := range edges {
			if e.To == "sym:Base" && containsJSON(e.Metadata, `"relation":"extends"`) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected inherits edge to sym:Base (relation=extends); got %+v", edges)
		}
	})
}

// TestIntegration_Graph_CrossFileEdgeReconstitution is the regression test for
// lib-flx: reindexing a file that defines a symbol must not permanently destroy
// cross-file edges from OTHER unchanged files that pointed at that symbol.
//
// Scenario:
//   - base.py defines class Base (in file base.py).
//   - child.py imports Base and declares class Child(Base) (in child.py).
//   - IndexProjectGraph creates: sym:base.Base, sym:child.Child, and an
//     inherits edge from sym:child.Child → sym:base.Base.
//   - base.py is modified (different content, so its hash changes).
//   - IndexProjectGraph runs again (incremental, force=false).
//   - Before the fix, DeleteSymbolsForFile(base.py) would cascade-delete the
//     inherits edge. child.py's hash is unchanged so child.py is never
//     reindexed, and the edge is gone forever.
//   - After the fix, the indexer detects that child.py holds a cross-file edge
//     into base.py's symbols and force-reindexes child.py to reconstitute it.
func TestIntegration_Graph_CrossFileEdgeReconstitution(t *testing.T) {
	root := t.TempDir()

	writeFileHelper := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Initial content: Base declares a class, Child inherits from it.
	writeFileHelper("base.py", "class Base:\n    pass\n")
	writeFileHelper("child.py", "from base import Base\nclass Child(Base):\n    pass\n")

	idx, s := newGraphTestIndexer(t, root)

	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("first IndexProjectGraph: %v", err)
	}

	// Confirm the inherits edge exists before we touch base.py.
	edges, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges after first index: %v", err)
	}
	var found bool
	for _, e := range edges {
		if e.Kind == store.EdgeKindInherits && e.From == "sym:child.Child" && e.To == "sym:base.Base" {
			found = true
		}
	}
	if !found {
		var syms []string
		nodes, _ := s.ListNodes()
		for _, n := range nodes {
			if n.Kind == "symbol" {
				syms = append(syms, n.ID)
			}
		}
		t.Fatalf("inherits edge from sym:child.Child → sym:base.Base missing after first index; symbols=%v edges=%+v", syms, edges)
	}

	// Modify base.py so its hash changes — simulates a real upstream edit.
	// The content is functionally equivalent; only the hash matters here.
	writeFileHelper("base.py", "class Base:\n    \"\"\"Upstream base class.\"\"\"\n    pass\n")

	// Incremental reindex: child.py is unchanged so only base.py is reindexed.
	if _, err := idx.IndexProjectGraph(root, false); err != nil {
		t.Fatalf("second IndexProjectGraph: %v", err)
	}

	// The reconstitution must have restored the inherits edge from child.py.
	edges, err = s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges after second index: %v", err)
	}
	found = false
	for _, e := range edges {
		if e.Kind == store.EdgeKindInherits && e.From == "sym:child.Child" && e.To == "sym:base.Base" {
			found = true
		}
	}
	if !found {
		var edgeSummary []string
		for _, e := range edges {
			edgeSummary = append(edgeSummary, fmt.Sprintf("%s -[%s]-> %s", e.From, e.Kind, e.To))
		}
		t.Errorf("inherits edge from sym:child.Child → sym:base.Base lost after reindexing base.py; all edges: %v", edgeSummary)
	}
}

// TestIntegration_Graph_PythonTypeVarProjectedAsSymbolNode pins the lib-0pa.2
// guarantee: a module-scope TypeVar declaration (`T = TypeVar("T")`) is
// projected into graph_nodes as a symbol node with id="sym:mymod.T" and
// kind_hint="typevar" in metadata. This guards the isSymbolKind gate in the
// graph pass — without "typevar" in that switch the Unit is silently dropped.
func TestIntegration_Graph_PythonTypeVarProjectedAsSymbolNode(t *testing.T) {
	root := t.TempDir()
	content := "T = TypeVar(\"T\")\nU = TypeVar(\"U\", bound=Hashable)\n"
	abs := filepath.Join(root, "mymod.py")
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write mymod.py: %v", err)
	}

	dbPath := filepath.Join(root, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
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

	byID := map[string]store.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	// T must project as a symbol node.
	tNode, ok := byID["sym:mymod.T"]
	if !ok {
		var symIDs []string
		for id, n := range byID {
			if n.Kind == "symbol" {
				symIDs = append(symIDs, id)
			}
		}
		t.Fatalf("sym:mymod.T not found in graph; symbol nodes: %v", symIDs)
	}
	if tNode.Kind != "symbol" {
		t.Errorf("sym:mymod.T kind = %q, want symbol", tNode.Kind)
	}
	if !containsJSON(tNode.Metadata, `"kind_hint":"typevar"`) {
		t.Errorf("sym:mymod.T metadata missing kind_hint=typevar; metadata=%s", tNode.Metadata)
	}

	// U must also project as a symbol node with bound="Hashable".
	uNode, ok := byID["sym:mymod.U"]
	if !ok {
		t.Errorf("sym:mymod.U not found in graph; all nodes: %v", strings.Join(func() []string {
			var ids []string
			for id := range byID {
				ids = append(ids, id)
			}
			return ids
		}(), ", "))
	} else if !containsJSON(uNode.Metadata, `"bound":"Hashable"`) {
		t.Errorf("sym:mymod.U metadata missing bound=Hashable; metadata=%s", uNode.Metadata)
	}
}

// --- lib-0pa.5: cross-module TypeVar resolution integration tests ---
//
// NOTE: Tests below use absolute imports (from mytypes import T) for simplicity.
// An integration test for the relative-import form (from .types import T), which
// exercises ResolveImports rewriting .types.T → mypkg.types.T via the
// __init__.py walk, is tracked as out-of-scope for lib-0pa.5 in lib-4w1.

// TestIntegration_Graph_PythonCrossModuleTypeVar_Resolved pins the lib-0pa.5
// guarantee: when T is imported from another module in the same project and
// that module declares T as a TypeVar, the post-graph-pass resolver populates
// type_args_resolved on the inherits edge.
//
// Layout: mytypes.py declares T; repo.py imports T absolutely and uses
// Generic[T]. After IndexProjectGraph the inherits edge from sym:repo.Foo
// to the Generic target should carry type_args_resolved=["sym:mytypes.T"].
func TestIntegration_Graph_PythonCrossModuleTypeVar_Resolved(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mytypes.py": "from typing import TypeVar\nT = TypeVar(\"T\")\n",
		"repo.py":    "from mytypes import T\nfrom typing import Generic\n\nclass Foo(Generic[T]):\n    pass\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Find the inherits edge from sym:repo.Foo to the Generic symbol.
	outEdges, err := s.Neighbors("sym:repo.Foo", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	var genericEdge *store.Edge
	for i := range outEdges {
		if containsJSON(outEdges[i].Metadata, `"type_args"`) {
			genericEdge = &outEdges[i]
			break
		}
	}
	if genericEdge == nil {
		t.Fatalf("expected Generic inherits edge from sym:repo.Foo with type_args; edges: %+v", outEdges)
	}

	// type_args_resolved must be present and point to sym:mytypes.T.
	if !containsJSON(genericEdge.Metadata, `"type_args_resolved":["sym:mytypes.T"]`) {
		t.Errorf("type_args_resolved missing or wrong; edge metadata: %s", genericEdge.Metadata)
	}
	// type_args_pending_cross_module should be absent (cleaned up after resolution).
	if containsJSON(genericEdge.Metadata, `"type_args_pending_cross_module"`) {
		t.Errorf("type_args_pending_cross_module should be absent after resolution; edge metadata: %s", genericEdge.Metadata)
	}
}

// TestIntegration_Graph_PythonCrossModuleTypeVar_ExternalUnresolved pins that
// an import from an external (un-indexed) package does not pollute
// type_args_resolved — sym:typing_extensions.T has no TypeVar symbol node
// in the project, so the inherits edge should lack type_args_resolved.
func TestIntegration_Graph_PythonCrossModuleTypeVar_ExternalUnresolved(t *testing.T) {
	root := t.TempDir()
	// Only repo.py; typing_extensions is not indexed as part of this project.
	src := "from typing_extensions import T\nfrom typing import Generic\n\nclass Foo(Generic[T]):\n    pass\n"
	if err := os.WriteFile(filepath.Join(root, "repo.py"), []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	outEdges, err := s.Neighbors("sym:repo.Foo", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	var genericEdge *store.Edge
	for i := range outEdges {
		if containsJSON(outEdges[i].Metadata, `"type_args"`) {
			genericEdge = &outEdges[i]
			break
		}
	}
	if genericEdge == nil {
		t.Fatalf("expected Generic inherits edge from sym:repo.Foo with type_args; edges: %+v", outEdges)
	}

	// type_args_resolved must be absent — typing_extensions.T is not a project TypeVar node.
	if containsJSON(genericEdge.Metadata, `"type_args_resolved"`) {
		t.Errorf("expected type_args_resolved absent for external import; edge metadata: %s", genericEdge.Metadata)
	}
	// type_args_pending_cross_module must be cleaned up even when unresolved.
	if containsJSON(genericEdge.Metadata, `"type_args_pending_cross_module"`) {
		t.Errorf("expected type_args_pending_cross_module absent after resolver run; edge metadata: %s", genericEdge.Metadata)
	}
}

// TestIntegration_Graph_PythonCrossModuleTypeVar_AliasedImport pins the
// alias-import form: `from mytypes import T as MyT`. The resolved
// type_args_resolved should contain sym:mytypes.T (the canonical path),
// not sym:mytypes.MyT (the local alias).
func TestIntegration_Graph_PythonCrossModuleTypeVar_AliasedImport(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mytypes.py": "from typing import TypeVar\nT = TypeVar(\"T\")\n",
		"repo.py":    "from mytypes import T as MyT\nfrom typing import Generic\n\nclass Foo(Generic[MyT]):\n    pass\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	outEdges, err := s.Neighbors("sym:repo.Foo", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	var genericEdge *store.Edge
	for i := range outEdges {
		if containsJSON(outEdges[i].Metadata, `"type_args"`) {
			genericEdge = &outEdges[i]
			break
		}
	}
	if genericEdge == nil {
		t.Fatalf("expected Generic inherits edge from sym:repo.Foo with type_args; edges: %+v", outEdges)
	}

	// type_args_resolved must use the canonical sym:mytypes.T path, not the local alias.
	if !containsJSON(genericEdge.Metadata, `"type_args_resolved":["sym:mytypes.T"]`) {
		t.Errorf("type_args_resolved missing or uses alias path; edge metadata: %s", genericEdge.Metadata)
	}
}

// TestIntegration_Graph_PythonCrossModuleTypeVar_IncrementalReindex pins that
// a second IndexProjectGraph call (incremental, files unchanged) preserves
// type_args_resolved and does not re-introduce type_args_pending_cross_module.
// On the first run, the resolver sets type_args_resolved and removes the
// pending key. On the second run, repo.py is hash-skipped so the edge is not
// rewritten, and ListEdgesWithMetadataContaining finds no pending edges.
func TestIntegration_Graph_PythonCrossModuleTypeVar_IncrementalReindex(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mytypes.py": "from typing import TypeVar\nT = TypeVar(\"T\")\n",
		"repo.py":    "from mytypes import T\nfrom typing import Generic\n\nclass Foo(Generic[T]):\n    pass\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)

	// First run: force=true so all files are processed.
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph (first run): %v", err)
	}

	// Second run: force=false — files are unchanged, so both are hash-skipped.
	if _, err := idx.IndexProjectGraph(root, false); err != nil {
		t.Fatalf("IndexProjectGraph (second run): %v", err)
	}

	outEdges, err := s.Neighbors("sym:repo.Foo", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	var genericEdge *store.Edge
	for i := range outEdges {
		if containsJSON(outEdges[i].Metadata, `"type_args"`) {
			genericEdge = &outEdges[i]
			break
		}
	}
	if genericEdge == nil {
		t.Fatalf("expected Generic inherits edge with type_args; edges: %+v", outEdges)
	}

	if !containsJSON(genericEdge.Metadata, `"type_args_resolved":["sym:mytypes.T"]`) {
		t.Errorf("type_args_resolved lost on incremental reindex; edge metadata: %s", genericEdge.Metadata)
	}
	if containsJSON(genericEdge.Metadata, `"type_args_pending_cross_module"`) {
		t.Errorf("type_args_pending_cross_module leaked into edge after incremental reindex; metadata: %s", genericEdge.Metadata)
	}
}

// TestIntegration_Graph_PythonCrossModuleTypeVar_RelativeImport pins the
// relative-import form: `from .types import T`. ResolveImports rewrites
// .types.T → mypkg.types.T using the __init__.py walk so the post-graph-pass
// resolver can connect the pending candidate to the sym:mypkg.types.T node.
//
// Layout: mypkg/__init__.py (empty), mypkg/types.py declares T, mypkg/repo.py
// uses from .types import T and class Foo(Generic[T]). After IndexProjectGraph
// the inherits edge from sym:mypkg.repo.Foo should carry
// type_args_resolved=["sym:mypkg.types.T"].
func TestIntegration_Graph_PythonCrossModuleTypeVar_RelativeImport(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"mypkg/__init__.py": "",
		"mypkg/types.py":    "from typing import TypeVar\nT = TypeVar(\"T\")\n",
		"mypkg/repo.py":     "from .types import T\nfrom typing import Generic\n\nclass Foo(Generic[T]):\n    pass\n",
	}
	for rel, body := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	idx, s := newGraphTestIndexer(t, root)
	if _, err := idx.IndexProjectGraph(root, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Find the inherits edge from sym:mypkg.repo.Foo to the Generic symbol.
	outEdges, err := s.Neighbors("sym:mypkg.repo.Foo", "out", store.EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	var genericEdge *store.Edge
	for i := range outEdges {
		if containsJSON(outEdges[i].Metadata, `"type_args"`) {
			genericEdge = &outEdges[i]
			break
		}
	}
	if genericEdge == nil {
		t.Fatalf("expected Generic inherits edge from sym:mypkg.repo.Foo with type_args; edges: %+v", outEdges)
	}

	// type_args_resolved must be present and point to sym:mypkg.types.T.
	if !containsJSON(genericEdge.Metadata, `"type_args_resolved":["sym:mypkg.types.T"]`) {
		t.Errorf("type_args_resolved missing or wrong; edge metadata: %s", genericEdge.Metadata)
	}
	// type_args_pending_cross_module should be absent (cleaned up after resolution).
	if containsJSON(genericEdge.Metadata, `"type_args_pending_cross_module"`) {
		t.Errorf("type_args_pending_cross_module should be absent after resolution; edge metadata: %s", genericEdge.Metadata)
	}
}

// keys returns the sorted key set of a map[string]string for stable
// failure messages.
func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// containsJSON is a forgiving substring check used above because Edge.Metadata
// is a JSON string whose key order is deterministic but whose presence of
// other keys (e.g., type_args) varies by test case. Substring-checking a
// fully-quoted key:value pair is enough to pin behaviour without depending
// on map layout.
func containsJSON(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexerTestContains(haystack, needle)
}

func indexerTestContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
