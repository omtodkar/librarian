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

// TestGoGrammar_ContainerKindDoesNotRewriteMethods verifies that the walker's
// `function → method` rewrite — introduced for Python to differentiate class
// methods from module-level functions — does not fire for Go. Go's AST has
// distinct `function_declaration` and `method_declaration` node types, so
// method detection is static; a future regression that broadened the rewrite
// condition (e.g., rewriting on any non-empty containerKind) would corrupt Go
// kinds. type_declaration is the only Go container kind and it never maps to
// "class", so this test pins that invariant.
func TestGoGrammar_ContainerKindDoesNotRewriteMethods(t *testing.T) {
	src := []byte(`package rw

type Svc struct{}

// Method on a Go type — AST node is method_declaration, already Kind="method".
func (s *Svc) Do() {}

// Top-level function — AST node is function_declaration, must stay "function".
func Helper() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("rw.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	gotKinds := map[string]string{}
	for _, u := range doc.Units {
		gotKinds[u.Title] = u.Kind
	}
	if gotKinds["Do"] != "method" {
		t.Errorf("Do Kind = %q, want method", gotKinds["Do"])
	}
	if gotKinds["Helper"] != "function" {
		t.Errorf("Helper Kind = %q, want function (rewrite must not fire outside class containers)", gotKinds["Helper"])
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

// --- lib-wji.1: interface embedding ---

// Single embedded interface: `type Reader interface { io.Reader }`.
// Relation is "embeds" — distinct from Java/TS "extends".
func TestGoGrammar_SingleInterfaceEmbedding(t *testing.T) {
	src := []byte(`package s

type Reader interface {
	io.Reader
	ReadOne() (byte, error)
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.Reader")
	if len(refs) != 1 {
		t.Fatalf("expected 1 embedded interface, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "io.Reader" {
		t.Errorf("Target = %q, want io.Reader", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
}

// Multiple embedded interfaces mixed with method specs. Method specs must
// not leak into inherits refs.
func TestGoGrammar_MultipleInterfaceEmbeddings(t *testing.T) {
	src := []byte(`package s

type ReadWriter interface {
	io.Reader
	io.Writer
	Close() error
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.ReadWriter")
	if len(refs) != 2 {
		t.Fatalf("expected 2 embeddings, got %d (%+v)", len(refs), refs)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.Target] = true
	}
	for _, want := range []string{"io.Reader", "io.Writer"} {
		if !seen[want] {
			t.Errorf("missing embedded interface %q (got %v)", want, seen)
		}
	}
}

// Bare-name embedding (same-package interface): `type X interface { Base }`.
func TestGoGrammar_BareInterfaceEmbedding(t *testing.T) {
	src := []byte(`package s

type Base interface {
	Do()
}

type X interface {
	Base
	Extra()
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.X")
	if len(refs) != 1 {
		t.Fatalf("expected 1 embedded interface, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
}

// Plain struct embedding emits one inherits edge with relation=embeds.
// Named fields (db *DB) are filtered out.
func TestGoGrammar_StructEmbeddingPlain(t *testing.T) {
	src := []byte(`package s

type Base struct{}

type Service struct {
	Base
	db *DB
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.Service")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
}

// --- lib-r4s.7: struct embedding ---

// Pointer embedding: *Base produces pointer=true in Metadata.
func TestGoGrammar_StructEmbeddingPointer(t *testing.T) {
	src := []byte(`package s

type S struct {
	*Base
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["pointer"] != true {
		t.Errorf("pointer = %v, want true", refs[0].Metadata["pointer"])
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
}

// Qualified embedding: pkg.Base produces qualified_name="pkg.Base" and leaf Name="Base".
func TestGoGrammar_StructEmbeddingQualified(t *testing.T) {
	src := []byte(`package s

type S struct {
	pkg.Base
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
	if refs[0].Metadata["qualified_name"] != "pkg.Base" {
		t.Errorf("qualified_name = %v, want pkg.Base", refs[0].Metadata["qualified_name"])
	}
}

// Pointer+qualified embedding: *pkg.Base produces both pointer=true and qualified_name.
func TestGoGrammar_StructEmbeddingPointerQualified(t *testing.T) {
	src := []byte(`package s

type S struct {
	*pkg.Base
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
	if refs[0].Metadata["pointer"] != true {
		t.Errorf("pointer = %v, want true", refs[0].Metadata["pointer"])
	}
	if refs[0].Metadata["qualified_name"] != "pkg.Base" {
		t.Errorf("qualified_name = %v, want pkg.Base", refs[0].Metadata["qualified_name"])
	}
}

// Generic embedding: Base[T] produces type_args=["T"].
func TestGoGrammar_StructEmbeddingGeneric(t *testing.T) {
	src := []byte(`package s

type S struct {
	Base[T]
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

// Qualified+generic embedding: pkg.Base[T] produces Name="Base", qualified_name="pkg.Base", type_args=["T"].
func TestGoGrammar_StructEmbeddingQualifiedGeneric(t *testing.T) {
	src := []byte(`package s

type S struct {
	pkg.Base[T]
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
	if refs[0].Metadata["qualified_name"] != "pkg.Base" {
		t.Errorf("qualified_name = %v, want pkg.Base", refs[0].Metadata["qualified_name"])
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

// Pointer+qualified+generic embedding: *pkg.Base[T] produces pointer=true, qualified_name="pkg.Base", type_args=["T"].
func TestGoGrammar_StructEmbeddingPointerQualifiedGeneric(t *testing.T) {
	src := []byte(`package s

type S struct {
	*pkg.Base[T]
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
	if refs[0].Metadata["pointer"] != true {
		t.Errorf("pointer = %v, want true", refs[0].Metadata["pointer"])
	}
	if refs[0].Metadata["qualified_name"] != "pkg.Base" {
		t.Errorf("qualified_name = %v, want pkg.Base", refs[0].Metadata["qualified_name"])
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

// Qualified+generic embedding with multiple type params: pkg.Base[K, V] produces
// Name="Base", qualified_name="pkg.Base", type_args=["K", "V"].
func TestGoGrammar_StructEmbeddingQualifiedGenericMultiParam(t *testing.T) {
	src := []byte(`package s

type S struct {
	pkg.Base[K, V]
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "embeds" {
		t.Errorf("relation = %v, want embeds", refs[0].Metadata["relation"])
	}
	if refs[0].Metadata["qualified_name"] != "pkg.Base" {
		t.Errorf("qualified_name = %v, want pkg.Base", refs[0].Metadata["qualified_name"])
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 2 || args[0] != "K" || args[1] != "V" {
		t.Errorf("type_args = %v, want [K V]", args)
	}
}

// Multiple embeddings: A, B, C all produce edges; named field filtered out.
func TestGoGrammar_StructEmbeddingMultiple(t *testing.T) {
	src := []byte(`package s

type S struct {
	A
	B
	C
	name string
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "s.S")
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs (A, B, C), got %d (%+v)", len(refs), refs)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.Target] = true
		if r.Metadata["relation"] != "embeds" {
			t.Errorf("ref %q: relation = %v, want embeds", r.Target, r.Metadata["relation"])
		}
	}
	for _, want := range []string{"A", "B", "C"} {
		if !seen[want] {
			t.Errorf("missing embedded target %q (got %v)", want, seen)
		}
	}
}

// Type aliases: `type Foo = int` (type_alias) and `type Bar int` (type_spec
// with a non-interface type) both produce Kind="type" Units but no parents.
func TestGoGrammar_NonInterfaceTypeSpecsYieldNoParents(t *testing.T) {
	src := []byte(`package s

type MyInt int
type MyString = string
type MyFn func(int) int
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("s.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, u := range doc.Units {
		if u.Kind != "type" {
			continue
		}
		refs := inheritsRefsBySource(doc, u.Path)
		if len(refs) != 0 {
			t.Errorf("non-interface type %q produced inherits refs: %+v", u.Path, refs)
		}
	}
}

// TestGoGrammar_MethodPathIncludesReceiverType pins the lib-cd5 / lib-6wz
// invariant: Go method Units embed the receiver type in Unit.Path while
// leaving Unit.Title as the bare method name. Two methods with the same
// name on distinct receivers in the same package therefore project to
// distinct sym: nodes rather than colliding on `sym:<pkg>.<method>`.
//
// Exercises value receivers (`(s Svc)`) and pointer receivers (`(s *Svc)`),
// and confirms a function_declaration (no receiver) stays at the flat
// `<pkg>.<name>` path — the resolver hook must be inert for non-methods.
func TestGoGrammar_MethodPathIncludesReceiverType(t *testing.T) {
	src := []byte(`package auth

type AuthServiceServer struct{}
type UnimplementedAuthServiceServer struct{}

// Pointer receiver.
func (s *AuthServiceServer) Login() error { return nil }

// Value receiver.
func (u UnimplementedAuthServiceServer) Login() error { return nil }

// Plain function (no receiver) — path stays flat.
func Helper() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("auth/service.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Unique-title symbols: one Unit each, assert Path directly by looking
	// up by (Kind, Title). The two Login methods share Title="Login" so they
	// can't go through this map — handled as a multi-match set below.
	pathsByKindTitle := map[string]string{}
	for _, u := range doc.Units {
		key := u.Kind + ":" + u.Title
		if u.Title == "Login" {
			continue
		}
		if existing, already := pathsByKindTitle[key]; already {
			t.Fatalf("fixture bug: %s appears twice (%q and %q); this table-lookup assumes unique Kind+Title", key, existing, u.Path)
		}
		pathsByKindTitle[key] = u.Path
	}
	for _, tc := range []struct {
		key, wantPath string
	}{
		{"function:Helper", "auth.Helper"},
		{"type:AuthServiceServer", "auth.AuthServiceServer"},
		{"type:UnimplementedAuthServiceServer", "auth.UnimplementedAuthServiceServer"},
	} {
		if got := pathsByKindTitle[tc.key]; got != tc.wantPath {
			t.Errorf("%s Path = %q, want %q", tc.key, got, tc.wantPath)
		}
	}

	// Multi-match set: Title="Login" on two distinct receivers. Both must
	// surface as Kind="method" with receiver-qualified Paths that differ by
	// receiver type only. The whole point of lib-6wz / lib-cd5 is that the
	// two no longer collide on a single sym:auth.Login node.
	wantPaths := map[string]bool{
		"auth.AuthServiceServer.Login":              false,
		"auth.UnimplementedAuthServiceServer.Login": false,
	}
	var loginPaths []string
	for _, u := range doc.Units {
		if u.Title != "Login" {
			continue
		}
		if u.Kind != "method" {
			t.Errorf("Login Unit Kind = %q, want method", u.Kind)
		}
		loginPaths = append(loginPaths, u.Path)
		if _, ok := wantPaths[u.Path]; !ok {
			t.Errorf("unexpected Login path %q", u.Path)
			continue
		}
		wantPaths[u.Path] = true
	}
	if len(loginPaths) != len(wantPaths) {
		t.Fatalf("expected %d Login method Units (one per receiver); got %d: %v",
			len(wantPaths), len(loginPaths), loginPaths)
	}
	for p, seen := range wantPaths {
		if !seen {
			t.Errorf("missing Login Unit with Path %q; got %v", p, loginPaths)
		}
	}
}

// TestGoGrammar_MethodPathGenericReceiverStripsTypeArgs pins the convention
// that `(c *Container[K, V]) Get()` emits path `pkg.Container.Get` — the
// type arguments are stripped so generic and non-generic receivers of the
// same type name share a sym: node (the Unit.Path identifies the declaring
// type, not the specific instantiation). Matches the Kotlin/Swift extension
// receiver metadata convention.
func TestGoGrammar_MethodPathGenericReceiverStripsTypeArgs(t *testing.T) {
	src := []byte(`package col

type Container[K comparable, V any] struct{}

func (c *Container[K, V]) Get(k K) V {
	var zero V
	return zero
}

func (c Container[K, V]) Set(k K, v V) {}

// Single-arg generic — exercises the same strip logic but with exactly one
// bracket parameter. A stripper that hand-rolled parsing of the comma-
// separated type-arg list could pass the two-arg case and silently fail
// here if the single-arg shape routes through a different branch.
type Box[T any] struct{}

func (b *Box[T]) Peek() T {
	var zero T
	return zero
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("col/container.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	paths := map[string]string{}
	for _, u := range doc.Units {
		if u.Kind == "method" {
			paths[u.Title] = u.Path
		}
	}
	if got := paths["Get"]; got != "col.Container.Get" {
		t.Errorf("Get Path = %q, want col.Container.Get (pointer-generic receiver stripped)", got)
	}
	if got := paths["Set"]; got != "col.Container.Set" {
		t.Errorf("Set Path = %q, want col.Container.Set (value-generic receiver stripped)", got)
	}
	if got := paths["Peek"]; got != "col.Box.Peek" {
		t.Errorf("Peek Path = %q, want col.Box.Peek (single-arg generic receiver stripped)", got)
	}
}

// --- lib-oyk: function-to-function call edges ---

// TestGoGrammar_CallEdges_SameFileResolved verifies that a same-file call from
// A() to B() emits a resolved call Reference with Source="pkg.A", Target="pkg.B",
// and Metadata["confidence"]="resolved".
func TestGoGrammar_CallEdges_SameFileResolved(t *testing.T) {
	src := []byte(`package svc

func A() {
	B()
}

func B() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/service.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "svc.A")
	if len(refs) == 0 {
		t.Fatalf("expected call refs from svc.A, got none")
	}

	var toB *indexer.Reference
	for i := range refs {
		if refs[i].Target == "svc.B" {
			toB = &refs[i]
			break
		}
	}
	if toB == nil {
		t.Fatalf("expected call edge svc.A → svc.B; refs from A: %+v", refs)
	}
	if toB.Metadata == nil || toB.Metadata["confidence"] != "resolved" {
		t.Errorf("call edge svc.A → svc.B: confidence = %v, want resolved", toB.Metadata)
	}
}

// TestGoGrammar_CallEdges_CrossFileUnresolved verifies that a call to an
// identifier not defined in the same file gets confidence="unresolved" and
// the target is the bare callee name (not sym-prefixed).
func TestGoGrammar_CallEdges_CrossFileUnresolved(t *testing.T) {
	src := []byte(`package svc

func Handler() {
	ExternalHelper()
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/handler.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "svc.Handler")
	if len(refs) == 0 {
		t.Fatalf("expected call refs from svc.Handler, got none")
	}

	var toExt *indexer.Reference
	for i := range refs {
		if refs[i].Target == "ExternalHelper" {
			toExt = &refs[i]
			break
		}
	}
	if toExt == nil {
		t.Fatalf("expected unresolved call to ExternalHelper; refs: %+v", refs)
	}
	if toExt.Metadata == nil || toExt.Metadata["confidence"] != "unresolved" {
		t.Errorf("expected confidence=unresolved; got %v", toExt.Metadata)
	}
}

// TestGoGrammar_CallEdges_MethodCallerPath verifies that a call inside a method
// anchors the edge at the receiver-qualified path (e.g. svc.Service.Handle),
// not the bare function name.
func TestGoGrammar_CallEdges_MethodCallerPath(t *testing.T) {
	src := []byte(`package svc

type Service struct{}

func (s *Service) Handle() {
	s.validate()
}

func (s *Service) validate() {}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/service.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "svc.Service.Handle")
	if len(refs) == 0 {
		t.Fatalf("expected call refs from svc.Service.Handle, got none")
	}

	var toValidate *indexer.Reference
	for i := range refs {
		if refs[i].Target == "svc.Service.validate" {
			toValidate = &refs[i]
			break
		}
	}
	if toValidate == nil {
		t.Fatalf("expected resolved edge to svc.Service.validate; refs from Handle: %+v", refs)
	}
	if toValidate.Metadata == nil || toValidate.Metadata["confidence"] != "resolved" {
		t.Errorf("expected confidence=resolved; got %v", toValidate.Metadata)
	}
}

// TestGoGrammar_CallEdges_PackageLevelNoEdge verifies that calls at package
// level (e.g. var init, package-level function calls outside any function body)
// do NOT produce call edges because there is no enclosing sym: node.
func TestGoGrammar_CallEdges_PackageLevelNoEdge(t *testing.T) {
	src := []byte(`package svc

func helper() string { return "x" }

var x = helper()
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/vars.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// helper() is called at package level — no call ref should be emitted at all.
	var callRefs []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "call" {
			callRefs = append(callRefs, r)
		}
	}
	if len(callRefs) != 0 {
		t.Errorf("package-level call produced %d call ref(s); want 0: %+v", len(callRefs), callRefs)
	}
}

// TestGoGrammar_CallEdges_AmbiguousOnSameTitleCollision verifies that when two
// methods in the same file share a bare name (e.g. Login on AuthServiceServer
// and UnimplementedAuthServiceServer), a call to that name gets
// confidence="ambiguous" rather than "resolved" pointing at the wrong receiver.
func TestGoGrammar_CallEdges_AmbiguousOnSameTitleCollision(t *testing.T) {
	src := []byte(`package svc

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() {}

type UnimplementedAuthServiceServer struct{}

func (s *UnimplementedAuthServiceServer) Login() {}

type Client struct{}

func (c *Client) DoLogin() {
	c.Login()
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/auth.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "svc.Client.DoLogin")
	if len(refs) == 0 {
		t.Fatalf("expected call refs from svc.Client.DoLogin, got none")
	}

	// When ambiguous, target is the bare callee name — not a receiver-qualified path.
	var loginRef *indexer.Reference
	for i := range refs {
		if refs[i].Target == "Login" {
			loginRef = &refs[i]
			break
		}
	}
	if loginRef == nil {
		t.Fatalf("expected a call ref with Target=\"Login\"; refs from DoLogin: %+v", refs)
	}
	if loginRef.Metadata == nil || loginRef.Metadata["confidence"] != "ambiguous" {
		t.Errorf("expected confidence=ambiguous for ambiguous Login call; got %v", loginRef.Metadata)
	}
}

// TestGoGrammar_CallEdges_AmbiguousThreeWayCollision verifies that N=3 same-title
// symbols (A.Login, B.Login, C.Login) produce confidence="ambiguous" — locking
// the len(paths) > 1 invariant for N > 2.
func TestGoGrammar_CallEdges_AmbiguousThreeWayCollision(t *testing.T) {
	src := []byte(`package svc

type A struct{}
type B struct{}
type C struct{}

func (a *A) Login() {}
func (b *B) Login() {}
func (c *C) Login() {}

type Client struct{}

func (cl *Client) DoLogin() {
	cl.Login()
}
`)
	h := code.New(code.NewGoGrammar())
	doc, err := h.Parse("svc/auth.go", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "svc.Client.DoLogin")
	if len(refs) == 0 {
		t.Fatalf("expected call refs from svc.Client.DoLogin, got none")
	}

	var loginRef *indexer.Reference
	for i := range refs {
		if refs[i].Target == "Login" {
			loginRef = &refs[i]
			break
		}
	}
	if loginRef == nil {
		t.Fatalf("expected call ref with Target=\"Login\" for 3-way collision; refs: %+v", refs)
	}
	if loginRef.Metadata == nil || loginRef.Metadata["confidence"] != "ambiguous" {
		t.Errorf("expected confidence=ambiguous for 3-way Login collision; got %v", loginRef.Metadata)
	}
}
