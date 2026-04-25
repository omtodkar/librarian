package indexer

import (
	"path/filepath"
	"sort"
	"testing"
)

// stubHandler is a minimal FileHandler useful only for walker tests — it
// declares a name and a set of extensions, and panics if Parse / Chunk are
// called. The walker only needs Name() and Extensions() to dispatch.
type stubHandler struct {
	name string
	exts []string
}

func (h *stubHandler) Name() string         { return h.name }
func (h *stubHandler) Extensions() []string { return h.exts }
func (h *stubHandler) Parse(path string, content []byte) (*ParsedDoc, error) {
	panic("stubHandler.Parse should not be called by walker tests")
}
func (h *stubHandler) Chunk(*ParsedDoc, ChunkOpts) ([]Chunk, error) {
	panic("stubHandler.Chunk should not be called by walker tests")
}

// walkerFixtureRegistry returns a registry with stub handlers for the
// extensions the tests exercise. Isolated from DefaultRegistry so tests
// don't pick up whatever the package init() wired up.
func walkerFixtureRegistry() *Registry {
	r := NewRegistry()
	r.Register(&stubHandler{name: "go", exts: []string{".go"}})
	r.Register(&stubHandler{name: "python", exts: []string{".py"}})
	r.Register(&stubHandler{name: "markdown", exts: []string{".md"}})
	return r
}

// paths returns the sorted FilePaths of a walk result so tests can assert
// against a stable order regardless of filesystem iteration.
func paths(results []WalkResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = filepath.ToSlash(r.FilePath)
	}
	sort.Strings(out)
	return out
}

func TestWalkGraph_BasicWalk(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, "internal/auth/service.go", "package auth\n")
	writeFile(t, root, "scripts/build.py", "print('hi')\n")
	writeFile(t, root, "README.md", "# proj\n") // markdown, skipped by SkipFormats

	results, err := WalkGraph(root, GraphWalkConfig{
		SkipFormats: map[string]bool{"markdown": true},
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"internal/auth/service.go", "main.go", "scripts/build.py"}
	if !equalSlices(got, want) {
		t.Errorf("files mismatch:\n got=%v\nwant=%v", got, want)
	}
}

// TestWalkGraph_SkipsLibrarianAndGit pins the hard-exclude guarantee: the
// user cannot accidentally (or deliberately, via empty ExcludePatterns) ask
// the graph pass to descend into .git or .librarian.
func TestWalkGraph_SkipsLibrarianAndGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/main\n") // not indexed but shouldn't even be visited
	writeFile(t, root, ".librarian/config.yaml", "docs_dir: docs\n")
	writeFile(t, root, ".librarian/skill.md", "body\n") // would match markdown but must be skipped
	writeFile(t, root, ".git/hooks/pre-commit.py", "print()\n")

	results, err := WalkGraph(root, GraphWalkConfig{
		// Empty ExcludePatterns + markdown NOT skipped — we want to prove
		// even a permissive config can't leak through the hard excludes.
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	for _, r := range results {
		p := filepath.ToSlash(r.FilePath)
		if p == ".git/hooks/pre-commit.py" || p == ".librarian/config.yaml" || p == ".librarian/skill.md" {
			t.Errorf("hard-excluded path surfaced: %s", p)
		}
	}
}

// TestWalkGraph_DefaultExcludes pins that node_modules, vendor, etc. are
// pruned by default without any user config.
func TestWalkGraph_DefaultExcludes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, "node_modules/foo/index.py", "")
	writeFile(t, root, "vendor/stuff.go", "")
	writeFile(t, root, "target/debug/x.go", "")
	writeFile(t, root, "build/gen.py", "")
	writeFile(t, root, ".venv/lib/util.py", "")
	writeFile(t, root, "dist/bundle.py", "")

	results, err := WalkGraph(root, GraphWalkConfig{}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"main.go"}
	if !equalSlices(got, want) {
		t.Errorf("default excludes not applied: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_MonorepoFrameworkDefaults pins PR 2's expanded defaults:
// framework cache directories common in JS/TS monorepos (.turbo, .nx,
// .yarn, .cache, .parcel-cache, .svelte-kit, .nuxt) are pruned without
// any user config. Plus Bazel symlink roots via the bazel-* glob.
func TestWalkGraph_MonorepoFrameworkDefaults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "apps/web/main.go", "")
	// The trap files below should all be pruned by the new defaults.
	writeFile(t, root, ".turbo/cache/x.py", "")
	writeFile(t, root, ".nx/cache/y.py", "")
	writeFile(t, root, ".yarn/cache/zzz.py", "")
	writeFile(t, root, ".cache/tool/a.py", "")
	writeFile(t, root, ".parcel-cache/b.py", "")
	writeFile(t, root, ".svelte-kit/generated/c.py", "")
	writeFile(t, root, ".nuxt/app/d.py", "")
	writeFile(t, root, "bazel-bin/pkg/e.py", "")
	writeFile(t, root, "bazel-out/f.py", "")
	writeFile(t, root, "bazel-testlogs/g.py", "")
	writeFile(t, root, "myproject.egg-info/h.py", "")
	// Nested node_modules (inside a sub-project) also pruned by name match.
	writeFile(t, root, "apps/web/node_modules/pkg/i.py", "")

	results, err := WalkGraph(root, GraphWalkConfig{}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"apps/web/main.go"}
	if !equalSlices(got, want) {
		t.Errorf("monorepo defaults not pruning:\n got=%v\nwant=%v", got, want)
	}
}

// TestWalkGraph_UserExcludePatterns checks cfg.ExcludePatterns stack on top
// of defaults without replacing them.
func TestWalkGraph_UserExcludePatterns(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "")
	writeFile(t, root, "generated/pb.go", "")
	writeFile(t, root, "scripts/tool.py", "")

	results, err := WalkGraph(root, GraphWalkConfig{
		ExcludePatterns: []string{"generated/**"},
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"main.go", "scripts/tool.py"}
	if !equalSlices(got, want) {
		t.Errorf("ExcludePatterns: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_Roots restricts the walk to subdirectories of the project.
func TestWalkGraph_Roots(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "apps/web/main.go", "")
	writeFile(t, root, "apps/mobile/main.go", "")
	writeFile(t, root, "services/auth/svc.go", "")

	results, err := WalkGraph(root, GraphWalkConfig{
		Roots: []string{"apps/web", "services/auth"},
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"apps/web/main.go", "services/auth/svc.go"}
	if !equalSlices(got, want) {
		t.Errorf("Roots scoping: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_Gitignore checks that HonorGitignore filters files matched
// by a root .gitignore rule. The layered-gitignore semantics are tested
// elsewhere in gitignore_test.go; this just confirms WalkGraph plumbs the
// matcher in correctly.
func TestWalkGraph_Gitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.gen.go\ntemp/\n")
	writeFile(t, root, "main.go", "")
	writeFile(t, root, "zz.gen.go", "")
	writeFile(t, root, "temp/scratch.py", "")

	results, err := WalkGraph(root, GraphWalkConfig{HonorGitignore: true}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"main.go"}
	if !equalSlices(got, want) {
		t.Errorf("gitignore filtering: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_SkipFormats pins that handlers in cfg.SkipFormats are
// silently bypassed — used by the real graph pass to skip markdown / docx /
// pdf that the docs pass already covered.
func TestWalkGraph_SkipFormats(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "")
	writeFile(t, root, "docs/guide.md", "")
	writeFile(t, root, "notes.md", "")

	results, err := WalkGraph(root, GraphWalkConfig{
		SkipFormats: map[string]bool{"markdown": true},
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	got := paths(results)
	want := []string{"main.go"}
	if !equalSlices(got, want) {
		t.Errorf("SkipFormats: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_MissingRootsSkipsWithoutError pins the UX contract for a
// misconfigured or not-yet-checked-out graph.roots entry: the walker logs
// a warning and continues with whatever roots DO exist, rather than
// failing the entire graph pass.
func TestWalkGraph_MissingRootsSkipsWithoutError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "apps/web/main.go", "package main\n")

	results, err := WalkGraph(root, GraphWalkConfig{
		Roots: []string{"apps/web", "services/typo-nonexistent"},
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph should tolerate missing root, got error: %v", err)
	}
	got := paths(results)
	want := []string{"apps/web/main.go"}
	if !equalSlices(got, want) {
		t.Errorf("missing root should be skipped silently: got=%v want=%v", got, want)
	}
}

// TestWalkGraph_HardExcludeWinsOverUserPattern — even if the user tries to
// include .librarian via some empty/permissive override in the future
// (PR 2's include_patterns), the hard-exclude guard must still fire.
func TestWalkGraph_HardExcludeWinsOverUserPattern(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "main.go", "")
	writeFile(t, root, ".librarian/config.yaml", "docs_dir: docs\n")
	writeFile(t, root, ".librarian/rules.md", "body\n")

	// Future-proof: regardless of ExcludePatterns contents (even empty),
	// .librarian stays pruned. This test will need amending when
	// include_patterns lands in PR 2 to assert that an include override
	// for .librarian is also ignored.
	results, err := WalkGraph(root, GraphWalkConfig{
		ExcludePatterns: nil,
	}, walkerFixtureRegistry())
	if err != nil {
		t.Fatalf("WalkGraph: %v", err)
	}

	for _, r := range results {
		if filepath.ToSlash(r.FilePath) == ".librarian/config.yaml" {
			t.Errorf(".librarian must be hard-excluded")
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
