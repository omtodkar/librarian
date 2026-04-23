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
		Path:        path,
		Format:      "markdown",
		Title:       pd.Title,
		DocType:     pd.DocType,
		Summary:     pd.Summary,
		Headings:    pd.Headings,
		Frontmatter: pd.Frontmatter,
		RawContent:  pd.RawContent,
		Metadata:    map[string]any{},
	}
	// Diagrams and Tables are markdown-specific extracted structures; keep
	// them in Metadata so consumers that care about them can type-assert,
	// while leaving the generic pipeline format-agnostic.
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
	u.Signals = s.Signals.ToSignals()
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


