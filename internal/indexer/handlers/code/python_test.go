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

