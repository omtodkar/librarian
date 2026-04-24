package office

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
	"librarian/internal/indexer"
)

// sheet pairs a sheet name with its row data. Ordered slice (not a map) so
// buildXlsx is deterministic — excelize's GetSheetList returns sheets in
// creation order, and tests that assert on that order would otherwise flake
// on Go's randomized map iteration.
type sheet struct {
	name string
	rows [][]string
}

// buildXlsx constructs an in-memory .xlsx using excelize so tests exercise
// the same parsing path production does, without relying on any binary
// fixture in the repo. Sheets are created in the order supplied.
func buildXlsx(t *testing.T, sheets []sheet) []byte {
	t.Helper()
	f := excelize.NewFile()
	for i, sh := range sheets {
		if i == 0 {
			// NewFile creates a default "Sheet1" — rename the first fixture
			// sheet into it instead of having both "Sheet1" and our desired
			// name present.
			if err := f.SetSheetName("Sheet1", sh.name); err != nil {
				t.Fatalf("rename Sheet1 → %s: %v", sh.name, err)
			}
		} else if _, err := f.NewSheet(sh.name); err != nil {
			t.Fatalf("new sheet %s: %v", sh.name, err)
		}
		for r, row := range sh.rows {
			for c, val := range row {
				cell, _ := excelize.CoordinatesToCellName(c+1, r+1)
				if err := f.SetCellValue(sh.name, cell, val); err != nil {
					t.Fatalf("set cell %s!%s: %v", sh.name, cell, err)
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	return buf.Bytes()
}

// Each sheet becomes an H2 section with the sheet name, followed by a GFM
// table. First row is the header; separator row is synthesised.
func TestXlsx_SheetsBecomeH2Sections(t *testing.T) {
	xlsx := buildXlsx(t, []sheet{
		{name: "People", rows: [][]string{
			{"Name", "Role"},
			{"Ada", "Pioneer"},
			{"Grace", "Engineer"},
		}},
	})
	md, err := convertXlsx(xlsx, DefaultConfig())
	if err != nil {
		t.Fatalf("convertXlsx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "## People") {
		t.Errorf("sheet heading missing:\n%s", s)
	}
	if !strings.Contains(s, "| Name | Role |") {
		t.Errorf("header row missing:\n%s", s)
	}
	if !strings.Contains(s, "| --- | --- |") {
		t.Errorf("separator row missing:\n%s", s)
	}
	if !strings.Contains(s, "| Ada | Pioneer |") {
		t.Errorf("data row missing:\n%s", s)
	}
}

// Row cap triggers the truncation marker and drops excess rows. Cols are
// capped similarly — we verify both in a single oversized sheet.
func TestXlsx_RowAndColCapsTruncate(t *testing.T) {
	// 5 rows × 4 cols; cap at 3 × 2 → should drop 2 rows (note: header is
	// kept inside the cap) and 2 cols.
	rows := [][]string{
		{"A", "B", "C", "D"},
		{"1", "2", "3", "4"},
		{"5", "6", "7", "8"},
		{"9", "10", "11", "12"},
		{"13", "14", "15", "16"},
	}
	xlsx := buildXlsx(t, []sheet{{name: "Data", rows: rows}})

	md, err := convertXlsx(xlsx, Config{XLSXMaxRows: 3, XLSXMaxCols: 2})
	if err != nil {
		t.Fatalf("convertXlsx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "| A | B |") {
		t.Errorf("header truncated to 2 cols missing:\n%s", s)
	}
	if strings.Contains(s, "| C |") || strings.Contains(s, "| D |") {
		t.Errorf("col-cap leaked columns:\n%s", s)
	}
	if !strings.Contains(s, "_[truncated: 2 more rows]_") {
		t.Errorf("row truncation marker missing:\n%s", s)
	}
}

// Cell containing a pipe must be escaped so the table row doesn't split.
func TestXlsx_CellPipeEscape(t *testing.T) {
	xlsx := buildXlsx(t, []sheet{
		{name: "M", rows: [][]string{
			{"A"},
			{"x | y"},
		}},
	})
	md, err := convertXlsx(xlsx, DefaultConfig())
	if err != nil {
		t.Fatalf("convertXlsx: %v", err)
	}
	if !strings.Contains(string(md), `x \| y`) {
		t.Errorf("pipe not escaped:\n%s", md)
	}
}

// Multiple sheets render in workbook order, each with its own H2 heading.
// Fixture explicitly orders Beta before Alpha so we catch any future
// regression where the converter sorts alphabetically instead of using
// workbook insertion order.
func TestXlsx_MultipleSheetsOrdered(t *testing.T) {
	xlsx := buildXlsx(t, []sheet{
		{name: "Beta", rows: [][]string{{"b1"}}},
		{name: "Alpha", rows: [][]string{{"a1"}}},
	})
	md, err := convertXlsx(xlsx, DefaultConfig())
	if err != nil {
		t.Fatalf("convertXlsx: %v", err)
	}
	s := string(md)
	betaIdx := strings.Index(s, "## Beta")
	alphaIdx := strings.Index(s, "## Alpha")
	if betaIdx < 0 || alphaIdx < 0 {
		t.Fatalf("missing sheet headings:\n%s", s)
	}
	if betaIdx > alphaIdx {
		t.Errorf("sheets out of workbook-insertion order (Beta should precede Alpha):\n%s", s)
	}
}

// Empty sheet emits only the heading, no malformed separator-only table.
func TestXlsx_EmptySheetHeadingOnly(t *testing.T) {
	xlsx := buildXlsx(t, []sheet{{name: "Blank"}})
	md, err := convertXlsx(xlsx, DefaultConfig())
	if err != nil {
		t.Fatalf("convertXlsx: %v", err)
	}
	s := string(md)
	if !strings.Contains(s, "## Blank") {
		t.Errorf("blank-sheet heading missing:\n%s", s)
	}
	if strings.Contains(s, "| --- |") {
		t.Errorf("blank sheet should not emit a separator row:\n%s", s)
	}
}

// Registration sanity.
func TestXlsxHandler_RegisteredByDefault(t *testing.T) {
	if h := indexer.DefaultRegistry().HandlerFor("x.xlsx"); h == nil {
		t.Error(".xlsx extension not registered")
	}
}

// Handler.Parse overrides Format/DocType so downstream filters distinguish
// spreadsheets from markdown.
func TestXlsxHandler_ParseOverridesFormat(t *testing.T) {
	xlsx := buildXlsx(t, []sheet{
		{name: "Data", rows: [][]string{
			{"Col1", "Col2"},
			{"x", "y"},
		}},
	})
	h := NewXlsx(DefaultConfig())
	doc, err := h.Parse("reports/q1.xlsx", xlsx)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "xlsx" {
		t.Errorf("Format = %q, want %q", doc.Format, "xlsx")
	}
}

// Malformed bytes return a wrapped error from excelize, not a panic.
func TestXlsx_MalformedBytes(t *testing.T) {
	_, err := convertXlsx([]byte("not an xlsx"), DefaultConfig())
	if err == nil {
		t.Error("expected error for non-xlsx bytes")
	}
}

// ZIP bomb guard: convertXlsx must reject archives with too many entries
// before handing bytes to excelize. The cap is shared with DOCX/PPTX via
// openZip; this regression test pins that XLSX isn't accidentally bypassed.
func TestXlsx_ZipBombGuardApplies(t *testing.T) {
	// Construct a ZIP with 10 001 entries — one over the openZip limit.
	files := make(map[string][]byte, 10001)
	for i := 0; i <= 10000; i++ {
		files["f"+strconvItoa(i)+".txt"] = []byte{}
	}
	bomb := buildZip(t, files)
	_, err := convertXlsx(bomb, DefaultConfig())
	if err == nil {
		t.Error("expected ZIP-bomb guard to reject archive with >10 000 entries")
	}
}

// strconvItoa keeps the zip-bomb test self-contained without importing
// strconv at file scope just for one number.
func strconvItoa(n int) string {
	return fmt.Sprintf("%d", n)
}
