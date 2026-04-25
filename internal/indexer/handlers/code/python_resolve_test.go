package code

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/indexer"
)

func TestResolveRelativeTarget(t *testing.T) {
	tests := []struct {
		name   string
		anchor []string
		raw    string
		want   string
	}{
		{
			name:   "from_dot",
			anchor: []string{"mypkg", "sub"},
			raw:    ".utils",
			want:   "mypkg.sub.utils",
		},
		{
			name:   "from_dot_with_module",
			anchor: []string{"mypkg", "sub"},
			raw:    ".utils.X",
			want:   "mypkg.sub.utils.X",
		},
		{
			name:   "from_dot_dot",
			anchor: []string{"mypkg", "sub"},
			raw:    "..sibling",
			want:   "mypkg.sibling",
		},
		{
			name:   "from_dot_dot_with_module",
			anchor: []string{"mypkg", "sub"},
			raw:    "..pkg.Thing",
			want:   "mypkg.pkg.Thing",
		},
		{
			name:   "wildcard",
			anchor: []string{"mypkg", "sub"},
			raw:    ".*",
			want:   "mypkg.sub.*",
		},
		{
			name:   "wildcard_from_module",
			anchor: []string{"mypkg", "sub"},
			raw:    ".pkg.*",
			want:   "mypkg.sub.pkg.*",
		},
		{
			name:   "over_dotted_clamps_to_empty",
			anchor: []string{"mypkg", "sub"},
			raw:    "....x",
			want:   "x",
		},
		{
			name:   "absolute_pass_through",
			anchor: []string{"mypkg"},
			raw:    "collections.deque",
			want:   "collections.deque",
		},
		{
			name:   "empty_anchor_single_dot",
			anchor: nil,
			raw:    ".utils",
			want:   "utils",
		},
		{
			// Degenerate: bare "." with no anchor and no tail. Grammar
			// doesn't emit this today (from-imports always carry a name),
			// but the resolver must not return "" — empty Target would
			// violate the Reference invariant. Pass the raw form through so
			// the downstream dot-check fires loudly rather than silently.
			name:   "bare_dot_with_empty_anchor",
			anchor: nil,
			raw:    ".",
			want:   ".",
		},
		{
			name:   "bare_double_dot_with_empty_anchor",
			anchor: nil,
			raw:    "..",
			want:   "..",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveRelativeTarget(tc.anchor, tc.raw)
			if got != tc.want {
				t.Errorf("resolveRelativeTarget(%v, %q) = %q, want %q", tc.anchor, tc.raw, got, tc.want)
			}
		})
	}
}

func TestIsValidPyIdentifier(t *testing.T) {
	valid := []string{"a", "_", "A", "foo_bar", "_private", "utils", "abc123"}
	invalid := []string{"", "1abc", "my-app", "foo.bar", "class", "if", "import", "None", "True", "a b"}
	for _, s := range valid {
		if !isValidPyIdentifier(s) {
			t.Errorf("isValidPyIdentifier(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if isValidPyIdentifier(s) {
			t.Errorf("isValidPyIdentifier(%q) = true, want false", s)
		}
	}
}

func TestVirtualPackageFromRelDir(t *testing.T) {
	tests := []struct {
		relDir string
		want   []string
		ok     bool
	}{
		{relDir: "scripts", want: []string{"scripts"}, ok: true},
		{relDir: "pkg/sub", want: []string{"pkg", "sub"}, ok: true},
		{relDir: ".", want: nil, ok: true},
		{relDir: "", want: nil, ok: true},
		{relDir: "my-app", want: nil, ok: false},         // hyphen
		{relDir: "pkg/1bad", want: nil, ok: false},       // leading digit
		{relDir: "pkg/class", want: nil, ok: false},      // keyword
		{relDir: "pkg/sub/a b", want: nil, ok: false},    // whitespace
	}
	for _, tc := range tests {
		t.Run(tc.relDir, func(t *testing.T) {
			got, ok := virtualPackageFromRelDir(tc.relDir)
			if ok != tc.ok {
				t.Errorf("virtualPackageFromRelDir(%q) ok = %v, want %v", tc.relDir, ok, tc.ok)
			}
			if !equalStrings(got, tc.want) {
				t.Errorf("virtualPackageFromRelDir(%q) = %v, want %v", tc.relDir, got, tc.want)
			}
		})
	}
}

func TestPackageFromInitWalk_OnDisk(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// Fixture 1: contiguous __init__.py chain.
	write("mypkg/__init__.py")
	write("mypkg/sub/__init__.py")
	write("mypkg/sub/a.py")
	got := packageFromInitWalk(filepath.Join(root, "mypkg", "sub", "a.py"), nil)
	want := []string{"mypkg", "sub"}
	if !equalStrings(got, want) {
		t.Errorf("init chain: got %v, want %v", got, want)
	}

	// Fixture 2: src-layout — no src/__init__.py.
	write("src/app/__init__.py")
	write("src/app/b.py")
	got = packageFromInitWalk(filepath.Join(root, "src", "app", "b.py"), nil)
	want = []string{"app"}
	if !equalStrings(got, want) {
		t.Errorf("src layout: got %v, want %v", got, want)
	}

	// Fixture 3: no __init__.py anywhere.
	write("scripts/tool.py")
	got = packageFromInitWalk(filepath.Join(root, "scripts", "tool.py"), nil)
	if len(got) != 0 {
		t.Errorf("no init: got %v, want empty", got)
	}
}

func TestPackageFromSrcRoot(t *testing.T) {
	roots := []string{"/proj/src", "/proj/libs"}

	tests := []struct {
		name    string
		absPath string
		want    []string
		ok      bool
	}{
		{
			name:    "nested_under_src",
			absPath: "/proj/src/mypkg/sub/a.py",
			want:    []string{"mypkg", "sub"},
			ok:      true,
		},
		{
			name:    "top_level_under_src",
			absPath: "/proj/src/mypkg/a.py",
			want:    []string{"mypkg"},
			ok:      true,
		},
		{
			name:    "directly_in_src",
			absPath: "/proj/src/a.py",
			want:    nil,
			ok:      true,
		},
		{
			name:    "different_root",
			absPath: "/proj/libs/other/x.py",
			want:    []string{"other"},
			ok:      true,
		},
		{
			name:    "outside_any_root",
			absPath: "/proj/scripts/tool.py",
			want:    nil,
			ok:      false,
		},
		{
			name:    "invalid_identifier_in_path",
			absPath: "/proj/src/my-app/tool.py",
			want:    nil,
			ok:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := packageFromSrcRoot(tc.absPath, roots)
			if ok != tc.ok {
				t.Errorf("ok = %v, want %v", ok, tc.ok)
			}
			if !equalStrings(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestContainingPackage_PriorityOrder(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// src_roots should win over __init__.py walk even when both are present.
	write("src/ns/__init__.py")
	write("src/ns/deep/__init__.py")
	write("src/ns/deep/a.py")
	absA := filepath.Join(root, "src", "ns", "deep", "a.py")
	srcRoots := []string{filepath.Join(root, "src")}
	got := containingPackage(absA, root, srcRoots, nil)
	want := []string{"ns", "deep"}
	if !equalStrings(got, want) {
		t.Errorf("src_roots priority: got %v, want %v", got, want)
	}

	// Without src_roots, the __init__.py walk yields the same result.
	got = containingPackage(absA, root, nil, nil)
	if !equalStrings(got, want) {
		t.Errorf("__init__.py walk: got %v, want %v", got, want)
	}

	// No __init__.py, no src_roots match → virtual package from relative dir.
	write("scripts/tool.py")
	absTool := filepath.Join(root, "scripts", "tool.py")
	got = containingPackage(absTool, root, srcRoots, nil)
	if !equalStrings(got, []string{"scripts"}) {
		t.Errorf("virtual fallback: got %v, want [scripts]", got)
	}

	// Invalid identifier in virtual fallback → nil.
	write("my-app/tool.py")
	absBadTool := filepath.Join(root, "my-app", "tool.py")
	got = containingPackage(absBadTool, root, srcRoots, nil)
	if got != nil {
		t.Errorf("invalid virtual fallback: got %v, want nil", got)
	}

	// Root-level script: virtual fallback yields empty parts.
	write("tool.py")
	absRootTool := filepath.Join(root, "tool.py")
	got = containingPackage(absRootTool, root, srcRoots, nil)
	if len(got) != 0 {
		t.Errorf("root-level script: got %v, want empty", got)
	}
}

func TestContainingPackage_SrcRootsBypassInitRequirement(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// PEP 420 namespace package: no __init__.py anywhere, but src_roots
	// gives us a clean anchor.
	write("src/ns/deep/a.py")
	absA := filepath.Join(root, "src", "ns", "deep", "a.py")
	srcRoots := []string{filepath.Join(root, "src")}
	got := containingPackage(absA, root, srcRoots, nil)
	want := []string{"ns", "deep"}
	if !equalStrings(got, want) {
		t.Errorf("pep420 via src_roots: got %v, want %v", got, want)
	}
}

// TestContainingPackage_InjectedStatFn verifies the statFn injection point
// works for pure tests — no filesystem writes required.
func TestContainingPackage_InjectedStatFn(t *testing.T) {
	inits := map[string]bool{
		"/proj/mypkg/__init__.py":     true,
		"/proj/mypkg/sub/__init__.py": true,
	}
	stat := func(p string) error {
		if inits[p] {
			return nil
		}
		return errors.New("not found")
	}
	got := packageFromInitWalk("/proj/mypkg/sub/a.py", stat)
	want := []string{"mypkg", "sub"}
	if !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestResolveImports_NoAbsPathIsNoop pins the backwards-compat guarantee:
// when ctx.AbsPath is empty (legacy Parse callers), targets are returned
// verbatim. The full resolution path is exercised by
// TestPythonGrammar_ResolvesRelativeImportsViaParseCtx in python_test.go.
func TestResolveImports_NoAbsPathIsNoop(t *testing.T) {
	g := &PythonGrammar{}
	in := []indexer.Reference{
		{Kind: "import", Target: ".utils"},
		{Kind: "import", Target: "..pkg.Thing"},
		{Kind: "import", Target: "collections.deque"},
	}
	out := g.ResolveImports(in, "a.py", indexer.ParseContext{})
	for i := range in {
		if out[i].Target != in[i].Target {
			t.Errorf("ResolveImports mutated target without AbsPath: %q → %q", in[i].Target, out[i].Target)
		}
	}
}

func TestResolveImports_NonImportRefsPassThrough(t *testing.T) {
	g := &PythonGrammar{}
	in := []indexer.Reference{
		{Kind: "call", Target: ".something"}, // synthetic; call refs aren't resolved
	}
	out := g.ResolveImports(in, "mypkg/a.py", indexer.ParseContext{AbsPath: "/proj/mypkg/a.py"})
	if out[0].Target != ".something" {
		t.Errorf("non-import ref got mutated: %q", out[0].Target)
	}
}

func TestInferProjectRoot(t *testing.T) {
	tests := []struct {
		abs, rel, want string
	}{
		{"/proj/pkg/a.py", "pkg/a.py", "/proj"},
		{"/proj/a.py", "a.py", "/proj"},
		{"/proj/deep/nested/b.py", "deep/nested/b.py", "/proj"},
		{"/x/a.py", "b.py", ""},
	}
	for _, tc := range tests {
		got := inferProjectRoot(tc.abs, tc.rel)
		if filepath.ToSlash(got) != tc.want {
			t.Errorf("inferProjectRoot(%q, %q) = %q, want %q", tc.abs, tc.rel, got, tc.want)
		}
	}
}

// equalStrings compares string slices, treating nil and [] as equal.
func equalStrings(a, b []string) bool {
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
