package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const goSample = `// Package auth handles authentication.
package auth

import (
	"fmt"
	"strings"
)

// Service authenticates users against a backing store.
type Service struct {
	db *DB
}

// Validate checks the given credentials.
// TODO: rate-limit failed attempts.
func (s *Service) Validate(user, pass string) bool {
	return s.db.Check(user, pass)
}

// helperFunc is a package-private utility.
// NOTE: kept public for testing.
func helperFunc(x string) string {
	// FIXME: this is quadratic
	return strings.ToLower(x)
}
`

func TestGoGrammar_ParseExtractsUnitsImportsSignals(t *testing.T) {
	h := code.New(code.NewGoGrammar())

	doc, err := h.Parse("auth/service.go", []byte(goSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Title tracks the package name.
	if doc.Title != "auth" {
		t.Errorf("Title = %q, want %q", doc.Title, "auth")
	}
	if doc.Format != "go" {
		t.Errorf("Format = %q, want %q", doc.Format, "go")
	}
	if doc.DocType != "code" {
		t.Errorf("DocType = %q, want %q", doc.DocType, "code")
	}

	// Symbols: Service (type), Validate (method), helperFunc (function).
	wantByTitle := map[string]string{
		"Service":    "type",
		"Validate":   "method",
		"helperFunc": "function",
	}
	gotByTitle := map[string]string{}
	for _, u := range doc.Units {
		gotByTitle[u.Title] = u.Kind
	}
	for name, kind := range wantByTitle {
		got, ok := gotByTitle[name]
		if !ok {
			t.Errorf("missing Unit for symbol %q", name)
			continue
		}
		if got != kind {
			t.Errorf("Unit %q Kind = %q, want %q", name, got, kind)
		}
	}

	// Unit.Path is prefixed with the package name.
	for _, u := range doc.Units {
		if !strings.HasPrefix(u.Path, "auth.") {
			t.Errorf("Unit %q Path = %q, expected to start with %q", u.Title, u.Path, "auth.")
		}
	}

	// Imports: fmt and strings.
	importSet := map[string]bool{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			importSet[r.Target] = true
		}
	}
	for _, want := range []string{"fmt", "strings"} {
		if !importSet[want] {
			t.Errorf("missing import %q; got %v", want, importSet)
		}
	}

	// Document-level signals: TODO (from Validate's docstring), NOTE (from
	// helperFunc's docstring), FIXME (inside helperFunc body).
	signalValues := map[string]bool{}
	for _, s := range doc.Signals {
		signalValues[s.Value] = true
	}
	for _, want := range []string{"todo", "fixme", "note"} {
		if !signalValues[want] {
			t.Errorf("missing signal %q; got %v", want, signalValues)
		}
	}

	// Per-unit signals: Validate should carry the TODO from its docstring.
	var validate *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Title == "Validate" {
			validate = &doc.Units[i]
			break
		}
	}
	if validate == nil {
		t.Fatal("Validate Unit not found")
	}
	haveTodoOnValidate := false
	for _, s := range validate.Signals {
		if s.Value == "todo" {
			haveTodoOnValidate = true
			break
		}
	}
	if !haveTodoOnValidate {
		t.Errorf("Validate unit missing TODO signal; got %+v", validate.Signals)
	}
}

func TestGoGrammar_ChunkPerSymbol(t *testing.T) {
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("auth/service.go", []byte(goSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 5, OverlapLines: 0})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	// Expect one chunk per symbol unit (above MinTokens). Service has no body
	// beyond the struct literal; still should exceed 5 tokens once the
	// section header is prepended.
	if len(chunks) < 2 {
		t.Errorf("expected at least 2 chunks, got %d", len(chunks))
	}
}

func TestGoGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("foo.go") == nil {
		t.Error(".go extension not registered")
	}
}

// TestGoGrammar_GroupedTypeDeclaration verifies that a `type ( X; Y; Z )`
// block emits one Unit per inner type_spec. The earlier implementation
// collapsed the whole declaration into a single Unit named after the first
// spec, silently dropping X's siblings.
func TestGoGrammar_GroupedTypeDeclaration(t *testing.T) {
	src := []byte(`package grouped

type (
	Foo struct {}
	Bar int
	Baz = string
)
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("grouped.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := map[string]bool{"Foo": true, "Bar": true, "Baz": true}
	got := map[string]bool{}
	for _, u := range doc.Units {
		got[u.Title] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing Unit for grouped type %q (got %v)", name, got)
		}
	}
}

// TestGoGrammar_BacktickImportPath verifies that raw_string_literal import
// paths (backtick-quoted) have their delimiters stripped. Double-quoted and
// backtick-quoted forms are both legal per the Go spec.
func TestGoGrammar_BacktickImportPath(t *testing.T) {
	src := []byte("package bt\n\nimport (\n\t`unsafe`\n\t\"fmt\"\n)\n\nfunc X() {}\n")
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("bt.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	paths := map[string]bool{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			paths[r.Target] = true
		}
	}
	if !paths["unsafe"] {
		t.Errorf("backtick import not stripped: %v", paths)
	}
	if !paths["fmt"] {
		t.Errorf("missing fmt import: %v", paths)
	}
}

// TestGoGrammar_BlankLineSeparatedComments verifies that a comment block
// separated from a symbol by a blank line does NOT attach as the symbol's
// docstring. tree-sitter emits each // line as its own comment node, so the
// walker must detect blank-line gaps via start-line positions.
func TestGoGrammar_BlankLineSeparatedComments(t *testing.T) {
	src := []byte(`package gap

// unrelated prologue about imports

// RealDoc is the actual docstring.
func RealDoc() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("gap.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var real *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Title == "RealDoc" {
			real = &doc.Units[i]
			break
		}
	}
	if real == nil {
		t.Fatal("RealDoc Unit not found")
	}
	if strings.Contains(real.Content, "unrelated prologue") {
		t.Errorf("blank-line-separated comment leaked into docstring:\n%s", real.Content)
	}
	if !strings.Contains(real.Content, "RealDoc is the actual docstring") {
		t.Errorf("expected real docstring to be present:\n%s", real.Content)
	}
}

// TestGoGrammar_AliasAndBlankImports verifies that aliased and blank-imported
// modules carry their alias in Reference.Metadata.
func TestGoGrammar_AliasAndBlankImports(t *testing.T) {
	src := []byte("package a\n\nimport (\n\taliased \"net/http\"\n\t_ \"net/http/pprof\"\n\t\"fmt\"\n)\n\nfunc X() {}\n")
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("a.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}
	if a, ok := byTarget["net/http"]; !ok {
		t.Errorf("missing aliased import")
	} else if a.Metadata == nil || a.Metadata["alias"] != "aliased" {
		t.Errorf("alias missing from metadata: %+v", a.Metadata)
	}
	if b, ok := byTarget["net/http/pprof"]; !ok {
		t.Errorf("missing blank import")
	} else if b.Metadata == nil || b.Metadata["alias"] != "_" {
		t.Errorf("blank-import alias missing: %+v", b.Metadata)
	}
}

// TestGoGrammar_SatisfiesGrammarInvariants runs the shared structural
// assertions that every Grammar implementation must satisfy.
func TestGoGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewGoGrammar())
	code.AssertGrammarInvariants(t, h, "auth/service.go", []byte(goSample))
}

// TestGoGrammar_SingleTypeWithDocstring guards against a regression where
// making type_declaration a container dropped preceding-comment docstrings on
// single-type forms. The second-round fix forwarded `pending` into the
// recursive extractUnits call so the inner type_spec claims the docstring.
func TestGoGrammar_SingleTypeWithDocstring(t *testing.T) {
	src := []byte(`package single

// MyType is the main type in this package.
type MyType struct {
	field int
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("single.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var myType *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Title == "MyType" {
			myType = &doc.Units[i]
			break
		}
	}
	if myType == nil {
		t.Fatal("MyType Unit not found")
	}
	if !strings.Contains(myType.Content, "MyType is the main type in this package.") {
		t.Errorf("docstring did not propagate into single-type declaration:\n%s", myType.Content)
	}
}

// TestGoGrammar_SequentialTypeDeclarations verifies that two ungrouped
// `type X`/`type Y` declarations each emit their own Unit. A regression
// where type_declaration got mis-mapped (e.g., back into SymbolKinds) would
// collapse both into one Unit under the first name.
func TestGoGrammar_SequentialTypeDeclarations(t *testing.T) {
	src := []byte(`package seq

type X struct{}
type Y int
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("seq.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := map[string]bool{}
	for _, u := range doc.Units {
		got[u.Title] = true
	}
	if !got["X"] || !got["Y"] {
		t.Errorf("expected both X and Y Units, got %v", got)
	}
}

// TestGoGrammar_MultiLineUnrelatedPrologue extends the blank-line-separation
// coverage from a one-line prologue to a multi-line one. Catches off-by-one
// bugs in commentsAreConsecutive's flush behaviour when `pending` has
// multiple items before the blank-line break.
func TestGoGrammar_MultiLineUnrelatedPrologue(t *testing.T) {
	src := []byte(`package prolog

// line one of an unrelated prologue
// line two of the same prologue
// line three of that prologue

// RealDoc is the docstring we want to attach.
func RealDoc() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("prolog.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var real *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Title == "RealDoc" {
			real = &doc.Units[i]
			break
		}
	}
	if real == nil {
		t.Fatal("RealDoc Unit not found")
	}
	if strings.Contains(real.Content, "prologue") {
		t.Errorf("multi-line prologue leaked into docstring:\n%s", real.Content)
	}
	if !strings.Contains(real.Content, "RealDoc is the docstring") {
		t.Errorf("actual docstring missing:\n%s", real.Content)
	}
}

// TestGoGrammar_EmptyFile verifies Parse on zero-length input returns a
// non-nil doc with zero Units and zero Refs. Chunking the empty doc should
// produce no chunks and no error.
func TestGoGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("empty.go", []byte{})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if doc == nil {
		t.Fatal("Parse(empty) returned nil doc")
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected 0 Units, got %d", len(doc.Units))
	}
	if len(doc.Refs) != 0 {
		t.Errorf("expected 0 Refs, got %d", len(doc.Refs))
	}
	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk(empty): %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

// TestGoGrammar_StaticFlagAlwaysFalse locks in the contract that the Go
// grammar never emits Static=true on import Refs. Java will use Static via
// `import static`; this negative test guards the shared Reference.Metadata
// plumbing from silent regression between now and when lib-x91 lands.
func TestGoGrammar_StaticFlagAlwaysFalse(t *testing.T) {
	src := []byte(`package s

import (
	"fmt"
	static "net/http"
	_ "net/http/pprof"
)

func X() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, r := range doc.Refs {
		if r.Kind != "import" {
			continue
		}
		if r.Metadata == nil {
			continue
		}
		if v, ok := r.Metadata["static"]; ok && v == true {
			t.Errorf("Go grammar should never emit Static=true; got %+v", r)
		}
	}
}
