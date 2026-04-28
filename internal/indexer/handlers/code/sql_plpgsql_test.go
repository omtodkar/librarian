package code

// sql_plpgsql_test.go — unit tests for the PL/pgSQL body walker (lib-o5dn.2).
//
// Tests verify Reference extraction from plpgsqlExtractRefs against the spike
// fixtures and spec from lib-o5dn-pg-query-go-spike.md. No edge emission —
// only in-memory Reference records are verified.

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
)

const plTestFuncPath = "public.testfunc"
const plTestSchema = "public"

// wrap builds a complete CREATE FUNCTION ... LANGUAGE plpgsql statement from a body.
func wrap(body string) string {
	return "CREATE FUNCTION testfunc() RETURNS void AS $$\n" +
		body + "\n$$ LANGUAGE plpgsql;"
}

// wrapSig builds a CREATE FUNCTION with an explicit argument signature.
func wrapSig(sig, body string) string {
	return "CREATE FUNCTION testfunc" + sig + " RETURNS void AS $$\n" +
		body + "\n$$ LANGUAGE plpgsql;"
}

// hasBodyRef returns true when refs contains a body_references entry with
// op and the sym:-prefixed target.
func hasBodyRef(t *testing.T, refs []indexer.Reference, op, symTarget string) bool {
	t.Helper()
	target := "sym:" + symTarget
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences && r.Metadata["op"] == op && r.Target == target {
			return true
		}
	}
	return false
}

// hasMetaBodyRef returns true when refs contains a body_references entry with
// the given metadata flag set to true.
func hasMetaFlag(t *testing.T, refs []indexer.Reference, flag string) bool {
	t.Helper()
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences {
			if v, ok := r.Metadata[flag].(bool); ok && v {
				return true
			}
		}
	}
	return false
}

// TestPlpgsql_SelectRead verifies SELECT FROM emits a read reference.
func TestPlpgsql_SelectRead(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  SELECT * FROM users;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "public.users") {
		t.Errorf("missing read ref to public.users; got %v", refs)
	}
}

// TestPlpgsql_InsertWrite verifies INSERT INTO emits a write reference.
func TestPlpgsql_InsertWrite(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  INSERT INTO users(name) VALUES ('a');
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.users") {
		t.Errorf("missing write ref to public.users; got %v", refs)
	}
}

// TestPlpgsql_InsertSelect verifies INSERT INTO ... SELECT emits write to target
// and read from source.
func TestPlpgsql_InsertSelect(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  INSERT INTO archive (id, name) SELECT id, name FROM users;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.archive") {
		t.Errorf("missing write ref to public.archive; got %v", refs)
	}
	if !hasBodyRef(t, refs, "read", "public.users") {
		t.Errorf("missing read ref to public.users; got %v", refs)
	}
}

// TestPlpgsql_UpdateWrite verifies UPDATE emits a write reference.
func TestPlpgsql_UpdateWrite(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  UPDATE users SET name = 'b' WHERE id = 1;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.users") {
		t.Errorf("missing write ref to public.users; got %v", refs)
	}
}

// TestPlpgsql_UpdateFrom verifies UPDATE ... FROM emits write to target and
// read from the FROM source (Postgres extension).
func TestPlpgsql_UpdateFrom(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  UPDATE users SET status = orders.status FROM orders WHERE orders.user_id = users.id;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.users") {
		t.Errorf("missing write ref to public.users; got %v", refs)
	}
	if !hasBodyRef(t, refs, "read", "public.orders") {
		t.Errorf("missing read ref to public.orders; got %v", refs)
	}
}

// TestPlpgsql_DeleteWrite verifies DELETE FROM emits a write reference.
func TestPlpgsql_DeleteWrite(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  DELETE FROM users WHERE id = 1;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.users") {
		t.Errorf("missing write ref to public.users; got %v", refs)
	}
}

// TestPlpgsql_DeleteUsing verifies DELETE ... USING emits write to target
// and read from USING source.
func TestPlpgsql_DeleteUsing(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  DELETE FROM users USING orders WHERE orders.user_id = users.id;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.users") {
		t.Errorf("missing write ref to public.users; got %v", refs)
	}
	if !hasBodyRef(t, refs, "read", "public.orders") {
		t.Errorf("missing read ref to public.orders; got %v", refs)
	}
}

// TestPlpgsql_CallProcedure verifies CALL emits a procedure_call reference.
func TestPlpgsql_CallProcedure(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  CALL refresh_cache(1);
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "procedure_call", "public.refresh_cache") {
		t.Errorf("missing procedure_call ref to public.refresh_cache; got %v", refs)
	}
}

// TestPlpgsql_QualifiedTable verifies schema-qualified table names are preserved.
func TestPlpgsql_QualifiedTable(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  INSERT INTO audit.log(x) VALUES (1);
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "audit.log") {
		t.Errorf("missing write ref to audit.log; got %v", refs)
	}
}

// TestPlpgsql_UnqualifiedDefaultsToPublic verifies unqualified names use default schema.
func TestPlpgsql_UnqualifiedDefaultsToPublic(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  SELECT * FROM events;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "public.events") {
		t.Errorf("missing read ref to public.events; got %v", refs)
	}
}

// TestPlpgsql_CustomDefaultSchema verifies a non-public defaultSchema is respected.
func TestPlpgsql_CustomDefaultSchema(t *testing.T) {
	refs, ok := plpgsqlExtractRefs("myschema.f", "myschema",
		"CREATE FUNCTION f() RETURNS void AS $$\nBEGIN SELECT * FROM logs; END\n$$ LANGUAGE plpgsql;")
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "myschema.logs") {
		t.Errorf("missing read ref to myschema.logs; got %v", refs)
	}
}

// TestPlpgsql_BeginEndBlock verifies BEGIN/END block bodies are walked.
func TestPlpgsql_BeginEndBlock(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  BEGIN
    INSERT INTO nested(x) VALUES (1);
  END;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.nested") {
		t.Errorf("missing write ref to public.nested; got %v", refs)
	}
}

// TestPlpgsql_IfThenElse verifies all IF/THEN/ELSIF/ELSE branches are walked.
func TestPlpgsql_IfThenElse(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  IF 1=1 THEN
    INSERT INTO a(x) VALUES (1);
  ELSIF 2=2 THEN
    INSERT INTO b(x) VALUES (2);
  ELSE
    INSERT INTO c(x) VALUES (3);
  END IF;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	for _, tbl := range []string{"public.a", "public.b", "public.c"} {
		if !hasBodyRef(t, refs, "write", tbl) {
			t.Errorf("missing write ref to %s; got %v", tbl, refs)
		}
	}
}

// TestPlpgsql_ForLoopQuery verifies FOR r IN SELECT LOOP produces read refs
// from the SELECT and walks the loop body.
func TestPlpgsql_ForLoopQuery(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT id, name FROM users LOOP
    UPDATE orders SET user_id = r.id WHERE user_name = r.name;
  END LOOP;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "public.users") {
		t.Errorf("missing read ref to public.users; got %v", refs)
	}
	if !hasBodyRef(t, refs, "write", "public.orders") {
		t.Errorf("missing write ref to public.orders; got %v", refs)
	}
}

// TestPlpgsql_ForLoopInteger verifies integer FOR LOOP body is walked.
func TestPlpgsql_ForLoopInteger(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
DECLARE i int;
BEGIN
  FOR i IN 1..10 LOOP
    INSERT INTO log(val) VALUES (i);
  END LOOP;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.log") {
		t.Errorf("missing write ref to public.log; got %v", refs)
	}
}

// TestPlpgsql_WhileLoop verifies WHILE LOOP body is walked.
func TestPlpgsql_WhileLoop(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
DECLARE cnt int := 0;
BEGIN
  WHILE cnt < 5 LOOP
    INSERT INTO events(n) VALUES (cnt);
    cnt := cnt + 1;
  END LOOP;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.events") {
		t.Errorf("missing write ref to public.events; got %v", refs)
	}
}

// TestPlpgsql_ExceptionHandler verifies statements inside EXCEPTION blocks are walked.
func TestPlpgsql_ExceptionHandler(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  INSERT INTO t VALUES (1);
EXCEPTION WHEN unique_violation THEN
  INSERT INTO dup_log VALUES (1);
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.t") {
		t.Errorf("missing write ref to public.t; got %v", refs)
	}
	if !hasBodyRef(t, refs, "write", "public.dup_log") {
		t.Errorf("missing write ref to public.dup_log in exception handler; got %v", refs)
	}
}

// TestPlpgsql_SelectJoin verifies SELECT with a JOIN emits reads for both tables.
func TestPlpgsql_SelectJoin(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  SELECT * FROM users JOIN orders ON users.id = orders.user_id;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "public.users") {
		t.Errorf("missing read ref to public.users; got %v", refs)
	}
	if !hasBodyRef(t, refs, "read", "public.orders") {
		t.Errorf("missing read ref to public.orders; got %v", refs)
	}
}

// TestPlpgsql_MergeStatement verifies MERGE emits both to the target and read
// to the source relation.
func TestPlpgsql_MergeStatement(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
BEGIN
  MERGE INTO target USING source ON target.id = source.id
  WHEN MATCHED THEN UPDATE SET name = source.name
  WHEN NOT MATCHED THEN INSERT (id, name) VALUES (source.id, source.name);
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "both", "public.target") {
		t.Errorf("missing both ref to public.target; got %v", refs)
	}
	if !hasBodyRef(t, refs, "read", "public.source") {
		t.Errorf("missing read ref to public.source; got %v", refs)
	}
}

// TestPlpgsql_CaseWhen verifies CASE WHEN branches are walked and refs collected
// from both THEN and ELSE bodies.
func TestPlpgsql_CaseWhen(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap(`
DECLARE x int := 1;
BEGIN
  CASE x
    WHEN 1 THEN INSERT INTO t1(v) VALUES (1);
    ELSE INSERT INTO t2(v) VALUES (2);
  END CASE;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.t1") {
		t.Errorf("missing write ref to public.t1 (THEN branch); got %v", refs)
	}
	if !hasBodyRef(t, refs, "write", "public.t2") {
		t.Errorf("missing write ref to public.t2 (ELSE branch); got %v", refs)
	}
}

// TestPlpgsql_ExecutePendingFlag verifies EXECUTE nodes are captured with
// pending_execute=true and not resolved.
func TestPlpgsql_ExecutePendingFlag(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrapSig("(tname text)", `
BEGIN
  EXECUTE 'DELETE FROM ' || tname;
  EXECUTE 'INSERT INTO audit VALUES (1)';
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasMetaFlag(t, refs, "pending_execute") {
		t.Errorf("expected pending_execute=true on at least one ref; got %v", refs)
	}
	executeCount := 0
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences {
			if v, ok := r.Metadata["pending_execute"].(bool); ok && v {
				executeCount++
			}
		}
	}
	if executeCount != 2 {
		t.Errorf("expected 2 EXECUTE refs with pending_execute=true, got %d", executeCount)
	}
}

// TestPlpgsql_TriggerNewOldFlag verifies trigger bodies produce refs with
// trigger_special=true for NEW/OLD field accesses.
func TestPlpgsql_TriggerNewOldFlag(t *testing.T) {
	refs, ok := plpgsqlExtractRefs("public.audit_trigger", plTestSchema,
		`CREATE FUNCTION audit_trigger() RETURNS trigger AS $$
BEGIN
  INSERT INTO audit (user_id, action) VALUES (NEW.user_id, TG_OP);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;`)
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "write", "public.audit") {
		t.Errorf("missing write ref to public.audit; got %v", refs)
	}
	if !hasMetaFlag(t, refs, "trigger_special") {
		t.Errorf("expected trigger_special=true on at least one ref; got %v", refs)
	}
}

// TestPlpgsql_MalformedBody verifies that a parse error returns (nil, false).
func TestPlpgsql_MalformedBody(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, "this is not valid sql at all")
	if ok {
		t.Error("expected ok=false for malformed body, got true")
	}
	if len(refs) != 0 {
		t.Errorf("expected nil/empty refs for malformed body, got %v", refs)
	}
}

// TestPlpgsql_EmptyBody verifies that an empty BEGIN END block returns ([], true).
func TestPlpgsql_EmptyBody(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrap("BEGIN\nEND"))
	if !ok {
		t.Error("expected ok=true for empty body")
	}
	bodyRefs := 0
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences {
			bodyRefs++
		}
	}
	if bodyRefs != 0 {
		t.Errorf("expected 0 body refs for empty body, got %d: %v", bodyRefs, refs)
	}
}

// TestPlpgsql_SelectIntoDoesNotEmitTarget verifies that SELECT INTO produces
// a read ref for the FROM table but no write ref for the INTO variable.
func TestPlpgsql_SelectIntoDoesNotEmitTarget(t *testing.T) {
	refs, ok := plpgsqlExtractRefs(plTestFuncPath, plTestSchema, wrapSig("(uid int)", `
DECLARE cnt int;
BEGIN
  SELECT count(*) INTO cnt FROM orders WHERE user_id = uid;
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	if !hasBodyRef(t, refs, "read", "public.orders") {
		t.Errorf("missing read ref to public.orders; got %v", refs)
	}
	// cnt is a variable, not a table — no write ref expected.
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences && r.Metadata["op"] == "write" {
			t.Errorf("unexpected write ref for SELECT INTO: %v", r)
		}
	}
}

// TestPlpgsql_SourceFieldPopulated verifies every emitted Reference has Source
// set to the caller-provided funcPath.
func TestPlpgsql_SourceFieldPopulated(t *testing.T) {
	refs, ok := plpgsqlExtractRefs("myschema.myfunc(int)", plTestSchema, wrap(`
BEGIN
  INSERT INTO t VALUES (1);
END`))
	if !ok {
		t.Fatal("parse failed")
	}
	for _, r := range refs {
		if r.Kind != edgeKindBodyReferences {
			continue
		}
		wantSource := "sym:myschema.myfunc(int)"
		if r.Source != wantSource {
			t.Errorf("Source = %q, want %q", r.Source, wantSource)
		}
	}
}

// TestPlpgsql_SpikeFunctionsParseClean verifies all 6 spike fixtures parse
// without error (regression guard — mirrors the spike's PROCEED verdict).
func TestPlpgsql_SpikeFunctionsParseClean(t *testing.T) {
	fixtures := []struct {
		name string
		sql  string
	}{
		{
			name: "f1-simple-body",
			sql: `CREATE FUNCTION f1() RETURNS void AS $$
BEGIN INSERT INTO users(name) VALUES ('a'); END;
$$ LANGUAGE plpgsql;`,
		},
		{
			name: "f2-declare-control-flow",
			sql: `CREATE FUNCTION f2(uid int) RETURNS int AS $$
DECLARE cnt int;
BEGIN
  SELECT count(*) INTO cnt FROM orders WHERE user_id = uid;
  IF cnt > 0 THEN RAISE NOTICE 'has orders'; END IF;
  RETURN cnt;
END;
$$ LANGUAGE plpgsql;`,
		},
		{
			name: "f3-for-loop",
			sql: `CREATE FUNCTION f3() RETURNS void AS $$
DECLARE r RECORD;
BEGIN
  FOR r IN SELECT id, name FROM users LOOP
    UPDATE orders SET user_id = r.id WHERE user_name = r.name;
  END LOOP;
END;
$$ LANGUAGE plpgsql;`,
		},
		{
			name: "f4-dynamic-execute",
			sql: `CREATE FUNCTION f4(tname text) RETURNS void AS $$
BEGIN
  EXECUTE 'DELETE FROM ' || tname;
  EXECUTE 'INSERT INTO audit VALUES (1)';
END;
$$ LANGUAGE plpgsql;`,
		},
		{
			name: "f5-exception-handler",
			sql: `CREATE FUNCTION f5() RETURNS void AS $$
BEGIN INSERT INTO t VALUES (1);
EXCEPTION WHEN unique_violation THEN NULL;
END;
$$ LANGUAGE plpgsql;`,
		},
		{
			name: "f6-trigger-new-old",
			sql: `CREATE FUNCTION audit_trigger() RETURNS trigger AS $$
BEGIN
  INSERT INTO audit (user_id, action) VALUES (NEW.user_id, TG_OP);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;`,
		},
	}

	for _, fix := range fixtures {
		t.Run(fix.name, func(t *testing.T) {
			_, ok := plpgsqlExtractRefs("public."+strings.Split(fix.name, "-")[0], plTestSchema, fix.sql)
			if !ok {
				t.Errorf("fixture %s: parse failed", fix.name)
			}
		})
	}
}
