package pdf

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// loadFixture reads a committed fixture PDF. Panics on failure because
// tests are useless without their inputs — prefer a loud early exit over
// t.Fatal scattered through every subtest.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if len(b) == 0 {
		t.Fatalf("fixture %s is empty — re-run testdata/generate.py", name)
	}
	return b
}

// The flat fallback is the always-viable last resort; plain.pdf has no
// outline and no struct tree, so the cascade must land here.
func TestConvert_Plain_FallsThroughToPages(t *testing.T) {
	md, source, err := convertPDF(loadFixture(t, "plain.pdf"), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPDF: %v", err)
	}
	if source != sourcePages {
		t.Errorf("structure source = %q, want %q", source, sourcePages)
	}
	s := string(md)
	if !strings.Contains(s, "## Page 1") {
		t.Errorf("missing page heading:\n%s", s)
	}
	if !strings.Contains(s, "Hello world") {
		t.Errorf("missing body text:\n%s", s)
	}
}

// The MaxPages cap clips the output before emission, so the flat
// fallback must reflect it — tier-4 is where the cap is user-visible.
func TestConvert_Multipage_MaxPagesCap(t *testing.T) {
	md, source, err := convertPDF(loadFixture(t, "multipage.pdf"), Config{MaxPages: 2})
	if err != nil {
		t.Fatalf("convertPDF: %v", err)
	}
	if source != sourcePages {
		t.Errorf("structure source = %q, want %q", source, sourcePages)
	}
	s := string(md)
	if !strings.Contains(s, "## Page 1") || !strings.Contains(s, "## Page 2") {
		t.Errorf("expected first two pages, got:\n%s", s)
	}
	if strings.Contains(s, "## Page 3") {
		t.Errorf("MaxPages=2 should have excluded page 3:\n%s", s)
	}
}

// Unlimited (MaxPages=0) must emit all five pages.
func TestConvert_Multipage_Unlimited(t *testing.T) {
	md, _, err := convertPDF(loadFixture(t, "multipage.pdf"), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPDF: %v", err)
	}
	for i := 1; i <= 5; i++ {
		needle := "## Page " + strconv.Itoa(i)
		if !strings.Contains(string(md), needle) {
			t.Errorf("missing %q:\n%s", needle, md)
		}
	}
}

// bookmarks.pdf has a 3-entry outline with one nested bookmark. Tier 2
// must win over the tier-4 fallback; bookmark titles must appear as H2/H3.
func TestConvert_Bookmarks_ProducesOutlineStructure(t *testing.T) {
	md, source, err := convertPDF(loadFixture(t, "bookmarks.pdf"), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPDF: %v", err)
	}
	if source != sourceBookmarks {
		t.Errorf("structure source = %q, want %q", source, sourceBookmarks)
	}
	s := string(md)
	for _, want := range []string{"## Introduction", "## Methods", "## Results"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing bookmark heading %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, "### Sub-method") {
		t.Errorf("nested bookmark missing:\n%s", s)
	}
	// Body text from at least one page should land under its bookmark.
	if !strings.Contains(s, "Alpha") && !strings.Contains(s, "Bravo") && !strings.Contains(s, "Charlie") {
		t.Errorf("bookmark body text missing:\n%s", s)
	}
}

// tagged.pdf carries heading-sized fonts. reportlab doesn't emit a real
// StructTreeRoot, so the cascade lands on tier 3 (heuristic). Falling
// through to tier 4 (sourcePages) would mean heading detection regressed,
// so we pin against that — stricter than the previous "any source is OK"
// assertion.
func TestConvert_Tagged_ExtractsHeadings(t *testing.T) {
	md, source, err := convertPDF(loadFixture(t, "tagged.pdf"), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPDF: %v", err)
	}
	if source == sourcePages {
		t.Errorf("tagged.pdf fell through to flat-page fallback — heading detection regressed")
	}
	s := string(md)
	if !strings.Contains(s, "Document Title") {
		t.Errorf("tagged heading missing:\n%s", s)
	}
	if !strings.Contains(s, "Overview") {
		t.Errorf("H2 missing:\n%s", s)
	}
}

// Malformed bytes must error, not panic. Regression guard for any tier
// that naively trusts PDFium's response shape.
func TestConvert_MalformedBytes(t *testing.T) {
	_, _, err := convertPDF([]byte("not a pdf at all"), DefaultConfig())
	if err == nil {
		t.Error("expected error for non-pdf bytes")
	}
}

// Empty input returns empty markdown and the pages source label (rather
// than failing) because an empty byte slice is distinguishable from a
// malformed one — callers can skip without error handling.
func TestConvert_EmptyBytes(t *testing.T) {
	md, source, err := convertPDF(nil, DefaultConfig())
	if err != nil {
		t.Fatalf("convertPDF(nil): %v", err)
	}
	if len(md) != 0 {
		t.Errorf("expected empty markdown, got %q", md)
	}
	if source != sourcePages {
		t.Errorf("source = %q, want %q", source, sourcePages)
	}
}

// Shutdown must be idempotent — cmd/index.go defers it, and a re-entry
// during error paths could fire it twice.
func TestShutdown_Idempotent(t *testing.T) {
	if err := Shutdown(); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	if err := Shutdown(); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}
}

