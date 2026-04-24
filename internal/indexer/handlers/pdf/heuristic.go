package pdf

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// tryHeuristic is tier 3 of the cascade: font-size clustering over the
// structured text extraction. Sizes strictly larger than the most-common
// (body) size become headings: largest → H2, next → H3, third → H4.
// Bold weight at body size is promoted to H5 so a terminal "sub-heading"
// style doesn't collide with the smallest numeric heading tier.
//
// Why H2 and not H1: the markdown handler treats the first H1 as the
// document title; a heuristically-detected heading isn't reliable enough
// to claim that role, and the same choice applies in tryBookmarks
// (`b.depth + 2`).
//
// Falls through when:
//   - PDFium returns no font information (some producers strip it).
//   - Only one size exists across the whole doc (uniform font — no
//     detectable heading signal).
//   - The mapping yields zero heading-classed rects.
func tryHeuristic(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) ([]byte, bool) {
	type pageRects struct {
		index int
		rects []*responses.GetPageTextStructuredRect
	}

	var collected []pageRects
	sizeCounts := map[float64]int{}

	for i := 0; i < pages; i++ {
		resp, err := inst.GetPageTextStructured(&requests.GetPageTextStructured{
			Page:                   pageRef(doc, i),
			Mode:                   requests.GetPageTextStructuredModeRects,
			CollectFontInformation: true,
		})
		if err != nil {
			continue
		}
		if len(resp.Rects) == 0 {
			continue
		}
		collected = append(collected, pageRects{index: i, rects: resp.Rects})
		for _, r := range resp.Rects {
			if r.FontInformation == nil {
				continue
			}
			sizeCounts[roundSize(r.FontInformation.RenderedSize)]++
		}
	}

	if len(sizeCounts) < 2 {
		return nil, false
	}
	bodySize, headings := classifyHeadings(sizeCounts)
	if len(headings) == 0 {
		return nil, false
	}

	var out bytes.Buffer
	any := false
	for _, pg := range collected {
		rects := sortReadingOrder(pg.rects)
		if emitHeuristicPage(&out, rects, bodySize, headings) {
			any = true
		}
	}
	if !any {
		return nil, false
	}
	return out.Bytes(), true
}

// classifyHeadings picks the body size (the mode of the font-size
// histogram) and builds the heading-tier map. Sizes strictly larger than
// the body size are heading candidates; the largest is the highest
// heading level (capped at H1 top, H3 bottom regardless of how many tiers
// exist).
func classifyHeadings(sizeCounts map[float64]int) (float64, map[float64]int) {
	type sc struct {
		size  float64
		count int
	}
	var all []sc
	for s, c := range sizeCounts {
		all = append(all, sc{size: s, count: c})
	}
	// Body = most common size. Ties broken by larger size so a body
	// mis-classified as heading is preferred over the other way round.
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].size > all[j].size
	})
	body := all[0].size

	var headingSizes []float64
	for _, s := range all {
		if s.size > body {
			headingSizes = append(headingSizes, s.size)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(headingSizes)))

	levels := map[float64]int{}
	for i, s := range headingSizes {
		level := i + 1
		if level > 3 {
			level = 3
		}
		levels[s] = level
	}
	return body, levels
}

// paragraphGapFactor: a vertical gap between rects larger than this
// multiple of the previous line height starts a new paragraph. Tight
// enough to keep justified body text together, loose enough to separate
// real paragraphs.
const paragraphGapFactor = 1.5

// emitHeuristicPage walks the sorted rects and emits markdown. A rect
// whose rounded size matches a heading tier becomes a heading line; bold
// text at body size is promoted to H5 (below the smallest numeric
// heading tier so the two signals don't collide). A paragraph break is
// inserted where the vertical gap between successive rects exceeds
// paragraphGapFactor × the previous line height.
func emitHeuristicPage(out *bytes.Buffer, rects []*responses.GetPageTextStructuredRect, bodySize float64, headings map[float64]int) bool {
	wrote := false
	var lastBottom, lastHeight float64
	inParagraph := false

	flushParagraph := func() {
		if inParagraph {
			out.WriteString("\n\n")
			inParagraph = false
		}
	}

	for _, r := range rects {
		text := strings.TrimSpace(r.Text)
		if text == "" {
			continue
		}
		size := 0.0
		weight := 0
		if r.FontInformation != nil {
			size = roundSize(r.FontInformation.RenderedSize)
			weight = r.FontInformation.Weight
		}

		lineHeight := r.PointPosition.Top - r.PointPosition.Bottom
		gap := lastBottom - r.PointPosition.Top
		if lastHeight > 0 && gap > paragraphGapFactor*lastHeight {
			flushParagraph()
		}
		lastBottom = r.PointPosition.Bottom
		if lineHeight > 0 {
			lastHeight = lineHeight
		}

		if level, ok := headings[size]; ok {
			flushParagraph()
			fmt.Fprintf(out, "%s %s\n\n", strings.Repeat("#", level+1), escapeInline(text))
			wrote = true
			continue
		}
		if weight >= 700 && size == bodySize {
			flushParagraph()
			fmt.Fprintf(out, "##### %s\n\n", escapeInline(text))
			wrote = true
			continue
		}

		if inParagraph {
			out.WriteString(" ")
		}
		out.WriteString(escapeInline(text))
		inParagraph = true
		wrote = true
	}
	flushParagraph()
	return wrote
}

// roundSize collapses trivially-different float sizes (10.0 vs 10.0001)
// into the same histogram bucket. 0.1 pt granularity is finer than any
// tagger bothers with but coarse enough to survive FP rounding.
func roundSize(size float64) float64 {
	return math.Round(size*10) / 10
}

// sameLineTolerancePt: two rects whose Top coordinates differ by less
// than this (in PDF points) are treated as being on the same line. Typical
// body-text baselines wobble a few tenths of a point due to descender
// handling; 2 pt is conservative without merging adjacent lines.
const sameLineTolerancePt = 2.0

// sortReadingOrder orders rects top-to-bottom, breaking ties left-to-right.
// PDFium returns rects in page-content order which is usually reading
// order, but not always — text objects can be emitted in arbitrary order.
func sortReadingOrder(rects []*responses.GetPageTextStructuredRect) []*responses.GetPageTextStructuredRect {
	out := make([]*responses.GetPageTextStructuredRect, len(rects))
	copy(out, rects)
	sort.SliceStable(out, func(i, j int) bool {
		// Higher Top = higher on the page. Invert so top-of-page rects
		// come first.
		if math.Abs(out[i].PointPosition.Top-out[j].PointPosition.Top) > sameLineTolerancePt {
			return out[i].PointPosition.Top > out[j].PointPosition.Top
		}
		return out[i].PointPosition.Left < out[j].PointPosition.Left
	})
	return out
}
