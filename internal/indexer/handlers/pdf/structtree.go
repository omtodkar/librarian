package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
)

// tryStructTree is tier 1 of the cascade. For each page it opens the
// tagged struct tree (FPDF_StructTree_GetForPage) and walks the element
// hierarchy, mapping semantic PDF tags (Title / H1..H6 / P / L / LI /
// Table) into markdown. When no page carries any typed element, the
// function falls through — the caller tries bookmarks next.
//
// Per-page errors from PDFium are tolerated: a single corrupt page
// shouldn't hide the whole document's tags. We only abandon the tier if
// every page errors or returns empty.
func tryStructTree(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) ([]byte, bool) {
	var out bytes.Buffer
	st := &structWalker{inst: inst}

	any := false
	for i := 0; i < pages; i++ {
		if emitStructPage(inst, doc, i, st, &out) {
			any = true
		}
	}

	if !any {
		return nil, false
	}
	return out.Bytes(), true
}

// emitStructPage runs the struct-tree walk for a single page. Extracted so
// the page and struct-tree handles can be released via defer — a panic
// between LoadPage and the explicit close would otherwise leak both in
// the loop body.
func emitStructPage(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, index int, st *structWalker, out *bytes.Buffer) bool {
	page, err := inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc, Index: index})
	if err != nil {
		return false
	}
	pageRef := page.Page
	defer inst.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: pageRef})

	treeResp, err := inst.FPDF_StructTree_GetForPage(&requests.FPDF_StructTree_GetForPage{
		Page: requests.Page{ByReference: &pageRef},
	})
	if err != nil {
		return false
	}
	tree := treeResp.StructTree
	defer inst.FPDF_StructTree_Close(&requests.FPDF_StructTree_Close{StructTree: tree})

	countResp, err := inst.FPDF_StructTree_CountChildren(&requests.FPDF_StructTree_CountChildren{StructTree: tree})
	if err != nil || countResp.Count == 0 {
		return false
	}
	wrote := false
	for j := 0; j < countResp.Count; j++ {
		child, err := inst.FPDF_StructTree_GetChildAtIndex(&requests.FPDF_StructTree_GetChildAtIndex{
			StructTree: tree,
			Index:      j,
		})
		if err != nil {
			continue
		}
		if st.emit(out, child.StructElement, 0) {
			wrote = true
		}
	}
	return wrote
}

// structWalker carries the PDFium instance + one-time state (whether a
// Title has already been emitted) across a depth-first walk of the
// tagged-element tree.
type structWalker struct {
	inst         pdfium.Pdfium
	titleEmitted bool
}

// elementType reads an element's PDF tag ("H1", "P", "LI", …). Returns
// false when PDFium reports an error — callers skip the element. Trim is
// applied because some taggers emit whitespace around tag names.
func (w *structWalker) elementType(el references.FPDF_STRUCTELEMENT) (string, bool) {
	resp, err := w.inst.FPDF_StructElement_GetType(&requests.FPDF_StructElement_GetType{StructElement: el})
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(resp.Type), true
}

// forEachChild iterates el's child struct elements in document order,
// calling fn for each. Errors on CountChildren abandon the walk; errors
// on individual GetChildAtIndex calls skip that child only. Centralises
// the boilerplate that the three tree-walking sites would otherwise
// duplicate.
func (w *structWalker) forEachChild(el references.FPDF_STRUCTELEMENT, fn func(references.FPDF_STRUCTELEMENT)) {
	countResp, err := w.inst.FPDF_StructElement_CountChildren(&requests.FPDF_StructElement_CountChildren{StructElement: el})
	if err != nil {
		return
	}
	for i := 0; i < countResp.Count; i++ {
		childResp, err := w.inst.FPDF_StructElement_GetChildAtIndex(&requests.FPDF_StructElement_GetChildAtIndex{
			StructElement: el,
			Index:         i,
		})
		if err != nil {
			continue
		}
		fn(childResp.StructElement)
	}
}

// emit writes a single struct element plus its descendants. Returns true
// if anything was written — the caller uses this to decide tier
// viability.
func (w *structWalker) emit(out *bytes.Buffer, el references.FPDF_STRUCTELEMENT, listDepth int) bool {
	tag, ok := w.elementType(el)
	if !ok {
		return false
	}
	text := w.elementText(el)
	wrote := false

	switch tag {
	case "Document", "Sect", "Part", "Art", "Div", "NonStruct":
		// Container — recurse into children without emitting markup.
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "Title":
		if text != "" {
			if w.titleEmitted {
				fmt.Fprintf(out, "## %s\n\n", escapeInline(text))
			} else {
				fmt.Fprintf(out, "# %s\n\n", escapeInline(text))
				w.titleEmitted = true
			}
			wrote = true
		}
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "H1", "H2", "H3", "H4", "H5", "H6":
		if text != "" {
			level := int(tag[1] - '0')
			fmt.Fprintf(out, "%s %s\n\n", strings.Repeat("#", level), escapeInline(text))
			wrote = true
		}
		// Headings in tagged PDFs are usually leaf elements, but recurse
		// defensively in case an author nested content inside.
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "H":
		// Generic heading without level. Treat as H2 — most common
		// interpretation when a tagger doesn't distinguish levels.
		if text != "" {
			fmt.Fprintf(out, "## %s\n\n", escapeInline(text))
			wrote = true
		}
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "P", "Caption", "Quote", "Note", "BlockQuote":
		if text != "" {
			out.WriteString(escapeInline(text))
			out.WriteString("\n\n")
			wrote = true
		}
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "L":
		// List container — recurse; its LI children carry the bullets.
		wrote = w.emitChildren(out, el, listDepth+1) || wrote

	case "LI", "LBody":
		indent := strings.Repeat("  ", max(0, listDepth-1))
		if text != "" {
			fmt.Fprintf(out, "%s- %s\n", indent, escapeInline(text))
			wrote = true
		}
		// Nested lists inside a list item — recurse at the same depth so
		// the child L bumps it.
		wrote = w.emitChildren(out, el, listDepth) || wrote

	case "Lbl":
		// List label — inlined into the LI already; skip.

	case "Table":
		wrote = w.emitTable(out, el) || wrote

	case "Figure":
		if alt := w.altTextFor(el); alt != "" {
			fmt.Fprintf(out, "![%s](omitted)\n\n", escapeInline(alt))
			wrote = true
		}

	default:
		// Unknown / unsupported tag — recurse so nested P/H isn't lost.
		wrote = w.emitChildren(out, el, listDepth) || wrote
	}

	// After a list item finishes, emit a trailing blank line at depth 0
	// so the next block starts cleanly.
	if tag == "LI" && listDepth == 1 {
		out.WriteString("\n")
	}
	return wrote
}

// emitTable walks a Table → TR → (TH|TD) subtree and writes a GFM table.
// Rows with no cells are skipped; a table with zero usable rows emits
// nothing.
func (w *structWalker) emitTable(out *bytes.Buffer, el references.FPDF_STRUCTELEMENT) bool {
	var rows [][]string
	w.forEachChild(el, func(child references.FPDF_STRUCTELEMENT) {
		tag, ok := w.elementType(child)
		if !ok {
			return
		}
		switch tag {
		case "TR":
			if cells := w.extractRow(child); len(cells) > 0 {
				rows = append(rows, cells)
			}
		case "THead", "TBody", "TFoot":
			// Section grouping — one level deeper to find TRs.
			w.forEachChild(child, func(tr references.FPDF_STRUCTELEMENT) {
				if cells := w.extractRow(tr); len(cells) > 0 {
					rows = append(rows, cells)
				}
			})
		}
	})
	if len(rows) == 0 {
		return false
	}
	writeGFMTable(out, rows)
	out.WriteString("\n")
	return true
}

// extractRow pulls cell text from a TR subtree. TH / TD are both treated
// as cells; position inside the row determines header vs body via
// writeGFMTable's convention that row 0 is the header.
func (w *structWalker) extractRow(tr references.FPDF_STRUCTELEMENT) []string {
	var cells []string
	w.forEachChild(tr, func(cell references.FPDF_STRUCTELEMENT) {
		tag, ok := w.elementType(cell)
		if !ok || (tag != "TH" && tag != "TD") {
			return
		}
		cells = append(cells, w.elementText(cell))
	})
	return cells
}

// emitChildren recurses through an element's children in document order
// and returns true if any child emitted output.
func (w *structWalker) emitChildren(out *bytes.Buffer, el references.FPDF_STRUCTELEMENT, listDepth int) bool {
	wrote := false
	w.forEachChild(el, func(child references.FPDF_STRUCTELEMENT) {
		if w.emit(out, child, listDepth) {
			wrote = true
		}
	})
	return wrote
}

// elementText resolves the text content for a struct element. PDF tagging
// specs prefer ActualText, fall back to Title when ActualText is absent,
// and otherwise rely on Marked-Content-ID linking (too deep to walk
// usefully here — a tagger that omits both fields will lose that
// element's text, which the heuristic tier can recover if needed).
func (w *structWalker) elementText(el references.FPDF_STRUCTELEMENT) string {
	if actual, err := w.inst.FPDF_StructElement_GetActualText(&requests.FPDF_StructElement_GetActualText{StructElement: el}); err == nil {
		if s := strings.TrimSpace(actual.Actualtext); s != "" {
			return s
		}
	}
	if title, err := w.inst.FPDF_StructElement_GetTitle(&requests.FPDF_StructElement_GetTitle{StructElement: el}); err == nil {
		if s := strings.TrimSpace(title.Title); s != "" {
			return s
		}
	}
	return ""
}

// altTextFor returns the Figure element's alt text, empty when absent.
func (w *structWalker) altTextFor(el references.FPDF_STRUCTELEMENT) string {
	alt, err := w.inst.FPDF_StructElement_GetAltText(&requests.FPDF_StructElement_GetAltText{StructElement: el})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(alt.AltText)
}
