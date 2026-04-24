package office

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

// PresentationML + DrawingML namespaces used by PPTX. As with DOCX, element
// matching uses xml.Name.Space+Local so namespace aliases (p14, a14) can't
// false-match on local name alone.
const (
	nsP                 = "http://schemas.openxmlformats.org/presentationml/2006/main"
	nsA                 = "http://schemas.openxmlformats.org/drawingml/2006/main"
	relTypeSlide        = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide"
	relTypeNotesSlide   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide"
)

// convertPptx extracts slide content in presentation order and renders it as
// markdown: each slide becomes `## Slide N: <title>` (or `## Slide N` if no
// title placeholder), followed by its body paragraphs with bullet-level
// indentation, and optionally a `### Notes` block.
func convertPptx(content []byte, cfg Config) ([]byte, error) {
	r, err := openZip(content)
	if err != nil {
		return nil, err
	}

	// presentation.xml + its rels gives us the authoritative slide order.
	presXML, err := readZipFile(r, "ppt/presentation.xml")
	if err != nil {
		return nil, err
	}
	if len(presXML) == 0 {
		return nil, nil
	}
	presRelsXML, err := readZipFile(r, "ppt/_rels/presentation.xml.rels")
	if err != nil {
		return nil, err
	}
	presRels, err := parseRels(presRelsXML)
	if err != nil {
		return nil, err
	}

	slidePaths, err := slideOrder(presXML, presRels)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer
	for i, slidePath := range slidePaths {
		if err := writeSlide(&out, r, slidePath, i+1, cfg); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// slideOrder returns the slide XML paths in presentation-declared order,
// resolving each `sldIdLst/sldId@r:id` via the presentation rels map.
// Authoritative source is `sldIdLst` — filename sort (`slide2.xml` vs
// `slide10.xml`) is lexicographically wrong. The walk is manual because
// struct-tag based decoding doesn't reliably resolve the `r:` namespace
// prefix across encoders.
func slideOrder(presXML []byte, rels *relMap) ([]string, error) {
	d := xml.NewDecoder(bytes.NewReader(presXML))
	var paths []string
	inList := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding presentation.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsP && t.Name.Local == "sldIdLst" {
				inList = true
				continue
			}
			if inList && t.Name.Space == nsP && t.Name.Local == "sldId" {
				rID := attrValue(t, nsR, "id")
				if rID != "" && rels != nil {
					if target, ok := rels.Targets[rID]; ok {
						// presentation.xml is at ppt/; slide targets are
						// relative to that directory (slides/slide1.xml).
						paths = append(paths, path.Clean(path.Join("ppt", target)))
					}
				}
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsP && t.Name.Local == "sldIdLst" {
				inList = false
			}
		}
	}
	return paths, nil
}

// writeSlide renders one slide's content starting with `## Slide N: Title`.
// The title is resolved from whichever shape has a `p:ph@type="title"` or
// `"ctrTitle"` placeholder; remaining shape text becomes body paragraphs
// with bullet-level indent. Speaker notes append a `### Notes` block when
// IncludeSpeakerNotes is true and notes are present.
func writeSlide(out *bytes.Buffer, r *zip.Reader, slidePath string, slideNum int, cfg Config) error {
	slideXML, err := readZipFile(r, slidePath)
	if err != nil {
		return err
	}
	if len(slideXML) == 0 {
		return nil
	}

	title, body := extractSlideContent(slideXML)
	if title == "" {
		fmt.Fprintf(out, "## Slide %d\n\n", slideNum)
	} else {
		fmt.Fprintf(out, "## Slide %d: %s\n\n", slideNum, title)
	}
	if body != "" {
		out.WriteString(body)
		if !strings.HasSuffix(body, "\n\n") {
			out.WriteByte('\n')
		}
	}

	if cfg.IncludeSpeakerNotes {
		notesPath, err := findNotesFor(r, slidePath)
		if err != nil {
			return err
		}
		if notesPath != "" {
			notesXML, err := readZipFile(r, notesPath)
			if err != nil {
				return err
			}
			if notes := extractNotesText(notesXML); notes != "" {
				out.WriteString("### Notes\n\n")
				out.WriteString(notes)
				out.WriteString("\n\n")
			}
		}
	}
	return nil
}

// findNotesFor returns the notesSlide XML path associated with a slide, or
// "" if no notes slide exists. Looks up the slide's own rels file.
func findNotesFor(r *zip.Reader, slidePath string) (string, error) {
	// slidePath like "ppt/slides/slide1.xml" → rels at
	// "ppt/slides/_rels/slide1.xml.rels".
	dir, file := path.Split(slidePath)
	relsPath := path.Join(dir, "_rels", file+".rels")
	b, err := readZipFile(r, relsPath)
	if err != nil || b == nil {
		return "", err
	}
	rels, err := parseRels(b)
	if err != nil {
		return "", err
	}
	for rid, rtype := range rels.Types {
		if rtype == relTypeNotesSlide {
			target := rels.Targets[rid]
			// Targets are relative to dir; resolve against it.
			return path.Clean(path.Join(dir, target)), nil
		}
	}
	return "", nil
}

// extractSlideContent runs a streaming pass over a slide's XML, pulling out
// the title-placeholder text (if any) plus all other shape text rendered as
// bullet-indented paragraphs. Tables inside slides are flattened into
// lines (v1 limitation — could be rendered as GFM tables later).
func extractSlideContent(xmlBytes []byte) (title, body string) {
	d := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var bodyBuf bytes.Buffer

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", bodyBuf.String()
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Space == nsP && start.Name.Local == "sp" {
			isTitle, text := consumeShape(d)
			if isTitle && title == "" {
				title = text
			} else if text != "" {
				bodyBuf.WriteString(text)
				if !strings.HasSuffix(text, "\n") {
					bodyBuf.WriteByte('\n')
				}
			}
		}
	}
	return title, bodyBuf.String()
}

// consumeShape reads one `p:sp`, returning whether it's a title placeholder
// and its accumulated text (bullet-indented paragraphs for body shapes,
// single-line text for titles).
func consumeShape(d *xml.Decoder) (isTitle bool, text string) {
	var (
		buf       bytes.Buffer
		wantTitle bool
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return false, buf.String()
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsP && t.Name.Local == "nvSpPr":
				// Scan for `p:ph@type="title"` | "ctrTitle".
				wantTitle = isTitlePlaceholder(d) || wantTitle
			case t.Name.Space == nsP && t.Name.Local == "txBody":
				consumeTxBody(d, &buf, wantTitle)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsP && t.Name.Local == "sp" {
				out := strings.TrimRight(buf.String(), "\n")
				if wantTitle {
					// Flatten multi-line title to a single line.
					out = strings.ReplaceAll(out, "\n", " ")
				}
				return wantTitle, out
			}
		}
	}
}

// isTitlePlaceholder walks `p:nvSpPr`'s subtree looking for `p:ph@type` equal
// to "title" or "ctrTitle". The placeholder lives at depth ≥ 2 (nested
// inside `p:nvPr`), so we track depth rather than calling d.Skip on every
// start — that would prematurely jump past `p:nvPr`'s children.
func isTitlePlaceholder(d *xml.Decoder) bool {
	found := false
	depth := 1 // caller has already consumed the `p:nvSpPr` StartElement
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return found
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Space == nsP && t.Name.Local == "ph" {
				typ := attrValue(t, "", "type")
				if typ == "title" || typ == "ctrTitle" {
					found = true
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return found
}

// consumeTxBody walks a `p:txBody`, emitting each `a:p` with bullet-level
// indentation (from `a:pPr@lvl`). Title bodies get plain text (no bullet
// markers).
func consumeTxBody(d *xml.Decoder, out *bytes.Buffer, isTitle bool) {
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsA && t.Name.Local == "p" {
				text, lvl := consumeAPara(d)
				if text == "" {
					continue
				}
				if isTitle {
					out.WriteString(text)
					out.WriteByte('\n')
				} else {
					indent := strings.Repeat("  ", lvl)
					fmt.Fprintf(out, "%s- %s\n", indent, text)
				}
				continue
			}
			d.Skip()
		case xml.EndElement:
			if t.Name.Space == nsP && t.Name.Local == "txBody" {
				return
			}
		}
	}
}

// consumeAPara reads one `a:p` (DrawingML paragraph) and returns its text
// plus the bullet-indent level from `a:pPr@lvl`.
func consumeAPara(d *xml.Decoder) (string, int) {
	var (
		buf strings.Builder
		lvl int
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return buf.String(), lvl
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsA && t.Name.Local == "pPr":
				if v := attrValue(t, "", "lvl"); v != "" {
					if n, err := strconv.Atoi(v); err == nil {
						lvl = n
					}
				}
				d.Skip()
			case t.Name.Space == nsA && t.Name.Local == "r":
				consumeARun(d, &buf)
			case t.Name.Space == nsA && t.Name.Local == "br":
				buf.WriteByte(' ')
				d.Skip()
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsA && t.Name.Local == "p" {
				return strings.TrimSpace(buf.String()), lvl
			}
		}
	}
}

// consumeARun reads one `a:r`, appending its text (from `a:t`) to dst.
func consumeARun(d *xml.Decoder, dst *strings.Builder) {
	for {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == nsA && t.Name.Local == "t" {
				for {
					inner, err := d.Token()
					if err != nil {
						return
					}
					if c, ok := inner.(xml.CharData); ok {
						dst.WriteString(string(c))
						continue
					}
					if end, ok := inner.(xml.EndElement); ok && end.Name.Space == nsA && end.Name.Local == "t" {
						break
					}
				}
			} else {
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsA && t.Name.Local == "r" {
				return
			}
		}
	}
}

// extractNotesText pulls plain text from a notesSlide XML, stripping the
// placeholder whose `p:ph@type="sldNum"` holds the auto-inserted slide-number.
func extractNotesText(xmlBytes []byte) string {
	if len(xmlBytes) == 0 {
		return ""
	}
	d := xml.NewDecoder(bytes.NewReader(xmlBytes))
	var notes []string

	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.Join(notes, "\n")
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Space == nsP && start.Name.Local == "sp" {
			isNum, _, text := consumeNotesShape(d)
			if !isNum && text != "" {
				notes = append(notes, text)
			}
		}
	}
	out := strings.TrimSpace(strings.Join(notes, "\n"))
	// Notes slides contain a separator run of empty lines; collapse.
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

// consumeNotesShape returns whether the shape is the slide-number
// placeholder, its placeholder type, and its accumulated text.
func consumeNotesShape(d *xml.Decoder) (isSlideNum bool, phType, text string) {
	var buf bytes.Buffer
	for {
		tok, err := d.Token()
		if err != nil {
			return isSlideNum, phType, buf.String()
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Space == nsP && t.Name.Local == "nvSpPr":
				phType = readPhType(d)
				if phType == "sldNum" {
					isSlideNum = true
				}
			case t.Name.Space == nsP && t.Name.Local == "txBody":
				consumeTxBody(d, &buf, false)
			default:
				d.Skip()
			}
		case xml.EndElement:
			if t.Name.Space == nsP && t.Name.Local == "sp" {
				// Strip our bullet markers for notes — they're prose, not a list.
				text = stripBulletMarkers(buf.String())
				return isSlideNum, phType, text
			}
		}
	}
}

// readPhType pulls `p:ph@type` out of a `p:nvSpPr` wrapper, or "" if absent.
// Uses the same depth-tracking approach as isTitlePlaceholder because `p:ph`
// lives nested inside `p:nvPr`, not as a direct child of `p:nvSpPr`.
func readPhType(d *xml.Decoder) string {
	var out string
	depth := 1 // caller has already consumed the `p:nvSpPr` StartElement
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Space == nsP && t.Name.Local == "ph" {
				out = attrValue(t, "", "type")
			}
		case xml.EndElement:
			depth--
		}
	}
	return out
}

// stripBulletMarkers removes the `- ` prefix consumeTxBody adds in non-
// title mode — speaker notes are flowing prose, not a bulleted list.
func stripBulletMarkers(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trim := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trim, "- ") {
			lines[i] = strings.TrimPrefix(trim, "- ")
		}
	}
	return strings.Join(lines, "\n")
}

