package code_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const pySample = `"""Module docstring for service."""

import os
import foo.bar as fb
from collections import deque, OrderedDict
from typing import TypeAlias
from . import utils
from ..pkg import Thing as T

# Type aliases surface as Kind="type" Units via PEP 695 + PEP 613
type UserID = int
LegacyUserID: TypeAlias = int

# module-level TODO: refactor this file
def top_level(x):
    """Top-level helper."""
    return x

async def async_fn():
    """Async function."""
    pass

@dataclass
class Service:
    """Service does things.

    TODO: document the wire format.
    """
    x: int

    def validate(self, y):
        """Validate y against x."""
        # FIXME: validation is O(n^2)
        return y

    async def fetch(self):
        return await something()
`

func TestPythonGrammar_ParseExtractsClassMethodsFunctions(t *testing.T) {
	h := code.New(code.NewPythonGrammar())

	doc, err := h.Parse("auth/service.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Title = file stem without extension (service.py → service).
	if doc.Title != "service" {
		t.Errorf("Title = %q, want %q", doc.Title, "service")
	}
	if doc.Format != "python" {
		t.Errorf("Format = %q, want %q", doc.Format, "python")
	}

	// Units: top_level (function), async_fn (function), Service (class),
	// validate + fetch (method).
	wantKinds := map[string]string{
		"top_level": "function",
		"async_fn":  "function",
		"Service":   "class",
		"validate":  "method",
		"fetch":     "method",
	}
	gotKinds := map[string]string{}
	for _, u := range doc.Units {
		gotKinds[u.Title] = u.Kind
	}
	for name, kind := range wantKinds {
		got, ok := gotKinds[name]
		if !ok {
			t.Errorf("missing Unit for %q (all: %+v)", name, gotKinds)
			continue
		}
		if got != kind {
			t.Errorf("Unit %q Kind = %q, want %q", name, got, kind)
		}
	}

	// Path: class methods use the dotted form module.Class.method.
	wantPaths := map[string]string{
		"top_level": "service.top_level",
		"async_fn":  "service.async_fn",
		"Service":   "service.Service",
		"validate":  "service.Service.validate",
		"fetch":     "service.Service.fetch",
	}
	for _, u := range doc.Units {
		if want, ok := wantPaths[u.Title]; ok && u.Path != want {
			t.Errorf("Unit %q Path = %q, want %q", u.Title, u.Path, want)
		}
	}
}

// TestPythonGrammar_ImportsRawFromGrammar pins the raw AST-level extraction
// contract: Parse (without ParseCtx) skips the relative-import resolver, so
// `from . import X` and `from ..pkg import Y as T` surface here with leading
// dots preserved. Production code goes through ParseCtx — see
// TestPythonGrammar_ResolvesRelativeImportsViaParseCtx for the resolved form.
func TestPythonGrammar_ImportsRawFromGrammar(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}

	wantTargets := []string{
		"os",
		"foo.bar", // aliased plain import
		"collections.deque",
		"collections.OrderedDict",
		".utils",      // from . import utils (raw, unresolved)
		"..pkg.Thing", // from ..pkg import Thing as T (raw, unresolved)
	}
	for _, want := range wantTargets {
		if _, ok := byTarget[want]; !ok {
			t.Errorf("missing import %q; got %v", want, byTarget)
		}
	}

	if r, ok := byTarget["foo.bar"]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "fb" {
			t.Errorf("foo.bar missing alias 'fb': %+v", r.Metadata)
		}
	}
	if r, ok := byTarget["..pkg.Thing"]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "T" {
			t.Errorf("..pkg.Thing missing alias 'T': %+v", r.Metadata)
		}
	}
}

// TestPythonGrammar_ResolvesRelativeImportsViaParseCtx exercises the full
// ParseCtx pipeline with a real on-disk fixture — two levels of __init__.py
// establish mypkg.sub as the file's containing package, and the grammar's
// ResolveImports post-pass rewrites `.utils` → `mypkg.sub.utils` and
// `..pkg.Thing` → `mypkg.pkg.Thing`.
func TestPythonGrammar_ResolvesRelativeImportsViaParseCtx(t *testing.T) {
	root := t.TempDir()
	mustWrite := func(rel, body string) {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("mypkg/__init__.py", "")
	mustWrite("mypkg/sub/__init__.py", "")

	const src = `from . import utils
from .utils import X
from .. import sibling
from ..pkg import Thing as T
from collections import deque
`
	mustWrite("mypkg/sub/a.py", src)
	abs := filepath.Join(root, "mypkg", "sub", "a.py")

	h := code.New(code.NewPythonGrammar())
	doc, err := h.ParseCtx("mypkg/sub/a.py", []byte(src), indexer.ParseContext{AbsPath: abs})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}

	got := map[string]bool{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			got[r.Target] = true
		}
	}
	want := []string{
		"mypkg.sub.utils",   // from . import utils
		"mypkg.sub.utils.X", // from .utils import X
		"mypkg.sibling",     // from .. import sibling
		"mypkg.pkg.Thing",   // from ..pkg import Thing as T
		"collections.deque", // absolute import — pass-through
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing resolved import %q; got %v", w, got)
		}
	}
	for tgt := range got {
		if strings.HasPrefix(tgt, ".") || strings.Contains(tgt, "..") {
			t.Errorf("unresolved relative import survived: %q", tgt)
		}
	}
}

func TestPythonGrammar_DocstringsAttachToUnits(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Service class's triple-quoted docstring should appear in its Unit Content.
	svc := findUnit(doc, "Service")
	if svc == nil {
		t.Fatal("Service Unit missing")
	}
	if !strings.Contains(svc.Content, "Service does things.") {
		t.Errorf("Service docstring missing from Content:\n%s", svc.Content)
	}

	// validate's docstring should be in its Unit too.
	val := findUnit(doc, "validate")
	if val == nil {
		t.Fatal("validate Unit missing")
	}
	if !strings.Contains(val.Content, "Validate y against x.") {
		t.Errorf("validate docstring missing from Content:\n%s", val.Content)
	}
}

// Decorators are registered as comment types so their text flows into the
// next symbol's docstring buffer. @dataclass should be discoverable as part
// of Service's preamble — otherwise AI agents searching "@dataclass" for
// dataclass classes would miss them.
func TestPythonGrammar_DecoratorAppearsInDocstring(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := findUnit(doc, "Service")
	if svc == nil {
		t.Fatal("Service Unit missing")
	}
	if !strings.Contains(svc.Content, "dataclass") {
		t.Errorf("@dataclass decorator not reachable from Service Unit:\n%s", svc.Content)
	}
}

func TestPythonGrammar_RationaleSignals(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Document-level: TODO (module comment + Service docstring), FIXME (inside validate).
	seen := map[string]bool{}
	for _, s := range doc.Signals {
		seen[s.Value] = true
	}
	for _, want := range []string{"todo", "fixme"} {
		if !seen[want] {
			t.Errorf("missing document-level signal %q; got %v", want, seen)
		}
	}

	// Service's docstring contains TODO — unit-level signal must reflect it.
	svc := findUnit(doc, "Service")
	if svc == nil {
		t.Fatal("Service Unit missing")
	}
	hasServiceTodo := false
	for _, s := range svc.Signals {
		if s.Value == "todo" {
			hasServiceTodo = true
		}
	}
	if !hasServiceTodo {
		t.Errorf("Service unit missing TODO signal from its docstring; got %+v", svc.Signals)
	}
}

func TestPythonGrammar_ChunkPerSymbol(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 5, OverlapLines: 0})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	// Expect at least one chunk per Unit (Service + methods + top-level fns).
	if len(chunks) < 3 {
		t.Errorf("expected at least 3 chunks, got %d", len(chunks))
	}
}

func TestPythonGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("foo.py") == nil {
		t.Error(".py extension not registered")
	}
}

func TestPythonGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	// pyInvariantSample mirrors pySample but drops relative imports — the
	// grammar's Parse path deliberately skips the ResolveImports post-pass
	// (that happens under ParseCtx, which needs a real filesystem fixture).
	// Standing up a tempdir just to satisfy the invariants test would conflate
	// grammar-structural assertions with resolver integration; the resolver
	// has its own dedicated tests. This sample exercises every invariant
	// dimension the grammar owns directly.
	const pyInvariantSample = `"""Module docstring for service."""

import os
import foo.bar as fb
from collections import deque, OrderedDict
from typing import TypeAlias

type UserID = int
LegacyUserID: TypeAlias = int

def top_level(x):
    """Top-level helper."""
    return x

@dataclass
class Service:
    """Service does things."""
    x: int

    def validate(self, y):
        """Validate y against x."""
        return y
`
	h := code.New(code.NewPythonGrammar())
	code.AssertGrammarInvariants(t, h, "auth/service.py", []byte(pyInvariantSample))
}

// Nested function definitions inside another function are NOT extracted as
// separate Units (function_definition isn't a container). Documenting this
// pins the contract — if a future change makes function_definition a
// container, this test will catch the scope broadening.
func TestPythonGrammar_NestedFunctionNotIndexed(t *testing.T) {
	src := []byte(`def outer():
    def inner():
        return 1
    return inner
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("nest.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	names := map[string]bool{}
	for _, u := range doc.Units {
		names[u.Title] = true
	}
	if !names["outer"] {
		t.Errorf("expected outer Unit, got %v", names)
	}
	if names["inner"] {
		t.Errorf("inner function should not be extracted as its own Unit (nested scope); got %v", names)
	}
}

// Classes without a preceding docstring-as-first-statement shouldn't produce
// empty docstring lines that corrupt the Unit Content.
func TestPythonGrammar_ClassWithoutDocstring(t *testing.T) {
	src := []byte(`class Bare:
    x = 1
    def method(self):
        pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("bare.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	bare := findUnit(doc, "Bare")
	if bare == nil {
		t.Fatal("Bare Unit missing")
	}
	// No docstring → Content is just the class body. No leading blank line.
	if strings.HasPrefix(bare.Content, "\n\n") {
		t.Errorf("Unit Content has spurious leading blank lines:\n%q", bare.Content)
	}
}

// Empty file: the parser shouldn't crash, and there should be zero Units
// plus zero chunks — the handler must tolerate whitespace-only input for
// incremental indexing scenarios.
func TestPythonGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("empty.py", []byte{})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if doc == nil {
		t.Fatal("nil doc")
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected 0 Units, got %d", len(doc.Units))
	}
	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk(empty): %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

// `from X import *` must emit an ImportRef with a `*` marker; silently
// dropping star-imports would lose the re-export signal that matters for
// __init__.py re-exports and __future__ imports.
func TestPythonGrammar_WildcardFromImport(t *testing.T) {
	src := []byte("from utils import *\nfrom .pkg import *\nfrom . import *\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("w.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	targets := importTargets(doc)
	for _, want := range []string{"utils.*", ".pkg.*", ".*"} {
		if !targets[want] {
			t.Errorf("missing wildcard import %q (got %v)", want, targets)
		}
	}
}

// CPython doesn't treat a bytes literal as a docstring — including one in
// Unit content would corrupt search results with binary-looking text. The
// grammar must reject `b"..."` / `B"..."` / `rb"..."` / `br"..."` as the
// first-statement docstring source.
func TestPythonGrammar_BytesLiteralNotAdocstring(t *testing.T) {
	src := []byte(`def f():
    b"binary docstring attempt"
    return 1

def g():
    B"upper-case prefix"
    return 2

def h():
    rb"raw bytes"
    return 3
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("b.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, name := range []string{"f", "g", "h"} {
		u := findUnit(doc, name)
		if u == nil {
			t.Fatalf("%s Unit missing", name)
		}
		if strings.Contains(u.Content, "binary docstring attempt") ||
			strings.Contains(u.Content, "upper-case prefix") ||
			strings.Contains(u.Content, "raw bytes") {
			// If this triggers it means the bytes literal WAS attached as a
			// docstring — the rejection check in isBytesStringLiteral is off.
			continue
		}
	}
	// More precise: check the docstring section of each Unit doesn't contain
	// the bytes text. Since docstring goes through buildUnit's "docstring\n\n"
	// prefix, any failure shows up as leading text before the def keyword.
	for _, name := range []string{"f", "g", "h"} {
		u := findUnit(doc, name)
		if u == nil {
			continue
		}
		// A correctly-rejected docstring means the Unit.Content starts with
		// the def line, not with the bytes text.
		if !strings.HasPrefix(strings.TrimSpace(u.Content), "def") {
			t.Errorf("%s Unit Content appears to include bytes-literal as docstring:\n%s", name, u.Content)
		}
	}
}

// Type aliases come in two flavors: PEP 695's `type X = ...` dedicated
// syntax, and PEP 613's `X: TypeAlias = ...` annotated-assignment form.
// Both emit Kind="type" Units. Regular annotated assignments (`x: int = 5`)
// must NOT become Units — the heuristic keys on the annotation being
// literally TypeAlias / *.TypeAlias so unrelated statements are skipped,
// including class attributes (regression guard against the walker
// accidentally emitting a "type" Unit for every typed class field).
func TestPythonGrammar_TypeAliases(t *testing.T) {
	src := []byte(`from typing import TypeAlias

# PEP 695 (Python 3.12+)
type Matrix = list[list[float]]
type Vec[T] = list[T]

# PEP 613 (legacy)
Vector: TypeAlias = list[float]
Path: "typing.TypeAlias" = str

# Module-level annotated assignment — must not become a Unit
counter: int = 0

class Metrics:
    # Class-body typed attributes — must not become type-alias Units
    count: int = 0
    name: str = "default"
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("types.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byTitle := map[string]indexer.Unit{}
	for _, u := range doc.Units {
		byTitle[u.Title] = u
	}
	for _, want := range []string{"Matrix", "Vec", "Vector", "Path"} {
		u, ok := byTitle[want]
		if !ok {
			t.Errorf("missing type alias Unit %q (got %d Units)", want, len(byTitle))
			continue
		}
		if u.Kind != "type" {
			t.Errorf("%s Kind = %q, want %q", want, u.Kind, "type")
		}
		if u.Path != "types."+want {
			t.Errorf("%s Path = %q, want %q", want, u.Path, "types."+want)
		}
	}
	for _, shouldSkip := range []string{"counter", "count", "name"} {
		if _, ok := byTitle[shouldSkip]; ok {
			t.Errorf("non-TypeAlias annotated assignment %q leaked into Units", shouldSkip)
		}
	}
}

// A comma-separated `from X import A, B, C` emits one ImportRef per name so
// downstream tooling can resolve each independently.
func TestPythonGrammar_MultiNameFromImport(t *testing.T) {
	src := []byte("from typing import List, Dict, Optional\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("t.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]bool{
		"typing.List":     true,
		"typing.Dict":     true,
		"typing.Optional": true,
	}
	got := map[string]bool{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			got[r.Target] = true
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing from-import %q (got %v)", k, got)
		}
	}
}

// --- lib-wji.1: inheritance extraction ---

func TestPythonGrammar_SingleBareBaseFlaggedUnresolved(t *testing.T) {
	src := []byte(`class Foo(Base):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("t.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"t.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	r := refs[0]
	if r.Target != "Base" {
		t.Errorf("Target = %q, want Base", r.Target)
	}
	if r.Metadata["relation"] != "extends" {
		t.Errorf("relation = %v, want extends", r.Metadata["relation"])
	}
	if v, _ := r.Metadata["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true on bare base; got %+v", r.Metadata)
	}
}

func TestPythonGrammar_MultipleInheritance(t *testing.T) {
	src := []byte(`class Foo(A, B, C):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("multi.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"multi.Foo")
	if len(refs) != 3 {
		t.Fatalf("expected 3 bases, got %d (%+v)", len(refs), refs)
	}
	seen := map[string]bool{}
	for _, r := range refs {
		seen[r.Target] = true
	}
	for _, want := range []string{"A", "B", "C"} {
		if !seen[want] {
			t.Errorf("missing base %q (got %v)", want, seen)
		}
	}
}

func TestPythonGrammar_KeywordArgumentsAreNotParents(t *testing.T) {
	src := []byte(`class Meta(Base, metaclass=ABCMeta, total=False):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("meta.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"meta.Meta")
	if len(refs) != 1 {
		t.Fatalf("expected only Base as parent (1 ref), got %d: %+v", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base (metaclass/total kwargs filtered)", refs[0].Target)
	}
}

func TestPythonGrammar_SubscriptBaseExtractsGeneric(t *testing.T) {
	src := []byte(`class Stack(Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("stack.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"stack.Stack")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Generic" {
		t.Errorf("Target = %q, want Generic", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

func TestPythonGrammar_CallBaseMarkedUnresolvedExpression(t *testing.T) {
	src := []byte(`class Foo(factory()):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("f.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"f.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (call fallback), got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "factory" {
		t.Errorf("Target = %q, want factory (callee identifier fallback)", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved_expression"].(bool); !v {
		t.Errorf("expected unresolved_expression=true; got metadata %+v", refs[0].Metadata)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("call base should not carry unresolved=true: %+v", refs[0].Metadata)
	}
}

func TestPythonGrammar_AttributeBaseDottedAndResolved(t *testing.T) {
	src := []byte(`class Foo(pkg.subpkg.Base):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("a.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"a.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "pkg.subpkg.Base" {
		t.Errorf("Target = %q, want pkg.subpkg.Base", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("dotted target should not be marked unresolved: %+v", refs[0].Metadata)
	}
}

func TestPythonGrammar_ResolvesBareBaseViaFromImport(t *testing.T) {
	src := []byte(`from mypkg.bases import Base
class Foo(Base):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("foo.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"foo.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "mypkg.bases.Base" {
		t.Errorf("Target = %q, want mypkg.bases.Base (resolved via from-import)", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("resolved ref should not be marked unresolved")
	}
}

func TestPythonGrammar_ResolvesAliasedFromImport(t *testing.T) {
	src := []byte(`from pkg.bases import RealBase as B
class Foo(B):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("aliased.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"aliased.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "pkg.bases.RealBase" {
		t.Errorf("Target = %q, want pkg.bases.RealBase (alias B → RealBase)", refs[0].Target)
	}
}

func TestPythonGrammar_WildcardImportDoesNotResolveBareParent(t *testing.T) {
	src := []byte(`from mypkg.bases import *
class Foo(UnknownBase):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("star.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc,"star.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "UnknownBase" {
		t.Errorf("Target = %q, want UnknownBase (wildcard does not bind names)", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true; got %+v", refs[0].Metadata)
	}
}

// --- lib-0pa.1: expression-valued class bases — structured handling ---

func TestPythonGrammar_SubscriptBase_GenericMultiArg(t *testing.T) {
	src := []byte(`class Foo(Generic[T, U]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("g.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "g.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Generic" {
		t.Errorf("Target = %q, want Generic", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 2 || args[0] != "T" || args[1] != "U" {
		t.Errorf("type_args = %v, want [T U]", args)
	}
}

func TestPythonGrammar_SubscriptBase_Protocol(t *testing.T) {
	src := []byte(`class Foo(Protocol[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("p.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "p.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Protocol" {
		t.Errorf("Target = %q, want Protocol", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

func TestPythonGrammar_SubscriptBase_BuiltinList(t *testing.T) {
	src := []byte(`class Foo(list[str]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("bl.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "bl.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "list" {
		t.Errorf("Target = %q, want list", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "str" {
		t.Errorf("type_args = %v, want [str]", args)
	}
	// `list` has no import binding → resolveInheritsRefs marks it unresolved.
	if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true for bare builtin `list`; got %+v", refs[0].Metadata)
	}
}

func TestPythonGrammar_AttributeBase_LeafPlusQualifiedName(t *testing.T) {
	src := []byte(`class Foo(pkg.mod.Base):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("attr.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "attr.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	// After ResolveParents uses qualified_name, Target becomes the FQN.
	if refs[0].Target != "pkg.mod.Base" {
		t.Errorf("Target = %q, want pkg.mod.Base", refs[0].Target)
	}
	if qn, _ := refs[0].Metadata["qualified_name"].(string); qn != "pkg.mod.Base" {
		t.Errorf("qualified_name = %q, want pkg.mod.Base", qn)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("attribute-chain base should not be marked unresolved: %+v", refs[0].Metadata)
	}
}

func TestPythonGrammar_AttributeBase_TypingProtocol(t *testing.T) {
	src := []byte(`class Foo(typing.Protocol):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("tp.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "tp.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "typing.Protocol" {
		t.Errorf("Target = %q, want typing.Protocol", refs[0].Target)
	}
	if qn, _ := refs[0].Metadata["qualified_name"].(string); qn != "typing.Protocol" {
		t.Errorf("qualified_name = %q, want typing.Protocol", qn)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("attribute-chain base should not be marked unresolved: %+v", refs[0].Metadata)
	}
}

func TestPythonGrammar_CallBase_UnknownFactory(t *testing.T) {
	src := []byte(`class Foo(make_base(Mixin)):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("cb.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "cb.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (call fallback), got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "make_base" {
		t.Errorf("Target = %q, want make_base (callee identifier fallback)", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved_expression"].(bool); !v {
		t.Errorf("expected unresolved_expression=true; got metadata %+v", refs[0].Metadata)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("call base should not carry unresolved=true: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_SubscriptBase_AttributeValue covers the case where the
// subscript's value is itself an attribute chain (e.g. typing.Generic[T]).
// extractPythonBase splits the attribute into leaf + qualified_name, same as
// for a plain attribute base. ResolveParents does NOT rewrite Target via
// qualified_name when type_args are present — import-binding resolution is
// the preferred path for parameterised bases.
func TestPythonGrammar_SubscriptBase_AttributeValue(t *testing.T) {
	src := []byte(`class Foo(typing.Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("tg.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "tg.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	// Leaf identifier is the Target; qualified_name carries the full chain.
	if refs[0].Target != "Generic" {
		t.Errorf("Target = %q, want Generic", refs[0].Target)
	}
	if qn, _ := refs[0].Metadata["qualified_name"].(string); qn != "typing.Generic" {
		t.Errorf("qualified_name = %q, want typing.Generic", qn)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args = %v, want [T]", args)
	}
}

func TestPythonGrammar_MixedAttributeAndSubscriptBases(t *testing.T) {
	src := []byte(`class Foo(pkg.mod.Base, Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mx.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mx.Foo")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d (%+v)", len(refs), refs)
	}
	// Collect refs by target.
	byTarget := map[string]indexer.Reference{}
	for _, r := range refs {
		byTarget[r.Target] = r
	}

	// Attribute-chain base.
	attrRef, ok := byTarget["pkg.mod.Base"]
	if !ok {
		t.Errorf("missing attribute-chain parent pkg.mod.Base; got %v", byTarget)
	} else {
		if qn, _ := attrRef.Metadata["qualified_name"].(string); qn != "pkg.mod.Base" {
			t.Errorf("attribute parent qualified_name = %q, want pkg.mod.Base", qn)
		}
		if v, _ := attrRef.Metadata["unresolved"].(bool); v {
			t.Errorf("attribute-chain base should not be unresolved: %+v", attrRef.Metadata)
		}
	}

	// Subscript base.
	subRef, ok := byTarget["Generic"]
	if !ok {
		t.Errorf("missing subscript parent Generic; got %v", byTarget)
	} else {
		args, _ := subRef.Metadata["type_args"].([]string)
		if len(args) != 1 || args[0] != "T" {
			t.Errorf("subscript parent type_args = %v, want [T]", args)
		}
	}
}

// --- lib-oyk: function-to-function call edges ---

// TestPythonGrammar_CallEdges_SameFileResolved verifies that a same-file call
// from process() to helper() emits a resolved call edge.
func TestPythonGrammar_CallEdges_SameFileResolved(t *testing.T) {
	src := []byte(`
def process():
    helper()

def helper():
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// doc.Title = "svc" (file stem), so Unit paths are "svc.process" and "svc.helper".
	refs := callRefsBySource(doc, "svc.process")
	var toHelper *indexer.Reference
	for i := range refs {
		if refs[i].Target == "svc.helper" {
			toHelper = &refs[i]
			break
		}
	}
	if toHelper == nil {
		t.Fatalf("expected resolved call svc.process → svc.helper; refs: %+v", refs)
	}
	if toHelper.Metadata == nil || toHelper.Metadata["confidence"] != "resolved" {
		t.Errorf("expected confidence=resolved; got %v", toHelper.Metadata)
	}
}

// TestPythonGrammar_CallEdges_MethodCallResolved verifies that a method calling
// another method in the same class resolves correctly using the local path.
func TestPythonGrammar_CallEdges_MethodCallResolved(t *testing.T) {
	src := []byte(`
class Service:
    def handle(self):
        self.validate()

    def validate(self):
        pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("service.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "service.Service.handle")
	var toValidate *indexer.Reference
	for i := range refs {
		if refs[i].Target == "service.Service.validate" {
			toValidate = &refs[i]
			break
		}
	}
	if toValidate == nil {
		t.Fatalf("expected resolved call service.Service.handle → service.Service.validate; refs: %+v", refs)
	}
	if toValidate.Metadata == nil || toValidate.Metadata["confidence"] != "resolved" {
		t.Errorf("expected confidence=resolved; got %v", toValidate.Metadata)
	}
}

// --- lib-0pa.2: TypeVar detection and Generic[T] resolution ---

// typeVarUnits returns all Units with Kind="typevar" from doc.
func typeVarUnits(doc *indexer.ParsedDoc) []indexer.Unit {
	var out []indexer.Unit
	for _, u := range doc.Units {
		if u.Kind == "typevar" {
			out = append(out, u)
		}
	}
	return out
}

// findTypeVarUnit returns the first typevar Unit with the given Title, or nil.
func findTypeVarUnit(doc *indexer.ParsedDoc, title string) *indexer.Unit {
	for i := range doc.Units {
		if doc.Units[i].Kind == "typevar" && doc.Units[i].Title == title {
			return &doc.Units[i]
		}
	}
	return nil
}

func TestPythonGrammar_TypeVarBasic(t *testing.T) {
	src := []byte(`T = TypeVar("T")
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; got units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.T" {
		t.Errorf("Path = %q, want mymod.T", u.Path)
	}
	if kh, _ := u.Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("kind_hint = %q, want typevar", kh)
	}
	if v, _ := u.Metadata["variance"].(string); v != "invariant" {
		t.Errorf("variance = %q, want invariant", v)
	}
	if _, ok := u.Metadata["bound"]; ok {
		t.Errorf("unexpected bound key; metadata = %+v", u.Metadata)
	}
}

func TestPythonGrammar_TypeVarWithBound(t *testing.T) {
	src := []byte(`from typing import TypeVar
U = TypeVar("U", bound=Hashable)
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "U")
	if u == nil {
		t.Fatalf("expected typevar unit U; got units: %+v", typeVarUnits(doc))
	}
	if bound, _ := u.Metadata["bound"].(string); bound != "Hashable" {
		t.Errorf("bound = %q, want Hashable", bound)
	}
	if v, _ := u.Metadata["variance"].(string); v != "invariant" {
		t.Errorf("variance = %q, want invariant", v)
	}
}

func TestPythonGrammar_TypeVarWithConstraints(t *testing.T) {
	src := []byte(`V = TypeVar("V", int, str)
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "V")
	if u == nil {
		t.Fatalf("expected typevar unit V; got units: %+v", typeVarUnits(doc))
	}
	constraints, _ := u.Metadata["constraints"].([]string)
	if len(constraints) != 2 || constraints[0] != "int" || constraints[1] != "str" {
		t.Errorf("constraints = %v, want [int str]", constraints)
	}
}

func TestPythonGrammar_TypeVarCovariant(t *testing.T) {
	src := []byte(`K_co = TypeVar("K_co", covariant=True)
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "K_co")
	if u == nil {
		t.Fatalf("expected typevar unit K_co; got units: %+v", typeVarUnits(doc))
	}
	if v, _ := u.Metadata["variance"].(string); v != "covariant" {
		t.Errorf("variance = %q, want covariant", v)
	}
}

func TestPythonGrammar_TypeVarContravariant(t *testing.T) {
	src := []byte(`KT_contra = TypeVar("KT_contra", contravariant=True)
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "KT_contra")
	if u == nil {
		t.Fatalf("expected typevar unit KT_contra; got units: %+v", typeVarUnits(doc))
	}
	if v, _ := u.Metadata["variance"].(string); v != "contravariant" {
		t.Errorf("variance = %q, want contravariant", v)
	}
}

func TestPythonGrammar_TypeVarAliasedImport(t *testing.T) {
	src := []byte(`from typing import TypeVar as TV
T = TV("T")
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T (via aliased import TV); got units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.T" {
		t.Errorf("Path = %q, want mymod.T", u.Path)
	}
}

func TestPythonGrammar_TypeVarNameMismatch(t *testing.T) {
	// T = TypeVar("U") — variable name and TypeVar name string differ.
	// The unit must still be created, but name_mismatch=true must be set.
	src := []byte(`T = TypeVar("U")
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; got units: %+v", typeVarUnits(doc))
	}
	if nm, _ := u.Metadata["name_mismatch"].(bool); !nm {
		t.Errorf("expected name_mismatch=true for T=TypeVar(\"U\"); metadata=%+v", u.Metadata)
	}
}

func TestPythonGrammar_TypeVarBareAssignmentNotRecognised(t *testing.T) {
	// TV = TypeVar (not a call) must NOT produce a typevar unit.
	src := []byte(`TV = TypeVar
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tvs := typeVarUnits(doc)
	if len(tvs) != 0 {
		t.Errorf("expected no typevar units for bare assignment; got %+v", tvs)
	}
}

func TestPythonGrammar_TypeVarNonTypingOriginIgnored(t *testing.T) {
	// from mylib import TypeVar  — user-defined class named TypeVar should NOT
	// be treated as typing.TypeVar, so T = mylib.TypeVar("T") must not produce
	// a typevar unit.
	src := []byte(`from mylib import TypeVar
T = TypeVar("T")
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The bare name "TypeVar" is always in the set (for un-imported usage), so
	// T = TypeVar("T") WILL still match via the default entry. To isolate the
	// non-typing-origin path, we need a distinct alias.
	src2 := []byte(`from mylib import TypeVar as TV
T = TV("T")
`)
	doc2, err := h.Parse("mymod.py", src2)
	if err != nil {
		t.Fatalf("Parse src2: %v", err)
	}
	tvs := typeVarUnits(doc2)
	if len(tvs) != 0 {
		t.Errorf("expected no typevar units for aliased import from non-typing module; got %+v", tvs)
	}
	_ = doc // suppress unused-variable error; bare TypeVar is always matched
}

func TestPythonGrammar_TypeVarResolvedInGenericBase(t *testing.T) {
	src := []byte(`T = TypeVar("T")

class Foo(Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// TypeVar unit must exist.
	if findTypeVarUnit(doc, "T") == nil {
		t.Fatalf("expected typevar unit T; got %+v", typeVarUnits(doc))
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if refs[i].Target == "Generic" || refs[i].Target == "typing.Generic" {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 1 || resolved[0] != "sym:mymod.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mymod.T]", resolved)
	}
	// type_args (bare names) must be preserved.
	typeArgs, _ := genericRef.Metadata["type_args"].([]string)
	if len(typeArgs) != 1 || typeArgs[0] != "T" {
		t.Errorf("type_args = %v, want [T] (original preserved)", typeArgs)
	}
}

func TestPythonGrammar_TypeVarImportedNotResolved(t *testing.T) {
	// U is imported from another module — not declared as TypeVar in this file.
	// type_args_resolved must be absent; type_args must preserve bare "U".
	src := []byte(`from other_module import U

class Foo(Generic[U]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if refs[i].Target == "Generic" || strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	if _, ok := genericRef.Metadata["type_args_resolved"]; ok {
		t.Errorf("expected type_args_resolved absent for cross-module TypeVar; metadata: %+v", genericRef.Metadata)
	}
	typeArgs, _ := genericRef.Metadata["type_args"].([]string)
	if len(typeArgs) != 1 || typeArgs[0] != "U" {
		t.Errorf("type_args = %v, want [U] (preserved)", typeArgs)
	}
}

func TestPythonGrammar_TypeVarMixedResolvedUnresolved(t *testing.T) {
	// T is local TypeVar; U is imported from elsewhere.
	// type_args_resolved should contain only sym:mymod.T.
	// type_args should preserve both T and U.
	src := []byte(`from other_module import U
T = TypeVar("T")

class Foo(Generic[T, U]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if refs[i].Target == "Generic" || strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 1 || resolved[0] != "sym:mymod.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mymod.T] (only T resolved)", resolved)
	}
	typeArgs, _ := genericRef.Metadata["type_args"].([]string)
	if len(typeArgs) != 2 || typeArgs[0] != "T" || typeArgs[1] != "U" {
		t.Errorf("type_args = %v, want [T U] (full list preserved)", typeArgs)
	}
}

// --- lib-0pa.5: cross-module TypeVar resolution (pending metadata) ---

// TestPythonGrammar_TypeVarCrossModule_PendingSet verifies that when T is
// imported from another module (not declared locally), PostProcess records
// the import binding in type_args_pending_cross_module so the post-graph-pass
// resolver can validate the TypeVar node later.
func TestPythonGrammar_TypeVarCrossModule_PendingSet(t *testing.T) {
	src := []byte(`from other.types import T

class Foo(Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	// type_args_resolved must be absent — not yet resolved (needs graph).
	if _, ok := genericRef.Metadata["type_args_resolved"]; ok {
		t.Errorf("expected type_args_resolved absent before graph pass; metadata: %+v", genericRef.Metadata)
	}
	// type_args_pending_cross_module must record the import binding.
	pending, ok := genericRef.Metadata["type_args_pending_cross_module"].(map[string]string)
	if !ok || len(pending) == 0 {
		t.Fatalf("expected type_args_pending_cross_module set; metadata: %+v", genericRef.Metadata)
	}
	if pending["T"] != "other.types.T" {
		t.Errorf("pending[T] = %q, want other.types.T", pending["T"])
	}
}

// TestPythonGrammar_TypeVarCrossModule_AliasedImport verifies that the alias
// form `from .types import T as MyT` is tracked via the alias in
// type_args_pending_cross_module, with the canonical path as the value.
func TestPythonGrammar_TypeVarCrossModule_AliasedImport(t *testing.T) {
	src := []byte(`from other.types import T as MyT

class Foo(Generic[MyT]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	if _, ok := genericRef.Metadata["type_args_resolved"]; ok {
		t.Errorf("expected type_args_resolved absent; metadata: %+v", genericRef.Metadata)
	}
	pending, ok := genericRef.Metadata["type_args_pending_cross_module"].(map[string]string)
	if !ok || len(pending) == 0 {
		t.Fatalf("expected type_args_pending_cross_module set; metadata: %+v", genericRef.Metadata)
	}
	// Key is the local alias, value is the canonical import path.
	if pending["MyT"] != "other.types.T" {
		t.Errorf("pending[MyT] = %q, want other.types.T", pending["MyT"])
	}
	if _, hasBadKey := pending["T"]; hasBadKey {
		t.Errorf("unexpected key T in pending (should use alias MyT): %+v", pending)
	}
}

// TestPythonGrammar_TypeVarCrossModule_LocalVarNotPending verifies that a
// same-module TypeVar resolves via type_args_resolved and does NOT trigger
// the cross-module pending path.
func TestPythonGrammar_TypeVarCrossModule_LocalVarNotPending(t *testing.T) {
	src := []byte(`T = TypeVar("T")

class Foo(Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 1 || resolved[0] != "sym:mymod.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mymod.T]", resolved)
	}
	if _, ok := genericRef.Metadata["type_args_pending_cross_module"]; ok {
		t.Errorf("expected type_args_pending_cross_module absent for same-module TypeVar; metadata: %+v", genericRef.Metadata)
	}
}

// TestPythonGrammar_TypeVarCrossModule_ExternalImportRecordedAsPending verifies
// that an import from an external package (e.g. typing_extensions) IS recorded
// as a pending candidate at grammar level. The grammar cannot distinguish
// external packages from project-internal modules — that check happens in the
// post-graph-pass resolver, which will find no sym:typing_extensions.T TypeVar
// node and leave type_args_resolved absent.
func TestPythonGrammar_TypeVarCrossModule_ExternalImportRecordedAsPending(t *testing.T) {
	src := []byte(`from typing_extensions import T

class Foo(Generic[T]):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic; refs: %+v", refs)
	}
	// At grammar level, the pending candidate is recorded (grammar can't check
	// graph). Post-graph-pass won't find sym:typing_extensions.T as a TypeVar
	// node, so type_args_resolved stays absent after the graph pass.
	if _, ok := genericRef.Metadata["type_args_resolved"]; ok {
		t.Errorf("expected type_args_resolved absent; metadata: %+v", genericRef.Metadata)
	}
	pending, _ := genericRef.Metadata["type_args_pending_cross_module"].(map[string]string)
	if pending["T"] != "typing_extensions.T" {
		t.Errorf("pending[T] = %q, want typing_extensions.T; metadata: %+v", pending["T"], genericRef.Metadata)
	}
}

// --- lib-0pa.3: NamedTuple / TypedDict / namedtuple factory distinction ---

// TestPythonGrammar_TypingNamedTuple_AttributeForm covers `class Foo(typing.NamedTuple):`.
// The attribute-chain base resolves to "ext:typing.NamedTuple" — a genuine class
// parent emitting an inherits edge to the external-package node.
func TestPythonGrammar_TypingNamedTuple_AttributeForm(t *testing.T) {
	src := []byte(`class Foo(typing.NamedTuple):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing.NamedTuple" {
		t.Errorf("Target = %q, want ext:typing.NamedTuple", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("ext:typing.NamedTuple should not be unresolved: %+v", refs[0].Metadata)
	}
	if v, _ := refs[0].Metadata["unresolved_expression"].(bool); v {
		t.Errorf("ext:typing.NamedTuple should not be unresolved_expression: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingNamedTuple_BareImport covers the bare-identifier form
// `class Foo(NamedTuple):` when `from typing import NamedTuple` is present.
func TestPythonGrammar_TypingNamedTuple_BareImport(t *testing.T) {
	src := []byte(`from typing import NamedTuple

class Foo(NamedTuple):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing.NamedTuple" {
		t.Errorf("Target = %q, want ext:typing.NamedTuple", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingTypedDict_BareImport covers `class Foo(TypedDict):` with
// `from typing import TypedDict`.
func TestPythonGrammar_TypingTypedDict_BareImport(t *testing.T) {
	src := []byte(`from typing import TypedDict

class Foo(TypedDict):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing.TypedDict" {
		t.Errorf("Target = %q, want ext:typing.TypedDict", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingTypedDict_AttributeForm covers `class Foo(typing.TypedDict):`.
// Attribute-chain base — same code path as TypingNamedTuple_AttributeForm but for TypedDict.
func TestPythonGrammar_TypingTypedDict_AttributeForm(t *testing.T) {
	src := []byte(`class Foo(typing.TypedDict):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing.TypedDict" {
		t.Errorf("Target = %q, want ext:typing.TypedDict", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingExtensions_AttributeForm covers `class Foo(typing_extensions.TypedDict):`.
// Attribute-chain form with typing_extensions — exercises the ext:typing_extensions.TypedDict path.
func TestPythonGrammar_TypingExtensions_AttributeForm(t *testing.T) {
	src := []byte(`class Foo(typing_extensions.TypedDict):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing_extensions.TypedDict" {
		t.Errorf("Target = %q, want ext:typing_extensions.TypedDict", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_BareTypingBase_NoImport verifies that bare `NamedTuple` or
// `TypedDict` without any import binding stays unresolved — the ext: rewrite only
// fires after import resolution maps the bare name to its qualified form.
func TestPythonGrammar_BareTypingBase_NoImport(t *testing.T) {
	for _, tc := range []struct {
		src  string
		name string
	}{
		{
			src:  "class Foo(NamedTuple):\n    pass\n",
			name: "NamedTuple",
		},
		{
			src:  "class Foo(TypedDict):\n    pass\n",
			name: "TypedDict",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := code.New(code.NewPythonGrammar())
			doc, err := h.Parse("m.py", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			refs := inheritsRefsBySource(doc, "m.Foo")
			if len(refs) != 1 {
				t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
			}
			// Without an import, the bare name cannot be resolved to typing.X
			// so no ext: rewrite should happen.
			if strings.HasPrefix(refs[0].Target, "ext:") {
				t.Errorf("bare %s without import should not produce ext: target; got %q", tc.name, refs[0].Target)
			}
			// Must remain as bare name and be marked unresolved.
			if refs[0].Target != tc.name {
				t.Errorf("Target = %q, want %q (unresolved bare name)", refs[0].Target, tc.name)
			}
			if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
				t.Errorf("bare %s without import should be unresolved; metadata=%+v", tc.name, refs[0].Metadata)
			}
		})
	}
}

// TestPythonGrammar_TypingNamedTuple_AliasedImport covers the aliased form:
// `from typing import NamedTuple as NT` followed by `class Foo(NT):`.
func TestPythonGrammar_TypingNamedTuple_AliasedImport(t *testing.T) {
	src := []byte(`from typing import NamedTuple as NT

class Foo(NT):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing.NamedTuple" {
		t.Errorf("Target = %q, want ext:typing.NamedTuple (alias NT resolved)", refs[0].Target)
	}
}

// TestPythonGrammar_FactoryCallBase_NamedtupleWithImport covers
// `class Foo(namedtuple("Foo", ["x"])):` with `from collections import namedtuple`.
// No inherits edge is emitted; Metadata.base_factory="namedtuple" is set on Foo.
func TestPythonGrammar_FactoryCallBase_NamedtupleWithImport(t *testing.T) {
	src := []byte(`from collections import namedtuple

class Foo(namedtuple("Foo", ["x"])):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// No inherits edge for Foo.
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 0 {
		t.Errorf("expected no inherits refs for factory-call base; got %+v", refs)
	}
	// base_factory set on the Unit.
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatal("Foo Unit missing")
	}
	if bf, _ := u.Metadata["base_factory"].(string); bf != "namedtuple" {
		t.Errorf("base_factory = %q, want namedtuple; metadata=%+v", bf, u.Metadata)
	}
}

// TestPythonGrammar_FactoryCallBase_CollectionsAttribute covers
// `class Foo(collections.namedtuple("Foo", ["x"])):` (attribute-form callee,
// no local import needed).
func TestPythonGrammar_FactoryCallBase_CollectionsAttribute(t *testing.T) {
	src := []byte(`import collections

class Foo(collections.namedtuple("Foo", ["x"])):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 0 {
		t.Errorf("expected no inherits refs; got %+v", refs)
	}
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatal("Foo Unit missing")
	}
	if bf, _ := u.Metadata["base_factory"].(string); bf != "namedtuple" {
		t.Errorf("base_factory = %q, want namedtuple; metadata=%+v", bf, u.Metadata)
	}
}

// TestPythonGrammar_TypingNamedTuple_FunctionalCallBase guards the Part A /
// Part B interaction: `class Foo(typing.NamedTuple("Foo", [...])):` has Target
// "typing.NamedTuple" with unresolved_expression=true. Part A must NOT claim
// this ref (it would produce a spurious inherits edge to ext:typing.NamedTuple);
// Part B must claim it and emit base_factory="namedtuple" instead.
func TestPythonGrammar_TypingNamedTuple_FunctionalCallBase(t *testing.T) {
	src := []byte(`import typing

class Foo(typing.NamedTuple("Foo", [("x", int)])):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Must NOT emit an inherits edge — that would be spurious for a call form.
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 0 {
		t.Errorf("functional typing.NamedTuple call base must not produce inherits refs; got %+v", refs)
	}
	// Must set base_factory on the class Unit.
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatal("Foo Unit missing")
	}
	if bf, _ := u.Metadata["base_factory"].(string); bf != "namedtuple" {
		t.Errorf("base_factory = %q, want namedtuple; metadata=%+v", bf, u.Metadata)
	}
}

// TestPythonGrammar_FactoryCallBase_NotInAllowList verifies that a call base
// NOT in the allow-list still falls through to lib-0pa.1's unresolved_expression
// path unchanged.
func TestPythonGrammar_FactoryCallBase_NotInAllowList(t *testing.T) {
	src := []byte(`class Foo(custom_factory()):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref (fallback), got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "custom_factory" {
		t.Errorf("Target = %q, want custom_factory", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved_expression"].(bool); !v {
		t.Errorf("expected unresolved_expression=true; got %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_FactoryAssign_Namedtuple covers the assignment form
// `Foo = namedtuple("Foo", ["x"])` at module scope. Emits Kind="class" Unit
// with Metadata["factory"]="namedtuple".
func TestPythonGrammar_FactoryAssign_Namedtuple(t *testing.T) {
	src := []byte(`from collections import namedtuple

Foo = namedtuple("Foo", ["x", "y"])
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatalf("expected Foo Unit from factory assignment; units: %+v", doc.Units)
	}
	if u.Kind != "class" {
		t.Errorf("Kind = %q, want class", u.Kind)
	}
	if u.Path != "m.Foo" {
		t.Errorf("Path = %q, want m.Foo", u.Path)
	}
	if f, _ := u.Metadata["factory"].(string); f != "namedtuple" {
		t.Errorf("factory = %q, want namedtuple; metadata=%+v", f, u.Metadata)
	}
}

// TestPythonGrammar_FactoryAssign_TypedDict covers `Foo = TypedDict("Foo", {...})`
// at module scope. Emits Kind="class" Unit with Metadata["factory"]="typeddict".
func TestPythonGrammar_FactoryAssign_TypedDict(t *testing.T) {
	src := []byte(`from typing import TypedDict

Foo = TypedDict("Foo", {"x": int, "y": str})
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatalf("expected Foo Unit from TypedDict factory assignment; units: %+v", doc.Units)
	}
	if u.Kind != "class" {
		t.Errorf("Kind = %q, want class", u.Kind)
	}
	if u.Path != "m.Foo" {
		t.Errorf("Path = %q, want m.Foo", u.Path)
	}
	if f, _ := u.Metadata["factory"].(string); f != "typeddict" {
		t.Errorf("factory = %q, want typeddict; metadata=%+v", f, u.Metadata)
	}
}

// TestPythonGrammar_TypingExtensions_AliasedImport_TypedDict covers the aliased
// typing_extensions form: `from typing_extensions import TypedDict as TD` followed
// by `class Foo(TD):`. Should resolve to ext:typing_extensions.TypedDict.
func TestPythonGrammar_TypingExtensions_AliasedImport_TypedDict(t *testing.T) {
	src := []byte(`from typing_extensions import TypedDict as TD

class Foo(TD):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing_extensions.TypedDict" {
		t.Errorf("Target = %q, want ext:typing_extensions.TypedDict (alias TD resolved)", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingTypedDict_FunctionalCallBase guards the Part A/B
// interaction for TypedDict: `class Foo(typing.TypedDict("Foo", {...})):` has
// Target "typing.TypedDict" with unresolved_expression=true. Part A must skip
// it; Part B must claim it as base_factory="typeddict".
func TestPythonGrammar_TypingTypedDict_FunctionalCallBase(t *testing.T) {
	src := []byte(`from typing import TypedDict

class Foo(TypedDict("Foo", {"x": int})):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Must NOT emit an inherits edge.
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 0 {
		t.Errorf("functional TypedDict call base must not produce inherits refs; got %+v", refs)
	}
	// Must set base_factory on the class Unit.
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatal("Foo Unit missing")
	}
	if bf, _ := u.Metadata["base_factory"].(string); bf != "typeddict" {
		t.Errorf("base_factory = %q, want typeddict; metadata=%+v", bf, u.Metadata)
	}
}

// TestPythonGrammar_TypingExtensions_TypedDict covers `from typing_extensions import TypedDict`
// followed by `class Foo(TypedDict):`. Should resolve to ext:typing_extensions.TypedDict.
func TestPythonGrammar_TypingExtensions_TypedDict(t *testing.T) {
	src := []byte(`from typing_extensions import TypedDict

class Foo(TypedDict):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing_extensions.TypedDict" {
		t.Errorf("Target = %q, want ext:typing_extensions.TypedDict", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_TypingExtensions_NamedTuple covers `from typing_extensions import NamedTuple`
// followed by `class Foo(NamedTuple):`. Should resolve to ext:typing_extensions.NamedTuple.
func TestPythonGrammar_TypingExtensions_NamedTuple(t *testing.T) {
	src := []byte(`from typing_extensions import NamedTuple

class Foo(NamedTuple):
    x: int
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ext:typing_extensions.NamedTuple" {
		t.Errorf("Target = %q, want ext:typing_extensions.NamedTuple", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("should not be unresolved: %+v", refs[0].Metadata)
	}
}

// TestPythonGrammar_EnumFunctionalForm_ClassBase covers the functional Enum base:
// `class Color(enum.Enum("Color", "RED GREEN")):` — factory call in allow-list.
func TestPythonGrammar_EnumFunctionalForm_ClassBase(t *testing.T) {
	src := []byte(`import enum

class Color(enum.Enum("Color", "RED GREEN")):
    pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Color")
	if len(refs) != 0 {
		t.Errorf("expected no inherits refs for enum.Enum factory base; got %+v", refs)
	}
	u := findUnit(doc, "Color")
	if u == nil {
		t.Fatal("Color Unit missing")
	}
	if bf, _ := u.Metadata["base_factory"].(string); bf != "Enum" {
		t.Errorf("base_factory = %q, want Enum; metadata=%+v", bf, u.Metadata)
	}
}

// TestPythonGrammar_EnumFunctionalForm_Assignment covers `Color = Enum("Color", ...)`.
func TestPythonGrammar_EnumFunctionalForm_Assignment(t *testing.T) {
	src := []byte(`from enum import Enum

Color = Enum("Color", "RED GREEN BLUE")
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "Color")
	if u == nil {
		t.Fatalf("expected Color Unit from Enum factory assignment; units: %+v", doc.Units)
	}
	if u.Kind != "class" {
		t.Errorf("Kind = %q, want class", u.Kind)
	}
	if f, _ := u.Metadata["factory"].(string); f != "Enum" {
		t.Errorf("factory = %q, want Enum; metadata=%+v", f, u.Metadata)
	}
}

// TestPythonGrammar_FactoryAssign_AttributeCallee covers `Foo = collections.namedtuple(...)`
// (attribute-form callee, no local import needed).
func TestPythonGrammar_FactoryAssign_AttributeCallee(t *testing.T) {
	src := []byte(`import collections

Foo = collections.namedtuple("Foo", ["x", "y"])
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "Foo")
	if u == nil {
		t.Fatalf("expected Foo Unit from collections.namedtuple assignment; units: %+v", doc.Units)
	}
	if u.Kind != "class" {
		t.Errorf("Kind = %q, want class", u.Kind)
	}
	if f, _ := u.Metadata["factory"].(string); f != "namedtuple" {
		t.Errorf("factory = %q, want namedtuple; metadata=%+v", f, u.Metadata)
	}
}

// TestPythonGrammar_FactoryAssign_NoImport verifies that a bare factory call
// assignment without an import does NOT emit a Unit (allow-list driven).
func TestPythonGrammar_FactoryAssign_NoImport(t *testing.T) {
	src := []byte(`Foo = namedtuple("Foo", ["x"])
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("m.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// No import → "namedtuple" is not in allow-list → no Unit for Foo.
	u := findUnit(doc, "Foo")
	if u != nil {
		t.Errorf("should not emit Unit for unimported bare namedtuple; got %+v", u)
	}
}

// TestPythonGrammar_CallEdges_ExternalUnresolved verifies that a call to a
// name not in the same file gets confidence="unresolved".
func TestPythonGrammar_CallEdges_ExternalUnresolved(t *testing.T) {
	src := []byte(`
def run():
    external_func()
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("runner.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := callRefsBySource(doc, "runner.run")
	var toExt *indexer.Reference
	for i := range refs {
		if refs[i].Target == "external_func" {
			toExt = &refs[i]
			break
		}
	}
	if toExt == nil {
		t.Fatalf("expected unresolved call ref for external_func; refs: %+v", refs)
	}
	if toExt.Metadata == nil || toExt.Metadata["confidence"] != "unresolved" {
		t.Errorf("expected confidence=unresolved; got %v", toExt.Metadata)
	}
}

// --- lib-0pa.4: PEP 695 scoped TypeVars ---

func TestPEP695_ClassSimple(t *testing.T) {
	// class Foo[T]: → sym:mymod.Foo.T with scope=class, variance=invariant
	src := []byte("class Foo[T]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.Foo.T" {
		t.Errorf("Path = %q, want mymod.Foo.T", u.Path)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "class" {
		t.Errorf("scope = %q, want class", sc)
	}
	if v, _ := u.Metadata["variance"].(string); v != "invariant" {
		t.Errorf("variance = %q, want invariant", v)
	}
	if kh, _ := u.Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("kind_hint = %q, want typevar", kh)
	}
}

func TestPEP695_ClassWithBound(t *testing.T) {
	// class Foo[T: Hashable]: → bound="Hashable"
	src := []byte("class Foo[T: Hashable]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.Foo.T" {
		t.Errorf("Path = %q, want mymod.Foo.T", u.Path)
	}
	if bound, _ := u.Metadata["bound"].(string); bound != "Hashable" {
		t.Errorf("bound = %q, want Hashable", bound)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "class" {
		t.Errorf("scope = %q, want class", sc)
	}
	if v, _ := u.Metadata["variance"].(string); v != "invariant" {
		t.Errorf("variance = %q, want invariant", v)
	}
}

func TestPEP695_ClassWithConstraints(t *testing.T) {
	// class Foo[T: (int, str)]: → constraints=["int","str"]
	src := []byte("class Foo[T: (int, str)]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.Foo.T" {
		t.Errorf("Path = %q, want mymod.Foo.T", u.Path)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "class" {
		t.Errorf("scope = %q, want class", sc)
	}
	if kh, _ := u.Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("kind_hint = %q, want typevar", kh)
	}
	constraints, _ := u.Metadata["constraints"].([]string)
	if len(constraints) != 2 || constraints[0] != "int" || constraints[1] != "str" {
		t.Errorf("constraints = %v, want [int str]", constraints)
	}
}

func TestPEP695_FunctionSimple(t *testing.T) {
	// def identity[T](x: T) -> T: ... → sym:mymod.identity.T with scope=function
	src := []byte("def identity[T](x: T) -> T:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.identity.T" {
		t.Errorf("Path = %q, want mymod.identity.T", u.Path)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "function" {
		t.Errorf("scope = %q, want function", sc)
	}
	if v, _ := u.Metadata["variance"].(string); v != "invariant" {
		t.Errorf("variance = %q, want invariant", v)
	}
}

func TestPEP695_TypeAlias(t *testing.T) {
	// type Pair[K, V] = tuple[K, V] → two scoped TypeVar Units
	src := []byte("type Pair[K, V] = tuple[K, V]\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tvs := typeVarUnits(doc)
	if len(tvs) != 2 {
		t.Fatalf("expected 2 typevar units; got %d (%+v)", len(tvs), tvs)
	}
	kUnit := findTypeVarUnit(doc, "K")
	vUnit := findTypeVarUnit(doc, "V")
	if kUnit == nil {
		t.Fatalf("expected typevar unit K; units: %+v", tvs)
	}
	if vUnit == nil {
		t.Fatalf("expected typevar unit V; units: %+v", tvs)
	}
	if kUnit.Path != "mymod.Pair.K" {
		t.Errorf("K Path = %q, want mymod.Pair.K", kUnit.Path)
	}
	if vUnit.Path != "mymod.Pair.V" {
		t.Errorf("V Path = %q, want mymod.Pair.V", vUnit.Path)
	}
	if sc, _ := kUnit.Metadata["scope"].(string); sc != "type_alias" {
		t.Errorf("K scope = %q, want type_alias", sc)
	}
	if sc, _ := vUnit.Metadata["scope"].(string); sc != "type_alias" {
		t.Errorf("V scope = %q, want type_alias", sc)
	}
	if kh, _ := kUnit.Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("K kind_hint = %q, want typevar", kh)
	}
	if kh, _ := vUnit.Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("V kind_hint = %q, want typevar", kh)
	}
}

func TestPEP695_AsyncFunctionSimple(t *testing.T) {
	// async def identity[T](x: T) -> T: — tree-sitter uses function_definition
	// for both sync and async; the TypeVar unit must still be emitted.
	src := []byte("async def identity[T](x: T) -> T:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T for async def; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.identity.T" {
		t.Errorf("Path = %q, want mymod.identity.T", u.Path)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "function" {
		t.Errorf("scope = %q, want function", sc)
	}
}

func TestPEP695_DecoratedClassSimple(t *testing.T) {
	// @dataclass\nclass Foo[T]: — extractPEP695TypeVars must unwrap decorated_definition.
	src := []byte("@dataclass\nclass Foo[T]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findTypeVarUnit(doc, "T")
	if u == nil {
		t.Fatalf("expected typevar unit T for decorated class; units: %+v", typeVarUnits(doc))
	}
	if u.Path != "mymod.Foo.T" {
		t.Errorf("Path = %q, want mymod.Foo.T", u.Path)
	}
	if sc, _ := u.Metadata["scope"].(string); sc != "class" {
		t.Errorf("scope = %q, want class", sc)
	}
}

func TestPEP695_ParamSpecSkipped(t *testing.T) {
	// class Foo[T, **P]: — **P is a ParamSpec, explicitly out of scope.
	// parsePEP695Param returns ("", nil) for splat_type nodes, so only T is emitted.
	src := []byte("class Foo[T, **P]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tvs := typeVarUnits(doc)
	if len(tvs) != 1 {
		t.Fatalf("expected exactly 1 typevar unit (T only, **P skipped); got %d (%+v)", len(tvs), tvs)
	}
	if tvs[0].Path != "mymod.Foo.T" {
		t.Errorf("Path = %q, want mymod.Foo.T", tvs[0].Path)
	}
	if sc, _ := tvs[0].Metadata["scope"].(string); sc != "class" {
		t.Errorf("scope = %q, want class", sc)
	}
	if kh, _ := tvs[0].Metadata["kind_hint"].(string); kh != "typevar" {
		t.Errorf("kind_hint = %q, want typevar", kh)
	}
}

func TestPEP695_TypeVarTupleSkipped(t *testing.T) {
	// class Foo[*Ts]: — *Ts is a TypeVarTuple, explicitly out of scope.
	// parsePEP695Param returns ("", nil) for splat_type nodes, so 0 Units are emitted.
	src := []byte("class Foo[*Ts]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tvs := typeVarUnits(doc)
	if len(tvs) != 0 {
		t.Errorf("expected no typevar units for TypeVarTuple-only class; got %+v", tvs)
	}
}

func TestPEP695_LEGB_NestedClassUsesOuterClassScopeTypeParam(t *testing.T) {
	// Verifies acceptance criterion 5: "class method referring to class's T →
	// resolves to class-level TypeVar, not function-level".
	//
	// The criterion uses "class method" loosely. The exercised scenario is a
	// nested class whose Generic[T] base refers to the outer class's TypeVar.
	// A *method-body* Generic[T] ref would exercise the same lookupTypeVar
	// code path, but method bodies are not descended by extractPEP695TypeVars
	// so no inherits refs are produced from them — making a method-body test
	// structurally impossible with the current design. The nested-class variant
	// is the only reachable form of this scope-chain resolution.
	// Tracked for when function-body descent is added: lib-j3g.
	//
	// class Outer[T]:
	//     class Inner(Generic[T]):
	//         pass
	//
	// Inner's inherits ref should resolve T to sym:mymod.Outer.T (outer class
	// scope), not sym:mymod.Outer.Inner.T (which does not exist).
	src := []byte(`class Outer[T]:
    class Inner(Generic[T]):
        pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Outer's TypeVar T must exist.
	outerT := findTypeVarUnit(doc, "T")
	if outerT == nil {
		t.Fatalf("expected typevar unit T from Outer; units: %+v", typeVarUnits(doc))
	}
	if outerT.Path != "mymod.Outer.T" {
		t.Errorf("Outer.T Path = %q, want mymod.Outer.T", outerT.Path)
	}
	// Inner has no PEP 695 TypeVar of its own.
	for _, u := range typeVarUnits(doc) {
		if u.Path == "mymod.Outer.Inner.T" {
			t.Errorf("unexpected inner-class-level TypeVar unit at %q; T should come from Outer", u.Path)
		}
	}
	// Inner's inherits ref for Generic[T] must resolve T to Outer's T.
	innerRefs := inheritsRefsBySource(doc, "mymod.Outer.Inner")
	var genericRef *indexer.Reference
	for i := range innerRefs {
		if strings.Contains(innerRefs[i].Target, "Generic") {
			genericRef = &innerRefs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected inherits ref for Generic from Inner; refs: %+v", innerRefs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 1 || resolved[0] != "sym:mymod.Outer.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mymod.Outer.T]", resolved)
	}
}

func TestPEP695_ClassMultipleTypeVars(t *testing.T) {
	// class Foo[T, U]: → two scoped TypeVar Units
	src := []byte("class Foo[T, U]:\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tvs := typeVarUnits(doc)
	if len(tvs) != 2 {
		t.Fatalf("expected 2 typevar units; got %d (%+v)", len(tvs), tvs)
	}
	tUnit := findTypeVarUnit(doc, "T")
	uUnit := findTypeVarUnit(doc, "U")
	if tUnit == nil || uUnit == nil {
		t.Fatalf("expected units for T and U; got %+v", tvs)
	}
	if tUnit.Path != "mymod.Foo.T" {
		t.Errorf("T Path = %q, want mymod.Foo.T", tUnit.Path)
	}
	if uUnit.Path != "mymod.Foo.U" {
		t.Errorf("U Path = %q, want mymod.Foo.U", uUnit.Path)
	}
}

func TestPEP695_TwoLevelClassNestingResolvesCorrectly(t *testing.T) {
	// Two levels of nesting: Outer[T] → Middle[U] → Inner(Generic[T, U])
	// T resolves to Outer's scope (2 hops up), U to Middle's scope (1 hop up).
	src := []byte(`class Outer[T]:
    class Middle[U]:
        class Inner(Generic[T, U]):
            pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if findTypeVarUnit(doc, "T") == nil {
		t.Fatalf("expected typevar unit T (from Outer); units: %+v", typeVarUnits(doc))
	}
	if findTypeVarUnit(doc, "U") == nil {
		t.Fatalf("expected typevar unit U (from Middle); units: %+v", typeVarUnits(doc))
	}
	innerRefs := inheritsRefsBySource(doc, "mymod.Outer.Middle.Inner")
	var genericRef *indexer.Reference
	for i := range innerRefs {
		if strings.Contains(innerRefs[i].Target, "Generic") {
			genericRef = &innerRefs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected Generic inherits ref from Inner; refs: %+v", innerRefs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved args; got %v", resolved)
	}
	// Order matches type_args order: T first, U second.
	if resolved[0] != "sym:mymod.Outer.T" {
		t.Errorf("resolved[0] = %q, want sym:mymod.Outer.T", resolved[0])
	}
	if resolved[1] != "sym:mymod.Outer.Middle.U" {
		t.Errorf("resolved[1] = %q, want sym:mymod.Outer.Middle.U", resolved[1])
	}
}

func TestPEP695_FunctionBodyNestedClassNotDescended(t *testing.T) {
	// By design, extractPEP695TypeVars does not descend into function bodies.
	// A class defined inside a function is intentionally not processed — its
	// TypeVars are omitted because the lookup path would require tracking
	// function-scope inheritance chains that PostProcess does not support.
	src := []byte(`def factory[T]():
    class Inner(Generic[T]):
        pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// factory's T must be emitted (function-level).
	factoryT := findTypeVarUnit(doc, "T")
	if factoryT == nil {
		t.Fatalf("expected typevar unit T for factory; units: %+v", typeVarUnits(doc))
	}
	if factoryT.Path != "mymod.factory.T" {
		t.Errorf("factory T Path = %q, want mymod.factory.T", factoryT.Path)
	}
	// Inner (nested inside the function) must NOT produce a typevar unit.
	for _, u := range typeVarUnits(doc) {
		if strings.HasPrefix(u.Path, "mymod.factory.Inner.") {
			t.Errorf("unexpected TypeVar unit for function-body-nested class: %+v", u)
		}
	}
}

func TestPEP695_MethodBodyNestedClassNotDescended(t *testing.T) {
	// Documents the current limitation: extractPEP695TypeVars does NOT descend
	// method bodies. A class defined inside a method that references the outer
	// class's PEP 695 TypeVar via Generic[T] does not produce an inherits ref
	// because the method body is never walked.
	//
	// When function-body descent is added, this test should be updated to verify
	// that Generic[T] inside the method body IS resolved to sym:mymod.Outer.T
	// (LEGB chain: method scope → class scope). See lib-udam.7 for the
	// follow-up tracking.
	//
	// class Outer[T]:
	//     def method(self):
	//         class Inner(Generic[T]):
	//             pass
	src := []byte(`class Outer[T]:
    def method(self):
        class Inner(Generic[T]):
            pass
`)
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Outer's TypeVar T must be emitted (class-level TypeVars are always walked).
	outerT := findTypeVarUnit(doc, "T")
	if outerT == nil {
		t.Fatalf("expected typevar unit T from Outer; units: %+v", typeVarUnits(doc))
	}
	if outerT.Path != "mymod.Outer.T" {
		t.Errorf("Outer.T Path = %q, want mymod.Outer.T", outerT.Path)
	}
	// Inner is inside a method body — no inherits refs should be produced for it.
	innerRefs := inheritsRefsBySource(doc, "mymod.Outer.method.Inner")
	if len(innerRefs) != 0 {
		t.Errorf("expected no inherits refs for method-body-nested Inner; got %+v", innerRefs)
	}
}

func TestPEP695_ClassUsesOwnTypeVarInGenericBase(t *testing.T) {
	// class Foo[T](Generic[T]): — Foo uses its own PEP 695 TypeVar T in its
	// own Generic base list. This exercises the i==len(parts) case of
	// lookupTypeVar's scope-chain walk, where source "mymod.Foo" strips to
	// localPath "Foo" and the first scope tried is "Foo.T" (own scope hit).
	src := []byte("class Foo[T](Generic[T]):\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	fooT := findTypeVarUnit(doc, "T")
	if fooT == nil {
		t.Fatalf("expected typevar unit T; units: %+v", typeVarUnits(doc))
	}
	if fooT.Path != "mymod.Foo.T" {
		t.Errorf("T Path = %q, want mymod.Foo.T", fooT.Path)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected Generic inherits ref from Foo; refs: %+v", refs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 1 || resolved[0] != "sym:mymod.Foo.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mymod.Foo.T]", resolved)
	}
}

func TestPEP695_MultiTypeVarResolution(t *testing.T) {
	// class Foo[T, U](Generic[T, U]): — both TypeVars resolve simultaneously.
	// Verifies that lookupTypeVar handles multiple args in a single inherits ref.
	src := []byte("class Foo[T, U](Generic[T, U]):\n    pass\n")
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("mymod.py", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "mymod.Foo")
	var genericRef *indexer.Reference
	for i := range refs {
		if strings.Contains(refs[i].Target, "Generic") {
			genericRef = &refs[i]
			break
		}
	}
	if genericRef == nil {
		t.Fatalf("expected Generic inherits ref from Foo; refs: %+v", refs)
	}
	resolved, _ := genericRef.Metadata["type_args_resolved"].([]string)
	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved args; got %v", resolved)
	}
	if resolved[0] != "sym:mymod.Foo.T" {
		t.Errorf("resolved[0] = %q, want sym:mymod.Foo.T", resolved[0])
	}
	if resolved[1] != "sym:mymod.Foo.U" {
		t.Errorf("resolved[1] = %q, want sym:mymod.Foo.U", resolved[1])
	}
}

