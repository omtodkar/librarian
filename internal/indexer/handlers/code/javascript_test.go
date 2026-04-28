package code_test

import (
	"os"
	"path/filepath"
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

// TestTypeScriptGrammar_ImportsRawFromGrammar pins the raw AST-level
// extraction contract: Parse (without ParseCtx) skips the relative-import
// resolver, so "./utils" and "./types" surface with path-style targets
// preserved and named members carried separately in Metadata["member"].
// Production code goes through ParseCtx — see
// TestTypeScriptGrammar_ResolvesRelativeImportsViaParseCtx for the
// resolved form.
func TestTypeScriptGrammar_ImportsRawFromGrammar(t *testing.T) {
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.Parse("t.ts", []byte(tsSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Group refs by (Target, member-metadata) so the two named imports of
	// "./utils" don't collide in a single map.
	type key struct{ target, member string }
	found := map[key]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind != "import" {
			continue
		}
		m := ""
		if r.Metadata != nil {
			if s, ok := r.Metadata["member"].(string); ok {
				m = s
			}
		}
		found[key{r.Target, m}] = r
	}

	want := []key{
		{"./utils", "foo"},       // named
		{"./utils", "bar"},       // named with alias
		{"./types", "Config"},    // type-only named
		{"lib", ""},              // default import: module with Alias, no member
		{"lib", "named"},         // named alongside default
		{"pkg", ""},              // namespace: module with Alias + Metadata
		{"side-effects-only", ""}, // side-effect only
	}
	for _, w := range want {
		if _, ok := found[w]; !ok {
			t.Errorf("missing import %+v (have %v)", w, found)
		}
	}

	// Type-only flag surfaces in Metadata on every named member from the
	// `import type { Config } from "./types"` clause.
	if r, ok := found[key{"./types", "Config"}]; ok {
		if r.Metadata == nil || r.Metadata["type_only"] != true {
			t.Errorf("expected type_only=true on ./types Config: %+v", r.Metadata)
		}
	}
	// Default import: Alias=defaultX, default=true.
	if r, ok := found[key{"lib", ""}]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "defaultX" || r.Metadata["default"] != true {
			t.Errorf("expected lib default import with alias=defaultX, default=true: %+v", r.Metadata)
		}
	}
	// Namespace import: Alias=ns, namespace=true.
	if r, ok := found[key{"pkg", ""}]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "ns" || r.Metadata["namespace"] != true {
			t.Errorf("expected pkg namespace import with alias=ns, namespace=true: %+v", r.Metadata)
		}
	}
	// Named-with-alias: foo no alias, bar alias=b.
	if r, ok := found[key{"./utils", "bar"}]; ok {
		if r.Metadata == nil || r.Metadata["alias"] != "b" {
			t.Errorf("expected ./utils bar with alias=b: %+v", r.Metadata)
		}
	}
}

// TestTypeScriptGrammar_ResolvesRelativeImportsViaParseCtx exercises the
// full ParseCtx pipeline with a tempdir fixture — on-disk sibling files
// establish the resolution targets, and the grammar's ResolveImports hook
// rewrites "./utils" → "src/utils.ts", "./types" → "src/types.ts", while
// bare specifiers like "lib" / "pkg" get tagged as external packages.
func TestTypeScriptGrammar_ResolvesRelativeImportsViaParseCtx(t *testing.T) {
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
	mustWrite("src/utils.ts", "export const foo = 1; export const bar = 2;\n")
	mustWrite("src/types.ts", "export type Config = { name: string };\n")

	const src = `import { foo, bar as b } from "./utils";
import type { Config } from "./types";
import defaultX, { named } from "lib";
import * as ns from "pkg";
import "side-effects-only";
`
	mustWrite("src/a.ts", src)
	abs := filepath.Join(root, "src", "a.ts")

	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("src/a.ts", []byte(src), indexer.ParseContext{AbsPath: abs, ProjectRoot: root})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}

	type seen struct {
		target, nodeKind string
	}
	kinds := map[seen]bool{}
	for _, r := range doc.Refs {
		if r.Kind != "import" {
			continue
		}
		nk := ""
		if r.Metadata != nil {
			if s, ok := r.Metadata["node_kind"].(string); ok {
				nk = s
			}
		}
		kinds[seen{r.Target, nk}] = true
	}

	for _, want := range []seen{
		{"src/utils.ts", "code_file"}, // resolved relative
		{"src/types.ts", "code_file"}, // resolved relative (type-only)
		{"lib", "external"},           // bare npm package
		{"pkg", "external"},           // bare namespace import
		{"side-effects-only", "external"},
	} {
		if !kinds[want] {
			t.Errorf("missing resolved import %+v; got %+v", want, kinds)
		}
	}
	for s := range kinds {
		if strings.HasPrefix(s.target, "./") || strings.HasPrefix(s.target, "../") {
			t.Errorf("unresolved relative import leaked through: %+v", s)
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

// Grammar invariants pass for all three flavours. Uses samples that avoid
// relative imports — the grammar's Parse path deliberately skips the
// ResolveImports post-pass (that happens under ParseCtx, which needs a real
// filesystem fixture), and the shared invariant rejects unresolved
// relative targets. The raw-grammar shape is exercised by
// TestTypeScriptGrammar_ImportsRawFromGrammar; resolution lives in
// TestTypeScriptGrammar_ResolvesRelativeImportsViaParseCtx.
func TestJSFamily_SatisfiesGrammarInvariants(t *testing.T) {
	const jsInvariantSample = `import defaultX, { named } from "lib";
import * as ns from "pkg";
import "side-effects-only";

export function greet(n) { return "hi " + n; }
export default class Service {
    constructor() { this.x = 1; }
    doThing() { return 42; }
}
export const useCounter = () => ({ count: 0 });
`
	const tsInvariantSample = `import defaultX, { named } from "lib";
import * as ns from "pkg";
import "side-effects-only";

export const PI = 3.14;
export function hello(name: string): string { return name; }

export const useAuth = (x: number): boolean => true;

export default class Service {
    readonly x: number = 1;
    constructor() {}
    async validate(u: string): Promise<boolean> { return true; }
}

export interface User { id: number; name: string }
export type Handler = (x: string) => void;
export enum Status { ACTIVE, INACTIVE }
`
	cases := []struct {
		name string
		g    code.Grammar
		path string
		src  []byte
	}{
		{"javascript", code.NewJavaScriptGrammar(), "u.js", []byte(jsInvariantSample)},
		{"typescript", code.NewTypeScriptGrammar(), "s.ts", []byte(tsInvariantSample)},
		{"tsx", code.NewTSXGrammar(), "c.tsx", []byte(tsInvariantSample)},
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

// --- lib-wji.1: inheritance extraction ---

func TestJavaScriptGrammar_ClassExtendsBareUnresolved(t *testing.T) {
	// JS: `class X extends Base` with no import for Base. ParseCtx with a
	// non-zero ctx is used so ResolveParents runs end-to-end (the legacy
	// Parse path no-ops all resolvers). Note that ResolveImports's
	// file-existence probe won't find "/tmp/x.js" on disk, which is fine —
	// there are no imports in the fixture, so ResolveImports has nothing
	// to resolve. ResolveParents then iterates the (empty) import bindings,
	// finds no match for bare "Base", and marks unresolved=true. The
	// resolver pathway IS exercised — this isn't a Parse-path surrogate.
	src := `class X extends Base {}`
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("x.js", []byte(src), indexer.ParseContext{AbsPath: "/tmp/x.js", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"x.X")
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
		t.Errorf("expected unresolved=true; got %+v", r.Metadata)
	}
}

func TestTypeScriptGrammar_ClassExtendsAndImplements(t *testing.T) {
	src := `class X extends Base implements IFoo, IBar {}`
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("x.ts", []byte(src), indexer.ParseContext{AbsPath: "/tmp/x.ts", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"x.X")
	byTargetRel := map[string]string{}
	for _, r := range refs {
		byTargetRel[r.Target] = r.Metadata["relation"].(string)
	}
	if byTargetRel["Base"] != "extends" {
		t.Errorf("Base relation = %q, want extends", byTargetRel["Base"])
	}
	if byTargetRel["IFoo"] != "implements" {
		t.Errorf("IFoo relation = %q, want implements", byTargetRel["IFoo"])
	}
	if byTargetRel["IBar"] != "implements" {
		t.Errorf("IBar relation = %q, want implements", byTargetRel["IBar"])
	}
}

func TestTypeScriptGrammar_InterfaceExtendsMultiple(t *testing.T) {
	src := `interface Auth extends Login, Logout { ok(): boolean; }`
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("auth.ts", []byte(src), indexer.ParseContext{AbsPath: "/tmp/auth.ts", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"auth.Auth")
	if len(refs) != 2 {
		t.Fatalf("expected 2 parents, got %d (%+v)", len(refs), refs)
	}
	for _, r := range refs {
		if r.Metadata["relation"] != "extends" {
			t.Errorf("interface-extends-interface relation for %q = %v, want extends", r.Target, r.Metadata["relation"])
		}
	}
}

func TestTypeScriptGrammar_AbstractClassInheritance(t *testing.T) {
	src := `abstract class BaseService extends Resource implements Lifecycle {}`
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("svc.ts", []byte(src), indexer.ParseContext{AbsPath: "/tmp/svc.ts", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"svc.BaseService")
	// Both extends + implements should land.
	seen := map[string]string{}
	for _, r := range refs {
		seen[r.Target] = r.Metadata["relation"].(string)
	}
	if seen["Resource"] != "extends" {
		t.Errorf("Resource relation = %q, want extends", seen["Resource"])
	}
	if seen["Lifecycle"] != "implements" {
		t.Errorf("Lifecycle relation = %q, want implements", seen["Lifecycle"])
	}
}

// Mixin-application (simple): class Foo extends Mixin(Base) emits two refs —
// one for the callee (Mixin) and one for the final argument (Base), both
// annotated with dynamic=true and mixin_chain.
func TestJavaScriptGrammar_MixinSimple(t *testing.T) {
	src := `class Foo extends Mixin(Base) {}`
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("m.js", []byte(src), indexer.ParseContext{AbsPath: "/tmp/m.js", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs (callee + base), got %d (%+v)", len(refs), refs)
	}
	targets := map[string]bool{}
	for _, r := range refs {
		targets[r.Target] = true
		if v, _ := r.Metadata["dynamic"].(bool); !v {
			t.Errorf("ref %q: expected dynamic=true; got %+v", r.Target, r.Metadata)
		}
		chain, _ := r.Metadata["mixin_chain"].([]string)
		if len(chain) != 2 || chain[0] != "Mixin" || chain[1] != "Base" {
			t.Errorf("ref %q: mixin_chain = %v, want [Mixin Base]", r.Target, chain)
		}
		if v, _ := r.Metadata["unresolved_expression"].(bool); v {
			t.Errorf("ref %q: must not carry unresolved_expression=true", r.Target)
		}
	}
	if !targets["Mixin"] {
		t.Errorf("expected a ref with Target=Mixin; got %+v", refs)
	}
	if !targets["Base"] {
		t.Errorf("expected a ref with Target=Base; got %+v", refs)
	}
}

// Mixin-application (nested): class Foo extends Mixin1(Mixin2(Base)) emits two
// refs — one for the outermost callee (Mixin1) and one for the final base (Base),
// both with mixin_chain=["Mixin1","Mixin2","Base"].
func TestJavaScriptGrammar_MixinNested(t *testing.T) {
	src := `class Foo extends Mixin1(Mixin2(Base)) {}`
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("m.js", []byte(src), indexer.ParseContext{AbsPath: "/tmp/m.js", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs (outermost callee + final base), got %d (%+v)", len(refs), refs)
	}
	targets := map[string]bool{}
	for _, r := range refs {
		targets[r.Target] = true
		if v, _ := r.Metadata["dynamic"].(bool); !v {
			t.Errorf("ref %q: expected dynamic=true; got %+v", r.Target, r.Metadata)
		}
		chain, _ := r.Metadata["mixin_chain"].([]string)
		wantChain := []string{"Mixin1", "Mixin2", "Base"}
		if len(chain) != 3 || chain[0] != wantChain[0] || chain[1] != wantChain[1] || chain[2] != wantChain[2] {
			t.Errorf("ref %q: mixin_chain = %v, want %v", r.Target, chain, wantChain)
		}
	}
	if !targets["Mixin1"] {
		t.Errorf("expected a ref with Target=Mixin1; got %+v", refs)
	}
	if !targets["Base"] {
		t.Errorf("expected a ref with Target=Base; got %+v", refs)
	}
	// Mixin2 is an intermediate callee — not emitted as a standalone ref
	if targets["Mixin2"] {
		t.Errorf("Mixin2 should not be emitted as a standalone ref; got %+v", refs)
	}
}

// Mixin-application with a non-class (string literal) argument: two refs are
// emitted — one for the callee (Mixin) and one whose Target is the literal text
// of the argument (including quotes). Both carry dynamic=true and mixin_chain.
func TestJavaScriptGrammar_MixinNonClassArg(t *testing.T) {
	src := `class Foo extends Mixin("not-a-class") {}`
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("m.js", []byte(src), indexer.ParseContext{AbsPath: "/tmp/m.js", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc, "m.Foo")
	// callee ref + literal-text ref
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs (callee + literal arg), got %d (%+v)", len(refs), refs)
	}
	targets := map[string]bool{}
	for _, r := range refs {
		targets[r.Target] = true
		if v, _ := r.Metadata["dynamic"].(bool); !v {
			t.Errorf("ref %q: expected dynamic=true; got %+v", r.Target, r.Metadata)
		}
		chain, _ := r.Metadata["mixin_chain"].([]string)
		if len(chain) != 2 {
			t.Errorf("ref %q: mixin_chain = %v, want 2-element chain", r.Target, chain)
		}
	}
	if !targets["Mixin"] {
		t.Errorf("expected a ref with Target=Mixin; got %+v", refs)
	}
}

func TestJavaScriptGrammar_MemberExpressionExtendsLeftAlone(t *testing.T) {
	// JS `class X extends pkg.Base` — dotted identifier; resolver skips it
	// (dotted Target is considered already-qualified).
	src := `class X extends ns.pkg.Base {}`
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("x.js", []byte(src), indexer.ParseContext{AbsPath: "/tmp/x.js", ProjectRoot: "/tmp"})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"x.X")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "ns.pkg.Base" {
		t.Errorf("Target = %q, want ns.pkg.Base", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("dotted target must not be marked unresolved: %+v", refs[0].Metadata)
	}
}

// Named import resolution: `import { Foo } from './utils'` + `class X
// extends Foo` → Target resolves to "utils.Foo" (stem of the resolved module
// path dot the member). Full ParseCtx with real temp files, mirroring the
// existing TypeScript resolver test.
func TestTypeScriptGrammar_NamedImportResolvesBareParent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "utils.ts"), []byte("export class Foo {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "src", "child.ts")
	childSrc := `import { Foo } from "./utils";
class Child extends Foo {}
`
	if err := os.WriteFile(childPath, []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("src/child.ts", []byte(childSrc), indexer.ParseContext{AbsPath: childPath, ProjectRoot: root})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"child.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "utils.Foo" {
		t.Errorf("Target = %q, want utils.Foo", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); v {
		t.Errorf("resolved named-import target should not be unresolved: %+v", refs[0].Metadata)
	}
}

// Aliased named import: `import { Foo as F } from './utils'` + `class X
// extends F` → resolver remaps local "F" to "utils.Foo".
func TestTypeScriptGrammar_AliasedNamedImportResolvesBareParent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "bases.ts"), []byte("export class RealBase {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "src", "c.ts")
	childSrc := `import { RealBase as B } from "./bases";
class Child extends B {}
`
	if err := os.WriteFile(childPath, []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	h := code.New(code.NewTypeScriptGrammar())
	doc, err := h.ParseCtx("src/c.ts", []byte(childSrc), indexer.ParseContext{AbsPath: childPath, ProjectRoot: root})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"c.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "bases.RealBase" {
		t.Errorf("Target = %q, want bases.RealBase (alias B → RealBase)", refs[0].Target)
	}
}

// Default imports are left unresolved — the default export's actual symbol
// name isn't inferable at parse time and lib-38i will handle this.
func TestJavaScriptGrammar_DefaultImportLeftUnresolved(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "foo.js"), []byte("export default class {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(root, "src", "c.js")
	childSrc := `import foo from "./foo";
class Child extends foo {}
`
	if err := os.WriteFile(childPath, []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	h := code.New(code.NewJavaScriptGrammar())
	doc, err := h.ParseCtx("src/c.js", []byte(childSrc), indexer.ParseContext{AbsPath: childPath, ProjectRoot: root})
	if err != nil {
		t.Fatalf("ParseCtx: %v", err)
	}
	refs := inheritsRefsBySource(doc,"c.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d (%+v)", len(refs), refs)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
		t.Errorf("default-import parent should be unresolved (deferred to lib-38i); got %+v", refs[0].Metadata)
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
