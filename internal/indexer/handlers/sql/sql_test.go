package sql_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // wire all handlers including sql
	sqlhandler "librarian/internal/indexer/handlers/sql"
)

// compile-time interface check
var _ indexer.FileHandler = (*sqlhandler.Handler)(nil)

func TestHandler_ExtensionRegistration(t *testing.T) {
	reg := indexer.DefaultRegistry()
	for _, ext := range []string{".sql", ".psql", ".ddl"} {
		if reg.HandlerFor("schema"+ext) == nil {
			t.Errorf("extension %q not registered in default registry", ext)
		}
	}
}

func TestHandler_Name(t *testing.T) {
	h := sqlhandler.New()
	if h.Name() != "sql" {
		t.Errorf("Name() = %q, want sql", h.Name())
	}
}

func TestHandler_Extensions(t *testing.T) {
	h := sqlhandler.New()
	exts := h.Extensions()
	want := map[string]bool{".sql": true, ".psql": true, ".ddl": true}
	if len(exts) != len(want) {
		t.Fatalf("Extensions() = %v, want %v", exts, want)
	}
	for _, e := range exts {
		if !want[e] {
			t.Errorf("unexpected extension %q", e)
		}
	}
}

func TestHandler_Parse_BasicFields(t *testing.T) {
	h := sqlhandler.New()
	content := []byte(`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL);`)
	doc, err := h.Parse("schema/users.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "sql" {
		t.Errorf("Format = %q, want sql", doc.Format)
	}
	if doc.DocType != "sql" {
		t.Errorf("DocType = %q, want sql", doc.DocType)
	}
	if doc.Title != "users.sql" {
		t.Errorf("Title = %q, want users.sql", doc.Title)
	}
	if doc.Path != "schema/users.sql" {
		t.Errorf("Path = %q", doc.Path)
	}
	if doc.RawContent != string(content) {
		t.Error("RawContent not set")
	}
}

func TestHandler_StatementBoundaryChunking(t *testing.T) {
	// Build a file with many distinct statements so statement-boundary splitting
	// produces multiple Units (and thus multiple chunks when chunked).
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		sb.WriteString("CREATE TABLE tbl_")
		writeIntStr(&sb, i)
		sb.WriteString(" (id SERIAL PRIMARY KEY, payload TEXT);\n\n")
	}
	content := []byte(sb.String())

	h := sqlhandler.New()
	doc, err := h.Parse("migration.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) < 5 {
		t.Errorf("expected many statement Units, got %d", len(doc.Units))
	}

	// Each Unit should contain exactly one CREATE TABLE statement.
	for i, u := range doc.Units {
		if !strings.Contains(u.Content, "CREATE TABLE") {
			t.Errorf("Unit[%d] missing CREATE TABLE: %q", i, u.Content)
		}
		// No Unit should span more than one semicolon (i.e. no two merged statements).
		semicolons := strings.Count(u.Content, ";")
		if semicolons > 1 {
			t.Errorf("Unit[%d] contains %d semicolons (expected ≤1): %q", i, semicolons, u.Content)
		}
	}

	// Chunk with small MaxTokens to ensure multiple chunks are produced.
	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 5})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 5 {
		t.Errorf("expected multiple chunks from 20-statement file, got %d", len(chunks))
	}
}

func TestHandler_LongSingleStatementFallsBackToChunker(t *testing.T) {
	// A single very long CREATE VIEW / stored-proc body with no semicolons.
	// The handler should still return a non-empty doc and produce at least one chunk.
	var sb strings.Builder
	sb.WriteString("CREATE OR REPLACE FUNCTION big_func() RETURNS void AS $$\n")
	for i := 0; i < 200; i++ {
		sb.WriteString("  -- line ")
		writeIntStr(&sb, i)
		sb.WriteString(" of function body with some SQL\n")
		sb.WriteString("  INSERT INTO log_table VALUES (")
		writeIntStr(&sb, i)
		sb.WriteString(", 'entry');\n")
	}
	sb.WriteString("$$ LANGUAGE plpgsql")

	content := []byte(sb.String())

	h := sqlhandler.New()
	doc, err := h.Parse("funcs.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) == 0 {
		t.Fatal("expected at least one Unit for long single-statement file")
	}

	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 128, MinTokens: 5})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk from long statement file")
	}
}

func TestHandler_CommentPreservation(t *testing.T) {
	content := []byte(`-- This table stores user accounts.
-- It is the source of truth for identity.
CREATE TABLE users (
    id   SERIAL PRIMARY KEY,
    name TEXT NOT NULL
);

/* orders table: tracks purchase history */
CREATE TABLE orders (
    id      SERIAL PRIMARY KEY,
    user_id INT REFERENCES users(id),
    total   NUMERIC(10,2)
);
`)

	h := sqlhandler.New()
	doc, err := h.Parse("schema.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) < 2 {
		t.Fatalf("expected at least 2 Units, got %d", len(doc.Units))
	}

	// The first Unit should include the leading -- comments AND the CREATE TABLE.
	firstUnit := doc.Units[0]
	if !strings.Contains(firstUnit.Content, "source of truth") {
		t.Errorf("first Unit missing leading comment: %q", firstUnit.Content)
	}
	if !strings.Contains(firstUnit.Content, "CREATE TABLE users") {
		t.Errorf("first Unit missing CREATE TABLE users: %q", firstUnit.Content)
	}

	// The second Unit should include the block comment AND the CREATE TABLE.
	secondUnit := doc.Units[1]
	if !strings.Contains(secondUnit.Content, "purchase history") {
		t.Errorf("second Unit missing block comment: %q", secondUnit.Content)
	}
	if !strings.Contains(secondUnit.Content, "CREATE TABLE orders") {
		t.Errorf("second Unit missing CREATE TABLE orders: %q", secondUnit.Content)
	}
}

func TestHandler_EmptyFile(t *testing.T) {
	h := sqlhandler.New()
	doc, err := h.Parse("empty.sql", []byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc == nil {
		t.Fatal("Parse returned nil doc")
	}

	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	// Empty file should produce no chunks (ChunkSections filters empty content).
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestHandler_CommentsOnlyFile(t *testing.T) {
	h := sqlhandler.New()
	doc, err := h.Parse("comments.sql", []byte("-- just a comment\n-- nothing else\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Should parse without panic; may produce 0 or 1 unit.
	_ = doc
}

func TestHandler_StringLiteralWithSemicolon(t *testing.T) {
	// A semicolon inside a string literal must NOT split the statement.
	content := []byte(`INSERT INTO messages (body) VALUES ('hello; world');
SELECT 1;
`)

	h := sqlhandler.New()
	doc, err := h.Parse("inserts.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Should produce exactly 2 Units — the semicolon in the string is NOT a boundary.
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units, got %d: %+v", len(doc.Units), unitTitles(doc.Units))
	}
	if !strings.Contains(doc.Units[0].Content, "hello; world") {
		t.Errorf("first Unit should contain the string with embedded semicolon: %q", doc.Units[0].Content)
	}
}

func TestHandler_DollarQuotedFunction(t *testing.T) {
	// PostgreSQL dollar-quoted string: semicolons inside $$ must not split.
	content := []byte(`CREATE OR REPLACE FUNCTION greet(name TEXT) RETURNS TEXT AS $$
BEGIN
  RETURN 'Hello, ' || name || ';';
END;
$$ LANGUAGE plpgsql;

SELECT 1;
`)

	h := sqlhandler.New()
	doc, err := h.Parse("funcs.sql", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Exactly 2 top-level statements: the function body and SELECT 1.
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units, got %d:", len(doc.Units))
		for i, u := range doc.Units {
			t.Logf("  Unit[%d]: %q", i, u.Title)
		}
	}
	if !strings.Contains(doc.Units[0].Content, "LANGUAGE plpgsql") {
		t.Errorf("first Unit should be the full function: %q", doc.Units[0].Content)
	}
}

func TestHandler_ChunkEmbeddingTextContainsTitle(t *testing.T) {
	content := []byte(`CREATE TABLE accounts (id SERIAL PRIMARY KEY);`)
	h := sqlhandler.New()
	doc, _ := h.Parse("accounts.sql", content)
	chunks, _ := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 1})
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	if !strings.Contains(chunks[0].EmbeddingText, "accounts.sql") {
		t.Errorf("EmbeddingText should reference the document title: %q", chunks[0].EmbeddingText)
	}
}

// writeIntStr writes a non-negative integer to a strings.Builder without importing strconv.
func writeIntStr(sb *strings.Builder, n int) {
	if n == 0 {
		sb.WriteByte('0')
		return
	}
	var digits [20]byte
	pos := len(digits)
	for n > 0 {
		pos--
		digits[pos] = byte('0' + n%10)
		n /= 10
	}
	sb.Write(digits[pos:])
}

func unitTitles(units []indexer.Unit) []string {
	titles := make([]string, len(units))
	for i, u := range units {
		titles[i] = u.Title
	}
	return titles
}
