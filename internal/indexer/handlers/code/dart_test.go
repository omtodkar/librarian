package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const dartSample = `library auth.service;

import 'package:flutter/material.dart';
import 'package:provider/provider.dart' as p show Consumer hide Widget;
import './utils.dart';

part 'auth_service.g.dart';
part of 'main.dart';

/// AuthService is the authentication contract.
@immutable
abstract class AuthService extends BaseService implements Disposable with LoggerMixin {
  final String apiRoot;
  AuthService(this.apiRoot);
  AuthService.local() : apiRoot = '';
  factory AuthService.fromConfig(Config c) => AuthService(c.root);

  @override
  void dispose() {}

  String get displayName => apiRoot;
  set displayName(String v) {}

  bool authenticate(String user, String password);
}

mixin LoggerMixin on BaseService {
  void log(String msg) {}
}

mixin Disposable {
  void dispose();
}

extension StringX on String {
  String slug() => toLowerCase();
}

extension type UserId(int id) implements Object {
  int get value => id;
}

enum Status implements Comparable<Status> {
  active, inactive;
  @override
  int compareTo(Status other) => index - other.index;
}

sealed class Shape {}
final class Circle extends Shape {}
base class Box {}
interface class Marker {}

typedef Parser = String Function(String);

String topLevelFunc() => 'hi';
`

// Core parse: every declaration kind + name is captured.
func TestDartGrammar_ParseExtractsDeclarationKinds(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("src/auth/service.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "dart" {
		t.Errorf("Format = %q, want %q", doc.Format, "dart")
	}
	if doc.Title != "auth.service" {
		t.Errorf("Title = %q, want %q (from library directive)", doc.Title, "auth.service")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []tk{
		{"AuthService", "class"},
		{"apiRoot", "field"},
		{"AuthService", "constructor"}, // default ctor — Java convention
		{"local", "constructor"},       // named ctor — just the named segment so Path=pkg.AuthService.local
		{"fromConfig", "constructor"},  // factory ctor
		{"dispose", "method"},
		{"displayName", "property"}, // getter AND setter
		{"authenticate", "method"},
		{"LoggerMixin", "mixin"},
		{"log", "method"},
		{"Disposable", "mixin"},
		{"StringX", "extension"},
		{"slug", "method"},
		{"UserId", "extension_type"},
		{"value", "property"},
		{"Status", "enum"},
		{"compareTo", "method"},
		{"Shape", "class"},
		{"Circle", "class"},
		{"Box", "class"},
		{"Marker", "class"},
		{"Parser", "type"},
		{"topLevelFunc", "function"},
	} {
		if !got[w] {
			t.Errorf("missing Unit {title=%q, kind=%q}", w.title, w.kind)
		}
	}
}

// Library directive → PackageName.
func TestDartGrammar_PackageNameFromLibrary(t *testing.T) {
	src := []byte(`library foo.bar.baz;
class X {}
`)
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("x.dart", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Title != "foo.bar.baz" {
		t.Errorf("Title = %q, want %q", doc.Title, "foo.bar.baz")
	}
}

// No library directive → file stem fallback.
func TestDartGrammar_NoLibraryFallsBackToStem(t *testing.T) {
	src := []byte(`class X {}
`)
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("lib/services/foo.dart", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Title != "foo" {
		t.Errorf("Title = %q, want %q (stem fallback)", doc.Title, "foo")
	}
}

// `class X extends A implements B with M` → three inherits edges with
// distinct relations. Critical coverage for the interfaces-ERROR quirk.
func TestDartGrammar_ClassInheritanceAllRelations(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "auth.service.AuthService")
	byTarget := map[string]string{}
	for _, r := range refs {
		rel, _ := r.Metadata["relation"].(string)
		byTarget[r.Target] = rel
	}
	if byTarget["BaseService"] != "extends" {
		t.Errorf("BaseService relation = %q, want extends", byTarget["BaseService"])
	}
	if byTarget["Disposable"] != "implements" {
		t.Errorf("Disposable relation = %q, want implements", byTarget["Disposable"])
	}
	if byTarget["LoggerMixin"] != "mixes" {
		t.Errorf("LoggerMixin relation = %q, want mixes", byTarget["LoggerMixin"])
	}
}

// `mixin M on Base` → Reference.Kind="requires" (NOT inherits). Critical
// test for the new edge kind.
func TestDartGrammar_MixinOnClauseEmitsRequires(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var requires []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "requires" && r.Source == "auth.service.LoggerMixin" {
			requires = append(requires, r)
		}
	}
	if len(requires) != 1 {
		t.Fatalf("expected 1 requires ref on LoggerMixin, got %d (%+v)", len(requires), requires)
	}
	if requires[0].Target != "BaseService" {
		t.Errorf("requires Target = %q, want BaseService", requires[0].Target)
	}
	// Must NOT also emit an inherits edge.
	for _, r := range doc.Refs {
		if r.Kind == "inherits" && r.Source == "auth.service.LoggerMixin" {
			t.Errorf("LoggerMixin should emit requires only, not inherits: %+v", r)
		}
	}
}

// `mixin M on A, B` → two requires refs, one per constraint.
func TestDartGrammar_MixinOnMultipleConstraints(t *testing.T) {
	src := []byte(`mixin Multi on Animal, Logger {
  void announce() {}
}
`)
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("m.dart", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var reqs []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "requires" && r.Source == "m.Multi" {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requires refs, got %d (%+v)", len(reqs), reqs)
	}
	targets := map[string]bool{}
	for _, r := range reqs {
		targets[r.Target] = true
	}
	if !targets["Animal"] || !targets["Logger"] {
		t.Errorf("expected Animal + Logger targets; got %v", targets)
	}
}

// `mixin M { ... }` (no on clause) → no parent refs.
func TestDartGrammar_MixinWithoutOnHasNoParents(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, r := range doc.Refs {
		if r.Source == "auth.service.Disposable" {
			t.Errorf("Disposable has no on-clause but emitted a parent: %+v", r)
		}
	}
}

// `part 'foo.dart'` → Reference.Kind="part" with no direction metadata.
// `part of 'bar.dart'` → Reference.Kind="part" with direction="of".
// Critical test for the new import kind.
func TestDartGrammar_PartDirectiveEmitsPartKind(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var parts, partsOf []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind != "part" {
			continue
		}
		if d, _ := r.Metadata["direction"].(string); d == "of" {
			partsOf = append(partsOf, r)
		} else {
			parts = append(parts, r)
		}
	}
	if len(parts) != 1 {
		t.Errorf("expected 1 `part` ref, got %d (%+v)", len(parts), parts)
	}
	if len(parts) > 0 && parts[0].Target != "auth_service.g.dart" {
		t.Errorf("part Target = %q, want %q", parts[0].Target, "auth_service.g.dart")
	}
	if len(partsOf) != 1 {
		t.Errorf("expected 1 `part of` ref, got %d (%+v)", len(partsOf), partsOf)
	}
	// Must NOT appear under Kind="import".
	for _, r := range doc.Refs {
		if r.Kind == "import" && strings.Contains(r.Target, "auth_service.g.dart") {
			t.Errorf("part directive leaked into imports: %+v", r)
		}
	}
}

// `enum Priority implements Comparable<Priority>` → single implements
// ref, no with-clause, no extends.
func TestDartGrammar_EnumImplementsOnly(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "auth.service.Status")
	if len(refs) != 1 {
		t.Fatalf("expected 1 implements ref on Status, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Comparable" {
		t.Errorf("Target = %q, want Comparable", refs[0].Target)
	}
	if rel, _ := refs[0].Metadata["relation"].(string); rel != "implements" {
		t.Errorf("relation = %q, want implements", rel)
	}
	// Verify type_args stripped for the graph target; raw args land in Metadata.
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 1 || args[0] != "Status" {
		t.Errorf("type_args = %v, want [Status]", args)
	}
}

// Extension: Unit.Title is the extension's OWN name (StringX), not the
// target type. Metadata["extends_type"] carries the target — parallels
// Swift's convention.
func TestDartGrammar_ExtensionDeclarationHasTargetMetadata(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "StringX")
	if u == nil {
		t.Fatal("StringX extension Unit missing")
	}
	if u.Kind != "extension" {
		t.Errorf("Kind = %q, want extension", u.Kind)
	}
	if got, _ := u.Metadata["extends_type"].(string); got != "String" {
		t.Errorf("extends_type = %q, want String", got)
	}
}

// Extension type: distinct Unit.Kind="extension_type"; representation
// type surfaces via extends_type metadata.
func TestDartGrammar_ExtensionTypeDeclaration(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "UserId")
	if u == nil {
		t.Fatal("UserId extension_type Unit missing")
	}
	if u.Kind != "extension_type" {
		t.Errorf("Kind = %q, want extension_type", u.Kind)
	}
	if got, _ := u.Metadata["extends_type"].(string); got != "int" {
		t.Errorf("extends_type = %q, want int", got)
	}
	// implements Object also surfaces as an inherits ref.
	refs := inheritsRefsBySource(doc, "auth.service.UserId")
	found := false
	for _, r := range refs {
		if r.Target == "Object" {
			if rel, _ := r.Metadata["relation"].(string); rel == "implements" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected UserId implements Object, got refs %+v", refs)
	}
}

// `as`, `show`, `hide` combinators captured as Alias / Metadata.
func TestDartGrammar_ImportCombinatorsCaptured(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var providerImport *indexer.Reference
	for i, r := range doc.Refs {
		if r.Kind == "import" && strings.Contains(r.Target, "provider.dart") {
			providerImport = &doc.Refs[i]
			break
		}
	}
	if providerImport == nil {
		t.Fatal("provider.dart import missing")
	}
	if alias, _ := providerImport.Metadata["alias"].(string); alias != "p" {
		t.Errorf("alias = %q, want p", alias)
	}
	show, _ := providerImport.Metadata["show"].([]string)
	if len(show) != 1 || show[0] != "Consumer" {
		t.Errorf("show = %v, want [Consumer]", show)
	}
	hide, _ := providerImport.Metadata["hide"].([]string)
	if len(hide) != 1 || hide[0] != "Widget" {
		t.Errorf("hide = %v, want [Widget]", hide)
	}
}

// Annotations surface as Signal{Kind="annotation"}.
func TestDartGrammar_AnnotationsEmitSignals(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	auth := findUnit(doc, "AuthService")
	if auth == nil {
		t.Fatal("AuthService Unit missing")
	}
	if !hasSignal(auth.Signals, "annotation", "immutable") {
		t.Errorf("expected @immutable on AuthService; got %+v", auth.Signals)
	}
}

// Class modifiers (abstract, sealed, base, interface, final) emit as
// Signal{Kind="label"} on the owning class Unit.
func TestDartGrammar_ClassModifierLabels(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tc := range []struct{ title, label string }{
		{"AuthService", "abstract"},
		{"Shape", "sealed"},
		{"Circle", "final"},
		{"Box", "base"},
		{"Marker", "interface"},
	} {
		u := findUnit(doc, tc.title)
		if u == nil {
			t.Errorf("%s Unit missing", tc.title)
			continue
		}
		if !hasSignal(u.Signals, "label", tc.label) {
			t.Errorf("expected label %q on %s; got %+v", tc.label, tc.title, u.Signals)
		}
	}
}

// Factory constructor emits a label signal.
func TestDartGrammar_FactoryLabel(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findByPath(doc, "auth.service.AuthService.fromConfig")
	if u == nil {
		t.Fatal("AuthService.fromConfig factory ctor Unit missing")
	}
	if !hasSignal(u.Signals, "label", "factory") {
		t.Errorf("expected factory label on fromConfig; got %+v", u.Signals)
	}
}

// Field modifier (final on apiRoot) emits a label signal.
func TestDartGrammar_FieldFinalLabel(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "apiRoot")
	if u == nil {
		t.Fatal("apiRoot field Unit missing")
	}
	if !hasSignal(u.Signals, "label", "final") {
		t.Errorf("expected final label on apiRoot; got %+v", u.Signals)
	}
}

// `///` doc comments flow into the following declaration's Unit.Content
// via the preceding-comment buffer.
func TestDartGrammar_DocstringsAttachToUnits(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("svc.dart", []byte(dartSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "AuthService")
	if u == nil {
		t.Fatal("AuthService missing")
	}
	if !strings.Contains(u.Content, "authentication contract") {
		t.Errorf("AuthService docstring missing:\n%s", u.Content)
	}
}

// Rationale signals (TODO/FIXME in function bodies) surface at doc level.
func TestDartGrammar_RationaleSignals(t *testing.T) {
	src := []byte(`class X {
  void f() {
    // TODO: implement
    // FIXME: handle error
  }
}
`)
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("x.dart", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range doc.Signals {
		seen[strings.ToLower(s.Value)] = true
	}
	for _, want := range []string{"todo", "fixme"} {
		if !seen[want] {
			t.Errorf("missing rationale signal %q (got %v)", want, seen)
		}
	}
}

func TestDartGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("Foo.dart") == nil {
		t.Error(".dart extension not registered")
	}
}

func TestDartGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	code.AssertGrammarInvariants(t, h, "lib/auth/service.dart", []byte(dartSample))
}

func TestDartGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewDartGrammar())
	doc, err := h.Parse("empty.dart", []byte(""))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "dart" {
		t.Errorf("Format = %q, want dart", doc.Format)
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected 0 Units on empty file, got %d", len(doc.Units))
	}
}
