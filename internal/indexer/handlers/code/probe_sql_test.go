package code_test

// probe_sql_test.go — AST explorer for DerekStride/tree-sitter-sql (v0.3.11, ABI 15).
//
// Run with:
//
//	go test ./internal/indexer/handlers/code -run TestProbeSQLGrammar -v
//
// This test is intentionally non-failing: it dumps the named AST shape for
// each fixture and helps discover the node-kind vocabulary used by
// SymbolKinds / SymbolName / SymbolMetadata in sql.go.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer/handlers/code/tree_sitter_sql"
)

func TestProbeSQLGrammar(t *testing.T) {
	lang := sitter.NewLanguage(tree_sitter_sql.Language())

	fixtures := []struct {
		name string
		sql  string
	}{
		{
			"create_table_simple",
			`CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL);`,
		},
		{
			"create_table_inline_fk",
			`CREATE TABLE orders (id UUID, user_id INT REFERENCES users(id));`,
		},
		{
			"create_table_schema_qualified",
			`CREATE TABLE reporting.metrics (id INT, val NUMERIC);`,
		},
		{
			"create_index",
			`CREATE INDEX orders_user_id_idx ON orders(user_id);`,
		},
		{
			"create_unique_index",
			`CREATE UNIQUE INDEX users_email_idx ON users(email);`,
		},
		{
			"create_view",
			`CREATE VIEW active_orders AS SELECT * FROM orders WHERE status = 'active';`,
		},
		{
			"create_materialized_view",
			`CREATE MATERIALIZED VIEW order_summary AS SELECT user_id, count(*) FROM orders GROUP BY user_id;`,
		},
		{
			"create_function",
			`CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN a + b; END; $$;`,
		},
		{
			"create_sequence",
			`CREATE SEQUENCE order_seq START 1 INCREMENT 1;`,
		},
		{
			"create_schema",
			`CREATE SCHEMA reporting;`,
		},
		{
			"alter_table_fk",
			`ALTER TABLE orders ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;`,
		},
		{
			"meta_command",
			`\timing on`,
		},
	}

	for _, fix := range fixtures {
		t.Run(fix.name, func(t *testing.T) {
			parser := sitter.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(lang); err != nil {
				t.Fatalf("SetLanguage: %v", err)
			}
			tree := parser.ParseCtx(context.Background(), []byte(fix.sql), nil)
			if tree == nil {
				t.Fatal("Parse returned nil tree")
			}
			defer tree.Close()
			root := tree.RootNode()
			t.Logf("SQL: %s", fix.sql)
			dumpNode(t, root, []byte(fix.sql), 0)
		})
	}
}

func dumpNode(t *testing.T, n *sitter.Node, src []byte, depth int) {
	t.Helper()
	indent := strings.Repeat("  ", depth)
	extra := ""
	if n.NamedChildCount() == 0 {
		txt := n.Utf8Text(src)
		if len(txt) > 40 {
			txt = txt[:40] + "..."
		}
		extra = fmt.Sprintf(" = %q", txt)
	}
	named := ""
	if !n.IsNamed() {
		named = " (anon)"
	}
	t.Logf("%s[%s]%s%s  start=%d end=%d", indent, n.Kind(), named, extra,
		n.StartByte(), n.EndByte())
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil {
			dumpNode(t, c, src, depth+1)
		}
	}
}

func TestProbeSQLExtraPatterns(t *testing.T) {
	lang := sitter.NewLanguage(tree_sitter_sql.Language())

	fixtures := []struct {
		name string
		sql  string
	}{
		{
			"table_level_fk",
			`CREATE TABLE orders (id UUID, user_id INT, FOREIGN KEY (user_id) REFERENCES users(id));`,
		},
		{
			"overloaded_functions",
			`CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE sql AS $$ SELECT a + b $$;`,
		},
		{
			"schema_qualified_inline_fk",
			`CREATE TABLE public.orders (id UUID, user_id INT REFERENCES public.users(id));`,
		},
		{
			"column_constraints",
			`CREATE TABLE t (id INT NOT NULL, name TEXT DEFAULT 'x', val NUMERIC CHECK (val > 0));`,
		},
		{
			"create_schema_authorization",
			`CREATE SCHEMA IF NOT EXISTS reporting AUTHORIZATION admin;`,
		},
		{
			"alter_table_fk_on_update",
			`ALTER TABLE orders ADD FOREIGN KEY (user_id) REFERENCES users(id) ON UPDATE CASCADE ON DELETE SET NULL;`,
		},
	}

	for _, fix := range fixtures {
		t.Run(fix.name, func(t *testing.T) {
			parser := sitter.NewParser()
			defer parser.Close()
			if err := parser.SetLanguage(lang); err != nil {
				t.Fatalf("SetLanguage: %v", err)
			}
			tree := parser.ParseCtx(context.Background(), []byte(fix.sql), nil)
			if tree == nil {
				t.Fatal("Parse returned nil tree")
			}
			defer tree.Close()
			root := tree.RootNode()
			t.Logf("SQL: %s", fix.sql)
			dumpNode(t, root, []byte(fix.sql), 0)
		})
	}
}

func TestProbeSQLReferencesNoColumn(t *testing.T) {
	lang := sitter.NewLanguage(tree_sitter_sql.Language())
	
	fixtures := []struct{ name, sql string }{
		{"ref_no_col", `CREATE TABLE items (cat_id INT REFERENCES categories);`},
		{"ref_with_col", `CREATE TABLE items (cat_id INT REFERENCES categories(id));`},
	}
	for _, fix := range fixtures {
		t.Run(fix.name, func(t *testing.T) {
			parser := sitter.NewParser()
			defer parser.Close()
			_ = parser.SetLanguage(lang)
			tree := parser.ParseCtx(context.Background(), []byte(fix.sql), nil)
			defer tree.Close()
			t.Logf("SQL: %s", fix.sql)
			dumpNode(t, tree.RootNode(), []byte(fix.sql), 0)
		})
	}
}
