package pdf

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
)

// flatByPage is the tier-4 always-viable fallback: one "## Page N"
// section per page with PDFium's plain-text extraction as body. Called
// when no structure source panned out — the page boundary at least gives
// the chunker a natural split and users a page-number citation.
func flatByPage(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, pages int) ([]byte, error) {
	var out bytes.Buffer
	for i := 0; i < pages; i++ {
		text, err := getPageText(inst, doc, i)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", i+1, err)
		}
		fmt.Fprintf(&out, "## Page %d\n\n", i+1)
		if text = strings.TrimSpace(text); text != "" {
			out.WriteString(text)
			out.WriteString("\n\n")
		}
	}
	return out.Bytes(), nil
}

// getPageText wraps GetPageText so every tier has a uniform way to pull
// plain text for a given 0-indexed page.
func getPageText(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, index int) (string, error) {
	resp, err := inst.GetPageText(&requests.GetPageText{Page: pageRef(doc, index)})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
