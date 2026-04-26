package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const swiftSample = `import UIKit
import class Foo.Bar
@testable import MyCore

/**
 * Auth is the authentication protocol.
 * TODO: add passkey support.
 */
@MainActor
protocol Auth: AnyObject {
    var isSignedIn: Bool { get }
    func signIn() async throws
}

/// AuthService handles user auth.
class AuthService: NSObject, Auth, Hashable {
    @Published var isSignedIn: Bool = false
    let db: Database
    init(db: Database) { self.db = db }
    @available(iOS 14, *)
    func signIn() async throws {
        // FIXME: rate limit
    }
    static func shared() -> AuthService { fatalError() }
}

struct User: Codable, Equatable {
    let id: String
    var name: String
}

enum Status: String, CaseIterable {
    case active, inactive
}

extension String: Identifiable {
    public var id: String { self }
    func slug() -> String { self.lowercased() }
}

typealias UserId = String

final class Cache<K: Hashable, V>: NSObject {
    var storage: [K: V] = [:]
}

protocol Container {
    associatedtype Element
    subscript(i: Int) -> Element { get }
}
`

// Core parse: every declaration kind + name is captured.
func TestSwiftGrammar_ParseExtractsDeclarationKinds(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("src/auth/service.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "swift" {
		t.Errorf("Format = %q, want %q", doc.Format, "swift")
	}
	if doc.Title != "service" {
		t.Errorf("Title = %q, want %q (file stem, Swift has no package clause)", doc.Title, "service")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []tk{
		{"Auth", "protocol"},
		{"isSignedIn", "property"},
		{"signIn", "method"},
		{"AuthService", "class"},
		{"init", "constructor"},
		{"shared", "method"},
		{"User", "struct"},
		{"id", "property"},
		{"name", "property"},
		{"Status", "enum"},
		{"String", "extension"}, // Title is the TARGET type for extensions
		{"slug", "method"},
		{"UserId", "type"},
		{"Cache", "class"},
		{"Container", "protocol"},
		{"Element", "type"},      // associatedtype
		{"subscript", "method"},  // subscript_declaration
	} {
		if !got[w] {
			t.Errorf("missing Unit {title=%q, kind=%q}; have %v", w.title, w.kind, got)
		}
	}
}

// Inheritance heuristic — class flavor gets first=extends, rest=conforms.
func TestSwiftGrammar_ClassInheritanceHeuristic(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "svc.AuthService")
	byTarget := map[string]string{}
	for _, r := range refs {
		rel, _ := r.Metadata["relation"].(string)
		byTarget[r.Target] = rel
	}
	if byTarget["NSObject"] != "extends" {
		t.Errorf("NSObject relation = %q, want extends (first specifier on class flavor)", byTarget["NSObject"])
	}
	if byTarget["Auth"] != "conforms" {
		t.Errorf("Auth relation = %q, want conforms (subsequent specifier on class flavor)", byTarget["Auth"])
	}
	if byTarget["Hashable"] != "conforms" {
		t.Errorf("Hashable relation = %q, want conforms", byTarget["Hashable"])
	}
}

// struct / enum — every specifier is a protocol conformance.
func TestSwiftGrammar_StructAllConforms(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "svc.User")
	if len(refs) != 2 {
		t.Fatalf("expected 2 conformances on User, got %d (%+v)", len(refs), refs)
	}
	for _, r := range refs {
		if r.Metadata["relation"] != "conforms" {
			t.Errorf("struct %q relation = %v, want conforms", r.Target, r.Metadata["relation"])
		}
	}
}

func TestSwiftGrammar_EnumAllConforms(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "svc.Status")
	// `enum Status: String, CaseIterable` → 2 conforms.
	seen := map[string]string{}
	for _, r := range refs {
		rel, _ := r.Metadata["relation"].(string)
		seen[r.Target] = rel
	}
	for _, want := range []string{"String", "CaseIterable"} {
		if seen[want] != "conforms" {
			t.Errorf("enum %q relation = %q, want conforms", want, seen[want])
		}
	}
}

// Protocol-extends-protocol: all inheritance_specifiers map to extends.
func TestSwiftGrammar_ProtocolAllExtends(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "svc.Auth")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent on Auth protocol, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "AnyObject" {
		t.Errorf("Auth parent = %q, want AnyObject", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "extends" {
		t.Errorf("protocol inheritance relation = %v, want extends", refs[0].Metadata["relation"])
	}
}

// Extension: target type is the Title, inheritance specifiers are conforms,
// and Unit.Metadata["extends_type"] carries the target.
func TestSwiftGrammar_ExtensionTargetAndConformances(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ext := findUnit(doc, "String")
	if ext == nil {
		t.Fatal("extension String Unit missing (Title should be target type)")
	}
	if ext.Kind != "extension" {
		t.Errorf("extension Kind = %q, want \"extension\"", ext.Kind)
	}
	if got, _ := ext.Metadata["extends_type"].(string); got != "String" {
		t.Errorf("Metadata[\"extends_type\"] = %q, want \"String\"", got)
	}
	refs := inheritsRefsBySource(doc, "svc.String")
	if len(refs) != 1 {
		t.Fatalf("expected 1 conformance on extension, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Identifiable" {
		t.Errorf("extension conformance = %q, want Identifiable", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "conforms" {
		t.Errorf("extension conformance relation = %v, want conforms", refs[0].Metadata["relation"])
	}
}

// Extension members carry receiver metadata. The extension-member receiver
// mirrors Kotlin's Metadata["receiver"] convention so cross-language "all
// extensions of String" queries work uniformly.
//
// Finds Units by Path prefix because Unit.Title alone is ambiguous here —
// `struct User { let id }` and `extension String { var id }` both emit
// Units with Title="id"; only the extension's one carries receiver=String.
func TestSwiftGrammar_ExtensionMemberReceiverMetadata(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	slug := findByPath(doc, "svc.String.slug")
	if slug == nil {
		t.Fatal("svc.String.slug Unit missing (extension method)")
	}
	if got, _ := slug.Metadata["receiver"].(string); got != "String" {
		t.Errorf("slug Metadata[\"receiver\"] = %q, want \"String\"", got)
	}
	extID := findByPath(doc, "svc.String.id")
	if extID == nil {
		t.Fatal("svc.String.id Unit missing (extension property)")
	}
	if got, _ := extID.Metadata["receiver"].(string); got != "String" {
		t.Errorf("extension id Metadata[\"receiver\"] = %q, want \"String\"", got)
	}
	// Sanity: the struct User's id property must NOT carry a receiver
	// (it isn't an extension member).
	userID := findByPath(doc, "svc.User.id")
	if userID == nil {
		t.Fatal("svc.User.id Unit missing")
	}
	if _, has := userID.Metadata["receiver"]; has {
		t.Errorf("struct property User.id must not carry receiver; got %+v", userID.Metadata)
	}
}

// Typealiases, subscripts, and init declarations inside an extension
// all carry receiver metadata — gaps here would silently hide members
// from "all extensions of X" queries.
func TestSwiftGrammar_ExtensionMemberReceiverMetadata_AllKinds(t *testing.T) {
	src := []byte(`extension Collection {
    typealias Head = Element
    subscript(head _: Int) -> Element? { first }
    init(single e: Element) { self.init() }
    func peek() -> Element? { first }
    var tail: Collection { self }
}
`)
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("c.swift", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, path := range []string{
		"c.Collection.Head",
		"c.Collection.subscript",
		"c.Collection.init",
		"c.Collection.peek",
		"c.Collection.tail",
	} {
		u := findByPath(doc, path)
		if u == nil {
			t.Errorf("missing Unit at %q", path)
			continue
		}
		if got, _ := u.Metadata["receiver"].(string); got != "Collection" {
			t.Errorf("%s receiver = %q, want %q", path, got, "Collection")
		}
	}
}

// Generic inheritance: `class Cache<K: Hashable, V>: NSObject` — NSObject
// lands as extends with the generic type arguments stripped (they live on
// the class_declaration's type_parameters, not the inheritance_specifier).
func TestSwiftGrammar_GenericClassInheritance(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "svc.Cache")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent on Cache, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "NSObject" {
		t.Errorf("Target = %q, want NSObject", refs[0].Target)
	}
}

// @testable import surfaces Metadata["testable"]=true; other imports don't.
func TestSwiftGrammar_TestableImportMetadata(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}
	// @testable import MyCore should carry testable=true.
	my, ok := byTarget["MyCore"]
	if !ok {
		t.Fatal("MyCore import missing")
	}
	if my.Metadata == nil || my.Metadata["testable"] != true {
		t.Errorf("MyCore should be testable; metadata = %+v", my.Metadata)
	}
	// Plain imports should NOT be testable.
	if uikit, ok := byTarget["UIKit"]; ok {
		if uikit.Metadata != nil && uikit.Metadata["testable"] == true {
			t.Errorf("UIKit should not be testable; metadata = %+v", uikit.Metadata)
		}
	}
}

// Attributes surface as annotation signals on the decorated Unit.
func TestSwiftGrammar_AttributesEmitAnnotationSignals(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	auth := findUnit(doc, "Auth")
	if auth == nil {
		t.Fatal("Auth protocol missing")
	}
	if !hasSignal(auth.Signals, "annotation", "MainActor") {
		t.Errorf("expected @MainActor on Auth; got %+v", auth.Signals)
	}
	// signIn appears twice — once as a protocol_function_declaration
	// requirement in Auth (no @available), once as the class method on
	// AuthService (the @available-annotated one). Verify at least one
	// signIn Unit carries the annotation.
	foundAvailable := false
	for _, u := range doc.Units {
		if u.Title == "signIn" && hasSignal(u.Signals, "annotation", "available") {
			foundAvailable = true
			break
		}
	}
	if !foundAvailable {
		t.Errorf("expected @available on at least one signIn Unit")
	}
	// @Published on the class property (not the protocol requirement —
	// both emit Kind=property so disambiguate by Path).
	if prop := findByPath(doc, "svc.AuthService.isSignedIn"); prop == nil {
		t.Error("svc.AuthService.isSignedIn Unit missing")
	} else if !hasSignal(prop.Signals, "annotation", "Published") {
		t.Errorf("expected @Published on AuthService.isSignedIn; got %+v", prop.Signals)
	}
}

// Modifier labels: final / static / open / override / mutating etc.
func TestSwiftGrammar_ModifierLabelSignals(t *testing.T) {
	src := []byte(`final class SealedBox {
    static let version = 1
    open func describe() {}
    override func hash(into: inout Hasher) {}
}
struct Counter {
    mutating func increment() {}
}
class Node {
    weak var parent: Node?
    lazy var expensive: Int = computeIt()
}
indirect enum Tree {
    case leaf(Int)
    case branch(Tree, Tree)
}
`)
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("mods.swift", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	hasLabel := func(title, label string) bool {
		u := findUnit(doc, title)
		if u == nil {
			return false
		}
		return hasSignal(u.Signals, "label", label)
	}
	for _, c := range []struct{ title, label string }{
		{"SealedBox", "final"},
		{"version", "static"},
		{"describe", "open"},
		{"hash", "override"},
		{"increment", "mutating"},
		{"parent", "weak"},
		{"expensive", "lazy"},
		{"Tree", "indirect"},
	} {
		if !hasLabel(c.title, c.label) {
			u := findUnit(doc, c.title)
			var sigs []indexer.Signal
			if u != nil {
				sigs = u.Signals
			}
			t.Errorf("expected label %q on %q; got signals %+v", c.label, c.title, sigs)
		}
	}
}

// Flavor labels: struct / enum / extension declarations carry their
// keyword as a label even though Unit.Kind is already the flavor (the
// label makes them filterable alongside other modifier labels).
func TestSwiftGrammar_FlavorLabels(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, c := range []struct{ title, label string }{
		{"User", "struct"},
		{"Status", "enum"},
		{"String", "extension"}, // the extension's Title is the target
	} {
		u := findUnit(doc, c.title)
		if u == nil {
			t.Errorf("%s Unit missing", c.title)
			continue
		}
		if !hasSignal(u.Signals, "label", c.label) {
			t.Errorf("expected label %q on %q; got %+v", c.label, c.title, u.Signals)
		}
	}
}

// Swift imports are module-level, not type-level — `import AuthKit` does
// NOT bind a symbol named `Base` at file scope the way Java's
// `import com.foo.Base` does. So `class Child: Base` can't be resolved
// via the same-file-import mechanism and must stay marked unresolved.
// (`import class Foo.Bar` IS type-binding — covered separately.)
func TestSwiftGrammar_ModuleImportDoesNotBindBareTypes(t *testing.T) {
	src := []byte(`import AuthKit

class Child: Base {}
`)
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("c.swift", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "c.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent, got %d (%+v)", len(refs), refs)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true on bare Base (module import doesn't bind types); got %+v", refs[0].Metadata)
	}
}

// Docstring attachment: Swift's /// and /** */ comments flow into the
// following declaration's Unit.Content via the preceding-comment buffer.
func TestSwiftGrammar_DocstringsAttachToUnits(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	auth := findUnit(doc, "Auth")
	if auth == nil {
		t.Fatal("Auth missing")
	}
	if !strings.Contains(auth.Content, "authentication protocol") {
		t.Errorf("Auth docstring missing:\n%s", auth.Content)
	}
	svc := findUnit(doc, "AuthService")
	if svc == nil {
		t.Fatal("AuthService missing")
	}
	if !strings.Contains(svc.Content, "handles user auth") {
		t.Errorf("AuthService /// docstring missing:\n%s", svc.Content)
	}
}

// Rationale signals (TODO, FIXME) surface at document level.
func TestSwiftGrammar_RationaleSignals(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("svc.swift", []byte(swiftSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range doc.Signals {
		seen[s.Value] = true
	}
	for _, want := range []string{"todo", "fixme"} {
		if !seen[want] {
			t.Errorf("missing document signal %q (got %v)", want, seen)
		}
	}
}

func TestSwiftGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("Foo.swift") == nil {
		t.Error(".swift extension not registered")
	}
}

func TestSwiftGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	code.AssertGrammarInvariants(t, h, "pkg/Auth.swift", []byte(swiftSample))
}

func TestSwiftGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewSwiftGrammar())
	doc, err := h.Parse("Empty.swift", []byte{})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected 0 Units, got %d", len(doc.Units))
	}
}
