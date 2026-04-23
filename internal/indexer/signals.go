package indexer

import (
	"encoding/json"
	"regexp"
	"strings"
)

// rationaleRegex matches TODO / FIXME / HACK / WHY / NOTE markers inside a
// comment body. Comment leaders ("//", "/*", "#", "<!--", etc.) must already
// be stripped before the text reaches this regex.
var rationaleRegex = regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|WHY|NOTE)\b:?\s*(.*)$`)

// ExtractRationaleSignals scans a block of text line-by-line for rationale
// markers and returns the corresponding generic Signals. TODO/FIXME/HACK are
// emitted as Kind="todo"; WHY/NOTE as Kind="rationale". Value holds the
// lower-cased marker word; Detail holds the text after it on the same line.
//
// Handlers (markdown, config, code, ...) share this extractor so rationale
// semantics stay consistent across formats. The function treats its input as
// already-stripped comment bodies — strip //, /*, */, #, <!--, --> leaders
// before passing text in.
func ExtractRationaleSignals(text string) []Signal {
	if text == "" {
		return nil
	}
	var out []Signal
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := rationaleRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kind := strings.ToLower(m[1])
		switch kind {
		case "todo", "fixme", "hack":
			out = append(out, Signal{Kind: "todo", Value: kind, Detail: strings.TrimSpace(m[2])})
		case "why", "note":
			out = append(out, Signal{Kind: "rationale", Value: kind, Detail: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// formatSignalEntry renders a Signal as "kind: detail" when both are present,
// falling back to just the value (or detail) when one is missing. Used by
// SignalsToJSON for "todo" and "rationale" kinds where the marker word
// ("fixme", "hack", "note") and the free-text body are both interesting.
func formatSignalEntry(s Signal) string {
	if s.Detail == "" {
		return s.Value
	}
	if s.Value == "" {
		return s.Detail
	}
	return s.Value + ": " + s.Detail
}

// SignalLineFromSignals produces the selective embedding-augmentation string
// for a generic []Signal. Only "label" and "risk" kinds contribute — the same
// subset EmphasisSignals.SignalLine includes (InlineLabels + RiskMarkers).
// Returns "" for empty input or when no contributing signals are present.
func SignalLineFromSignals(sigs []Signal) string {
	var parts []string
	for _, s := range sigs {
		if s.Kind == "label" || s.Kind == "risk" {
			parts = append(parts, s.Value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Signals: " + strings.Join(parts, ", ")
}

// SignalsToJSON serializes a generic []Signal for storage in Chunk.SignalMeta.
//
// The wire format intentionally mirrors the legacy EmphasisSignals JSON shape
// so the markdown parity tests — and any downstream consumer that may have
// parsed SignalMeta as an EmphasisSignals-shaped object — keep working. "{}"
// is returned for empty/nil input.
func SignalsToJSON(sigs []Signal) string {
	if len(sigs) == 0 {
		return "{}"
	}
	type jsonSignals struct {
		InlineLabels  []string          `json:"inline_labels,omitempty"`
		RiskMarkers   []string          `json:"risk_markers,omitempty"`
		EmphasisTerms []string          `json:"emphasis_terms,omitempty"`
		LabelValues   map[string]string `json:"label_values,omitempty"`
		Todos         []string          `json:"todos,omitempty"`
		Rationale     []string          `json:"rationale,omitempty"`
		HasWarning    bool              `json:"has_warning,omitempty"`
		HasDecision   bool              `json:"has_decision,omitempty"`
	}
	var out jsonSignals
	for _, s := range sigs {
		switch s.Kind {
		case "label":
			out.InlineLabels = append(out.InlineLabels, s.Value)
			switch s.Value {
			case "warning":
				out.HasWarning = true
			case "decision":
				out.HasDecision = true
			}
		case "risk":
			out.RiskMarkers = append(out.RiskMarkers, s.Value)
		case "emphasis":
			out.EmphasisTerms = append(out.EmphasisTerms, s.Value)
		case "label-value":
			if out.LabelValues == nil {
				out.LabelValues = map[string]string{}
			}
			out.LabelValues[s.Value] = s.Detail
		case "todo":
			out.Todos = append(out.Todos, formatSignalEntry(s))
		case "rationale":
			out.Rationale = append(out.Rationale, formatSignalEntry(s))
		}
	}
	if len(out.InlineLabels) == 0 && len(out.RiskMarkers) == 0 &&
		len(out.EmphasisTerms) == 0 && len(out.LabelValues) == 0 &&
		len(out.Todos) == 0 && len(out.Rationale) == 0 {
		return "{}"
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}
