package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// tryBookmarks is tier 2 of the cascade. When the author has embedded an
// outline / TOC, each bookmark's depth determines heading level and its
// destination page bounds the body text that belongs under it. Body text
// on pages preceding the first bookmark (title pages, forewords) is
// emitted as an untitled leading block so search still has something to
// match.
//
// The tier falls through (returns false) when no bookmarks exist — not
// when the outline is shallow; a flat outline of ## headings is still
// more useful than a per-page fallback.
func tryBookmarks(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) ([]byte, bool) {
	resp, err := inst.GetBookmarks(&requests.GetBookmarks{Document: doc})
	if err != nil || len(resp.Bookmarks) == 0 {
		return nil, false
	}

	flat := flattenBookmarks(resp.Bookmarks, 0, pages)
	if len(flat) == 0 {
		return nil, false
	}
	// Depth-first flatten preserves parent-before-child ordering, which
	// is the invariant markdown hierarchy depends on. We deliberately do
	// NOT sort by startPage: out-of-order outlines (a child whose
	// destination page precedes its parent's) would otherwise produce
	// `###` before `##`, which the markdown chunker treats as
	// malformed. Body ranges are computed by scanning forward for the
	// next entry with a strictly greater startPage (see below) so
	// backward-pointing bookmarks don't corrupt the range either.

	var out bytes.Buffer

	// Leading text before the first bookmark — often cover pages or a
	// table of contents. Emit without a heading so it still gets indexed.
	if first := flat[0].startPage; first > 0 {
		leading := concatPageText(inst, doc, 0, first)
		if s := strings.TrimSpace(leading); s != "" {
			out.WriteString(s)
			out.WriteString("\n\n")
		}
	}

	for i, b := range flat {
		level := b.depth + 2 // depth 0 → H2 so the markdown handler treats it as a section
		if level > 6 {
			level = 6
		}
		fmt.Fprintf(&out, "%s %s\n\n", strings.Repeat("#", level), escapeInline(b.title))

		// Find the next entry whose start page is strictly greater —
		// skips same-page successors (nested sub-bookmarks that land on
		// the same page) and backward-pointing entries that would otherwise
		// produce an empty or inverted range.
		end := pages
		for j := i + 1; j < len(flat); j++ {
			if flat[j].startPage > b.startPage {
				end = flat[j].startPage
				break
			}
		}
		if end <= b.startPage {
			// Last real entry, or every successor lands at or before this
			// bookmark's page. Claim at least the landing page so the
			// heading has body text attached.
			end = b.startPage + 1
		}
		if end > pages {
			end = pages
		}
		if body := concatPageText(inst, doc, b.startPage, end); strings.TrimSpace(body) != "" {
			out.WriteString(strings.TrimSpace(body))
			out.WriteString("\n\n")
		}
	}

	return out.Bytes(), true
}

// flatBookmark is the depth-annotated shape fed to the range-and-emit
// loop. PDF bookmarks form a tree; markdown headings are a flat stream,
// so we flatten depth-first with each node's tree depth preserved.
type flatBookmark struct {
	title     string
	depth     int
	startPage int
}

func flattenBookmarks(bs []responses.GetBookmarksBookmark, depth int, pages int) []flatBookmark {
	var out []flatBookmark
	for _, b := range bs {
		title := strings.TrimSpace(b.Title)
		if title == "" {
			// Skip bookmarks without titles — PDFium occasionally returns
			// empty entries for invalid destinations. Their children
			// still contribute though.
			out = append(out, flattenBookmarks(b.Children, depth+1, pages)...)
			continue
		}
		start := 0
		if b.DestInfo != nil {
			start = b.DestInfo.PageIndex
		}
		if start < 0 {
			start = 0
		}
		if start >= pages {
			start = pages - 1
		}
		out = append(out, flatBookmark{title: title, depth: depth, startPage: start})
		out = append(out, flattenBookmarks(b.Children, depth+1, pages)...)
	}
	return out
}

// concatPageText collects plain text from pages [start, end) into one
// string. Errors on individual pages (corrupted streams, unsupported
// encodings) are silently skipped — the bookmark heading itself is still
// worth indexing even if the body is partially missing.
func concatPageText(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, start, end int) string {
	if start >= end {
		return ""
	}
	var buf bytes.Buffer
	for i := start; i < end; i++ {
		text, err := getPageText(inst, doc, i)
		if err != nil {
			continue
		}
		if text = strings.TrimSpace(text); text != "" {
			buf.WriteString(text)
			buf.WriteString("\n\n")
		}
	}
	return buf.String()
}
