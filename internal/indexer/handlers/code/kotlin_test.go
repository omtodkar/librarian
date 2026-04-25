package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const kotlinSample = `package com.example.auth

import com.example.base.BaseService
import com.example.util.Logger as Log
import kotlinx.coroutines.*

/**
 * Service handles authentication.
 * TODO: rate-limit failures.
 */
@Service
@Transactional(readOnly = true)
data class AuthService(private val db: String) : BaseService(), Authenticator {

    /** Validate the credentials. */
    @Deprecated("use validateAsync")
    override fun validate(user: String): Boolean {
        // FIXME: quadratic
        return true
    }

    companion object {
        const val MAX_ATTEMPTS = 3
    }
}

sealed class Result {
    object Success : Result()
    data class Failure(val reason: String) : Result()
}

interface Authenticator : Login, Logout {
    suspend fun authenticate(): Boolean
}

interface Login
interface Logout

enum class Status : Displayable {
    ACTIVE, INACTIVE
}

typealias UserId = String

fun String.toSlug(): String = this.lowercase().replace(" ", "-")
`

func TestKotlinGrammar_ParseExtractsTypesAndFunctions(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("src/AuthService.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "kotlin" {
		t.Errorf("Format = %q, want %q", doc.Format, "kotlin")
	}
	if doc.Title != "com.example.auth" {
		t.Errorf("Title = %q, want %q", doc.Title, "com.example.auth")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []tk{
		{"AuthService", "class"},
		{"validate", "method"},    // function inside class_body → Kind=method via walker rewrite
		{"Result", "class"},       // sealed class also emits Kind=class
		{"Success", "object"},     // nested object
		{"Failure", "class"},      // nested data class
		{"Authenticator", "class"},
		{"authenticate", "method"}, // inside interface body → method
		{"Status", "class"},        // enum class emits Kind=class + label=enum
		{"UserId", "type"},
		{"toSlug", "function"},     // top-level extension function → function
	} {
		if !got[w] {
			t.Errorf("missing Unit {title=%q, kind=%q}; have %v", w.title, w.kind, got)
		}
	}
}

func TestKotlinGrammar_ImportsWithAliasAndWildcard(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}
	// Plain, aliased, wildcard.
	for _, want := range []string{
		"com.example.base.BaseService",
		"com.example.util.Logger",
		"kotlinx.coroutines.*",
	} {
		if _, ok := byTarget[want]; !ok {
			t.Errorf("missing import %q (got %v)", want, byTarget)
		}
	}
	// Alias metadata on the aliased import.
	if r, ok := byTarget["com.example.util.Logger"]; !ok {
		t.Error("aliased import missing")
	} else if r.Metadata == nil || r.Metadata["alias"] != "Log" {
		t.Errorf("expected alias=\"Log\" metadata; got %+v", r.Metadata)
	}
	// Wildcard metadata on the star import.
	if r, ok := byTarget["kotlinx.coroutines.*"]; !ok {
		t.Error("wildcard import missing")
	} else if r.Metadata == nil || r.Metadata["wildcard"] != true {
		t.Errorf("expected wildcard=true metadata; got %+v", r.Metadata)
	}
}

func TestKotlinGrammar_AnnotationsEmitSignals(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := findUnit(doc, "AuthService")
	if svc == nil {
		t.Fatal("AuthService Unit missing")
	}
	for _, want := range []string{"Service", "Transactional"} {
		if !hasSignal(svc.Signals, "annotation", want) {
			t.Errorf("expected @%s annotation on AuthService; got %+v", want, svc.Signals)
		}
	}

	validate := findUnit(doc, "validate")
	if validate == nil {
		t.Fatal("validate Unit missing")
	}
	if !hasSignal(validate.Signals, "annotation", "Deprecated") {
		t.Errorf("expected @Deprecated on validate; got %+v", validate.Signals)
	}
}

func TestKotlinGrammar_ModifierLabelSignals(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// data class AuthService → data label
	if svc := findUnit(doc, "AuthService"); svc == nil {
		t.Fatal("AuthService missing")
	} else if !hasSignal(svc.Signals, "label", "data") {
		t.Errorf("expected data label on AuthService; got %+v", svc.Signals)
	}
	// sealed class Result → sealed label
	if res := findUnit(doc, "Result"); res == nil {
		t.Fatal("Result missing")
	} else if !hasSignal(res.Signals, "label", "sealed") {
		t.Errorf("expected sealed label on Result; got %+v", res.Signals)
	}
	// interface Authenticator → interface label
	if auth := findUnit(doc, "Authenticator"); auth == nil {
		t.Fatal("Authenticator missing")
	} else if !hasSignal(auth.Signals, "label", "interface") {
		t.Errorf("expected interface label on Authenticator; got %+v", auth.Signals)
	}
	// enum class Status → enum label
	if st := findUnit(doc, "Status"); st == nil {
		t.Fatal("Status missing")
	} else if !hasSignal(st.Signals, "label", "enum") {
		t.Errorf("expected enum label on Status; got %+v", st.Signals)
	}
	// override fun validate → override label
	if v := findUnit(doc, "validate"); v == nil {
		t.Fatal("validate missing")
	} else if !hasSignal(v.Signals, "label", "override") {
		t.Errorf("expected override label on validate; got %+v", v.Signals)
	}
	// suspend fun authenticate → suspend label
	if a := findUnit(doc, "authenticate"); a == nil {
		t.Fatal("authenticate missing")
	} else if !hasSignal(a.Signals, "label", "suspend") {
		t.Errorf("expected suspend label on authenticate; got %+v", a.Signals)
	}
}

// Inheritance: data class AuthService : BaseService(), Authenticator.
// Heuristic: BaseService() = extends (constructor_invocation); Authenticator = implements (plain user_type).
func TestKotlinGrammar_ClassExtendsAndImplements(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("auth.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.auth.AuthService")
	if len(refs) != 2 {
		t.Fatalf("expected 2 parents, got %d (%+v)", len(refs), refs)
	}
	byTarget := map[string]string{}
	for _, r := range refs {
		byTarget[r.Target] = r.Metadata["relation"].(string)
	}
	// BaseService is imported, so it resolves to com.example.base.BaseService.
	if rel := byTarget["com.example.base.BaseService"]; rel != "extends" {
		t.Errorf("BaseService relation = %q, want extends", rel)
	}
	// Authenticator is same-package, no import → bare name with unresolved.
	for _, r := range refs {
		if r.Target == "Authenticator" {
			if r.Metadata["relation"] != "implements" {
				t.Errorf("Authenticator relation = %v, want implements", r.Metadata["relation"])
			}
			if v, _ := r.Metadata["unresolved"].(bool); !v {
				t.Errorf("Authenticator should be unresolved (no matching import); got %+v", r.Metadata)
			}
		}
	}
}

// Interface inheritance: `interface Authenticator : Login, Logout` — all
// targets land as extends (interfaces inherit interfaces).
func TestKotlinGrammar_InterfaceExtendsMultiple(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("auth.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.auth.Authenticator")
	if len(refs) != 2 {
		t.Fatalf("expected 2 interface parents, got %d (%+v)", len(refs), refs)
	}
	for _, r := range refs {
		if r.Metadata["relation"] != "extends" {
			t.Errorf("interface-extends-interface: %q relation = %v, want extends", r.Target, r.Metadata["relation"])
		}
	}
}

// `enum class Status : Displayable` — enum can only implement.
func TestKotlinGrammar_EnumImplements(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("auth.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.auth.Status")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Displayable" {
		t.Errorf("Target = %q, want Displayable", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "implements" {
		t.Errorf("enum inheritance relation = %v, want implements", refs[0].Metadata["relation"])
	}
}

// Aliased import: `import com.example.util.Logger as Log` then a bare `Log`
// reference resolves to com.example.util.Logger.
func TestKotlinGrammar_AliasedImportResolvesBareParent(t *testing.T) {
	src := []byte(`package com.example

import com.example.base.RealBase as B

class Child : B()
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Child.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "com.example.base.RealBase" {
		t.Errorf("Target = %q, want com.example.base.RealBase (alias B → RealBase)", refs[0].Target)
	}
}

// Generic inheritance: `class Cache<K, V> : HashMap<K, V>()` — generics
// strip from Target, preserved in metadata.type_args.
func TestKotlinGrammar_StripsGenericsAndCapturesTypeArgs(t *testing.T) {
	src := []byte(`package com.example

import java.util.HashMap

class Cache<K, V> : HashMap<K, V>()
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Cache.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.Cache")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "java.util.HashMap" {
		t.Errorf("Target = %q, want java.util.HashMap (resolved + generics stripped)", refs[0].Target)
	}
	args, _ := refs[0].Metadata["type_args"].([]string)
	if len(args) != 2 || args[0] != "K" || args[1] != "V" {
		t.Errorf("type_args = %v, want [K V]", args)
	}
}

// Wildcard import does NOT resolve a bare parent.
func TestKotlinGrammar_WildcardImportDoesNotResolve(t *testing.T) {
	src := []byte(`package com.example

import com.example.stars.*

class Child : Base()
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Child.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.Child")
	if len(refs) != 1 {
		t.Fatalf("expected 1 parent, got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "Base" {
		t.Errorf("Target = %q, want Base", refs[0].Target)
	}
	if v, _ := refs[0].Metadata["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true (wildcard doesn't bind); got %+v", refs[0].Metadata)
	}
}

// ref.Source matches a Unit.Path in the same doc — invariant from lib-wji.1
// enforced at the grammar layer by AssertGrammarInvariants.
func TestKotlinGrammar_SourceIsContainingSymbolPath(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	unitPaths := map[string]bool{}
	for _, u := range doc.Units {
		unitPaths[u.Path] = true
	}
	for _, r := range doc.Refs {
		if r.Kind != "inherits" {
			continue
		}
		if r.Source == "" {
			t.Errorf("inherits ref has empty Source: %+v", r)
			continue
		}
		if !unitPaths[r.Source] {
			t.Errorf("inherits ref Source %q does not match any Unit.Path", r.Source)
		}
	}
}

// Javadoc-style KDoc comments (`/** ... */`) flow into Unit.Content via the
// preceding-comment buffer, same as Java.
func TestKotlinGrammar_KDocAsDocstring(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := findUnit(doc, "AuthService")
	if svc == nil {
		t.Fatal("AuthService missing")
	}
	if !strings.Contains(svc.Content, "Service handles authentication.") {
		t.Errorf("KDoc didn't flow into AuthService Content:\n%s", svc.Content)
	}
	v := findUnit(doc, "validate")
	if v == nil {
		t.Fatal("validate missing")
	}
	if !strings.Contains(v.Content, "Validate the credentials.") {
		t.Errorf("KDoc missing on validate:\n%s", v.Content)
	}
}

func TestKotlinGrammar_RationaleSignals(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("t.kt", []byte(kotlinSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	seen := map[string]bool{}
	for _, s := range doc.Signals {
		seen[s.Value] = true
	}
	for _, want := range []string{"todo", "fixme"} {
		if !seen[want] {
			t.Errorf("missing document-level signal %q (got %v)", want, seen)
		}
	}
}

func TestKotlinGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("Foo.kt") == nil {
		t.Error(".kt extension not registered")
	}
}

func TestKotlinGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	code.AssertGrammarInvariants(t, h, "com/example/Auth.kt", []byte(kotlinSample))
}

func TestKotlinGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Empty.kt", []byte{})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected 0 Units, got %d", len(doc.Units))
	}
}
