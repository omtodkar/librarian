package office

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// convertXlsx produces markdown with one `## <Sheet Name>` section per sheet,
// each followed by a GFM table. Row and column caps from cfg limit the
// per-sheet rendering; truncated sheets append a `_[truncated: N more rows]_`
// note. Empty sheets emit only the heading.
//
// Formula evaluation: excelize returns the workbook's cached calculation
// value; sheets without cached values render the formula cell as empty.
// Merged cells: the top-left cell holds the value, the others render
// empty — a documented v1 limitation tracked as a follow-up.
func convertXlsx(content []byte, cfg Config) ([]byte, error) {
	// Apply the same ZIP-bomb guard DOCX/PPTX get — excelize opens the
	// archive internally with no size limits otherwise, so a crafted XLSX
	// with a high compression ratio could exhaust memory before reaching
	// any per-sheet cap we set here.
	if _, err := openZip(content); err != nil {
		return nil, err
	}

	f, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("opening xlsx: %w", err)
	}
	defer f.Close()

	var out bytes.Buffer
	for _, sheet := range f.GetSheetList() {
		writeSheet(&out, f, sheet, cfg)
	}
	return out.Bytes(), nil
}

// writeSheet renders one worksheet into out. The first row is treated as the
// table header; when no rows are present, only the heading is written.
func writeSheet(out *bytes.Buffer, f *excelize.File, sheet string, cfg Config) {
	fmt.Fprintf(out, "## %s\n\n", sheet)

	rows, err := f.GetRows(sheet)
	if err != nil || len(rows) == 0 {
		return
	}

	maxRows := cfg.XLSXMaxRows
	if maxRows <= 0 {
		maxRows = 100
	}
	maxCols := cfg.XLSXMaxCols
	if maxCols <= 0 {
		maxCols = 50
	}

	limit := len(rows)
	if limit > maxRows {
		limit = maxRows
	}

	// Compute the widest row within the visible window so header padding
	// matches the data; excelize drops trailing empty cells so rows can be
	// ragged on the right edge.
	width := 0
	for i := 0; i < limit; i++ {
		if len(rows[i]) > width {
			width = len(rows[i])
		}
	}
	if width == 0 {
		return
	}
	if width > maxCols {
		width = maxCols
	}

	writeGFMTable(out, rows[:limit], width)

	if len(rows) > maxRows {
		fmt.Fprintf(out, "\n_[truncated: %d more rows]_\n", len(rows)-maxRows)
	}
	out.WriteByte('\n')
}
