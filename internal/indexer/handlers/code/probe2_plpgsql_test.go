package code

import (
	"encoding/json"
	"fmt"
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

func TestProbeInsertNewRef(t *testing.T) {
	// Test what pg_query does with NEW.field in SQL
	sql := "INSERT INTO audit (user_id, action) VALUES (NEW.user_id, TG_OP)"
	result, err := pg_query.ParseToJSON(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)
	data, _ := json.MarshalIndent(parsed, "", "  ")
	fmt.Println(string(data))
}

func TestProbeDynExecute(t *testing.T) {
	// Test EXECUTE literals
	sqls := []struct{ name, sql string }{
		{"literal", "SELECT 'INSERT INTO audit VALUES (1)'"},
		{"variable", "SELECT 'DELETE FROM ' || tname"},
		{"concat_partial", "SELECT 'SELECT * FROM ' || tname || ' WHERE id=1'"},
		{"nested_exec", "SELECT 'EXECUTE ''SELECT 1'''"},
	}
	for _, tc := range sqls {
		result, err := pg_query.ParseToJSON(tc.sql)
		if err != nil {
			t.Logf("%s: error: %v", tc.name, err)
			continue
		}
		var parsed map[string]any
		json.Unmarshal([]byte(result), &parsed)
		data, _ := json.MarshalIndent(parsed, "", "  ")
		fmt.Printf("=== %s ===\n%s\n", tc.name, data)
	}
}

func TestProbeDynExecuteInPLPGSQL(t *testing.T) {
	sql := `CREATE FUNCTION f(tname text) RETURNS void AS $$
BEGIN
  EXECUTE 'DELETE FROM ' || tname;
  EXECUTE 'INSERT INTO audit VALUES (1)';
  EXECUTE 'SELECT * FROM ' || tname || ' WHERE id=1';
END;
$$ LANGUAGE plpgsql;`

	result, err := pg_query.ParsePlPgSqlToJSON(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var funcs []map[string]any
	json.Unmarshal([]byte(result), &funcs)
	data, _ := json.MarshalIndent(funcs[0], "", "  ")
	fmt.Println(string(data))
}
