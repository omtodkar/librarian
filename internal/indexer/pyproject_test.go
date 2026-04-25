package indexer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"librarian/internal/config"
)

// writePyproject seeds <root>/pyproject.toml with the given body; helper used
// by the detection tests that don't need a full fixture tree.
func writePyproject(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
}

func TestDetectPythonSrcRoots_Setuptools(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools.packages.find]
where = ["src"]
`)
	got, err := detectPythonSrcRootsFromPyproject(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectPythonSrcRoots_SetuptoolsMultipleWhere(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools.packages.find]
where = ["src", "libs"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src"), filepath.Join(root, "libs")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestDetectPythonSrcRoots_SetuptoolsPackageDir covers the pre-find
// [tool.setuptools] package-dir = {"" = "src"} idiom, which is still the
// dominant way to declare a src-layout for projects that haven't moved to
// packages.find. Non-empty keys in package-dir map sub-packages and must
// not be treated as src_roots.
func TestDetectPythonSrcRoots_SetuptoolsPackageDir(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools]
package-dir = {"" = "src", "mypkg.sub" = "lib/sub"}
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestDetectPythonSrcRoots_HatchSdistOnly covers projects that publish
// source-only (no wheel target). Same `packages = ["src/foo"]` shape under
// [tool.hatch.build.targets.sdist] must be detected.
func TestDetectPythonSrcRoots_HatchSdistOnly(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.hatch.build.targets.sdist]
packages = ["src/foo"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A project with both wheel and sdist declaring the same packages should
// see the src_root listed once (dedup via cleaned-path key).
func TestDetectPythonSrcRoots_HatchWheelAndSdistDeduped(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.hatch.build.targets.wheel]
packages = ["src/foo"]

[tool.hatch.build.targets.sdist]
packages = ["src/foo"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectPythonSrcRoots_Poetry(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[[tool.poetry.packages]]
include = "foo"
from = "src"
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Packages with `include` only (no `from`) are root-layout — the
// __init__.py walk handles them, so auto-detect must NOT synthesize a
// src_root pointing at the project root.
func TestDetectPythonSrcRoots_PoetryNoFromDropped(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[[tool.poetry.packages]]
include = "foo"
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

// Hatch packages = ["src/foo", "src/bar"] should collapse to a single
// src/ root — both packages sit under the same parent.
func TestDetectPythonSrcRoots_HatchDedupesParents(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.hatch.build.targets.wheel]
packages = ["src/foo", "src/bar"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Hatch packages = ["mypkg"] — a root-layout package — should be dropped,
// same rationale as the Poetry no-`from` case.
func TestDetectPythonSrcRoots_HatchRootLayoutDropped(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.hatch.build.targets.wheel]
packages = ["mypkg"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestDetectPythonSrcRoots_CombinedSections(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools.packages.find]
where = ["src"]

[tool.hatch.build.targets.wheel]
packages = ["libs/x"]
`)
	got, _ := detectPythonSrcRootsFromPyproject(root)
	want := []string{filepath.Join(root, "src"), filepath.Join(root, "libs")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDetectPythonSrcRoots_MissingFile(t *testing.T) {
	root := t.TempDir()
	got, err := detectPythonSrcRootsFromPyproject(root)
	if err != nil {
		t.Errorf("expected no error for missing pyproject.toml, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestDetectPythonSrcRoots_Malformed(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, "[tool.setuptools\nbroken = ]")
	got, err := detectPythonSrcRootsFromPyproject(root)
	if err == nil {
		t.Error("expected parse error on malformed TOML")
	}
	if got != nil {
		t.Errorf("expected nil on parse error, got %v", got)
	}
}

// pyproject.toml parses cleanly but has no recognised tool tables (common
// when only [project] metadata is declared).
func TestDetectPythonSrcRoots_UnrecognizedSections(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[project]
name = "demo"
version = "0.1.0"
`)
	got, err := detectPythonSrcRootsFromPyproject(root)
	if err != nil {
		t.Errorf("unexpected parse error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for no-tool-tables, got %v", got)
	}
}

func TestResolvePythonSrcRoots_MergesExplicitAndAutoDetect(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools.packages.find]
where = ["src"]
`)
	cfg := &config.Config{
		ProjectRoot: root,
		Python:      config.PythonConfig{SrcRoots: []string{"scripts"}},
	}
	got := resolvePythonSrcRoots(cfg)
	// Explicit entries first, then auto-detected.
	want := []string{filepath.Join(root, "scripts"), filepath.Join(root, "src")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolvePythonSrcRoots_DedupesConfigAndAutoDetect(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, `
[tool.setuptools.packages.find]
where = ["src"]
`)
	cfg := &config.Config{
		ProjectRoot: root,
		Python:      config.PythonConfig{SrcRoots: []string{"src"}},
	}
	got := resolvePythonSrcRoots(cfg)
	if len(got) != 1 || got[0] != filepath.Join(root, "src") {
		t.Errorf("expected single src entry, got %v", got)
	}
}

func TestResolvePythonSrcRoots_MalformedDoesNotSuppressExplicit(t *testing.T) {
	root := t.TempDir()
	writePyproject(t, root, "[tool.setuptools\nbroken")
	cfg := &config.Config{
		ProjectRoot: root,
		Python:      config.PythonConfig{SrcRoots: []string{"scripts"}},
	}
	got := resolvePythonSrcRoots(cfg)
	want := []string{filepath.Join(root, "scripts")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v — malformed pyproject shouldn't eat explicit config", got, want)
	}
}

// TestResolvePythonSrcRoots_EmptyRootReturnsNil guards the "no workspace"
// branch (some grammar-level tests use a config with empty ProjectRoot).
func TestResolvePythonSrcRoots_EmptyRootReturnsNil(t *testing.T) {
	cfg := &config.Config{
		Python: config.PythonConfig{SrcRoots: []string{"src"}},
	}
	if got := resolvePythonSrcRoots(cfg); got != nil {
		t.Errorf("expected nil for empty ProjectRoot, got %v", got)
	}
}

// Explicit absolute paths in config should pass through unchanged (not
// re-joined onto ProjectRoot).
func TestResolvePythonSrcRoots_AbsoluteConfigPassThrough(t *testing.T) {
	root := t.TempDir()
	absSrc := filepath.Join(root, "external_src")
	cfg := &config.Config{
		ProjectRoot: root,
		Python:      config.PythonConfig{SrcRoots: []string{absSrc}},
	}
	got := resolvePythonSrcRoots(cfg)
	if len(got) != 1 || got[0] != absSrc {
		t.Errorf("absolute path not preserved: got %v, want [%s]", got, absSrc)
	}
	// Sanity: the test string filepath.Clean'd against itself is still absSrc.
	if !strings.HasPrefix(got[0], string(filepath.Separator)) && !filepath.IsAbs(got[0]) {
		t.Errorf("result %q should be absolute", got[0])
	}
}
