package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WordprocessingML namespace URIs. All XML element matching compares on the
// full xml.Name.Space+Local tuple rather than the local name, because Office
// documents frequently mix in Microsoft-specific extension namespaces (w14,
// mc:AlternateContent) that reuse the same local names.
const (
	nsW              = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR              = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	relTypeHyperlink = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink"
)

// numberingMap captures each `w:numId`'s per-level numFmt ("bullet" vs
// "decimal" etc.). Missing numbering.xml yields nil, treated as bullet by
// convention.
type numberingMap map[string]map[int]string // numId → ilvl → numFmt

// convertDocx turns a .docx archive into structured markdown. Paragraph-,
// list-, table-, and hyperlink-level structure survives; headers/footers,
// comments, footnotes, images, math, and text boxes are intentionally
// dropped (tracked by follow-up bd issues).
func convertDocx(content []byte) ([]byte, error) {
	r, err := openZip(content)
	if err != nil {
		return nil, err
	}
	bodyXML, err := readZipFile(r, "word/document.xml")
	if err != nil {
		return nil, err
	}
	if len(bodyXML) == 0 {
		return nil, nil
	}

	relsXML, err := readZipFile(r, "word/_rels/document.xml.rels")
	if err != nil {
		return nil, err
	}
	rels, err := parseRels(relsXML)
	if err != nil {
		return nil, err
	}

	// Word itself tolerates a corrupt numbering.xml: text content stays
	// visible, only list markers become generic. Match that behaviour —
	// failing the whole conversion would hide body content for recoverable
	// damage to a metadata part.
	numbering, err := parseNumbering(r)
	if err != nil {
		numbering = nil
	}

	return renderDocxBody(bodyXML, rels, numbering)
}

// parseNumbering reads word/numbering.xml (optional) and builds the
// numId→ilvl→numFmt map used to decide whether a paragraph is a bullet
// (`- `) or a numbered list (`1. `).
func parseNumbering(r *zip.Reader) (numberingMap, error) {
	b, err := readZipFile(r, "word/numbering.xml")
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}

	type lvl struct {
		Ilvl   int    `xml:"ilvl,attr"`
		NumFmt struct {
			Val string `xml:"val,attr"`
		} `xml:"numFmt"`
	}
	type abstractNum struct {
		ID   string `xml:"abstractNumId,attr"`
		Lvls []lvl  `xml:"lvl"`
	}
	type numEntry struct {
		NumID           string `xml:"numId,attr"`
		AbstractNumID struct {
			Val string `xml:"val,attr"`
		} `xml:"abstractNumId"`
	}
	type numbering struct {
		Abstract []abstractNum `xml:"abstractNum"`
		Nums     []numEntry    `xml:"num"`
	}
	var doc numbering
	if err := xml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("parsing numbering.xml: %w", err)
	}

	// abstractNumId → ilvl → numFmt
	byAbstract := make(map[string]map[int]string, len(doc.Abstract))
	for _, a := range doc.Abstract {
		levels := make(map[int]string, len(a.Lvls))
		for _, l := range a.Lvls {
			levels[l.Ilvl] = l.NumFmt.Val
		}
		byAbstract[a.ID] = levels
	}
	out := make(numberingMap, len(doc.Nums))
	for _, n := range doc.Nums {
		if fmts, ok := byAbstract[n.AbstractNumID.Val]; ok {
			out[n.NumID] = fmts
		}
	}
	return out, nil
}

// renderDocxBody is a streaming XML pass over document.xml that emits
// markdown in document order. A single pass keeps memory bounded for large
// documents; buffered state (in-progress tables, text runs) is reset at
// block boundaries.
func renderDocxBody(xmlBytes []byte, rels *relMap, numbering numberingMap) ([]byte, error) {
	d := xml.NewDecoder(bytes.NewReader(xmlBytes))
	d.DefaultSpace = nsW

	var out bytes.Buffer
	state := &docxState{rels: rels, numbering: numbering, out: &out}

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding document.xml: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Space != nsW {
			continue
		}
		switch start.Name.Local {
		case "p":
			if err := state.consumeParagraph(d, start); err != nil {
				return nil, err
			}
		case "tbl":
			if err := state.consumeTable(d, start); err != nil {
				return nil, err
			}
		}
	}
	return out.Bytes(), nil
}

// docxState threads rendering context through the streaming walk. Everything
// the renderer writes goes through `out`; temporary per-paragraph buffers
// live on the stack in consumeParagraph.
type docxState struct {
	rels      *relMap
	numbering numberingMap
	out       *bytes.Buffer
}

// consumeParagraph walks one `w:p` and emits either a heading (`# `/`## `/
// `### `), a list item (indented `- ` or `1. `), or a plain paragraph.
// Returns after the matching EndElement is consumed.
func (s *docxState) consumeParagraph(d *xml.Decoder, start xml.StartElement) error {
	var (
		headingLevel int    // 0 = none
		numID        string // list numId
		ilvl         int    // list indent level
		text         strings.Builder
	)

	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("reading w:p: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "pPr":
				headingLevel, numID, ilvl = readParagraphProperties(d)
			case "r":
				consumeRun(d, &text)
			case "hyperlink":
				if err := s.consumeHyperlink(d, t, &text); err != nil {
					return err
				}
			case "ins":
				// Accept tracked insertions as kept text.
				// `w:ins` wraps further `w:r` children.
				continue
			case "del":
				// Tracked deletions: skip everything under this node.
				if err := d.Skip(); err != nil {
					return err
				}
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "p" {
				s.flushParagraph(headingLevel, numID, ilvl, text.String())
				return nil
			}
		}
	}
}

// readParagraphProperties extracts heading style + list metadata from one
// `w:pPr`. Consumes through the matching EndElement.
func readParagraphProperties(d *xml.Decoder) (headingLevel int, numID string, ilvl int) {
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "pStyle":
				if v := attrValue(t, nsW, "val"); strings.HasPrefix(v, "Heading") {
					if n, err := strconv.Atoi(strings.TrimPrefix(v, "Heading")); err == nil && n >= 1 && n <= 6 {
						headingLevel = n
					}
				}
				d.Skip()
			case "numPr":
				numID, ilvl = readNumPr(d)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "pPr" {
				return
			}
		}
	}
}

// readNumPr pulls numId + ilvl out of `w:numPr`.
func readNumPr(d *xml.Decoder) (numID string, ilvl int) {
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "numId":
				numID = attrValue(t, nsW, "val")
				d.Skip()
			case "ilvl":
				if v := attrValue(t, nsW, "val"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						ilvl = n
					}
				}
				d.Skip()
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "numPr" {
				return
			}
		}
	}
}

// consumeRun collects text from one `w:r`, honouring `w:t`, `w:br`, `w:tab`.
func consumeRun(d *xml.Decoder, dst *strings.Builder) {
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "t":
				// Preserve significant whitespace when xml:space="preserve".
				var s string
				for {
					inner, err := d.Token()
					if err != nil {
						return
					}
					if c, ok := inner.(xml.CharData); ok {
						s += string(c)
						continue
					}
					if end, ok := inner.(xml.EndElement); ok && end.Name.Space == nsW && end.Name.Local == "t" {
						break
					}
				}
				dst.WriteString(s)
			case "br":
				dst.WriteString("  \n")
				d.Skip()
			case "tab":
				dst.WriteString("  ")
				d.Skip()
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "r" {
				return
			}
		}
	}
}

// consumeHyperlink reads a `w:hyperlink` element, resolving its rId to a
// URL via the rels map and emitting `[text](url)`. Internal bookmarks
// (`w:anchor`-only links with no rId) flatten to plain text.
func (s *docxState) consumeHyperlink(d *xml.Decoder, start xml.StartElement, dst *strings.Builder) error {
	rID := attrValue(start, nsR, "id")
	var label strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("reading w:hyperlink: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsW && t.Name.Local == "r" {
				consumeRun(d, &label)
				continue
			}
			d.Skip()
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "hyperlink" {
				text := label.String()
				target := ""
				if rID != "" && s.rels != nil {
					if s.rels.Types[rID] == relTypeHyperlink && s.rels.Modes[rID] == "External" {
						target = s.rels.Targets[rID]
					}
				}
				if target == "" {
					dst.WriteString(escapeInline(text))
				} else {
					dst.WriteString("[")
					dst.WriteString(escapeInline(text))
					dst.WriteString("](")
					dst.WriteString(target)
					dst.WriteString(")")
				}
				return nil
			}
		}
	}
}

// flushParagraph writes the assembled paragraph text with the appropriate
// markdown prefix. Empty paragraphs skip the heading/list markers entirely —
// Word uses a blank `Heading1` paragraph for spacing, and `# \n\n` is
// malformed markdown that trips strict parsers.
func (s *docxState) flushParagraph(headingLevel int, numID string, ilvl int, text string) {
	text = strings.TrimRight(text, " \t")
	if text == "" {
		s.out.WriteByte('\n')
		return
	}
	switch {
	case headingLevel > 0:
		fmt.Fprintf(s.out, "%s %s\n\n", strings.Repeat("#", headingLevel), text)
	case numID != "" && numID != "0":
		bullet := listBullet(numID, ilvl, s.numbering)
		fmt.Fprintf(s.out, "%s%s %s\n", strings.Repeat("  ", ilvl), bullet, text)
	default:
		s.out.WriteString(text)
		s.out.WriteString("\n\n")
	}
}

// listBullet returns the markdown list marker for the given numbering
// level. Defaults to `-` when the numbering.xml entry is absent or missing
// a format for this level.
func listBullet(numID string, ilvl int, numbering numberingMap) string {
	if numbering == nil {
		return "-"
	}
	levels, ok := numbering[numID]
	if !ok {
		return "-"
	}
	fmt, ok := levels[ilvl]
	if !ok || fmt == "bullet" {
		return "-"
	}
	// decimal, lowerRoman, upperLetter, etc. — markdown renderers treat `1.`
	// as an ordered-list marker regardless of the declared format, so flat
	// "1." preserves the ordered-list intent.
	return "1."
}

// consumeTable reads a `w:tbl` and emits a GitHub-flavored markdown table.
// The first row is treated as the header; a separator row is synthesised
// after it.
func (s *docxState) consumeTable(d *xml.Decoder, start xml.StartElement) error {
	var rows [][]string
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("reading w:tbl: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "tr":
				row, err := s.consumeRow(d)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "tbl" {
				s.flushTable(rows)
				return nil
			}
		}
	}
}

// consumeRow reads one `w:tr`, returning its cell text in column order.
func (s *docxState) consumeRow(d *xml.Decoder) ([]string, error) {
	var row []string
	for {
		tok, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("reading w:tr: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "tc":
				text, err := s.consumeCell(d)
				if err != nil {
					return nil, err
				}
				row = append(row, text)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "tr" {
				return row, nil
			}
		}
	}
}

// consumeCell aggregates all paragraph text inside one `w:tc` into a single
// markdown-table cell. Nested tables are flattened — `escapeCell` converts
// newlines to `<br>`.
func (s *docxState) consumeCell(d *xml.Decoder) (string, error) {
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("reading w:tc: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "p":
				var para strings.Builder
				if err := consumeParagraphText(d, &para); err != nil {
					return "", err
				}
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(para.String())
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "tc" {
				return strings.TrimSpace(text.String()), nil
			}
		}
	}
}

// consumeParagraphText reads a `w:p` into dst as plain text. Lighter-weight
// than consumeParagraph — used inside table cells where we don't need
// heading / list markers, just concatenated run text.
func consumeParagraphText(d *xml.Decoder, dst *strings.Builder) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != nsW {
				d.Skip()
				continue
			}
			switch t.Name.Local {
			case "r":
				consumeRun(d, dst)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsW && t.Name.Local == "p" {
				return nil
			}
		}
	}
}

// flushTable writes accumulated rows as a GFM table via the shared renderer.
// Ragged rows (some with fewer cells than the header) are padded to the widest
// row so the markdown stays well-formed.
func (s *docxState) flushTable(rows [][]string) {
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	writeGFMTable(s.out, rows, width)
	if width > 0 {
		s.out.WriteByte('\n')
	}
}
