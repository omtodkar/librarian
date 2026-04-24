package office

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
)

// minimalDocx builds a .docx archive with one paragraph. Returned bytes pass
// through convertDocx without relying on optional parts (numbering.xml,
// rels) that the converter treats as absent-OK.
func minimalDocx(bodyXML string) []byte {
	const shell = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
            xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <w:body>%s</w:body>
</w:document>`
	return []byte(strings.Replace(shell, "%s", bodyXML, 1))
}

func TestDocx_MinimalParagraph(t *testing.T) {
	body := `<w:p><w:r><w:t>Hello world</w:t></w:r></w:p>`
	zip := buildZip(t, map[string][]byte{
		"word/document.xml": minimalDocx(body),
	})

	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	if !strings.Contains(string(md), "Hello world") {
		t.Errorf("expected markdown to contain paragraph text; got:\n%s", md)
	}
}

// Heading1/2/3 must map to # / ## / ### — the core information-architecture
// contract. A regression would flatten all headings to plain paragraphs.
func TestDocx_HeadingsPreserveLevels(t *testing.T) {
	body := `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Top</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Middle</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading3"/></w:pPr><w:r><w:t>Deep</w:t></w:r></w:p>
`
	zip := buildZip(t, map[string][]byte{"word/document.xml": minimalDocx(body)})
	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	s := string(md)
	for _, want := range []string{"# Top", "## Middle", "### Deep"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing heading %q in:\n%s", want, s)
		}
	}
}

// Bullet vs numbered lists are determined by numbering.xml; w:numId=0 means
// "no list", rendered as a plain paragraph. Nested bullets via w:ilvl must
// survive with 2-space indentation per level.
func TestDocx_ListsBulletAndNumbered(t *testing.T) {
	body := `
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>
  <w:r><w:t>bullet a</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr>
  <w:r><w:t>bullet a.1</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr>
  <w:r><w:t>numbered one</w:t></w:r></w:p>
`
	numbering := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:abstractNum w:abstractNumId="0">
    <w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl>
    <w:lvl w:ilvl="1"><w:numFmt w:val="bullet"/></w:lvl>
  </w:abstractNum>
  <w:abstractNum w:abstractNumId="1">
    <w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl>
  </w:abstractNum>
  <w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
  <w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>
</w:numbering>`)
	zip := buildZip(t, map[string][]byte{
		"word/document.xml":  minimalDocx(body),
		"word/numbering.xml": numbering,
	})
	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "- bullet a") {
		t.Errorf("top-level bullet missing:\n%s", s)
	}
	if !strings.Contains(s, "  - bullet a.1") {
		t.Errorf("indented bullet missing (expected 2-space indent):\n%s", s)
	}
	if !strings.Contains(s, "1. numbered one") {
		t.Errorf("numbered item missing:\n%s", s)
	}
}

// Tables become GFM with a synthesised separator row; cells with pipes must
// be escaped so downstream markdown parsers don't split the row mid-cell.
func TestDocx_TableRendersAsGFM(t *testing.T) {
	body := `
<w:tbl>
  <w:tr>
    <w:tc><w:p><w:r><w:t>Name</w:t></w:r></w:p></w:tc>
    <w:tc><w:p><w:r><w:t>Role</w:t></w:r></w:p></w:tc>
  </w:tr>
  <w:tr>
    <w:tc><w:p><w:r><w:t>Ada</w:t></w:r></w:p></w:tc>
    <w:tc><w:p><w:r><w:t>CS | pioneer</w:t></w:r></w:p></w:tc>
  </w:tr>
</w:tbl>
`
	zip := buildZip(t, map[string][]byte{"word/document.xml": minimalDocx(body)})
	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "| Name | Role |") {
		t.Errorf("header row missing:\n%s", s)
	}
	if !strings.Contains(s, "| --- | --- |") {
		t.Errorf("separator row missing:\n%s", s)
	}
	if !strings.Contains(s, `CS \| pioneer`) {
		t.Errorf("cell pipe not escaped:\n%s", s)
	}
}

// External hyperlinks land as [text](url); internal bookmark links lose
// their target and become plain text.
func TestDocx_HyperlinksExternalAndInternal(t *testing.T) {
	body := `
<w:p>
  <w:hyperlink r:id="rId1"><w:r><w:t>Docs</w:t></w:r></w:hyperlink>
  <w:r><w:t> and </w:t></w:r>
  <w:hyperlink w:anchor="_bookmark"><w:r><w:t>Internal</w:t></w:r></w:hyperlink>
</w:p>
`
	rels := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1"
                Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink"
                Target="https://example.com/docs" TargetMode="External"/>
</Relationships>`)
	zip := buildZip(t, map[string][]byte{
		"word/document.xml":            minimalDocx(body),
		"word/_rels/document.xml.rels": rels,
	})
	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "[Docs](https://example.com/docs)") {
		t.Errorf("external link not rendered:\n%s", s)
	}
	if strings.Contains(s, "(Internal)") || strings.Contains(s, "[Internal]") {
		t.Errorf("internal link should flatten to plain text, got:\n%s", s)
	}
	if !strings.Contains(s, "Internal") {
		t.Errorf("internal link text missing:\n%s", s)
	}
}

// DocxHandler.Parse must delegate the generated markdown through the
// markdown handler and then override Format/DocType so downstream consumers
// can filter by "docx". This is the core contract of the package.
func TestDocxHandler_ParseOverridesFormat(t *testing.T) {
	// H1 + H2 so the markdown parser (which treats the first H1 as document
	// title) emits at least one section Unit under it.
	body := `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Design Notes</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Overview</w:t></w:r></w:p>
<w:p><w:r><w:t>Intro paragraph with some text.</w:t></w:r></w:p>
`
	zipBytes := buildZip(t, map[string][]byte{"word/document.xml": minimalDocx(body)})

	h := NewDocx(DefaultConfig())
	doc, err := h.Parse("ideas/notes.docx", zipBytes)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "docx" {
		t.Errorf("Format = %q, want %q", doc.Format, "docx")
	}
	if doc.DocType != "docx" {
		t.Errorf("DocType = %q, want %q", doc.DocType, "docx")
	}
	// H2 section must surface in the unit tree; body paragraph text must
	// end up inside it.
	found := false
	for _, u := range doc.Units {
		if u.Title == "Overview" || strings.Contains(u.Content, "Intro paragraph") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Overview section or body content in Units; got %+v", doc.Units)
	}
}

// Malformed numbering.xml must NOT abort the whole conversion — Word itself
// tolerates corrupt numbering parts and renders body text. Regression guard:
// the earlier behaviour of bubbling the error would hide all document text
// for a document with recoverable metadata damage.
func TestDocx_MalformedNumberingDoesNotAbortParse(t *testing.T) {
	body := `<w:p><w:r><w:t>Real content survives.</w:t></w:r></w:p>`
	broken := []byte(`<?xml version="1.0"?><w:numbering><w:abstractNum`) // truncated
	zipBytes := buildZip(t, map[string][]byte{
		"word/document.xml":  minimalDocx(body),
		"word/numbering.xml": broken,
	})
	md, err := convertDocx(zipBytes)
	if err != nil {
		t.Fatalf("convertDocx should have recovered from broken numbering.xml, got: %v", err)
	}
	if !strings.Contains(string(md), "Real content survives.") {
		t.Errorf("body text missing after numbering fallback:\n%s", md)
	}
}

// An empty paragraph carrying a `Heading1` style is legitimate Word usage
// (decorative spacing). The converter must not emit `# \n\n` — malformed
// markdown that trips strict parsers. Regression guard for the fix in
// flushParagraph that drops heading/list markers on empty text.
func TestDocx_EmptyHeadingParagraphEmitsNoMarker(t *testing.T) {
	body := `
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr></w:p>
<w:p><w:r><w:t>after-empty-heading</w:t></w:r></w:p>
`
	zipBytes := buildZip(t, map[string][]byte{"word/document.xml": minimalDocx(body)})
	md, err := convertDocx(zipBytes)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	s := string(md)
	// The pathological "# " (hash followed by space followed by newline) must
	// not appear anywhere in the output.
	if strings.Contains(s, "# \n") {
		t.Errorf("empty Heading1 paragraph emitted malformed `# ` marker:\n%q", s)
	}
	if !strings.Contains(s, "after-empty-heading") {
		t.Errorf("following paragraph lost:\n%s", s)
	}
}

// Malformed ZIP must return a wrapped error, not panic.
func TestDocx_MalformedZip(t *testing.T) {
	_, err := convertDocx([]byte("not a zip"))
	if err == nil {
		t.Error("expected error for non-zip bytes")
	}
}

// Empty document.xml yields empty markdown (Parse downstream gets an empty
// ParsedDoc with Format set — still valid for the indexer).
func TestDocx_EmptyDocumentXML(t *testing.T) {
	zip := buildZip(t, map[string][]byte{"word/document.xml": []byte{}})
	md, err := convertDocx(zip)
	if err != nil {
		t.Fatalf("convertDocx: %v", err)
	}
	if len(md) != 0 {
		t.Errorf("expected empty markdown for empty document.xml, got %q", md)
	}
}

// Registration sanity — the default registry must resolve .docx to a handler.
func TestDocxHandler_RegisteredByDefault(t *testing.T) {
	if h := indexer.DefaultRegistry().HandlerFor("x.docx"); h == nil {
		t.Error(".docx extension not registered in DefaultRegistry")
	}
}
