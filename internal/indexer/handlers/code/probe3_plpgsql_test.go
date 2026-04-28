package code

import (
	"fmt"
	"testing"
	sitter "github.com/tree-sitter/go-tree-sitter"
	"librarian/internal/indexer/handlers/code/tree_sitter_sql"
)

func dumpTree(n *sitter.Node, source []byte, indent int) {
	if n == nil {
		return
	}
	prefix := ""
	for i := 0; i < indent; i++ {
		prefix += "  "
	}
	text := ""
	if n.ChildCount() == 0 {
		text = fmt.Sprintf(" [%q]", n.Utf8Text(source))
	}
	fmt.Printf("%s%s%s\n", prefix, n.Kind(), text)
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		dumpTree(c, source, indent+1)
	}
}

func TestProbeCreateTriggerTree(t *testing.T) {
	src := []byte(`CREATE TRIGGER my_trigger
  AFTER INSERT OR UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION audit_func();`)

	parser := sitter.NewParser()
	defer parser.Close()
	parser.SetLanguage(sitter.NewLanguage(tree_sitter_sql.Language()))
	tree := parser.Parse(src, nil)
	defer tree.Close()

	root := tree.RootNode()
	dumpTree(root, src, 0)
}
