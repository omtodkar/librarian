package jsresolve

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeFS builds a statFn over a fixed set of paths (behaviour-preserving
// mirror of the helper in handlers/code).
func fakeFS(existing ...string) func(string) bool {
	have := map[string]bool{}
	for _, p := range existing {
		have[filepath.Clean(p)] = true
	}
	return func(p string) bool {
		return have[filepath.Clean(p)]
	}
}

func TestResolve_RelativeBareResolvesToTS(t *testing.T) {
	got, ok := Resolve("./utils", "/proj/src/a.ts", fakeFS("/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v), want (/proj/src/utils.ts, true)", got, ok)
	}
}

func TestResolve_PrefersTSOverJS(t *testing.T) {
	got, ok := Resolve("./utils", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.js", "/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v); .ts should win over .js", got, ok)
	}
}

func TestResolve_TSXFallback(t *testing.T) {
	got, ok := Resolve("./Button", "/proj/src/a.tsx",
		fakeFS("/proj/src/Button.tsx"))
	if !ok || got != "/proj/src/Button.tsx" {
		t.Errorf("got (%q, %v), want /proj/src/Button.tsx", got, ok)
	}
}

func TestResolve_IndexResolution(t *testing.T) {
	got, ok := Resolve("./utils", "/proj/src/a.ts",
		fakeFS("/proj/src/utils/index.ts"))
	if !ok || got != "/proj/src/utils/index.ts" {
		t.Errorf("got (%q, %v), want directory index resolution", got, ok)
	}
}

func TestResolve_ExplicitJSRewritesToTS(t *testing.T) {
	got, ok := Resolve("./utils.js", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v); .js-suffixed import should rewrite to .ts when only .ts exists", got, ok)
	}
}

func TestResolve_ExplicitJSStaysJSWhenNoTS(t *testing.T) {
	got, ok := Resolve("./utils.js", "/proj/src/a.js",
		fakeFS("/proj/src/utils.js"))
	if !ok || got != "/proj/src/utils.js" {
		t.Errorf("got (%q, %v); pure-JS project should resolve .js literally", got, ok)
	}
}

func TestResolve_ParentDirectory(t *testing.T) {
	got, ok := Resolve("../lib/foo", "/proj/src/a.ts",
		fakeFS("/proj/lib/foo.tsx"))
	if !ok || got != "/proj/lib/foo.tsx" {
		t.Errorf("got (%q, %v), want /proj/lib/foo.tsx (parent-dir resolution)", got, ok)
	}
}

func TestResolve_NoCandidateReturnsFalse(t *testing.T) {
	_, ok := Resolve("./missing", "/proj/src/a.ts", fakeFS())
	if ok {
		t.Error("expected no resolution for missing file")
	}
}

func TestResolve_MJSResolvesToMTS(t *testing.T) {
	got, ok := Resolve("./utils.mjs", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.mts"))
	if !ok || got != "/proj/src/utils.mts" {
		t.Errorf("got (%q, %v), want .mts rewrite for .mjs import", got, ok)
	}
}

func TestResolve_DirectoryCandidateRejectedByStatFn(t *testing.T) {
	stat := func(p string) bool {
		return p == "/proj/src/utils/index.ts"
	}
	got, ok := Resolve("./utils", "/proj/src/a.ts", stat)
	if !ok || got != "/proj/src/utils/index.ts" {
		t.Errorf("got (%q, %v); directory candidate must not match, index.ts should win", got, ok)
	}
}

func TestResolve_TSExplicitFallsBackToTSXSibling(t *testing.T) {
	got, ok := Resolve("./Button.ts", "/proj/src/a.ts",
		fakeFS("/proj/src/Button.tsx"))
	if !ok || got != "/proj/src/Button.tsx" {
		t.Errorf("got (%q, %v); .ts-suffixed import should fall through to .tsx sibling", got, ok)
	}
}

func TestStatIsFile_RejectsDirectories(t *testing.T) {
	dir := t.TempDir()
	if StatIsFile(dir) {
		t.Errorf("StatIsFile returned true for a directory %q", dir)
	}
}

func TestStatIsFile_AcceptsRegularFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "utils.ts")
	if err := os.WriteFile(f, []byte("export const x = 1"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !StatIsFile(f) {
		t.Errorf("StatIsFile returned false for a regular file %q", f)
	}
}

func TestStatIsFile_ReturnsFalseForMissing(t *testing.T) {
	if StatIsFile("/tmp/this_path_should_not_exist_librarian_test.ts") {
		t.Error("StatIsFile returned true for a non-existent path")
	}
}
