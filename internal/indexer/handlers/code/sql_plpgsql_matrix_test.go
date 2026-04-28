package code

// sql_plpgsql_matrix_test.go — fixture-driven test matrix for the PL/pgSQL
// body walker (lib-o5dn.5).
//
// Each sub-test loads a .sql fixture from testdata/plpgsql/, calls
// plpgsqlExtractRefs, and asserts the expected body_references edge set.
//
// Run one construct at a time:
//
//	go test ./internal/indexer/handlers/code -run TestPlpgsqlMatrix/simple_insert
//
// Run all constructs verbosely (one pass/fail line per construct):
//
//	go test ./internal/indexer/handlers/code -run TestPlpgsqlMatrix -v

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/indexer"
)

type plpgsqlEdge struct {
	op  string
	sym string // without "sym:" prefix
}

type plpgsqlCase struct {
	name      string
	file      string
	funcPath  string
	schema    string
	wantOK    bool
	edges     []plpgsqlEdge
	metaFlags []string // metadata flag keys that must be true on at least one edge
	noEdges   bool     // expect zero body_references edges after resolution
}

// TestPlpgsqlMatrix exercises every construct in the fixture matrix.
// Each sub-test name matches the fixture file stem so `-run TestPlpgsqlMatrix/<name>` works.
func TestPlpgsqlMatrix(t *testing.T) {
	cases := []plpgsqlCase{
		{
			name:     "simple_insert",
			file:     "simple_insert.sql",
			funcPath: "public.simple_insert",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.users"}},
		},
		{
			name:     "select_with_join",
			file:     "select_with_join.sql",
			funcPath: "public.select_with_join",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"read", "public.users"}, {"read", "public.orders"}},
		},
		{
			name:     "update_from",
			file:     "update_from.sql",
			funcPath: "public.update_from",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.orders"}, {"read", "public.users"}},
		},
		{
			name:     "delete_using",
			file:     "delete_using.sql",
			funcPath: "public.delete_using",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.orders"}, {"read", "public.users"}},
		},
		{
			name:     "merge",
			file:     "merge.sql",
			funcPath: "public.merge_example",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"both", "public.target"}, {"read", "public.source"}},
		},
		{
			name:     "declare_control_flow",
			file:     "declare_control_flow.sql",
			funcPath: "public.declare_control_flow",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"read", "public.orders"}},
		},
		{
			name:     "for_loop",
			file:     "for_loop.sql",
			funcPath: "public.for_loop",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"read", "public.users"}, {"write", "public.orders"}},
		},
		{
			name:     "while_loop",
			file:     "while_loop.sql",
			funcPath: "public.while_loop",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.events"}},
		},
		{
			name:     "case_expression",
			file:     "case_expression.sql",
			funcPath: "public.case_expression",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.t1"}, {"write", "public.t2"}},
		},
		{
			name:     "exception_handler",
			file:     "exception_handler.sql",
			funcPath: "public.exception_handler",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.t"}, {"write", "public.dup_log"}},
		},
		{
			// Literal EXECUTE — resolver emits via_execute=true edge to audit.
			name:      "execute_literal",
			file:      "execute_literal.sql",
			funcPath:  "public.execute_literal",
			schema:    "public",
			wantOK:    true,
			edges:     []plpgsqlEdge{{"write", "public.audit"}},
			metaFlags: []string{"via_execute"},
		},
		{
			// Variable EXECUTE — resolver emits uses_dynamic_sql=true marker, no sym: edge.
			name:      "execute_variable",
			file:      "execute_variable.sql",
			funcPath:  "public.execute_variable",
			schema:    "public",
			wantOK:    true,
			metaFlags: []string{"uses_dynamic_sql"},
		},
		{
			// Mixed literal+variable concat — still treated as variable (Case B).
			name:      "execute_concat_mixed",
			file:      "execute_concat_mixed.sql",
			funcPath:  "public.execute_concat_mixed",
			schema:    "public",
			wantOK:    true,
			metaFlags: []string{"uses_dynamic_sql"},
		},
		{
			// Literal whose content starts with EXECUTE — nested_execute=true marker, no recursion.
			name:      "execute_nested",
			file:      "execute_nested.sql",
			funcPath:  "public.execute_nested",
			schema:    "public",
			wantOK:    true,
			metaFlags: []string{"nested_execute"},
		},
		{
			// Trigger reading NEW.email: INSERT into audit survives; trigger_special refs dropped
			// (empty triggerTarget in plpgsqlExtractRefs).
			name:     "trigger_new_read",
			file:     "trigger_new_read.sql",
			funcPath: "public.trigger_new_read",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.audit"}},
		},
		{
			// Trigger assigning NEW.updated_at := now(): datum-based ref captured then dropped
			// by empty-triggerTarget resolver; PLpgSQL_stmt_assign skipped by walker.
			name:     "trigger_new_write",
			file:     "trigger_new_write.sql",
			funcPath: "public.trigger_new_write",
			schema:   "public",
			wantOK:   true,
			noEdges:  true,
		},
		{
			// INSERT INTO audit SELECT NEW.*: INSERT target gives write ref to audit;
			// current walker does not emit NEW.* trigger_special (future lib-uer8 work).
			name:     "trigger_new_star",
			file:     "trigger_new_star.sql",
			funcPath: "public.trigger_new_star",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"write", "public.audit"}},
		},
		{
			// RETURN NEW alone: PLpgSQL_stmt_return skipped; no datums with recfield accesses.
			name:     "trigger_return_new",
			file:     "trigger_return_new.sql",
			funcPath: "public.trigger_return_new",
			schema:   "public",
			wantOK:   true,
			noEdges:  true,
		},
		{
			// SELECT 1 without BEGIN/END is not valid PL/pgSQL; pg_query rejects it.
			name:     "malformed_body",
			file:     "malformed_body.sql",
			funcPath: "public.malformed_body",
			schema:   "public",
			wantOK:   false,
		},
		{
			name:     "call_procedure",
			file:     "call_procedure.sql",
			funcPath: "public.call_procedure",
			schema:   "public",
			wantOK:   true,
			edges:    []plpgsqlEdge{{"procedure_call", "public.my_procedure"}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sql, err := os.ReadFile(filepath.Join("testdata", "plpgsql", tc.file))
			if err != nil {
				t.Fatalf("read fixture %s: %v", tc.file, err)
			}

			refs, ok := plpgsqlExtractRefs(tc.funcPath, tc.schema, string(sql))
			if ok != tc.wantOK {
				t.Fatalf("parse ok=%v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				if len(refs) != 0 {
					t.Errorf("expected nil/empty refs on parse failure; got %v", refs)
				}
				return
			}

			// Check that no resolver-internal flags escaped into the final refs.
			assertNoPendingFlags(t, refs)

			// Assert expected edges.
			for _, e := range tc.edges {
				if !hasBodyRef(t, refs, e.op, e.sym) {
					t.Errorf("missing edge op=%s sym=%s; got %v", e.op, e.sym, refs)
				}
			}

			// Assert required metadata flags.
			for _, flag := range tc.metaFlags {
				if !hasMetaFlag(t, refs, flag) {
					t.Errorf("missing metadata flag %q; got %v", flag, refs)
				}
			}

			// Assert zero body_references when noEdges is set.
			if tc.noEdges {
				assertNoBodyRefEdges(t, refs)
			}
		})
	}
}

// assertNoPendingFlags verifies that no resolver-internal flags (pending_execute,
// trigger_special) remain in refs after plpgsqlExtractRefs returns. These are
// always consumed by ResolveDynamicExecute / ResolveTriggerNewOld.
func assertNoPendingFlags(t *testing.T, refs []indexer.Reference) {
	t.Helper()
	for _, r := range refs {
		if v, _ := r.Metadata["pending_execute"].(bool); v {
			t.Errorf("pending_execute should be cleared by resolver; got %v", r)
		}
		if v, _ := r.Metadata["trigger_special"].(bool); v {
			t.Errorf("trigger_special should be cleared by resolver; got %v", r)
		}
	}
}

// assertNoBodyRefEdges verifies that refs contains no body_references edges
// with sym: targets.
func assertNoBodyRefEdges(t *testing.T, refs []indexer.Reference) {
	t.Helper()
	for _, r := range refs {
		if r.Kind == edgeKindBodyReferences && strings.HasPrefix(r.Target, "sym:") {
			t.Errorf("expected no body_references sym: edges; got %v", r)
		}
	}
}
