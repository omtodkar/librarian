package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDetectDocDirs verifies that detectDocDirs finds docs/ directories at
// root level and one level deep in sub-directories, while skipping noise dirs
// (hidden, node_modules, vendor, .git, .librarian, etc.).
func TestDetectDocDirs(t *testing.T) {
	tests := []struct {
		name  string
		setup func(root string) // creates dirs under root
		want  []string
	}{
		{
			name: "root_docs_only",
			setup: func(root string) {
				must(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
			},
			want: []string{"docs"},
		},
		{
			name: "sub_repo_docs_only",
			setup: func(root string) {
				must(t, os.MkdirAll(filepath.Join(root, "proto", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, "backend", "docs"), 0o755))
			},
			want: []string{
				filepath.Join("backend", "docs"),
				filepath.Join("proto", "docs"),
			},
		},
		{
			name: "root_and_sub_repo_docs",
			setup: func(root string) {
				must(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, "proto", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, "astro-engine", "docs"), 0o755))
			},
			want: []string{
				filepath.Join("astro-engine", "docs"),
				"docs",
				filepath.Join("proto", "docs"),
			},
		},
		{
			name: "skips_noise_dirs",
			setup: func(root string) {
				// These should be ignored.
				must(t, os.MkdirAll(filepath.Join(root, "node_modules", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, "vendor", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, ".git", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, ".librarian", "docs"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, ".hidden", "docs"), 0o755))
				// This one should be found.
				must(t, os.MkdirAll(filepath.Join(root, "services", "docs"), 0o755))
			},
			want: []string{filepath.Join("services", "docs")},
		},
		{
			name: "no_docs_dirs",
			setup: func(root string) {
				must(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
				must(t, os.MkdirAll(filepath.Join(root, "lib"), 0o755))
			},
			want: nil,
		},
		{
			name: "polyrepo_akaya_like",
			setup: func(root string) {
				for _, sub := range []string{
					"proto/docs",
					"astro-engine/docs",
					"postgres/docs",
					"iac-terraform-gcp/docs",
					"k8s-deployments/docs",
				} {
					must(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(sub)), 0o755))
				}
			},
			want: []string{
				filepath.Join("astro-engine", "docs"),
				filepath.Join("iac-terraform-gcp", "docs"),
				filepath.Join("k8s-deployments", "docs"),
				filepath.Join("postgres", "docs"),
				filepath.Join("proto", "docs"),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			tc.setup(root)

			got := detectDocDirs(root)

			// Normalize nil vs empty for comparison.
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("detectDocDirs(%q):\n  got  %v\n  want %v", root, got, tc.want)
			}
		})
	}
}

// TestBuildConfigYAML checks that buildConfigYAML produces a valid YAML block
// with the expected doc_dirs entries and that it preserves the rest of the
// template (embedding, chunking, graph, office, pdf, etc.).
func TestBuildConfigYAML(t *testing.T) {
	tests := []struct {
		name     string
		dirs     []string
		wantYAML []string // substrings that must appear
	}{
		{
			name: "single_default",
			dirs: []string{"docs"},
			wantYAML: []string{
				"doc_dirs:",
				`  - "docs"`,
				"embedding:",
				"chunking:",
				"graph:",
				"code_file_patterns:",
				"exclude_patterns:",
				// Office/PDF sections must survive template splits.
				"office:",
				"pdf:",
				"xlsx_max_rows:",
				"max_pages:",
			},
		},
		{
			name: "multi_dirs",
			dirs: []string{"docs", "proto/docs", "astro-engine/docs"},
			wantYAML: []string{
				"doc_dirs:",
				`  - "docs"`,
				`  - "proto/docs"`,
				`  - "astro-engine/docs"`,
				"embedding:",
				"chunking:",
				"office:",
				"pdf:",
				"xlsx_max_rows:",
				"max_pages:",
			},
		},
		{
			name: "yaml_special_chars",
			dirs: []string{`my:docs`, `back\slash`},
			wantYAML: []string{
				"doc_dirs:",
				// colon-containing dir must be quoted so YAML doesn't interpret it
				// as a mapping key.
				`  - "my:docs"`,
				// backslash must be escaped inside the double-quoted scalar.
				`  - "back\\slash"`,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildConfigYAML(tc.dirs)
			for _, want := range tc.wantYAML {
				if !strings.Contains(got, want) {
					t.Errorf("buildConfigYAML(%v): missing %q\nfull output:\n%s", tc.dirs, want, got)
				}
			}
			// Must NOT contain the old single-scalar form.
			if strings.Contains(got, "docs_dir:") {
				t.Errorf("buildConfigYAML should not contain deprecated docs_dir scalar; got:\n%s", got)
			}
		})
	}
}

// TestResolveDocDirs_ExplicitOverride verifies that explicit --doc-dir flags
// bypass auto-detection entirely.
func TestResolveDocDirs_ExplicitOverride(t *testing.T) {
	root := t.TempDir()
	// Plant a docs/ directory that would be detected if we auto-detected.
	must(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))

	explicit := []string{"custom/docs", "other/docs"}
	got := resolveDocDirs(root, explicit)

	if !reflect.DeepEqual(got, explicit) {
		t.Errorf("explicit override: got %v, want %v", got, explicit)
	}
}

// TestResolveDocDirs_ZeroDetected verifies that when no docs/ dirs are found
// and no --doc-dir flags given, we fall back to ["docs"].
func TestResolveDocDirs_ZeroDetected(t *testing.T) {
	root := t.TempDir()
	// No docs dirs planted.
	got := resolveDocDirs(root, nil)
	want := []string{"docs"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("zero detected: got %v, want %v", got, want)
	}
}

// TestResolveDocDirs_OneDetected verifies that when exactly one docs/ dir is
// found, it is returned directly without prompting (no TTY needed).
func TestResolveDocDirs_OneDetected(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))

	got := resolveDocDirs(root, nil)
	want := []string{"docs"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("one detected: got %v, want %v", got, want)
	}
}

// TestResolveDocDirs_MultiNonTTY verifies that in a non-TTY context (piped /
// CI) with multiple detected dirs, resolveDocDirs proceeds with all detected
// dirs without prompting. In a test environment stdin is never a real TTY, so
// this exercises the non-interactive path automatically.
func TestResolveDocDirs_MultiNonTTY(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "proto", "docs"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "backend", "docs"), 0o755))

	// In test runs stdin is a pipe, not a TTY — resolveDocDirs should return
	// all three without blocking on a prompt.
	got := resolveDocDirs(root, nil)
	want := []string{
		filepath.Join("backend", "docs"),
		"docs",
		filepath.Join("proto", "docs"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("multi non-TTY: got %v, want %v", got, want)
	}
}

// must is a tiny test helper that fatals on os.MkdirAll errors.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
