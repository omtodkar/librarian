package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

func parseBlock(t *testing.T, md string) (ast.Node, []byte) {
	t.Helper()
	source := []byte(md)
	reader := text.NewReader(source)
	doc := goldmark.DefaultParser().Parse(reader)
	return doc, source
}

func firstBlock(doc ast.Node) ast.Node {
	return doc.FirstChild()
}

func TestExtractEmphasisSignals_LabelPattern(t *testing.T) {
	doc, src := parseBlock(t, "**Warning:** do not use this in production")
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	if !sig.HasWarning {
		t.Error("expected HasWarning to be true")
	}
	if len(sig.InlineLabels) != 1 || sig.InlineLabels[0] != "warning" {
		t.Errorf("expected inline_labels=[warning], got %v", sig.InlineLabels)
	}
}

func TestExtractEmphasisSignals_LabelColonOutside(t *testing.T) {
	// Test the pattern where colon is after bold: **Warning**: text
	doc, src := parseBlock(t, "**Warning**: do not use this in production")
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	if !sig.HasWarning {
		t.Error("expected HasWarning to be true")
	}
}

func TestExtractEmphasisSignals_RiskMarker(t *testing.T) {
	doc, src := parseBlock(t, "This API is **deprecated** and should not be used.")
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	if len(sig.RiskMarkers) != 1 || sig.RiskMarkers[0] != "deprecated" {
		t.Errorf("expected risk_markers=[deprecated], got %v", sig.RiskMarkers)
	}
}

func TestExtractEmphasisSignals_KeyValue(t *testing.T) {
	doc, src := parseBlock(t, "**Timeout:** 30s")
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	if sig.LabelValues == nil {
		t.Fatal("expected label_values to be non-nil")
	}
	if v, ok := sig.LabelValues["timeout"]; !ok || v != "30s" {
		t.Errorf("expected label_values[timeout]=30s, got %v", sig.LabelValues)
	}
}

func TestExtractEmphasisSignals_MultipleSignals(t *testing.T) {
	md := "**Warning:** be careful. This is **deprecated** and **unsafe**."
	doc, src := parseBlock(t, md)
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	if !sig.HasWarning {
		t.Error("expected HasWarning")
	}
	if len(sig.RiskMarkers) < 2 {
		t.Errorf("expected at least 2 risk markers, got %v", sig.RiskMarkers)
	}
}

func TestExtractEmphasisSignals_ItalicIgnored(t *testing.T) {
	doc, src := parseBlock(t, "This is *italic* text with no bold.")
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig != nil {
		t.Errorf("expected nil signals for italic-only text, got %+v", sig)
	}
}

func TestExtractEmphasisSignals_ListItems(t *testing.T) {
	md := "- **Note:** first item\n- **Tip:** second item\n"
	doc, src := parseBlock(t, md)
	// The list is the first child of the document
	sig := ExtractEmphasisSignals(firstBlock(doc), src)

	if sig == nil {
		t.Fatal("expected signals, got nil")
	}
	found := false
	for _, label := range sig.InlineLabels {
		if label == "note" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'note' in inline_labels, got %v", sig.InlineLabels)
	}
}

func TestNormalizeTerm(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Warning:", "warning"},
		{"  DEPRECATED  ", "deprecated"},
		{"Breaking Change", "breaking change"},
		{"**text**", "text"},
		{"hello!", "hello"},
		{"do-not-run", "do-not-run"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeTerm(tt.input)
			if got != tt.want {
				t.Errorf("normalizeTerm(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSignalLine(t *testing.T) {
	sig := &EmphasisSignals{
		InlineLabels: []string{"warning"},
		RiskMarkers:  []string{"deprecated"},
	}
	got := sig.SignalLine()
	if got != "Signals: warning, deprecated" {
		t.Errorf("SignalLine() = %q, want %q", got, "Signals: warning, deprecated")
	}
}

func TestSignalLine_Empty(t *testing.T) {
	sig := &EmphasisSignals{}
	got := sig.SignalLine()
	if got != "" {
		t.Errorf("SignalLine() = %q, want empty string", got)
	}

	var nilSig *EmphasisSignals
	got = nilSig.SignalLine()
	if got != "" {
		t.Errorf("nil.SignalLine() = %q, want empty string", got)
	}
}

func TestToJSON(t *testing.T) {
	sig := &EmphasisSignals{
		InlineLabels: []string{"warning"},
		HasWarning:   true,
	}
	j := sig.ToJSON()
	if !strings.Contains(j, `"inline_labels"`) {
		t.Errorf("ToJSON() missing inline_labels: %s", j)
	}
	if !strings.Contains(j, `"has_warning":true`) {
		t.Errorf("ToJSON() missing has_warning: %s", j)
	}
}

func TestToJSON_Nil(t *testing.T) {
	var sig *EmphasisSignals
	got := sig.ToJSON()
	if got != "{}" {
		t.Errorf("nil.ToJSON() = %q, want %q", got, "{}")
	}
}

func TestEmphasisIntegration(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "test.md")
	content := `# Test Document

## Warnings Section

**Warning:** This feature is experimental.

**Deprecated:** use NewAPI instead.

## Regular Section

Some normal text without bold signals.
`
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseMarkdown(mdPath)
	if err != nil {
		t.Fatal(err)
	}

	var warningSection *Section
	for i := range parsed.Sections {
		if strings.Contains(parsed.Sections[i].Heading, "Warnings Section") {
			warningSection = &parsed.Sections[i]
			break
		}
	}

	if warningSection == nil {
		t.Fatal("expected a section containing 'Warnings Section' to exist")
	}
	if warningSection.Signals == nil {
		t.Fatal("expected Signals to be non-nil for Warnings Section")
	}
	if !warningSection.Signals.HasWarning {
		t.Error("expected HasWarning to be true")
	}

	// Check regular section has no signals
	for _, section := range parsed.Sections {
		if strings.Contains(section.Heading, "Regular Section") {
			if section.Signals != nil {
				t.Errorf("expected nil Signals for Regular Section, got %+v", section.Signals)
			}
			break
		}
	}
}

func TestChunkDocumentWithSignals(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "test.md")
	content := `# Test Doc

## Important Section

**Warning:** critical issue here. **deprecated** API.

This paragraph has enough text to satisfy the minimum token requirement for chunking so we add more words here to make it long enough for the chunker to accept it as valid content.
`
	if err := os.WriteFile(mdPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseMarkdown(mdPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := ChunkConfig{
		MaxTokens:    512,
		OverlapLines: 0,
		MinTokens:    5,
	}
	chunks := ChunkDocument(parsed, cfg)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	found := false
	for _, chunk := range chunks {
		if chunk.SignalMeta != "" && chunk.SignalMeta != "{}" {
			found = true
			if !strings.Contains(chunk.SignalMeta, "warning") {
				t.Errorf("expected SignalMeta to contain 'warning', got %s", chunk.SignalMeta)
			}
			if !strings.Contains(chunk.EmbeddingText, "Signals:") {
				t.Errorf("expected EmbeddingText to contain 'Signals:', got %s", chunk.EmbeddingText)
			}
			break
		}
	}
	if !found {
		t.Error("expected at least one chunk with non-empty SignalMeta")
	}
}
