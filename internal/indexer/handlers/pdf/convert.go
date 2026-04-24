package pdf

import (
	"fmt"

	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
)

// Source labels record which cascade tier produced the markdown. They're
// written to Metadata["pdf.structure_source"] (handler.go) so downstream
// debugging can tell which tier won for a given document.
const (
	sourceStructTree = "struct-tree"
	sourceBookmarks  = "bookmarks"
	sourceHeuristic  = "heuristic"
	sourcePages      = "pages"
)

// convertPDF opens the bytes in PDFium, walks the structure cascade, and
// returns the first viable tier's markdown output alongside the tier
// label. The caller (Handler.Parse) records the label on ParsedDoc
// Metadata for diagnostics.
func convertPDF(content []byte, cfg Config) ([]byte, string, error) {
	if len(content) == 0 {
		return nil, sourcePages, nil
	}
	if len(content) > maxPDFBytes {
		return nil, "", fmt.Errorf("pdf too large: %d bytes (limit %d)", len(content), maxPDFBytes)
	}

	inst, release, err := getInstance()
	if err != nil {
		return nil, "", fmt.Errorf("pdfium init: %w", err)
	}
	defer release()

	openResp, err := inst.OpenDocument(&requests.OpenDocument{File: &content})
	if err != nil {
		return nil, "", fmt.Errorf("open pdf: %w", err)
	}
	docRef := openResp.Document
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: docRef})

	countResp, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: docRef})
	if err != nil {
		return nil, "", fmt.Errorf("get page count: %w", err)
	}
	pages := countResp.PageCount
	if cfg.MaxPages > 0 && pages > cfg.MaxPages {
		pages = cfg.MaxPages
	}
	if pages <= 0 {
		return nil, sourcePages, nil
	}

	if md, ok := tryStructTree(inst, docRef, pages); ok {
		return md, sourceStructTree, nil
	}
	if md, ok := tryBookmarks(inst, docRef, pages); ok {
		return md, sourceBookmarks, nil
	}
	if md, ok := tryHeuristic(inst, docRef, pages); ok {
		return md, sourceHeuristic, nil
	}
	md, err := flatByPage(inst, docRef, pages)
	if err != nil {
		return nil, "", fmt.Errorf("flat page fallback: %w", err)
	}
	return md, sourcePages, nil
}

// pageRef builds the by-index Page argument accepted by the page-scoped
// PDFium calls. Extracted because every tier needs it.
func pageRef(doc references.FPDF_DOCUMENT, i int) requests.Page {
	return requests.Page{
		ByIndex: &requests.PageByIndex{
			Document: doc,
			Index:    i,
		},
	}
}
