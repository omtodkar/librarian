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

// --- lib-gyh: Kotlin coverage gaps ---

// Secondary constructor: class with a primary constructor + a `constructor(...)`
// block emits a Unit with Kind="constructor".
func TestKotlinGrammar_SecondaryConstructor(t *testing.T) {
	src := []byte(`package com.example

class Pair(val a: Int, val b: Int) {
    constructor(raw: String) : this(raw.length, 0)
}
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Pair.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// There should be exactly one Unit with Kind="constructor". The Title
	// is the containing class name — tree-sitter-kotlin surfaces the
	// constructor keyword as the secondary_constructor node, and
	// SymbolName falls back to the first simple_identifier (class name,
	// since there's no `name` field on secondary_constructor).
	var ctor *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Kind == "constructor" {
			ctor = &doc.Units[i]
			break
		}
	}
	if ctor == nil {
		t.Fatal("no constructor Unit emitted")
	}
	if !strings.Contains(ctor.Content, "constructor(raw: String)") {
		t.Errorf("constructor Unit.Content missing the secondary constructor signature:\n%s", ctor.Content)
	}
}

// Interface delegation: `class Foo : Bar by delegate` produces an inherits
// ref to Bar via the explicit_delegation → user_type path.
func TestKotlinGrammar_ExplicitDelegation(t *testing.T) {
	src := []byte(`package com.example

import com.example.base.Bar

class Impl(private val delegate: Bar) : Bar by delegate
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("Impl.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefsBySource(doc, "com.example.Impl")
	if len(refs) != 1 {
		t.Fatalf("expected 1 inherits ref (via explicit_delegation), got %d (%+v)", len(refs), refs)
	}
	if refs[0].Target != "com.example.base.Bar" {
		t.Errorf("Target = %q, want com.example.base.Bar", refs[0].Target)
	}
	if refs[0].Metadata["relation"] != "implements" {
		t.Errorf("relation = %v, want implements (bare user_type → implements)", refs[0].Metadata["relation"])
	}
}

// Expanded modifier coverage from lib-gyh: the allow-list in kotlinLabelModifiers
// surfaces the full set as labels. One fixture, many assertions.
func TestKotlinGrammar_ExpandedModifierLabels(t *testing.T) {
	src := []byte(`package m

abstract class Service {
    abstract val name: String
    open fun describe() {}
    inline fun <reified T> cast(x: Any): T = x as T
    tailrec fun sum(n: Int, acc: Int = 0): Int = if (n == 0) acc else sum(n - 1, acc + n)
    operator fun plus(other: Int) = this
    infix fun combine(other: Service): Service = this
    external fun nativeCall()
    suspend fun loadAsync(): Int = 0
    fun apply(block: (Int) -> Unit, noinline cb: () -> Unit, crossinline tap: () -> Unit) {}
}

const val MAX = 42

class Inner {
    inner class Nested
    lateinit var later: String
}

annotation class Audited

value class UserId(val raw: String)

expect class PlatformCommon
actual class PlatformJvm
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("modifiers.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// hasLabel searches for Kind="label" Value=label on the Unit with
	// the given Title (first match wins). Returns false when the Unit
	// is missing or the label isn't on it.
	hasLabel := func(title, label string) bool {
		u := findUnit(doc, title)
		if u == nil {
			return false
		}
		return hasSignal(u.Signals, "label", label)
	}

	cases := []struct {
		title, label string
	}{
		{"Service", "abstract"},
		{"describe", "open"},
		{"cast", "inline"},
		{"cast", "reified"}, // T is reified — reification_modifier
		{"sum", "tailrec"},
		{"plus", "operator"},
		{"combine", "infix"},
		{"nativeCall", "external"},
		{"loadAsync", "suspend"},
		{"MAX", "const"},
		{"Nested", "inner"},
		{"later", "lateinit"},
		{"Audited", "annotation"},
		{"UserId", "value"},
		{"PlatformCommon", "expect"},
		{"PlatformJvm", "actual"},
	}
	for _, c := range cases {
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

// A companion object carries the `companion` label — proves the
// class_modifier allow-list handles the `companion` keyword specifically.
// Round-3 review found this was only implicitly tested (the main fixture
// has a companion object but no test asserted the label).
func TestKotlinGrammar_CompanionObjectCarriesLabel(t *testing.T) {
	src := []byte(`package m
class Foo {
    companion object {
        const val X = 1
    }
}
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("foo.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := findUnit(doc, "Companion")
	if c == nil {
		t.Fatal("Companion Unit missing — unnamed companion object should default to Title=\"Companion\"")
	}
	if c.Kind != "object" {
		t.Errorf("Companion Kind = %q, want \"object\"", c.Kind)
	}
	if !hasSignal(c.Signals, "label", "companion") {
		t.Errorf("Companion should carry `companion` label; got %+v", c.Signals)
	}
}

// Primary-constructor `val`/`var` parameters emit as property Units;
// plain parameters (no val/var) do NOT — they're just ctor args.
func TestKotlinGrammar_PrimaryConstructorValParamsAreProperties(t *testing.T) {
	src := []byte(`package m
data class User(val id: String, var name: String, plain: Int)
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("user.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// val id and var name should both be property Units.
	for _, title := range []string{"id", "name"} {
		u := findUnit(doc, title)
		if u == nil {
			t.Errorf("expected property Unit %q from primary-constructor val/var", title)
			continue
		}
		if u.Kind != "property" {
			t.Errorf("%s Kind = %q, want \"property\"", title, u.Kind)
		}
	}
	// `plain: Int` has no binding_pattern_kind → should NOT emit a Unit.
	if u := findUnit(doc, "plain"); u != nil {
		t.Errorf("plain ctor-only parameter should not emit a property Unit; got %+v", u)
	}
}

// Regression: the reified walker in SymbolExtraSignals must be gated to
// function_declaration nodes. Previously it ran on every class-family
// symbol, descending the class_body into member functions and leaking
// their `reified` type-parameter modifier up to the enclosing class's
// Signals. The fixture intentionally pairs a class with a reified-T
// method to prove the class itself does NOT carry the reified label.
func TestKotlinGrammar_ReifiedDoesNotLeakToEnclosingClass(t *testing.T) {
	src := []byte(`package m
abstract class Service {
    inline fun <reified T> cast(x: Any): T = x as T
}
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("svc.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := findUnit(doc, "Service")
	if svc == nil {
		t.Fatal("Service Unit missing")
	}
	if hasSignal(svc.Signals, "label", "reified") {
		t.Errorf("class Service should not carry `reified` label; got %+v", svc.Signals)
	}
	// Sanity: the cast function IS reified.
	cast := findUnit(doc, "cast")
	if cast == nil {
		t.Fatal("cast Unit missing")
	}
	if !hasSignal(cast.Signals, "label", "reified") {
		t.Errorf("cast should carry `reified` label; got %+v", cast.Signals)
	}
}

// expect / actual (Kotlin Multiplatform): two declarations of the same
// class name, one with `expect`, one with `actual`. Both emit label signals
// of the corresponding keyword.
func TestKotlinGrammar_MultiplatformExpectActual(t *testing.T) {
	src := []byte(`package m

expect class Platform {
    fun name(): String
}
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("common.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := findUnit(doc, "Platform")
	if p == nil {
		t.Fatal("Platform Unit missing")
	}
	if !hasSignal(p.Signals, "label", "expect") {
		t.Errorf("expected 'expect' label; got %+v", p.Signals)
	}
}

// fun interface + context receivers are tree-sitter-kotlin upstream gaps
// (see kotlin.go's "Known tree-sitter-kotlin gaps" block). No tests for
// either; they'd fail against the current grammar parser regardless of
// what librarian does. Follow-ups tracked separately.

// --- lib-23w: extension-function receiver metadata ---

// Extension functions on a named type: `fun String.toSlug()` lands with
// Metadata["receiver"]="String".
func TestKotlinGrammar_ExtensionReceiverMetadata_SimpleType(t *testing.T) {
	src := []byte(`package m
fun String.toSlug(): String = this.lowercase()
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("ext.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "toSlug")
	if u == nil {
		t.Fatal("toSlug Unit missing")
	}
	if u.Metadata == nil {
		t.Fatalf("expected Metadata with \"receiver\" key; got nil")
	}
	if got, _ := u.Metadata["receiver"].(string); got != "String" {
		t.Errorf("receiver = %q, want \"String\"", got)
	}
}

// Generic receiver: `fun List<Int>.sum()` strips to receiver="List".
// type_arguments drop so "all extensions of List" matches both
// `fun List<Int>.` and `fun List<String>.`.
func TestKotlinGrammar_ExtensionReceiverMetadata_GenericStripped(t *testing.T) {
	src := []byte(`package m
fun List<Int>.sum(): Int = 0
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("sum.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "sum")
	if u == nil {
		t.Fatal("sum Unit missing")
	}
	if got, _ := u.Metadata["receiver"].(string); got != "List" {
		t.Errorf("receiver = %q, want \"List\" (generics stripped)", got)
	}
}

// Non-extension function: plain `fun ordinary()` gets NO receiver key.
// Unit.Metadata stays nil (no spurious metadata allocation when the
// grammar has nothing to contribute).
func TestKotlinGrammar_NonExtensionFunctionHasNoReceiver(t *testing.T) {
	src := []byte(`package m
fun ordinary(): Int = 42
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("ord.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "ordinary")
	if u == nil {
		t.Fatal("ordinary Unit missing")
	}
	if u.Metadata != nil {
		if _, has := u.Metadata["receiver"]; has {
			t.Errorf("non-extension function should not carry a receiver: %+v", u.Metadata)
		}
	}
}

// Nullable type-parameter receiver: `fun <T> T?.orDefault()` — T is
// both a type parameter and the nullable receiver base. Should land
// Metadata["receiver"]="T" (nullable marker stripped, type-parameter
// identifier preserved verbatim).
func TestKotlinGrammar_ExtensionReceiverMetadata_NullableTypeParam(t *testing.T) {
	src := []byte(`package m
fun <T> T?.orDefault(default: T): T = this ?: default
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("tp.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "orDefault")
	if u == nil {
		t.Fatal("orDefault Unit missing")
	}
	if got, _ := u.Metadata["receiver"].(string); got != "T" {
		t.Errorf("receiver = %q, want \"T\"", got)
	}
}

// Nullable receiver: `fun String?.safeLowercase()` lands with
// Metadata["receiver"]="String" — the nullable marker is stripped so
// "all extensions of String" matches both `String` and `String?`
// receivers without the caller having to normalise.
func TestKotlinGrammar_ExtensionReceiverMetadata_NullableStripped(t *testing.T) {
	src := []byte(`package m
fun String?.safeLowercase(): String = this?.lowercase() ?: ""
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("nullable.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "safeLowercase")
	if u == nil {
		t.Fatal("safeLowercase Unit missing")
	}
	if got, _ := u.Metadata["receiver"].(string); got != "String" {
		t.Errorf("receiver = %q, want \"String\" (nullable marker stripped)", got)
	}
}

// Dotted-receiver extension: `fun pkg.Type.method()`. kotlinUserTypeName
// returns the dotted form for a fully-qualified receiver.
func TestKotlinGrammar_ExtensionReceiverMetadata_Dotted(t *testing.T) {
	src := []byte(`package m
fun foo.bar.Baz.ext(): Int = 0
`)
	h := code.New(code.NewKotlinGrammar())
	doc, err := h.Parse("dot.kt", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findUnit(doc, "ext")
	if u == nil {
		t.Fatal("ext Unit missing")
	}
	if got, _ := u.Metadata["receiver"].(string); got != "foo.bar.Baz" {
		t.Errorf("receiver = %q, want \"foo.bar.Baz\"", got)
	}
}
