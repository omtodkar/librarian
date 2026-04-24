package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const pySample = `"""Module docstring for service."""

import os
import foo.bar as fb
from collections import deque, OrderedDict
from . import utils
from ..pkg import Thing as T

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

func TestPythonGrammar_Imports(t *testing.T) {
	h := code.New(code.NewPythonGrammar())
	doc, err := h.Parse("svc.py", []byte(pySample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Expected import Paths — from-imports are dotted, relative imports keep
	// their leading dots, aliased imports carry Alias.
	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}

	wantTargets := []string{
		"os",
		"foo.bar",                // aliased plain import
		"collections.deque",      // from X import Y
		"collections.OrderedDict",
		".utils",  // from . import utils
		"..pkg.Thing", // from ..pkg import Thing as T
	}
	for _, want := range wantTargets {
		if _, ok := byTarget[want]; !ok {
			t.Errorf("missing import %q; got %v", want, byTarget)
		}
	}

	// Aliases surface in Metadata.
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
	h := code.New(code.NewPythonGrammar())
	code.AssertGrammarInvariants(t, h, "auth/service.py", []byte(pySample))
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

