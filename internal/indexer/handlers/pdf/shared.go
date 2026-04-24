package pdf

import (
	"bytes"
	"strings"
)

// maxPDFBytes guards against pathological inputs before they reach the
// WebAssembly runtime, which would otherwise happily allocate hundreds of
// MB. Matches the 200 MB cap the Office handlers use via openZip.
const maxPDFBytes = 200 * 1024 * 1024

// escapeInline backslash-escapes markdown metacharacters that would
// otherwise corrupt the generated document when PDF text contains
// brackets, parens, or formatting runes.
func escapeInline(s string) string {
	if s == "" {
		return s
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		`[`, `\[`,
		`]`, `\]`,
		`(`, `\(`,
		`)`, `\)`,
		`*`, `\*`,
		`_`, `\_`,
		"`", "\\`",
	)
	return r.Replace(s)
}

// escapeCell prepares text for a GFM table cell: pipes would split the
// row, newlines would break the table. Similar in intent to
// office/shared.go's helper, but not identical — office has already
// removed CRs by the time text reaches it, and writeGFMTable's signature
// differs because XLSX cares about caller-controlled column widths.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, `|`, `\|`)
	s = strings.ReplaceAll(s, "\n", "<br>")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// writeGFMTable writes rows as a GitHub-Flavored Markdown table, with the
// first row treated as the header and a synthesised separator line. Empty
// input is a no-op.
func writeGFMTable(out *bytes.Buffer, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}
	if width == 0 {
		return
	}

	writeRow := func(row []string) {
		out.WriteString("|")
		for i := 0; i < width; i++ {
			out.WriteString(" ")
			if i < len(row) {
				out.WriteString(escapeCell(row[i]))
			}
			out.WriteString(" |")
		}
		out.WriteString("\n")
	}

	writeRow(rows[0])
	out.WriteString("|")
	for i := 0; i < width; i++ {
		out.WriteString(" --- |")
	}
	out.WriteString("\n")
	for _, row := range rows[1:] {
		writeRow(row)
	}
}
