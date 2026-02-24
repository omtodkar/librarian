package indexer

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
)

// EmphasisSignals holds classified signals extracted from bold text in markdown.
type EmphasisSignals struct {
	InlineLabels  []string          `json:"inline_labels,omitempty"`
	RiskMarkers   []string          `json:"risk_markers,omitempty"`
	EmphasisTerms []string          `json:"emphasis_terms,omitempty"`
	LabelValues   map[string]string `json:"label_values,omitempty"`
	HasWarning    bool              `json:"has_warning,omitempty"`
	HasDecision   bool              `json:"has_decision,omitempty"`
}

// canonicalLabels maps variations to canonical label names.
var canonicalLabels = map[string]string{
	"warn":          "warning",
	"warning":       "warning",
	"caution":       "warning",
	"note":          "note",
	"info":          "note",
	"tip":           "note",
	"decision":      "decision",
	"important":     "important",
	"input":         "input",
	"output":        "output",
	"example":       "example",
	"todo":          "todo",
	"fixme":         "fixme",
	"default":       "default",
	"prerequisite":  "prerequisite",
	"requirement":   "requirement",
}

// canonicalRiskMarkers maps variations to canonical risk marker names.
var canonicalRiskMarkers = map[string]string{
	"deprecated":      "deprecated",
	"breaking":        "breaking-change",
	"breaking change": "breaking-change",
	"unsafe":          "unsafe",
	"experimental":    "experimental",
	"unstable":        "experimental",
	"do not run":      "do-not-run",
	"do-not-run":      "do-not-run",
}

// ExtractEmphasisSignals extracts bold text signals from a block-level AST node.
func ExtractEmphasisSignals(blockNode ast.Node, source []byte) *EmphasisSignals {
	signals := &EmphasisSignals{}
	extractEmphasisFromNode(blockNode, source, signals)

	if len(signals.InlineLabels) == 0 && len(signals.RiskMarkers) == 0 &&
		len(signals.EmphasisTerms) == 0 && len(signals.LabelValues) == 0 {
		return nil
	}

	for _, label := range signals.InlineLabels {
		if label == "warning" {
			signals.HasWarning = true
		}
		if label == "decision" {
			signals.HasDecision = true
		}
	}

	return signals
}

func extractEmphasisFromNode(node ast.Node, source []byte, signals *EmphasisSignals) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		emph, ok := child.(*ast.Emphasis)
		if ok && emph.Level == 2 {
			boldText := extractInlineTextToString(emph, source)
			normalized := normalizeTerm(boldText)
			if normalized == "" {
				continue
			}

			signals.EmphasisTerms = appendUnique(signals.EmphasisTerms, normalized)

			// Check if bold text itself ends with ':'
			trimmedBold := strings.TrimSpace(boldText)
			boldEndsWithColon := strings.HasSuffix(trimmedBold, ":")

			// Check if next sibling starts with ':'
			colonAfter := hasColonAfter(emph, source)

			if boldEndsWithColon || colonAfter {
				// It's a label pattern — extract key and value
				key := normalized
				if boldEndsWithColon {
					key = normalizeTerm(strings.TrimSuffix(trimmedBold, ":"))
				}
				value := extractValueAfterColon(emph, source, boldEndsWithColon)
				classifyLabel(key, value, signals)
			} else {
				// Check if it's a standalone risk marker
				if canonical, ok := canonicalRiskMarkers[normalized]; ok {
					signals.RiskMarkers = appendUnique(signals.RiskMarkers, canonical)
				}
			}
			continue
		}
		// Recurse into non-emphasis children (handles List → ListItem → Paragraph nesting)
		extractEmphasisFromNode(child, source, signals)
	}
}

func extractInlineTextToString(node ast.Node, source []byte) string {
	var buf bytes.Buffer
	extractInlineTextForEmphasis(node, source, &buf)
	return buf.String()
}

func extractInlineTextForEmphasis(node ast.Node, source []byte, buf *bytes.Buffer) {
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if t, ok := child.(*ast.Text); ok {
			buf.Write(t.Segment.Value(source))
			if t.SoftLineBreak() {
				buf.WriteByte(' ')
			}
		} else {
			extractInlineTextForEmphasis(child, source, buf)
		}
	}
}

func hasColonAfter(emphNode ast.Node, source []byte) bool {
	next := emphNode.NextSibling()
	if next == nil {
		return false
	}
	if t, ok := next.(*ast.Text); ok {
		text := string(t.Segment.Value(source))
		trimmed := strings.TrimLeft(text, " ")
		return strings.HasPrefix(trimmed, ":")
	}
	return false
}

func extractValueAfterColon(emphNode ast.Node, source []byte, boldEndsWithColon bool) string {
	next := emphNode.NextSibling()
	if next == nil {
		return ""
	}

	var raw string
	if t, ok := next.(*ast.Text); ok {
		raw = string(t.Segment.Value(source))
	} else {
		return ""
	}

	if !boldEndsWithColon {
		// The colon is at the start of the next sibling
		idx := strings.Index(raw, ":")
		if idx < 0 {
			return ""
		}
		raw = raw[idx+1:]
	}

	// Trim and take up to the first sentence end or newline
	raw = strings.TrimSpace(raw)
	if idx := strings.IndexAny(raw, ".\n"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw)
}

func classifyLabel(key, value string, signals *EmphasisSignals) {
	if canonical, ok := canonicalLabels[key]; ok {
		signals.InlineLabels = appendUnique(signals.InlineLabels, canonical)
		if value != "" {
			if signals.LabelValues == nil {
				signals.LabelValues = make(map[string]string)
			}
			signals.LabelValues[canonical] = value
		}
		return
	}

	// Also check risk markers when used as labels (e.g., **Deprecated:** reason)
	if canonical, ok := canonicalRiskMarkers[key]; ok {
		signals.RiskMarkers = appendUnique(signals.RiskMarkers, canonical)
		if value != "" {
			if signals.LabelValues == nil {
				signals.LabelValues = make(map[string]string)
			}
			signals.LabelValues[canonical] = value
		}
		return
	}

	// Unknown label — still store the key-value if present
	if value != "" {
		if signals.LabelValues == nil {
			signals.LabelValues = make(map[string]string)
		}
		signals.LabelValues[key] = value
	}
}

func normalizeTerm(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) && r != '-'
	})
	return strings.ToLower(strings.TrimSpace(s))
}

func appendUnique(slice []string, item string) []string {
	for _, existing := range slice {
		if existing == item {
			return slice
		}
	}
	return append(slice, item)
}

// mergeSignals merges src into the section's Signals, initializing if needed.
func mergeSignals(section *Section, src *EmphasisSignals) {
	if src == nil {
		return
	}
	if section.Signals == nil {
		section.Signals = &EmphasisSignals{}
	}
	dst := section.Signals

	for _, v := range src.InlineLabels {
		dst.InlineLabels = appendUnique(dst.InlineLabels, v)
	}
	for _, v := range src.RiskMarkers {
		dst.RiskMarkers = appendUnique(dst.RiskMarkers, v)
	}
	for _, v := range src.EmphasisTerms {
		dst.EmphasisTerms = appendUnique(dst.EmphasisTerms, v)
	}
	if src.LabelValues != nil {
		if dst.LabelValues == nil {
			dst.LabelValues = make(map[string]string)
		}
		for k, v := range src.LabelValues {
			dst.LabelValues[k] = v
		}
	}
	if src.HasWarning {
		dst.HasWarning = true
	}
	if src.HasDecision {
		dst.HasDecision = true
	}
}

// SignalLine generates a selective embedding augmentation string.
// Only includes InlineLabels and RiskMarkers (not all emphasis terms).
func (s *EmphasisSignals) SignalLine() string {
	if s == nil {
		return ""
	}
	var parts []string
	parts = append(parts, s.InlineLabels...)
	parts = append(parts, s.RiskMarkers...)
	if len(parts) == 0 {
		return ""
	}
	return "Signals: " + strings.Join(parts, ", ")
}

// ToJSON serializes the signals to JSON for storage.
func (s *EmphasisSignals) ToJSON() string {
	if s == nil {
		return "{}"
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}
