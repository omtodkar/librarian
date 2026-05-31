package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// TestLoad_AnalyticsCommunityTimeout_Default exercises the lib-7gyz default:
// when no user config sets analytics.community_timeout, Load() must return
// the 60s production default (NOT 0, which would mean "disabled" / legacy
// unbounded Louvain — exactly the bug Phase 2 is fixing).
func TestLoad_AnalyticsCommunityTimeout_Default(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 60*time.Second {
		t.Errorf("default CommunityTimeout = %s, want 60s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_UserOverride verifies the user can
// shorten the bound via config. mapstructure decodes Go duration strings
// natively (e.g. "5s" → 5 * time.Second).
func TestLoad_AnalyticsCommunityTimeout_UserOverride(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  community_timeout: 5s\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 5*time.Second {
		t.Errorf("user-overridden CommunityTimeout = %s, want 5s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_ExplicitDisable verifies the explicit
// "0" sentinel for disabling the bound survives Load(). This is the escape
// hatch for users who genuinely want to wait indefinitely; the IsSet guard
// must NOT clobber it back to the 60s default.
func TestLoad_AnalyticsCommunityTimeout_ExplicitDisable(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  community_timeout: 0\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 0 {
		t.Errorf("explicit 0 should disable the bound; got %s", cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_AnalyticsCommunityTimeout_PartialAnalyticsBlock guards the IsSet
// edge case: a user has an `analytics:` section in their config with
// future-keys-we-don't-have-yet but does NOT set community_timeout. Without
// the IsSet guard, mapstructure would Unmarshal the missing duration as 0
// and silently disable the bound. With the guard, the 60s default survives.
func TestLoad_AnalyticsCommunityTimeout_PartialAnalyticsBlock(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	// Hypothetical future key under analytics: that doesn't shadow our default.
	if err := viper.ReadConfig(strings.NewReader("analytics:\n  some_future_key: foo\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Analytics.CommunityTimeout != 60*time.Second {
		t.Errorf("partial analytics block clobbered default: got %s, want 60s",
			cfg.Analytics.CommunityTimeout)
	}
}

// TestLoad_GraphEnabled_Default verifies graph.enabled defaults to true so the
// graph pass keeps running for users who never set the key.
func TestLoad_GraphEnabled_Default(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	cfg := Load()
	if !cfg.Graph.Enabled {
		t.Errorf("default Graph.Enabled = false, want true")
	}
}

// TestLoad_GraphEnabled_ExplicitDisable verifies a docs-only workspace can
// turn the graph pass off persistently. The IsSet guard must NOT clobber an
// explicit `false` back to the true default.
func TestLoad_GraphEnabled_ExplicitDisable(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("graph:\n  enabled: false\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if cfg.Graph.Enabled {
		t.Errorf("explicit graph.enabled: false should disable the graph pass; got true")
	}
}

// TestLoad_GraphEnabled_PartialGraphBlock guards the IsSet edge case: a user
// has a `graph:` section that sets some other key but omits `enabled`. Without
// the guard, mapstructure would decode the missing bool as false and silently
// disable the graph pass; with it, the true default survives.
func TestLoad_GraphEnabled_PartialGraphBlock(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	if err := viper.ReadConfig(strings.NewReader("graph:\n  honor_gitignore: false\n")); err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}

	cfg := Load()
	if !cfg.Graph.Enabled {
		t.Errorf("partial graph block clobbered default: Graph.Enabled = false, want true")
	}
}

// ---------------------------------------------------------------------------
// ResolvedDocDirs — precedence, cleaning, and de-duplication
// ---------------------------------------------------------------------------

// TestResolvedDocDirs_DocDirsWinsOverDocsDir verifies that when both DocDirs
// and DocsDir are set, DocDirs takes precedence and DocsDir is silently
// ignored. This implements the §3.1 alias-precedence rule.
func TestResolvedDocDirs_DocDirsWinsOverDocsDir(t *testing.T) {
	cfg := &Config{
		DocsDir: "old-docs",
		DocDirs: []string{"proto/docs", "astro-engine/docs"},
	}
	got := cfg.ResolvedDocDirs()
	want := []string{"proto/docs", "astro-engine/docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// TestResolvedDocDirs_DocsDirOnlyYieldsSingleton verifies that when only the
// deprecated DocsDir is set (DocDirs is empty), the result is a single-entry
// slice — preserving full backward compatibility with pre-multi-dir configs.
func TestResolvedDocDirs_DocsDirOnlyYieldsSingleton(t *testing.T) {
	cfg := &Config{DocsDir: "docs"}
	got := cfg.ResolvedDocDirs()
	want := []string{"docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// TestResolvedDocDirs_BothEmptyFallsBackToDefault verifies the fallback to
// ["docs"] when both DocDirs and DocsDir are unset/empty. This preserves the
// historic config.go:213 default so binaries without any config still work.
func TestResolvedDocDirs_BothEmptyFallsBackToDefault(t *testing.T) {
	cfg := &Config{}
	got := cfg.ResolvedDocDirs()
	want := []string{"docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// TestResolvedDocDirs_CleanAndDedupe verifies that trailing slashes, dot
// prefixes, and exact duplicates are all collapsed into a single cleaned entry.
// This guards the filesystem-I/O-free normalization step.
func TestResolvedDocDirs_CleanAndDedupe(t *testing.T) {
	cfg := &Config{
		DocDirs: []string{"docs/", "docs", "./docs"},
	}
	got := cfg.ResolvedDocDirs()
	want := []string{"docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v (expected dedupe+clean)", got, want)
	}
}

// TestResolvedDocDirs_BlankEntriesDropped verifies that empty and
// whitespace-only strings in DocDirs are silently discarded so a stray
// trailing comma in the YAML doesn't produce an empty dir entry.
func TestResolvedDocDirs_BlankEntriesDropped(t *testing.T) {
	cfg := &Config{
		DocDirs: []string{"", "  ", "proto/docs", ""},
	}
	got := cfg.ResolvedDocDirs()
	want := []string{"proto/docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// TestResolvedDocDirs_WhitespaceOnlyDropped verifies that a whitespace-only
// entry is dropped from the result, leaving only valid non-empty directories.
func TestResolvedDocDirs_WhitespaceOnlyDropped(t *testing.T) {
	cfg := &Config{
		DocDirs: []string{"docs", "   "},
	}
	got := cfg.ResolvedDocDirs()
	want := []string{"docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// TestResolvedDocDirs_PreservesFirstSeenOrder verifies that de-duplication
// retains the first occurrence and discards later duplicates, keeping the
// user's intended priority order stable.
func TestResolvedDocDirs_PreservesFirstSeenOrder(t *testing.T) {
	cfg := &Config{
		DocDirs: []string{"b/docs", "a/docs", "b/docs", "c/docs"},
	}
	got := cfg.ResolvedDocDirs()
	want := []string{"b/docs", "a/docs", "c/docs"}
	if !equalStringSlice(got, want) {
		t.Errorf("ResolvedDocDirs() = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// ValidateDocDirs — overlap rejection, containment, and edge cases
// ---------------------------------------------------------------------------

// TestValidateDocDirs_OverlapRejected verifies that a parent/child pair in
// doc_dirs is rejected. ["docs", "docs/api"] would cause file_path collisions
// in the documents table (both files land under a "docs" prefix) — this must
// fail fast at config validation rather than hit the UNIQUE constraint mid-index.
func TestValidateDocDirs_OverlapRejected(t *testing.T) {
	cfg := &Config{DocDirs: []string{"docs", "docs/api"}}
	if err := cfg.ValidateDocDirs(""); err == nil {
		t.Error("expected error for overlapping dirs [docs, docs/api], got nil")
	}
}

// TestValidateDocDirs_SiblingNamesNotFlagged is the critical false-positive
// guard: "docs" and "docs-api" share a string prefix but are NOT in an
// ancestor relationship — the segment-aware check must pass them as valid.
func TestValidateDocDirs_SiblingNamesNotFlagged(t *testing.T) {
	cfg := &Config{DocDirs: []string{"docs", "docs-api"}}
	if err := cfg.ValidateDocDirs(""); err != nil {
		t.Errorf("sibling dirs [docs, docs-api] must not be flagged as overlapping; got: %v", err)
	}
}

// TestValidateDocDirs_MultiSiblingOK verifies that a realistic polyrepo
// configuration with sibling sub-repo docs trees passes validation cleanly.
func TestValidateDocDirs_MultiSiblingOK(t *testing.T) {
	cfg := &Config{
		DocDirs: []string{"proto/docs", "astro-engine/docs", "k8s-deployments/docs"},
	}
	if err := cfg.ValidateDocDirs(""); err != nil {
		t.Errorf("non-overlapping multi-dir config must pass validation; got: %v", err)
	}
}

// TestValidateDocDirs_WithinRootOK verifies that dirs that genuinely resolve
// within the projectRoot pass the containment check without error.
func TestValidateDocDirs_WithinRootOK(t *testing.T) {
	root := t.TempDir()
	cfg := &Config{DocDirs: []string{"proto/docs", "astro-engine/docs"}}
	if err := cfg.ValidateDocDirs(root); err != nil {
		t.Errorf("dirs within projectRoot must pass containment check; got: %v", err)
	}
}

// TestValidateDocDirs_DotDotEscapeRejected verifies that a ".." traversal
// escaping the projectRoot is caught by the containment check. Such an entry
// would produce a stored file_path outside the workspace, breaking the
// ProjectRoot-relative path invariant.
func TestValidateDocDirs_DotDotEscapeRejected(t *testing.T) {
	root := t.TempDir()
	cfg := &Config{DocDirs: []string{"../sibling-repo/docs"}}
	if err := cfg.ValidateDocDirs(root); err == nil {
		t.Error("expected error for .. escape outside projectRoot, got nil")
	}
}

// TestValidateDocDirs_AbsoluteOutsideRootRejected verifies that an absolute
// path that does not live under projectRoot is rejected by the containment
// check. Absolute siblings violate the workspace-relative path convention
// established in §3.2 of the RFC.
func TestValidateDocDirs_AbsoluteOutsideRootRejected(t *testing.T) {
	root := t.TempDir()
	// Use a different temp dir as the absolute-outside path.
	outsideDir := filepath.Dir(root) // parent — definitely outside root
	cfg := &Config{DocDirs: []string{outsideDir}}
	if err := cfg.ValidateDocDirs(root); err == nil {
		t.Errorf("expected error for absolute dir outside projectRoot (%s not under %s), got nil", outsideDir, root)
	}
}

// TestValidateDocDirs_EmptyProjectRootSkipsContainment verifies that when
// projectRoot is "" (standalone/test flows), the containment check is skipped
// entirely — but the overlap check still runs. This preserves test-harness
// ergonomics: tests that don't have a real workspace root can still exercise
// ResolvedDocDirs without having to supply a fake root.
func TestValidateDocDirs_EmptyProjectRootSkipsContainment(t *testing.T) {
	// Overlap should still be caught even with empty root.
	cfg := &Config{DocDirs: []string{"docs", "docs/sub"}}
	if err := cfg.ValidateDocDirs(""); err == nil {
		t.Error("overlap must still be caught when projectRoot is empty")
	}

	// Non-overlapping dirs with empty root should be fine (no containment check).
	cfg2 := &Config{DocDirs: []string{"../anything", "some/other/dir"}}
	if err := cfg2.ValidateDocDirs(""); err != nil {
		t.Errorf("with empty projectRoot, containment must be skipped; got: %v", err)
	}
}

// TestValidateDocDirs_OverlapRejectedReverseOrder ensures the overlap check is
// symmetric: [docs/api, docs] (child first, parent second) must also error.
func TestValidateDocDirs_OverlapRejectedReverseOrder(t *testing.T) {
	cfg := &Config{DocDirs: []string{"docs/api", "docs"}}
	if err := cfg.ValidateDocDirs(""); err == nil {
		t.Error("expected error for overlapping dirs [docs/api, docs] (child first), got nil")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// equalStringSlice returns true if a and b have identical length and elements
// in the same order.
func equalStringSlice(a, b []string) bool {
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
