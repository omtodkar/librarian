package indexer

import (
	"fmt"
	"strings"

	east "github.com/yuin/goldmark/extension/ast"
	"golang.org/x/net/html"
)

// TableInfo holds metadata about a parsed table.
type TableInfo struct {
	Headers    []string
	NumRows    int
	NumColumns int
	Summary    string
	IsHTML     bool
}

const (
	MaxRows    = 20
	MaxColumns = 20
)

// ProcessTableNode extracts table data from a goldmark Table AST node and
// returns a TableInfo and a linearized summary string for embedding.
func ProcessTableNode(table *east.Table, source []byte) (*TableInfo, string) {
	var headers []string
	var rows [][]string

	for child := table.FirstChild(); child != nil; child = child.NextSibling() {
		switch node := child.(type) {
		case *east.TableHeader:
			// TableHeader directly contains TableCell children (not nested in TableRow)
			for cell := node.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if _, ok := cell.(*east.TableCell); ok {
					headers = append(headers, extractText(cell, source))
				}
			}
		case *east.TableRow:
			if len(rows) >= MaxRows {
				continue
			}
			var row []string
			for cell := node.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if _, ok := cell.(*east.TableCell); ok {
					row = append(row, extractText(cell, source))
				}
			}
			rows = append(rows, row)
		}
	}

	// Cap columns
	if len(headers) > MaxColumns {
		headers = headers[:MaxColumns]
	}
	for i, row := range rows {
		if len(row) > MaxColumns {
			rows[i] = row[:MaxColumns]
		}
	}

	summary := linearizeTable(headers, rows)

	info := &TableInfo{
		Headers:    headers,
		NumRows:    len(rows),
		NumColumns: len(headers),
		Summary:    summary,
		IsHTML:     false,
	}

	return info, summary
}

// ProcessHTMLTable parses an HTML string containing a table and returns
// a TableInfo and linearized summary. Returns false if no table is found.
func ProcessHTMLTable(htmlContent string) (*TableInfo, string, bool) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, "", false
	}

	tableNode := findHTMLTable(doc)
	if tableNode == nil {
		return nil, "", false
	}

	headers, rows := extractHTMLTableData(tableNode)

	// If no headers found, generate Column 1, Column 2, etc.
	if len(headers) == 0 && len(rows) > 0 {
		numCols := len(rows[0])
		headers = make([]string, numCols)
		for i := range headers {
			headers[i] = fmt.Sprintf("Column %d", i+1)
		}
	}

	// Cap columns
	if len(headers) > MaxColumns {
		headers = headers[:MaxColumns]
	}
	// Cap rows
	if len(rows) > MaxRows {
		rows = rows[:MaxRows]
	}
	for i, row := range rows {
		if len(row) > MaxColumns {
			rows[i] = row[:MaxColumns]
		}
	}

	summary := linearizeTable(headers, rows)

	info := &TableInfo{
		Headers:    headers,
		NumRows:    len(rows),
		NumColumns: len(headers),
		Summary:    summary,
		IsHTML:     true,
	}

	return info, summary, true
}

// isHTMLTable checks if the content starts with a <table tag.
func isHTMLTable(content string) bool {
	lower := strings.TrimSpace(strings.ToLower(content))
	return strings.HasPrefix(lower, "<table")
}

// linearizeTable converts headers and rows into a natural-language representation.
func linearizeTable(headers []string, rows [][]string) string {
	if len(headers) == 0 {
		return ""
	}

	var sb strings.Builder

	// Prefix line
	sb.WriteString(fmt.Sprintf("[Table: %d columns, %d rows — %s]\n",
		len(headers), len(rows), strings.Join(headers, ", ")))

	// One line per row
	for _, row := range rows {
		var pairs []string
		for i, header := range headers {
			if i < len(row) {
				val := strings.TrimSpace(row[i])
				if val != "" {
					pairs = append(pairs, header+": "+val)
				}
			}
		}
		if len(pairs) > 0 {
			sb.WriteString(strings.Join(pairs, ", ") + "\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// findHTMLTable recursively searches for a <table> element in the HTML tree.
func findHTMLTable(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.Data == "table" {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if result := findHTMLTable(c); result != nil {
			return result
		}
	}
	return nil
}

// extractHTMLTableData walks a <table> node and extracts headers and row data.
func extractHTMLTableData(table *html.Node) (headers []string, rows [][]string) {
	var thead, tbody *html.Node
	var directTRs []*html.Node

	for c := table.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.Data {
		case "thead":
			thead = c
		case "tbody":
			tbody = c
		case "tr":
			directTRs = append(directTRs, c)
		}
	}

	// Extract headers from <thead>
	if thead != nil {
		for tr := thead.FirstChild; tr != nil; tr = tr.NextSibling {
			if tr.Type == html.ElementNode && tr.Data == "tr" {
				cells := extractHTMLRowCells(tr)
				if len(cells) > 0 {
					headers = cells
					break // take first header row
				}
			}
		}
	}

	// Collect all <tr> elements from tbody and direct children
	var allTRs []*html.Node
	if tbody != nil {
		for tr := tbody.FirstChild; tr != nil; tr = tr.NextSibling {
			if tr.Type == html.ElementNode && tr.Data == "tr" {
				allTRs = append(allTRs, tr)
			}
		}
	}
	allTRs = append(allTRs, directTRs...)

	if thead != nil {
		// Headers already extracted from thead; all TRs are data rows
		for _, tr := range allTRs {
			rows = append(rows, extractHTMLRowCells(tr))
		}
	} else if len(allTRs) > 0 {
		// No <thead>: check if first row has <th> cells
		firstRow := allTRs[0]
		hasHeaders := false
		for c := firstRow.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "th" {
				hasHeaders = true
				break
			}
		}

		if hasHeaders {
			headers = extractHTMLRowCells(firstRow)
		} else {
			// All <td> — use first row as header
			headers = extractHTMLRowCells(firstRow)
		}
		for _, tr := range allTRs[1:] {
			rows = append(rows, extractHTMLRowCells(tr))
		}
	}

	return headers, rows
}

// extractHTMLText recursively extracts text content from an HTML node.
func extractHTMLText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(extractHTMLText(c))
	}
	return strings.TrimSpace(sb.String())
}

// extractHTMLRowCells extracts cell text from a <tr> element.
func extractHTMLRowCells(tr *html.Node) []string {
	var cells []string
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
			cells = append(cells, extractHTMLText(c))
		}
	}
	return cells
}
