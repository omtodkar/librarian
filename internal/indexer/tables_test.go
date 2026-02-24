package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

func TestLinearizeTable(t *testing.T) {
	t.Run("narrow table", func(t *testing.T) {
		headers := []string{"Method", "Endpoint", "Auth"}
		rows := [][]string{
			{"GET", "/users", "Required"},
			{"POST", "/login", "No"},
		}

		result := linearizeTable(headers, rows)

		if !strings.Contains(result, "[Table: 3 columns, 2 rows — Method, Endpoint, Auth]") {
			t.Errorf("expected prefix line, got %q", result)
		}
		if !strings.Contains(result, "Method: GET, Endpoint: /users, Auth: Required") {
			t.Errorf("expected first row, got %q", result)
		}
		if !strings.Contains(result, "Method: POST, Endpoint: /login, Auth: No") {
			t.Errorf("expected second row, got %q", result)
		}
	})

	t.Run("wide table", func(t *testing.T) {
		headers := []string{"A", "B", "C", "D", "E"}
		rows := [][]string{
			{"1", "2", "3", "4", "5"},
		}

		result := linearizeTable(headers, rows)

		if !strings.Contains(result, "[Table: 5 columns, 1 rows — A, B, C, D, E]") {
			t.Errorf("expected prefix with 5 columns, got %q", result)
		}
	})

	t.Run("empty cells skipped", func(t *testing.T) {
		headers := []string{"Name", "Value", "Note"}
		rows := [][]string{
			{"foo", "", "important"},
			{"bar", "42", ""},
		}

		result := linearizeTable(headers, rows)

		if strings.Contains(result, "Value: ,") || strings.Contains(result, "Value: \n") {
			t.Errorf("empty Value should be skipped, got %q", result)
		}
		if !strings.Contains(result, "Name: foo, Note: important") {
			t.Errorf("expected first row without empty Value, got %q", result)
		}
		if !strings.Contains(result, "Name: bar, Value: 42") {
			t.Errorf("expected second row, got %q", result)
		}
	})

	t.Run("empty headers returns empty string", func(t *testing.T) {
		result := linearizeTable(nil, nil)
		if result != "" {
			t.Errorf("expected empty string for no headers, got %q", result)
		}
	})
}

func TestProcessTableNode(t *testing.T) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.Table),
	)

	src := []byte("| Method | Endpoint | Auth |\n| ------ | -------- | ---- |\n| GET    | /users   | Yes  |\n| POST   | /login   | No   |\n")

	reader := text.NewReader(src)
	doc := md.Parser().Parse(reader)

	// Find the table node
	var tableNode *east.Table
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		if tn, ok := child.(*east.Table); ok {
			tableNode = tn
			break
		}
	}

	if tableNode == nil {
		t.Fatal("expected to find a Table node in parsed AST")
	}

	info, summary := ProcessTableNode(tableNode, src)

	if info == nil {
		t.Fatal("expected non-nil TableInfo")
	}
	if len(info.Headers) != 3 {
		t.Errorf("expected 3 headers, got %d: %v", len(info.Headers), info.Headers)
	}
	if info.NumRows != 2 {
		t.Errorf("expected 2 rows, got %d", info.NumRows)
	}
	if info.NumColumns != 3 {
		t.Errorf("expected 3 columns, got %d", info.NumColumns)
	}
	if info.IsHTML {
		t.Error("expected IsHTML to be false")
	}

	if !strings.Contains(summary, "[Table:") {
		t.Errorf("summary should have table prefix, got %q", summary)
	}
	if !strings.Contains(summary, "Method") {
		t.Errorf("summary should contain 'Method', got %q", summary)
	}
	if !strings.Contains(summary, "/users") {
		t.Errorf("summary should contain '/users', got %q", summary)
	}
}

func TestProcessHTMLTable(t *testing.T) {
	t.Run("basic table with thead/tbody", func(t *testing.T) {
		htmlStr := `<table>
<thead><tr><th>Name</th><th>Role</th></tr></thead>
<tbody>
<tr><td>Alice</td><td>Admin</td></tr>
<tr><td>Bob</td><td>User</td></tr>
</tbody>
</table>`

		info, summary, ok := ProcessHTMLTable(htmlStr)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if len(info.Headers) != 2 {
			t.Errorf("expected 2 headers, got %d", len(info.Headers))
		}
		if info.NumRows != 2 {
			t.Errorf("expected 2 rows, got %d", info.NumRows)
		}
		if !info.IsHTML {
			t.Error("expected IsHTML to be true")
		}
		if !strings.Contains(summary, "Name") || !strings.Contains(summary, "Alice") {
			t.Errorf("summary should contain table data, got %q", summary)
		}
	})

	t.Run("table without thead", func(t *testing.T) {
		htmlStr := `<table>
<tr><th>City</th><th>Population</th></tr>
<tr><td>NYC</td><td>8M</td></tr>
<tr><td>LA</td><td>4M</td></tr>
</table>`

		info, summary, ok := ProcessHTMLTable(htmlStr)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if info.Headers[0] != "City" {
			t.Errorf("expected first header 'City', got %q", info.Headers[0])
		}
		if info.NumRows != 2 {
			t.Errorf("expected 2 data rows, got %d", info.NumRows)
		}
		if !strings.Contains(summary, "NYC") {
			t.Errorf("summary should contain 'NYC', got %q", summary)
		}
	})

	t.Run("table with only td (no th)", func(t *testing.T) {
		htmlStr := `<table>
<tr><td>Name</td><td>Value</td></tr>
<tr><td>foo</td><td>bar</td></tr>
</table>`

		info, _, ok := ProcessHTMLTable(htmlStr)
		if !ok {
			t.Fatal("expected ok to be true")
		}
		if info.Headers[0] != "Name" {
			t.Errorf("expected first header 'Name', got %q", info.Headers[0])
		}
		if info.NumRows != 1 {
			t.Errorf("expected 1 data row, got %d", info.NumRows)
		}
	})

	t.Run("malformed HTML returns false", func(t *testing.T) {
		htmlStr := `<div>not a table</div>`
		_, _, ok := ProcessHTMLTable(htmlStr)
		if ok {
			t.Error("expected ok to be false for non-table HTML")
		}
	})
}

func TestIsHTMLTable(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"lowercase table", "<table><tr><td>x</td></tr></table>", true},
		{"uppercase TABLE", "<TABLE><TR><TD>x</TD></TR></TABLE>", true},
		{"table with class", `<table class="api-ref">`, true},
		{"div not table", "<div>content</div>", false},
		{"empty string", "", false},
		{"table with whitespace", "  <table>\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isHTMLTable(tt.content)
			if got != tt.want {
				t.Errorf("isHTMLTable(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestProcessHTMLTable_RowAndColumnLimits(t *testing.T) {
	t.Run("rows capped at MaxRows", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("<table><thead><tr><th>ID</th></tr></thead><tbody>")
		for i := 0; i < 30; i++ {
			sb.WriteString(fmt.Sprintf("<tr><td>%d</td></tr>", i))
		}
		sb.WriteString("</tbody></table>")

		info, _, ok := ProcessHTMLTable(sb.String())
		if !ok {
			t.Fatal("expected ok")
		}
		if info.NumRows != MaxRows {
			t.Errorf("expected %d rows, got %d", MaxRows, info.NumRows)
		}
	})

	t.Run("columns capped at MaxColumns", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString("<table><thead><tr>")
		for i := 0; i < 25; i++ {
			sb.WriteString(fmt.Sprintf("<th>Col%d</th>", i))
		}
		sb.WriteString("</tr></thead><tbody><tr>")
		for i := 0; i < 25; i++ {
			sb.WriteString(fmt.Sprintf("<td>Val%d</td>", i))
		}
		sb.WriteString("</tr></tbody></table>")

		info, _, ok := ProcessHTMLTable(sb.String())
		if !ok {
			t.Fatal("expected ok")
		}
		if info.NumColumns != MaxColumns {
			t.Errorf("expected %d columns, got %d", MaxColumns, info.NumColumns)
		}
	})
}

func TestTableIntegration(t *testing.T) {
	mdContent := `# API Reference

Here are the endpoints:

| Method | Endpoint | Auth Required |
| ------ | -------- | ------------- |
| GET    | /users   | Yes           |
| POST   | /login   | No            |
| DELETE | /users/:id | Yes         |
`

	// Write to temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.md")
	if err := os.WriteFile(tmpFile, []byte(mdContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	parsed, err := ParseMarkdown(tmpFile)
	if err != nil {
		t.Fatalf("ParseMarkdown error: %v", err)
	}

	// Verify tables were extracted
	if len(parsed.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(parsed.Tables))
	}
	table := parsed.Tables[0]
	if table.NumRows != 3 {
		t.Errorf("expected 3 rows, got %d", table.NumRows)
	}
	if table.NumColumns != 3 {
		t.Errorf("expected 3 columns, got %d", table.NumColumns)
	}

	// Verify section content has linearized output
	if len(parsed.Sections) < 1 {
		t.Fatal("expected at least 1 section")
	}
	sectionContent := parsed.Sections[0].Content
	if !strings.Contains(sectionContent, "[Table:") {
		t.Errorf("section content should contain [Table: prefix, got %q", sectionContent)
	}
	if !strings.Contains(sectionContent, "Endpoint: /users") {
		t.Errorf("section content should contain 'Endpoint: /users', got %q", sectionContent)
	}
}
