package markdown

import (
	"librarian/internal/indexer"
)

// toParsedDoc converts the legacy ParsedDocument to the format-agnostic ParsedDoc.
// Sections become Units of Kind "section". Signals flatten into the generic []Signal
// slice on each Unit. Markdown-specific extras (diagrams, tables, frontmatter,
// flat headings list) live in ParsedDoc.Metadata.
func toParsedDoc(pd *indexer.ParsedDocument, path string) *indexer.ParsedDoc {
	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "markdown",
		Title:      pd.Title,
		DocType:    pd.DocType,
		Summary:    pd.Summary,
		RawContent: pd.RawContent,
		Metadata:   map[string]any{},
	}
	if pd.Frontmatter != nil {
		doc.Metadata["frontmatter"] = pd.Frontmatter
	}
	if len(pd.Headings) > 0 {
		doc.Metadata["headings"] = pd.Headings
	}
	if len(pd.Diagrams) > 0 {
		doc.Metadata["diagrams"] = pd.Diagrams
	}
	if len(pd.Tables) > 0 {
		doc.Metadata["tables"] = pd.Tables
	}
	for _, s := range pd.Sections {
		doc.Units = append(doc.Units, sectionToUnit(s))
	}
	return doc
}

func sectionToUnit(s indexer.Section) indexer.Unit {
	u := indexer.Unit{
		Kind:     "section",
		Title:    s.Heading,
		Path:     joinHierarchy(s.Hierarchy),
		Content:  s.Content,
		Metadata: map[string]any{},
	}
	if s.Level != 0 {
		u.Metadata["level"] = s.Level
	}
	if len(s.Hierarchy) > 0 {
		// Preserved as a slice so round-trip survives headings containing " > ".
		u.Metadata["hierarchy"] = s.Hierarchy
	}
	if s.Signals != nil {
		u.Signals = emphasisToSignals(s.Signals)
	}
	return u
}

func joinHierarchy(h []string) string {
	if len(h) == 0 {
		return ""
	}
	out := h[0]
	for i := 1; i < len(h); i++ {
		out += " > " + h[i]
	}
	return out
}

// emphasisToSignals flattens an EmphasisSignals struct into the generic []Signal
// shape. Each InlineLabel / RiskMarker / EmphasisTerm becomes its own Signal;
// each LabelValues entry becomes a "label-value" Signal with Value=key, Detail=value.
// HasWarning / HasDecision are omitted because they're derivable from InlineLabels.
func emphasisToSignals(e *indexer.EmphasisSignals) []indexer.Signal {
	if e == nil {
		return nil
	}
	var out []indexer.Signal
	for _, l := range e.InlineLabels {
		out = append(out, indexer.Signal{Kind: "label", Value: l})
	}
	for _, r := range e.RiskMarkers {
		out = append(out, indexer.Signal{Kind: "risk", Value: r})
	}
	for _, t := range e.EmphasisTerms {
		out = append(out, indexer.Signal{Kind: "emphasis", Value: t})
	}
	for k, v := range e.LabelValues {
		out = append(out, indexer.Signal{Kind: "label-value", Value: k, Detail: v})
	}
	return out
}

// signalsToEmphasis is the inverse of emphasisToSignals. HasWarning / HasDecision
// are re-derived from InlineLabels.
func signalsToEmphasis(sigs []indexer.Signal) *indexer.EmphasisSignals {
	if len(sigs) == 0 {
		return nil
	}
	e := &indexer.EmphasisSignals{}
	for _, s := range sigs {
		switch s.Kind {
		case "label":
			e.InlineLabels = append(e.InlineLabels, s.Value)
			switch s.Value {
			case "warning":
				e.HasWarning = true
			case "decision":
				e.HasDecision = true
			}
		case "risk":
			e.RiskMarkers = append(e.RiskMarkers, s.Value)
		case "emphasis":
			e.EmphasisTerms = append(e.EmphasisTerms, s.Value)
		case "label-value":
			if e.LabelValues == nil {
				e.LabelValues = map[string]string{}
			}
			e.LabelValues[s.Value] = s.Detail
		}
	}
	if len(e.InlineLabels) == 0 && len(e.RiskMarkers) == 0 &&
		len(e.EmphasisTerms) == 0 && len(e.LabelValues) == 0 {
		return nil
	}
	return e
}
