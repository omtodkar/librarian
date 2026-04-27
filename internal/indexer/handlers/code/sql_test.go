package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
	"librarian/internal/store"
)

// TestSQLGrammar_SatisfiesGrammarInvariants verifies that SQLGrammar satisfies
// the structural invariants every Grammar implementation must pass.
//
// SQL paths are schema-qualified absolute names (e.g. "public.users") rather
// than file-stem-prefixed names (e.g. "001_create_users.public.users"). The
// shared invariant harness exempts "sql_graph" from the title-prefix check
// because there is no meaningful relationship between a migration filename stem
// and the canonical schema-qualified symbol path.
func TestSQLGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewSQLGrammar())
	src := []byte(`
CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE orders (id UUID, user_id INT REFERENCES users(id));
CREATE INDEX orders_user_id_idx ON orders(user_id);
CREATE VIEW active_orders AS SELECT * FROM orders WHERE status = 'active';
CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE sql AS $$ SELECT a + b $$;
CREATE SEQUENCE order_seq START 1 INCREMENT 1;
CREATE SCHEMA reporting;
`)
	code.AssertGrammarInvariants(t, h, "migrations/20230101000000_schema.sql", src)
}

// helper: parse SQL source and return the doc.
func parseSQLDoc(t *testing.T, filename, src string) *indexer.ParsedDoc {
	t.Helper()
	h := code.New(code.NewSQLGrammar())
	doc, err := h.Parse(filename, []byte(src))
	if err != nil {
		t.Fatalf("Parse(%q): %v", filename, err)
	}
	return doc
}

// helper: find a Unit by path (exact match).
func sqlUnit(doc *indexer.ParsedDoc, path string) *indexer.Unit {
	for i := range doc.Units {
		if doc.Units[i].Path == path {
			return &doc.Units[i]
		}
	}
	return nil
}

// helper: find a Reference by Kind+Source+Target.
func sqlRef(doc *indexer.ParsedDoc, kind, src, target string) *indexer.Reference {
	for i := range doc.Refs {
		r := &doc.Refs[i]
		if r.Kind == kind && r.Source == src && r.Target == target {
			return r
		}
	}
	return nil
}

// ---------- table → column contains edges ----------

func TestSQLGrammar_TableContainsColumnEdges(t *testing.T) {
	src := `CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL);`
	doc := parseSQLDoc(t, "schema.sql", src)

	// Both columns must have a contains edge from the table.
	for _, colPath := range []string{"public.users.id", "public.users.name"} {
		found := false
		for _, r := range doc.Refs {
			if r.Kind == store.EdgeKindContains && r.Source == "public.users" && r.Target == colPath {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing contains edge public.users → %s; refs: %v", colPath, refSummary(doc))
		}
	}
}

// ---------- CREATE TABLE ----------

func TestSQLGrammar_CreateTableSimple(t *testing.T) {
	src := `CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL);`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.users")
	if u == nil {
		t.Fatalf("Unit public.users not found; got paths: %v", unitPaths(doc))
	}
	if u.Kind != "table" {
		t.Errorf("Kind = %q, want %q", u.Kind, "table")
	}
	if u.Title != "users" {
		t.Errorf("Title = %q, want %q", u.Title, "users")
	}

	// Two column children.
	id := sqlUnit(doc, "public.users.id")
	name := sqlUnit(doc, "public.users.name")
	if id == nil {
		t.Error("column public.users.id not found")
	}
	if name == nil {
		t.Error("column public.users.name not found")
	}
	if id != nil {
		if id.Kind != "column" {
			t.Errorf("id.Kind = %q, want %q", id.Kind, "column")
		}
		if pk, _ := id.Metadata["primary_key"].(bool); !pk {
			t.Error("id column: primary_key should be true")
		}
	}
	if name != nil {
		if nullable, _ := name.Metadata["nullable"].(bool); nullable {
			t.Error("name column: nullable should be false (NOT NULL)")
		}
	}
}

func TestSQLGrammar_CreateTableSchemaQualified(t *testing.T) {
	src := `CREATE TABLE reporting.metrics (id INT, val NUMERIC);`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "reporting.metrics")
	if u == nil {
		t.Fatalf("Unit reporting.metrics not found; got: %v", unitPaths(doc))
	}
	if u.Metadata["schema"] != "reporting" {
		t.Errorf("schema = %v, want %q", u.Metadata["schema"], "reporting")
	}

	id := sqlUnit(doc, "reporting.metrics.id")
	val := sqlUnit(doc, "reporting.metrics.val")
	if id == nil {
		t.Error("column reporting.metrics.id not found")
	}
	if val == nil {
		t.Error("column reporting.metrics.val not found")
	}
}

// ---------- FK edges — inline REFERENCES ----------

func TestSQLGrammar_InlineFKReference(t *testing.T) {
	src := `CREATE TABLE orders (id UUID, user_id INT REFERENCES users(id));`
	doc := parseSQLDoc(t, "schema.sql", src)

	// Column units exist.
	if sqlUnit(doc, "public.orders.user_id") == nil {
		t.Fatalf("column public.orders.user_id not found; units: %v", unitPaths(doc))
	}

	// FK reference edge emitted.
	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing FK Reference orders.user_id → users.id; got refs: %v", refSummary(doc))
	}
}

func TestSQLGrammar_InlineFKReferenceSchemaQualified(t *testing.T) {
	src := `CREATE TABLE public.orders (id UUID, user_id INT REFERENCES public.users(id));`
	doc := parseSQLDoc(t, "schema.sql", src)

	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing FK Reference; got refs: %v", refSummary(doc))
	}
}

func TestSQLGrammar_InlineFKReferenceNoColumn(t *testing.T) {
	// REFERENCES without explicit column — target is table-level.
	src := `CREATE TABLE items (cat_id INT REFERENCES categories);`
	doc := parseSQLDoc(t, "schema.sql", src)

	// FK target should be "public.categories" (no column qualifier).
	ref := sqlRef(doc, store.EdgeKindReferences, "public.items.cat_id", "public.categories")
	if ref == nil {
		t.Errorf("missing FK Reference items.cat_id → categories; got refs: %v", refSummary(doc))
	}
}

// ---------- FK edges — table-level constraint ----------

func TestSQLGrammar_TableLevelFKConstraint(t *testing.T) {
	src := `CREATE TABLE orders (id UUID, user_id INT, FOREIGN KEY (user_id) REFERENCES users(id));`
	doc := parseSQLDoc(t, "schema.sql", src)

	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing table-level FK reference; got refs: %v", refSummary(doc))
	}
}

// ---------- FK edges — ALTER TABLE ----------

func TestSQLGrammar_AlterTableFKAddConstraint(t *testing.T) {
	src := `ALTER TABLE orders ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;`
	doc := parseSQLDoc(t, "schema.sql", src)

	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing ALTER TABLE FK ref; got refs: %v", refSummary(doc))
	}
	if ref != nil {
		if od, _ := ref.Metadata["on_delete"].(string); od != "CASCADE" {
			t.Errorf("on_delete = %q, want %q", od, "CASCADE")
		}
	}
}

func TestSQLGrammar_AlterTableFKNoConstraintName(t *testing.T) {
	src := `ALTER TABLE orders ADD FOREIGN KEY (user_id) REFERENCES users(id) ON UPDATE CASCADE ON DELETE SET NULL;`
	doc := parseSQLDoc(t, "schema.sql", src)

	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing ALTER TABLE FK ref (no constraint name); got refs: %v", refSummary(doc))
	}
	if ref != nil {
		if ou, _ := ref.Metadata["on_update"].(string); ou != "CASCADE" {
			t.Errorf("on_update = %q, want %q", ou, "CASCADE")
		}
		if od, _ := ref.Metadata["on_delete"].(string); od != "SET NULL" {
			t.Errorf("on_delete = %q, want %q", od, "SET NULL")
		}
	}
}

func TestSQLGrammar_FKOnDeleteSetDefault(t *testing.T) {
	src := `ALTER TABLE orders ADD CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET DEFAULT;`
	doc := parseSQLDoc(t, "schema.sql", src)

	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("missing FK ref; got refs: %v", refSummary(doc))
	}
	if ref != nil {
		if od, _ := ref.Metadata["on_delete"].(string); od != "SET DEFAULT" {
			t.Errorf("on_delete = %q, want %q", od, "SET DEFAULT")
		}
	}
}

// ---------- CREATE INDEX ----------

func TestSQLGrammar_CreateIndex(t *testing.T) {
	src := `CREATE INDEX orders_user_id_idx ON orders(user_id);`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.orders_user_id_idx")
	if u == nil {
		t.Fatalf("index unit not found; got: %v", unitPaths(doc))
	}
	if u.Kind != "index" {
		t.Errorf("Kind = %q, want %q", u.Kind, "index")
	}
	if table, _ := u.Metadata["table"].(string); table != "public.orders" {
		t.Errorf("table = %q, want %q", table, "public.orders")
	}
	if unique, _ := u.Metadata["unique"].(bool); unique {
		t.Error("unique should be false for non-unique index")
	}
}

func TestSQLGrammar_CreateUniqueIndex(t *testing.T) {
	src := `CREATE UNIQUE INDEX users_email_idx ON users(email);`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.users_email_idx")
	if u == nil {
		t.Fatalf("unique index unit not found; got: %v", unitPaths(doc))
	}
	if unique, _ := u.Metadata["unique"].(bool); !unique {
		t.Error("unique should be true for UNIQUE INDEX")
	}
	cols, _ := u.Metadata["columns"].([]string)
	if len(cols) == 0 || cols[0] != "email" {
		t.Errorf("columns = %v, want [email]", cols)
	}
}

// ---------- CREATE VIEW ----------

func TestSQLGrammar_CreateView(t *testing.T) {
	src := `CREATE VIEW active_orders AS SELECT * FROM orders WHERE status = 'active';`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.active_orders")
	if u == nil {
		t.Fatalf("view unit not found; got: %v", unitPaths(doc))
	}
	if u.Kind != "view" {
		t.Errorf("Kind = %q, want %q", u.Kind, "view")
	}
	if mat, _ := u.Metadata["materialized"].(bool); mat {
		t.Error("materialized should be false for regular view")
	}
}

func TestSQLGrammar_CreateMaterializedView(t *testing.T) {
	src := `CREATE MATERIALIZED VIEW order_summary AS SELECT user_id, count(*) FROM orders GROUP BY user_id;`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.order_summary")
	if u == nil {
		t.Fatalf("materialized view unit not found; got: %v", unitPaths(doc))
	}
	if mat, _ := u.Metadata["materialized"].(bool); !mat {
		t.Error("materialized should be true for MATERIALIZED VIEW")
	}
}

// ---------- CREATE FUNCTION ----------

func TestSQLGrammar_CreateFunction(t *testing.T) {
	src := `CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN a + b; END; $$;`
	doc := parseSQLDoc(t, "schema.sql", src)

	// Path includes argument types for disambiguation.
	found := false
	for _, u := range doc.Units {
		if u.Kind == "function" && u.Title == "add" {
			found = true
			if lang, _ := u.Metadata["language"].(string); lang != "plpgsql" {
				t.Errorf("language = %q, want %q", lang, "plpgsql")
			}
			if ret, _ := u.Metadata["returns"].(string); ret == "" {
				t.Error("returns should be non-empty")
			}
		}
	}
	if !found {
		t.Errorf("function 'add' not found; got units: %v", unitPaths(doc))
	}
}

func TestSQLGrammar_OverloadedFunctions(t *testing.T) {
	src := `
CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE sql AS $$ SELECT a + b $$;
CREATE FUNCTION add(a text, b text) RETURNS text LANGUAGE sql AS $$ SELECT a || b $$;
`
	doc := parseSQLDoc(t, "schema.sql", src)

	// Both overloads must produce distinct Unit.Path values.
	seen := map[string]bool{}
	for _, u := range doc.Units {
		if u.Kind == "function" && u.Title == "add" {
			if seen[u.Path] {
				t.Errorf("duplicate function path %q", u.Path)
			}
			seen[u.Path] = true
		}
	}
	if len(seen) != 2 {
		t.Errorf("want 2 distinct 'add' overloads, got %d; paths: %v", len(seen), unitPaths(doc))
	}
	// Paths must include type info to be distinct.
	for p := range seen {
		if !strings.Contains(p, "(") {
			t.Errorf("function path %q should contain argument types for disambiguation", p)
		}
	}
}

// ---------- CREATE SEQUENCE ----------

func TestSQLGrammar_CreateSequence(t *testing.T) {
	src := `CREATE SEQUENCE order_seq START 1 INCREMENT 1;`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "public.order_seq")
	if u == nil {
		t.Fatalf("sequence unit not found; got: %v", unitPaths(doc))
	}
	if u.Kind != "sequence" {
		t.Errorf("Kind = %q, want %q", u.Kind, "sequence")
	}
	if start, _ := u.Metadata["start"].(string); start != "1" {
		t.Errorf("start = %q, want %q", start, "1")
	}
	if incr, _ := u.Metadata["increment"].(string); incr != "1" {
		t.Errorf("increment = %q, want %q", incr, "1")
	}
}

// ---------- CREATE SCHEMA ----------

func TestSQLGrammar_CreateSchema(t *testing.T) {
	src := `CREATE SCHEMA reporting;`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "reporting")
	if u == nil {
		t.Fatalf("schema unit not found; got: %v", unitPaths(doc))
	}
	if u.Kind != "schema" {
		t.Errorf("Kind = %q, want %q", u.Kind, "schema")
	}
}

func TestSQLGrammar_CreateSchemaAuthorization(t *testing.T) {
	src := `CREATE SCHEMA IF NOT EXISTS reporting AUTHORIZATION admin;`
	doc := parseSQLDoc(t, "schema.sql", src)

	u := sqlUnit(doc, "reporting")
	if u == nil {
		t.Fatalf("schema unit not found; got: %v", unitPaths(doc))
	}
	if auth, _ := u.Metadata["authorization"].(string); auth != "admin" {
		t.Errorf("authorization = %q, want %q", auth, "admin")
	}
}

// ---------- Meta-command tolerance ----------

func TestSQLGrammar_MetaCommandTolerance(t *testing.T) {
	src := `\timing on
\c mydb
CREATE TABLE users (id SERIAL PRIMARY KEY);
`
	doc := parseSQLDoc(t, "schema.sql", src)

	// Must not fail, and must still extract the table.
	u := sqlUnit(doc, "public.users")
	if u == nil {
		t.Errorf("CREATE TABLE after meta-commands not extracted; got: %v", unitPaths(doc))
	}
}

// ---------- Migration detection ----------

func TestSQLGrammar_MigrationDetection_Goose(t *testing.T) {
	src := `CREATE TABLE users (id SERIAL);`
	// Goose convention: db/migrations/20230101123456_create_users.sql
	doc := parseSQLDoc(t, "db/migrations/20230101123456_create_users.sql", src)

	if tool, _ := doc.Metadata["migration_tool"].(string); tool != "goose" {
		t.Errorf("migration_tool = %q, want %q", tool, "goose")
	}
}

func TestSQLGrammar_MigrationDetection_Flyway(t *testing.T) {
	src := `CREATE TABLE users (id SERIAL);`
	// Flyway convention: resources/db/migration/V1__create_users.sql
	doc := parseSQLDoc(t, "resources/db/migration/V1__create_users.sql", src)

	if tool, _ := doc.Metadata["migration_tool"].(string); tool != "flyway" {
		t.Errorf("migration_tool = %q, want %q", tool, "flyway")
	}
}

func TestSQLGrammar_MigrationDetection_NoMatch(t *testing.T) {
	src := `CREATE TABLE users (id SERIAL);`
	// Plain schema file — no migration convention.
	doc := parseSQLDoc(t, "sql/schema.sql", src)

	if tool, _ := doc.Metadata["migration_tool"].(string); tool != "" {
		t.Errorf("migration_tool = %q, want empty for plain schema file", tool)
	}
}

// ---------- Multi-statement file ----------

func TestSQLGrammar_MultipleStatements(t *testing.T) {
	src := `
CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT);
CREATE TABLE orders (id UUID, user_id INT REFERENCES users(id));
CREATE INDEX orders_user_id_idx ON orders(user_id);
CREATE VIEW active_orders AS SELECT * FROM orders WHERE status = 'active';
`
	doc := parseSQLDoc(t, "schema.sql", src)

	for _, wantPath := range []string{
		"public.users", "public.users.id", "public.users.name",
		"public.orders", "public.orders.id", "public.orders.user_id",
		"public.orders_user_id_idx",
		"public.active_orders",
	} {
		if sqlUnit(doc, wantPath) == nil {
			t.Errorf("unit %q not found; got: %v", wantPath, unitPaths(doc))
		}
	}

	// FK reference.
	ref := sqlRef(doc, store.EdgeKindReferences, "public.orders.user_id", "public.users.id")
	if ref == nil {
		t.Errorf("FK reference orders.user_id → users.id not found; refs: %v", refSummary(doc))
	}
}

// ---------- helpers ----------

func unitPaths(doc *indexer.ParsedDoc) []string {
	out := make([]string, 0, len(doc.Units))
	for _, u := range doc.Units {
		out = append(out, u.Path)
	}
	return out
}

func refSummary(doc *indexer.ParsedDoc) []string {
	out := make([]string, 0, len(doc.Refs))
	for _, r := range doc.Refs {
		out = append(out, r.Kind+":"+r.Source+"->"+r.Target)
	}
	return out
}
