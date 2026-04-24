package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const tsSample = `/** JSDoc for Service. */
import { foo, bar as b } from "./utils";
import type { Config } from "./types";
import defaultX, { named } from "lib";
import * as ns from "pkg";
import "side-effects-only";

export const PI = 3.14;
export function hello(name: string): string { return name; }

/** useAuth is a hook. */
export const useAuth = (x: number): boolean => true;

export default class Service {
    readonly x: number = 1;
    constructor() {}
    /** validate doc */
    async validate(u: string): Promise<boolean> { return true; }
    static helper() {}
}

export interface User { id: number; name: string }
export type Handler = (x: string) => void;
export enum Status { ACTIVE, INACTIVE }

const arrow = (x: number) => x + 1;
// TODO: refactor arrow
`

const jsSample = `import { foo } from "./u";
export function greet(n) { return "hi " + n; }
export default class Service {
    constructor() { this.x = 1; }
    doThing() { return 42; }
}
export const useCounter = () => ({ count: 0 });
const notAFn = 42;
`

// TypeScript grammar extracts the full set of TS-specific kinds along with
// the shared function/class/method set. Arrow consts are promoted to
// function Units so modern React/TS patterns are indexable.
func TestTypeScriptGrammar_ExtractsAllKinds(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("src/auth/service.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "typescript" {
		t.Errorf("Format = %q, want %q", doc.Format, "typescript")
	}
	if doc.Title != "service" {
		t.Errorf("Title = %q, want %q (stem fallback without .ts)", doc.Title, "service")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []struct{ title, kind string }{
		{"hello", "function"},
		{"useAuth", "function"}, // arrow-const lifted to function
		{"Service", "class"},
		{"validate", "method"},
		{"helper", "method"},
		{"User", "interface"},
		{"Handler", "type"},
		{"Status", "enum"},
		{"arrow", "function"}, // arrow-const at bottom
	} {
		if !got[tk{w.title, w.kind}] {
			t.Errorf("missing Unit {title=%q, kind=%q}; got %v", w.title, w.kind, got)
		}
	}
	// PI is a plain const — should NOT be indexed (value isn't an arrow).
	if got[tk{"PI", "function"}] {
		t.Error("PI should not be indexed; only arrow-const declarators become Units")
	}
}

// JavaScript grammar has no interface/type/enum (TS-only); the rest matches.
func TestJavaScriptGrammar_CoreShape(t *testing.T) {
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.Parse("util.js", []byte(jsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "javascript" {
		t.Errorf("Format = %q, want %q", doc.Format, "javascript")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []struct{ title, kind string }{
		{"greet", "function"},
		{"Service", "class"},
		{"constructor", "method"},
		{"doThing", "method"},
		{"useCounter", "function"}, // arrow-const
	} {
		if !got[tk{w.title, w.kind}] {
			t.Errorf("missing Unit {title=%q, kind=%q}; got %v", w.title, w.kind, got)
		}
	}
	if got[tk{"notAFn", "function"}] {
		t.Error("non-arrow const should not become a function Unit")
	}
}

// Import handling covers all five shapes: default, named+alias, namespace,
// type-only, side-effect. Static imports don't apply to JS/TS (that's Java).
func TestTypeScriptGrammar_Imports(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("t.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}

	// Every expected import must be present.
	for _, want := range []string{
		"./utils.foo",        // named
		"./utils.bar",        // named with alias
		"./types.Config",     // type-only named
		"lib",                // default import (module itself with Alias)
		"lib.named",          // named alongside default
		"pkg",                // namespace (module itself with Alias + Metadata)
		"side-effects-only",  // side-effect only
	} {
		if _, ok := byTarget[want]; !ok {
			t.Errorf("missing import %q (have %v)", want, byTarget)
		}
	}

	// Type-only flag surfaces in Metadata.
	if r, ok := byTarget["./types.Config"]; ok {
		if r.Metadata == nil || r.Metadata["type_only"] != true {
			t.Errorf("expected type_only=true on ./types.Config: %+v", r.Metadata)
		}
	}
	// Default import: Alias=defaultX, default=true.
	if r, ok := byTarget["lib"]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "defaultX" || r.Metadata["default"] != true {
			t.Errorf("expected lib default import with alias=defaultX, default=true: %+v", r.Metadata)
		}
	}
	// Namespace import: Alias=ns, namespace=true.
	if r, ok := byTarget["pkg"]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "ns" || r.Metadata["namespace"] != true {
			t.Errorf("expected pkg namespace import with alias=ns, namespace=true: %+v", r.Metadata)
		}
	}
	// Named-with-alias: foo no alias, bar alias=b.
	if r, ok := byTarget["./utils.bar"]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "b" {
			t.Errorf("expected ./utils.bar with alias=b: %+v", r.Metadata)
		}
	}
}

// Exported top-level symbols carry a Kind="label" Value="exported" signal;
// export-default symbols additionally carry Value="default-export". Methods
// inside an exported class don't get the label (they're not module-scope).
func TestTypeScriptGrammar_ExportSignals(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("t.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	hello := findUnit(doc, "hello")
	if hello == nil {
		t.Fatal("hello Unit missing")
	}
	if !hasSignal(hello.Signals, "label", "exported") {
		t.Errorf("hello missing 'exported' label: %+v", hello.Signals)
	}

	svc := findUnit(doc, "Service")
	if svc == nil {
		t.Fatal("Service Unit missing")
	}
	if !hasSignal(svc.Signals, "label", "exported") {
		t.Errorf("Service missing 'exported' label: %+v", svc.Signals)
	}
	if !hasSignal(svc.Signals, "label", "default-export") {
		t.Errorf("Service missing 'default-export' label: %+v", svc.Signals)
	}

	// Arrow-const exports: variable_declarator is nested two levels deep
	// inside export_statement (via lexical_declaration). A regression to
	// single-level parent traversal would silently drop this signal.
	useAuth := findUnit(doc, "useAuth")
	if useAuth == nil {
		t.Fatal("useAuth Unit missing")
	}
	if !hasSignal(useAuth.Signals, "label", "exported") {
		t.Errorf("useAuth (arrow-const export) missing 'exported' label: %+v", useAuth.Signals)
	}

	// `validate` is inside an exported class — but itself sits under
	// class_body, not export_statement. No label.
	val := findUnit(doc, "validate")
	if val == nil {
		t.Fatal("validate Unit missing")
	}
	if hasSignal(val.Signals, "label", "exported") {
		t.Errorf("validate should not carry 'exported' label (it's a method, not module-scope): %+v", val.Signals)
	}

	// `arrow` is a non-exported const; no label.
	arrow := findUnit(doc, "arrow")
	if arrow == nil {
		t.Fatal("arrow Unit missing")
	}
	if hasSignal(arrow.Signals, "label", "exported") {
		t.Errorf("arrow should not be labelled exported: %+v", arrow.Signals)
	}
}

// JSDoc flows into each symbol's docstring via the shared preceding-comment
// buffer. TODO markers become rationale signals.
func TestTypeScriptGrammar_JSDocAndRationale(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("t.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	useAuth := findUnit(doc, "useAuth")
	if useAuth == nil {
		t.Fatal("useAuth Unit missing")
	}
	if !strings.Contains(useAuth.Content, "useAuth is a hook.") {
		t.Errorf("JSDoc didn't attach to useAuth:\n%s", useAuth.Content)
	}

	seen := map[string]bool{}
	for _, s := range doc.Signals {
		seen[s.Value] = true
	}
	if !seen["todo"] {
		t.Errorf("missing document-level TODO signal (got %v)", seen)
	}
}

// Export label signals must survive into the chunk's SignalMeta JSON so the
// re-ranker can boost exported symbols. Mirrors the Java annotation-
// persistence test — both kinds flow through the same SignalsToJSON path.
func TestTypeScriptGrammar_ExportedLabelPersistsInChunk(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("t.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 1, OverlapLines: 0})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	found := false
	for _, c := range chunks {
		if strings.Contains(c.SignalMeta, `"inline_labels"`) &&
			strings.Contains(c.SignalMeta, `"exported"`) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected some chunk SignalMeta to contain exported label; got:")
		for _, c := range chunks {
			if c.SignalMeta != "{}" {
				t.Logf("  %q: %s", c.SectionHeading, c.SignalMeta)
			}
		}
	}
}

// Extension registration: every file extension advertised by the three
// grammars must resolve to a handler in the default registry.
func TestJSFamily_AllExtensionsRegistered(t *testing.T) {
	reg := indexer.DefaultRegistry()
	for _, ext := range []string{
		"x.js", "x.jsx", "x.mjs", "x.cjs",
		"x.ts", "x.mts", "x.cts",
		"x.tsx",
	} {
		if reg.HandlerFor(ext) == nil {
			t.Errorf("%q extension not registered", ext)
		}
	}
}

// Grammar invariants pass for all three flavours.
func TestJSFamily_SatisfiesGrammarInvariants(t *testing.T) {
	cases := []struct {
		name string
		g    code.Grammar
		path string
		src  []byte
	}{
		{"javascript", code.NewJavaScriptGrammar(), "u.js", []byte(jsSample)},
		{"typescript", code.NewTypeScriptGrammar(), "s.ts", []byte(tsSample)},
		{"tsx", code.NewTSXGrammar(), "c.tsx", []byte(tsSample)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := code.New(tc.g)
			code.AssertGrammarInvariants(t, h, tc.path, tc.src)
		})
	}
}

// TSX grammar accepts JSX syntax that plain-TypeScript would reject.
func TestTSXGrammar_ParsesJSX(t *testing.T) {
	src := []byte(`import React from "react";
export const Hello = ({ name }: { name: string }) => <div>Hello {name}</div>;
`)
	h := code.New(code.NewTSXGrammar())
	doc, err := h.Parse("h.tsx", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	hello := findUnit(doc, "Hello")
	if hello == nil {
		t.Fatal("Hello component Unit missing; JSX arrow-const should still be extracted")
	}
	if hello.Kind != "function" {
		t.Errorf("Hello Kind = %q, want function", hello.Kind)
	}
}

// Interface members: method_signature and property_signature are distinct
// from class method_definition / public_field_definition but equally worth
// indexing. An interface whose methods aren't extracted means
// `librarian search someInterfaceMethod` silently misses them.
func TestTypeScriptGrammar_InterfaceMembersIndexed(t *testing.T) {
	src := []byte(`export interface Service {
    /** Fetch by id. */
    fetch(id: string): Promise<User>;
    readonly maxRetries: number;
    save(): void;
}
`)
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("svc.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []struct{ title, kind string }{
		{"Service", "interface"},
		{"fetch", "method"},
		{"save", "method"},
		{"maxRetries", "field"},
	} {
		if !got[tk{w.title, w.kind}] {
			t.Errorf("missing interface member Unit {title=%q, kind=%q}; got %v", w.title, w.kind, got)
		}
	}
	// Path should read svc.Service.fetch — full dotted scope.
	fetch := findUnit(doc, "fetch")
	if fetch != nil && fetch.Path != "svc.Service.fetch" {
		t.Errorf("fetch Path = %q, want svc.Service.fetch", fetch.Path)
	}
}

// Abstract classes are a distinct AST node type (abstract_class_declaration).
// Skipping them leaves enterprise TS codebases with silent gaps.
func TestTypeScriptGrammar_AbstractClassIndexed(t *testing.T) {
	src := []byte(`export abstract class Base {
    /** Abstract stub. */
    abstract run(): void;
    concrete(): number { return 1; }
}
`)
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("base.ts", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	base := findUnit(doc, "Base")
	if base == nil {
		t.Fatal("Base Unit missing")
	}
	if base.Kind != "class" {
		t.Errorf("Base Kind = %q, want class", base.Kind)
	}
	// `abstract run()` produces an abstract_method_signature; `concrete()` a
	// method_definition. Both should be Units under the class.
	if concrete := findUnit(doc, "concrete"); concrete == nil {
		t.Errorf("concrete method Unit missing")
	}
	if run := findUnit(doc, "run"); run == nil {
		t.Errorf("abstract run method Unit missing")
	}
}

// TSX-specific invariants: parse a file that actually contains JSX so the
// tsx tree-sitter grammar exercises JSX node types (jsx_element,
// jsx_self_closing_element, jsx_fragment).
func TestTSXGrammar_InvariantsWithRealJSX(t *testing.T) {
	src := []byte(`import React from "react";

/** Greeting renders hello. */
export const Greeting = ({ name }: { name: string }) => (
    <div className="greet">
        <h1>Hello {name}</h1>
        <>fragment</>
        <img src="x.png" alt="" />
    </div>
);

export default function App() {
    return <Greeting name="World" />;
}
`)
	h := code.New(code.NewTSXGrammar())
	code.AssertGrammarInvariants(t, h, "App.tsx", src)

	// Spot-check: Greeting + App both extracted as functions.
	doc, _ := h.Parse("App.tsx", src)
	if findUnit(doc, "Greeting") == nil {
		t.Error("Greeting arrow-const missing")
	}
	if findUnit(doc, "App") == nil {
		t.Error("App default-export function missing")
	}
}

// Empty file must parse cleanly.
func TestJSFamily_EmptyFile(t *testing.T) {
	for _, g := range []code.Grammar{
		code.NewJavaScriptGrammar(),
		code.NewTypeScriptGrammar(),
		code.NewTSXGrammar(),
	} {
		t.Run(g.Name(), func(t *testing.T) {
			h := code.New(g)
			doc, err := h.Parse("empty."+g.Name(), []byte{})
			if err != nil {
				t.Fatalf("Parse(empty): %v", err)
			}
			if len(doc.Units) != 0 {
				t.Errorf("expected 0 Units, got %d", len(doc.Units))
			}
		})
	}
}
