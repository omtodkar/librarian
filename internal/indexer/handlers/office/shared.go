package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// ZIP-bomb defences — an Office file legitimately rarely exceeds these
// thresholds; a malicious crafted archive can. Values chosen above what any
// sane corporate document uses.
const (
	maxUncompressedBytes = 200 * 1024 * 1024 // 200 MB
	maxZipEntries        = 10000
)

// openZip parses the bytes as a ZIP archive with bomb-guard limits applied.
func openZip(b []byte) (*zip.Reader, error) {
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, fmt.Errorf("opening office archive: %w", err)
	}
	if len(r.File) > maxZipEntries {
		return nil, fmt.Errorf("office archive has %d entries (max %d)", len(r.File), maxZipEntries)
	}
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize64
		if total > maxUncompressedBytes {
			return nil, fmt.Errorf("office archive uncompressed size exceeds %d bytes", maxUncompressedBytes)
		}
	}
	return r, nil
}

// readZipFile returns the contents of the named entry, or (nil, nil) if the
// entry is absent — Office documents frequently omit optional parts
// (numbering.xml, notesSlide*.xml) and callers handle "not present" as
// "nothing to emit".
func readZipFile(r *zip.Reader, name string) ([]byte, error) {
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("opening %s: %w", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", name, err)
		}
		return b, nil
	}
	return nil, nil
}

// relMap maps Relationship Id → Target URL/path. Built per-document from
// `*/_rels/*.xml.rels` files and consumed by hyperlink + slide-order logic.
type relMap struct {
	Targets map[string]string // rId → Target
	Types   map[string]string // rId → Relationship Type URI
	Modes   map[string]string // rId → TargetMode ("External" or "")
}

// parseRels decodes a .rels XML payload. Absent/empty input yields an empty
// relMap so callers can always treat lookups as best-effort.
func parseRels(b []byte) (*relMap, error) {
	out := &relMap{
		Targets: map[string]string{},
		Types:   map[string]string{},
		Modes:   map[string]string{},
	}
	if len(b) == 0 {
		return out, nil
	}
	type rel struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	}
	type relationships struct {
		Rels []rel `xml:"Relationship"`
	}
	var doc relationships
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing .rels: %w", err)
	}
	for _, r := range doc.Rels {
		out.Targets[r.ID] = r.Target
		out.Types[r.ID] = r.Type
		out.Modes[r.ID] = r.TargetMode
	}
	return out, nil
}

// escapeCell sanitises text for a markdown-table cell. Pipes terminate the
// cell so they're backslash-escaped; newlines split the row so they become
// <br>. Callers apply this per cell, not to entire paragraphs.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", "<br>")
	return strings.ReplaceAll(s, "|", `\|`)
}

// escapeInline backslash-escapes markdown link-bracket characters so that
// verbatim `[` / `]` / `(` / `)` in user text don't accidentally form
// links. Used inside link labels and plain paragraph text.
func escapeInline(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

// attrValue returns the value of the first attribute matching space+local on
// a tree-sitter/XML start element, or "" if absent. Used by every office
// converter for namespaced attribute lookups (e.g., `w:val`, `r:id`,
// attribute-less `type` on PPTX placeholders).
func attrValue(e xml.StartElement, space, local string) string {
	for _, a := range e.Attr {
		if a.Name.Space == space && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// writeGFMTable emits rows as a GitHub-Flavored Markdown table to out. The
// first row is the header; a separator row is synthesised. Cells are
// escaped via escapeCell. Shorter rows are padded with empty cells to
// width; empty rows (width==0) emit nothing.
func writeGFMTable(out *bytes.Buffer, rows [][]string, width int) {
	if len(rows) == 0 || width == 0 {
		return
	}
	writeRow := func(cells []string) {
		out.WriteByte('|')
		for i := 0; i < width; i++ {
			var v string
			if i < len(cells) {
				v = escapeCell(cells[i])
			}
			out.WriteByte(' ')
			out.WriteString(v)
			out.WriteByte(' ')
			out.WriteByte('|')
		}
		out.WriteByte('\n')
	}
	writeRow(rows[0])
	out.WriteByte('|')
	for i := 0; i < width; i++ {
		out.WriteString(" --- |")
	}
	out.WriteByte('\n')
	for _, r := range rows[1:] {
		writeRow(r)
	}
}
