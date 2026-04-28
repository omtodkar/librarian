package code

import (
	"encoding/json"
	"fmt"
	"testing"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

func TestProbeTriggerAST(t *testing.T) {
	sql := `CREATE FUNCTION audit_trigger() RETURNS trigger AS $$
BEGIN
  INSERT INTO audit (user_id, action) VALUES (NEW.user_id, TG_OP);
  NEW.updated_at := now();
  IF NEW.email = OLD.email THEN
    NULL;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;`

	result, err := pg_query.ParsePlPgSqlToJSON(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var funcs []map[string]any
	if err := json.Unmarshal([]byte(result), &funcs); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	data, _ := json.MarshalIndent(funcs[0], "", "  ")
	fmt.Println(string(data))
	t.Log("printed AST")
}

func TestProbeCreateTrigger(t *testing.T) {
	sql := `CREATE TRIGGER my_trigger
  AFTER INSERT OR UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION audit_func();`

	result, err := pg_query.ParseToJSON(sql)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	fmt.Println(result)
	t.Log("printed create trigger AST")
}
