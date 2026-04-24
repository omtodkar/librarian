package office

import (
	"strconv"
	"strings"
	"testing"

	"librarian/internal/indexer"
)

// slideXML renders one minimal slide with an optional title shape and one
// body shape containing paragraphs at given bullet levels.
func slideXML(title string, bodyParas []pptxPara) []byte {
	const shell = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
       xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>%s%s</p:spTree></p:cSld>
</p:sld>`
	var titleXML string
	if title != "" {
		titleXML = `
      <p:sp>
        <p:nvSpPr><p:nvPr><p:ph type="title"/></p:nvPr></p:nvSpPr>
        <p:txBody><a:p><a:r><a:t>` + title + `</a:t></a:r></a:p></p:txBody>
      </p:sp>`
	}
	var bodyXMLParts []string
	for _, p := range bodyParas {
		lvlAttr := ""
		if p.Lvl > 0 {
			lvlAttr = ` lvl="` + strconv.Itoa(p.Lvl) + `"`
		}
		bodyXMLParts = append(bodyXMLParts, `
        <a:p><a:pPr`+lvlAttr+`/><a:r><a:t>`+p.Text+`</a:t></a:r></a:p>`)
	}
	bodyXML := ""
	if len(bodyXMLParts) > 0 {
		bodyXML = `
      <p:sp>
        <p:nvSpPr><p:nvPr/></p:nvSpPr>
        <p:txBody>` + strings.Join(bodyXMLParts, "") + `</p:txBody>
      </p:sp>`
	}
	return []byte(strings.Replace(strings.Replace(shell, "%s", titleXML, 1), "%s", bodyXML, 1))
}

type pptxPara struct {
	Lvl  int
	Text string
}

// minimalPresentation builds a presentation.xml referencing N slides via
// rIds `rId1`..`rIdN` in order.
func minimalPresentation(n int) ([]byte, []byte) {
	const pres = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>%s</p:sldIdLst>
</p:presentation>`
	var ids, rels []string
	for i := 1; i <= n; i++ {
		ids = append(ids, `<p:sldId id="`+strconv.Itoa(255+i)+`" r:id="rId`+strconv.Itoa(i)+`"/>`)
		rels = append(rels, `<Relationship Id="rId`+strconv.Itoa(i)+`"
			Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide"
			Target="slides/slide`+strconv.Itoa(i)+`.xml"/>`)
	}
	relsDoc := `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  ` + strings.Join(rels, "\n  ") + `
</Relationships>`
	return []byte(strings.Replace(pres, "%s", strings.Join(ids, "\n  "), 1)), []byte(relsDoc)
}

// A slide with title + two bullet levels produces `## Slide 1: <title>`
// and correctly indented list items — the core info-architecture contract.
func TestPptx_TitleAndBulletLevels(t *testing.T) {
	pres, presRels := minimalPresentation(1)
	files := map[string][]byte{
		"ppt/presentation.xml":               pres,
		"ppt/_rels/presentation.xml.rels":    presRels,
		"ppt/slides/slide1.xml": slideXML("Intro", []pptxPara{
			{Lvl: 0, Text: "bullet a"},
			{Lvl: 1, Text: "bullet a.1"},
			{Lvl: 0, Text: "bullet b"},
		}),
	}
	md, err := convertPptx(buildZip(t, files), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPptx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "## Slide 1: Intro") {
		t.Errorf("title heading missing:\n%s", s)
	}
	if !strings.Contains(s, "- bullet a") {
		t.Errorf("top-level bullet missing:\n%s", s)
	}
	if !strings.Contains(s, "  - bullet a.1") {
		t.Errorf("nested bullet indent missing:\n%s", s)
	}
}

// Slide without a title placeholder falls back to `## Slide N`.
func TestPptx_SlideWithoutTitle(t *testing.T) {
	pres, presRels := minimalPresentation(1)
	files := map[string][]byte{
		"ppt/presentation.xml":            pres,
		"ppt/_rels/presentation.xml.rels": presRels,
		"ppt/slides/slide1.xml": slideXML("", []pptxPara{
			{Lvl: 0, Text: "only body"},
		}),
	}
	md, err := convertPptx(buildZip(t, files), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPptx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "## Slide 1\n") {
		t.Errorf("title-less fallback heading missing:\n%s", s)
	}
	if !strings.Contains(s, "- only body") {
		t.Errorf("body missing:\n%s", s)
	}
}

// Multiple slides preserve presentation-order, not filename sort order.
// A file named slide2.xml declared before slide1.xml in sldIdLst should
// render in rId-declared order.
func TestPptx_MultipleSlidesOrdered(t *testing.T) {
	const pres = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
                xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId2"/>
    <p:sldId id="257" r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`
	const presRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
</Relationships>`
	files := map[string][]byte{
		"ppt/presentation.xml":            []byte(pres),
		"ppt/_rels/presentation.xml.rels": []byte(presRels),
		"ppt/slides/slide1.xml":           slideXML("First", nil),
		"ppt/slides/slide2.xml":           slideXML("Second", nil),
	}
	md, err := convertPptx(buildZip(t, files), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPptx: %v", err)
	}
	s := string(md)
	// Second should appear before First because rId2 is first in sldIdLst.
	secondIdx := strings.Index(s, "## Slide 1: Second")
	firstIdx := strings.Index(s, "## Slide 2: First")
	if secondIdx < 0 || firstIdx < 0 {
		t.Fatalf("slides missing in output:\n%s", s)
	}
	if secondIdx >= firstIdx {
		t.Errorf("slides out of presentation order:\n%s", s)
	}
}

// Speaker notes append a `### Notes` section when IncludeSpeakerNotes is
// true; disabling the flag drops them entirely.
func TestPptx_SpeakerNotesRespectConfig(t *testing.T) {
	pres, presRels := minimalPresentation(1)
	const notes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"
         xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree>
    <p:sp>
      <p:nvSpPr><p:nvPr><p:ph type="body"/></p:nvPr></p:nvSpPr>
      <p:txBody><a:p><a:r><a:t>Remember to pause.</a:t></a:r></a:p></p:txBody>
    </p:sp>
  </p:spTree></p:cSld>
</p:notes>`
	const slideRels = `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rIdN"
                Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide"
                Target="../notesSlides/notesSlide1.xml"/>
</Relationships>`
	files := map[string][]byte{
		"ppt/presentation.xml":            pres,
		"ppt/_rels/presentation.xml.rels": presRels,
		"ppt/slides/slide1.xml":           slideXML("Hello", nil),
		"ppt/slides/_rels/slide1.xml.rels": []byte(slideRels),
		"ppt/notesSlides/notesSlide1.xml":  []byte(notes),
	}
	// Enabled
	md, err := convertPptx(buildZip(t, files), DefaultConfig())
	if err != nil {
		t.Fatalf("convertPptx (notes on): %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "### Notes") {
		t.Errorf("notes section missing when IncludeSpeakerNotes=true:\n%s", s)
	}
	if !strings.Contains(s, "Remember to pause.") {
		t.Errorf("notes body missing:\n%s", s)
	}

	// Disabled
	off := DefaultConfig()
	off.IncludeSpeakerNotes = false
	md2, err := convertPptx(buildZip(t, files), off)
	if err != nil {
		t.Fatalf("convertPptx (notes off): %v", err)
	}
	if strings.Contains(string(md2), "### Notes") {
		t.Errorf("notes section emitted when IncludeSpeakerNotes=false:\n%s", md2)
	}
}

// Registration sanity.
func TestPptxHandler_RegisteredByDefault(t *testing.T) {
	if h := indexer.DefaultRegistry().HandlerFor("x.pptx"); h == nil {
		t.Error(".pptx extension not registered")
	}
}

// Handler.Parse overrides Format/DocType.
func TestPptxHandler_ParseOverridesFormat(t *testing.T) {
	pres, presRels := minimalPresentation(1)
	files := map[string][]byte{
		"ppt/presentation.xml":            pres,
		"ppt/_rels/presentation.xml.rels": presRels,
		"ppt/slides/slide1.xml": slideXML("Title", []pptxPara{
			{Lvl: 0, Text: "body text for chunking"},
		}),
	}
	h := NewPptx(DefaultConfig())
	doc, err := h.Parse("decks/q4.pptx", buildZip(t, files))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "pptx" {
		t.Errorf("Format = %q, want %q", doc.Format, "pptx")
	}
}

// Malformed ZIP must be rejected.
func TestPptx_MalformedZip(t *testing.T) {
	_, err := convertPptx([]byte("not a zip"), DefaultConfig())
	if err == nil {
		t.Error("expected error for non-zip bytes")
	}
}
