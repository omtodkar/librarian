package code_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const javaSample = `package com.example.auth;

import java.util.List;
import java.util.Map;
import static java.util.Collections.emptyList;
import java.util.ArrayList;
import java.io.*;

/**
 * Service handles authentication.
 * TODO: rate-limit failures.
 */
@Service
@Transactional(readOnly = true)
public class AuthService extends BaseService implements Authenticator {
    private final List<String> users;
    private int attempts, failures;

    /** Create a new service. */
    public AuthService() {
        this.users = new ArrayList<>();
    }

    /**
     * Validate the credentials.
     * @param user the user
     */
    @Deprecated
    public boolean validate(String user) {
        // FIXME: quadratic
        return users.contains(user);
    }

    private static class Helper {
        void inner() {}
    }
}

interface Authenticator {
    boolean authenticate();
}

enum Status { ACTIVE, INACTIVE }

record Pair(String a, int b) {}
`

func TestJavaGrammar_ParseExtractsTypesMethodsFields(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("src/main/java/com/example/auth/AuthService.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if doc.Format != "java" {
		t.Errorf("Format = %q, want %q", doc.Format, "java")
	}
	if doc.Title != "com.example.auth" {
		t.Errorf("Title = %q, want %q (dotted package)", doc.Title, "com.example.auth")
	}

	// Key by (title, kind) pairs because some titles repeat across kinds —
	// e.g. AuthService appears as both a class AND a constructor.
	want := []struct{ title, kind string }{
		{"AuthService", "class"},
		{"AuthService", "constructor"},
		{"Authenticator", "interface"},
		{"Status", "enum"},
		{"Pair", "record"},
		{"validate", "method"},
		{"authenticate", "method"},
		{"Helper", "class"},
		{"inner", "method"},
		{"users", "field"},
		{"attempts", "field"}, // first declarator of multi-var `attempts, failures`
	}
	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range want {
		if !got[tk{w.title, w.kind}] {
			t.Errorf("missing Unit {title=%q, kind=%q} (have: %v)", w.title, w.kind, got)
		}
	}
}

// Constructor gets its own Kind and the Unit.Path surfaces the
// class.Class pattern so filters can find init-style code.
func TestJavaGrammar_ConstructorKind(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("AuthService.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
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
	if ctor.Title != "AuthService" {
		t.Errorf("constructor Title = %q, want %q", ctor.Title, "AuthService")
	}
	// Constructor's Path is package.Class.Class because Java constructors
	// carry the class name. The two Units (class + constructor) with
	// overlapping Paths are disambiguated by Kind.
	if !strings.HasSuffix(ctor.Path, ".AuthService.AuthService") {
		t.Errorf("constructor Path = %q, want suffix .AuthService.AuthService", ctor.Path)
	}
}

// Nested types produce fully-qualified paths: Outer.Inner.method.
func TestJavaGrammar_NestedClassPath(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("AuthService.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var inner *indexer.Unit
	for i := range doc.Units {
		if doc.Units[i].Title == "inner" {
			inner = &doc.Units[i]
			break
		}
	}
	if inner == nil {
		t.Fatal("inner method Unit missing")
	}
	want := "com.example.auth.AuthService.Helper.inner"
	if inner.Path != want {
		t.Errorf("inner Path = %q, want %q", inner.Path, want)
	}
}

func TestJavaGrammar_Imports(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byTarget := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byTarget[r.Target] = r
		}
	}

	// Plain, static, wildcard — all three forms must surface.
	for _, want := range []string{
		"java.util.List",
		"java.util.Map",
		"java.util.ArrayList",
		"java.util.Collections.emptyList",
		"java.io.*",
	} {
		if _, ok := byTarget[want]; !ok {
			t.Errorf("missing import %q (got %v)", want, byTarget)
		}
	}

	// Static flag survives the import_declaration traversal.
	if r, ok := byTarget["java.util.Collections.emptyList"]; !ok {
		t.Error("static import missing")
	} else if r.Metadata == nil || r.Metadata["static"] != true {
		t.Errorf("expected Static=true metadata on static import, got %+v", r.Metadata)
	}

	// Non-static imports must NOT carry Static=true metadata (guards the
	// Metadata plumbing against regression once other grammars also use it).
	for _, t2 := range []string{"java.util.List", "java.io.*"} {
		r := byTarget[t2]
		if r.Metadata != nil && r.Metadata["static"] == true {
			t.Errorf("%q should not be static: %+v", t2, r.Metadata)
		}
	}
}

// Annotations surface as Signal{Kind:"annotation"} on the target Unit.
// @Deprecated on validate, @Service + @Transactional on AuthService.
func TestJavaGrammar_AnnotationsEmitSignals(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	validate := findUnit(doc, "validate")
	if validate == nil {
		t.Fatal("validate Unit missing")
	}
	if !hasSignal(validate.Signals, "annotation", "Deprecated") {
		t.Errorf("expected @Deprecated annotation on validate; got %+v", validate.Signals)
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
}

// Annotation signals must survive into the chunk's SignalMeta JSON so
// downstream re-rankers can boost on them. SignalsToJSON previously had no
// case for kind="annotation" and dropped them silently; this pins the fix.
func TestJavaGrammar_AnnotationsPersistedInChunkSignalMeta(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	// AuthService carries @Service / @Transactional on its class declaration.
	// Find the chunk for the class-level section and assert its SignalMeta
	// JSON contains an `annotations` array with both names.
	var found bool
	for _, c := range chunks {
		if !strings.Contains(c.SignalMeta, `"annotations"`) {
			continue
		}
		if strings.Contains(c.SignalMeta, `"Service"`) && strings.Contains(c.SignalMeta, `"Transactional"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected some chunk SignalMeta to contain `annotations` with Service + Transactional; got chunks with SignalMeta:")
		for _, c := range chunks {
			if c.SignalMeta != "{}" {
				t.Logf("  %q: %s", c.SectionHeading, c.SignalMeta)
			}
		}
	}
}

// Rationale signals (TODO, FIXME) still flow through: TODO in the class
// Javadoc, FIXME inside the validate body.
func TestJavaGrammar_RationaleSignals(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
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

// Javadoc comments (/** ... */) attach as docstrings via the shared
// preceding-comment buffer and stripCommentMarkers handling.
func TestJavaGrammar_JavadocAsDocstring(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	svc := findUnit(doc, "AuthService")
	if svc == nil {
		t.Fatal("AuthService Unit missing")
	}
	if !strings.Contains(svc.Content, "Service handles authentication.") {
		t.Errorf("Javadoc didn't flow into AuthService Content:\n%s", svc.Content)
	}

	val := findUnit(doc, "validate")
	if val == nil {
		t.Fatal("validate Unit missing")
	}
	if !strings.Contains(val.Content, "Validate the credentials.") {
		t.Errorf("Javadoc missing on validate:\n%s", val.Content)
	}
}

func TestJavaGrammar_RegisteredByDefault(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("Foo.java") == nil {
		t.Error(".java extension not registered")
	}
}

func TestJavaGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	code.AssertGrammarInvariants(t, h, "com/example/Auth.java", []byte(javaSample))
}

// Default-package file: no `package ...;` declaration. Title falls back to
// the file stem, and Unit.Path for a class named after the stem comes out
// as `Orphan.Orphan` — unambiguous but redundant. Silently collapsing the
// duplicate would break Go/Python files where a type happens to share its
// package/module name, so we accept the repetition as documented behaviour.
func TestJavaGrammar_DefaultPackageUsesStem(t *testing.T) {
	src := []byte("public class Orphan {\n    void x() {}\n}\n")
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Orphan.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Title != "Orphan" {
		t.Errorf("Title = %q, want %q (stem fallback)", doc.Title, "Orphan")
	}
	orphan := findUnit(doc, "Orphan")
	if orphan == nil {
		t.Fatal("Orphan Unit missing")
	}
	if orphan.Path != "Orphan.Orphan" {
		t.Errorf("Orphan Path = %q, want %q", orphan.Path, "Orphan.Orphan")
	}
	x := findUnit(doc, "x")
	if x == nil {
		t.Fatal("x Unit missing")
	}
	if x.Path != "Orphan.Orphan.x" {
		t.Errorf("x Path = %q, want %q", x.Path, "Orphan.Orphan.x")
	}
}

// --- lib-wji.1: inheritance extraction ---

// inheritsRefs filters a doc's References down to Kind="inherits" and indexes
// them by (Source, Target) for precise assertion in the presence of multiple
// parents per class.
func inheritsRefs(doc *indexer.ParsedDoc) map[[2]string]indexer.Reference {
	out := map[[2]string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "inherits" {
			out[[2]string{r.Source, r.Target}] = r
		}
	}
	return out
}

func TestJavaGrammar_ClassExtendsAndImplements(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("src/AuthService.java", []byte(javaSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)

	authService := "com.example.auth.AuthService"
	extendsBase, ok := refs[[2]string{authService, "BaseService"}]
	if !ok {
		t.Fatalf("expected inherits ref %s → BaseService; got %+v", authService, refs)
	}
	if extendsBase.Metadata["relation"] != "extends" {
		t.Errorf("extends relation = %v, want \"extends\"", extendsBase.Metadata["relation"])
	}

	implAuth, ok := refs[[2]string{authService, "Authenticator"}]
	if !ok {
		t.Fatalf("expected inherits ref %s → Authenticator; got %+v", authService, refs)
	}
	if implAuth.Metadata["relation"] != "implements" {
		t.Errorf("implements relation = %v, want \"implements\"", implAuth.Metadata["relation"])
	}
}

func TestJavaGrammar_SourceIsContainingSymbolPath(t *testing.T) {
	// The ref.Source field is what lets the graph pass anchor inherits edges
	// at sym:<Source> — if it drifts from the Unit.Path, edges point at
	// phantom nodes. AssertGrammarInvariants already enforces this, but the
	// dedicated test documents the contract.
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("t.java", []byte(javaSample))
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

func TestJavaGrammar_InterfaceExtendsMultiple(t *testing.T) {
	src := []byte(`package com.example;

interface Login { void login(); }
interface Logout { void logout(); }
interface Auth extends Login, Logout { boolean ok(); }
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Auth.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)
	auth := "com.example.Auth"
	for _, want := range []string{"Login", "Logout"} {
		r, ok := refs[[2]string{auth, want}]
		if !ok {
			t.Errorf("missing inherits ref %s → %s (got %+v)", auth, want, refs)
			continue
		}
		if r.Metadata["relation"] != "extends" {
			t.Errorf("%s → %s relation = %v, want \"extends\" (interface-extends-interface)", auth, want, r.Metadata["relation"])
		}
	}
}

func TestJavaGrammar_RecordAndEnumImplements(t *testing.T) {
	src := []byte(`package com.example;

import java.io.Serializable;

record Pair(String a, int b) implements Comparable, Serializable {}
enum Status implements Displayable { ACTIVE, INACTIVE; }
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Types.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)

	for _, want := range []string{"Comparable", "java.io.Serializable"} {
		r, ok := refs[[2]string{"com.example.Pair", want}]
		if !ok {
			t.Errorf("missing record implements %q (got %+v)", want, refs)
			continue
		}
		if r.Metadata["relation"] != "implements" {
			t.Errorf("record implements relation = %v", r.Metadata["relation"])
		}
	}
	if _, ok := refs[[2]string{"com.example.Status", "Displayable"}]; !ok {
		t.Errorf("missing enum implements Displayable (got %+v)", refs)
	}
}

func TestJavaGrammar_StripsGenericsAndCapturesTypeArgs(t *testing.T) {
	src := []byte(`package com.example;

import java.util.HashMap;

public class Cache<K, V> extends HashMap<K, V> {}
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Cache.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)
	r, ok := refs[[2]string{"com.example.Cache", "java.util.HashMap"}]
	if !ok {
		t.Fatalf("expected resolved HashMap parent (got %+v)", refs)
	}
	args, _ := r.Metadata["type_args"].([]string)
	if len(args) != 2 || args[0] != "K" || args[1] != "V" {
		t.Errorf("type_args = %v, want [K V]", args)
	}
}

// Same-file import lookup: `class X extends BaseService` + `import
// com.acme.BaseService;` → inherits target is the FQN, not the bare name.
func TestJavaGrammar_ResolvesBareNameViaSameFileImport(t *testing.T) {
	src := []byte(`package com.example;

import com.acme.auth.BaseService;

public class AuthService extends BaseService {}
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("AuthService.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)
	if _, ok := refs[[2]string{"com.example.AuthService", "com.acme.auth.BaseService"}]; !ok {
		t.Errorf("expected bare `BaseService` resolved to com.acme.auth.BaseService; got %+v", refs)
	}
	// And the raw "BaseService" Target must not survive.
	for key, r := range refs {
		if r.Source == "com.example.AuthService" && r.Target == "BaseService" {
			t.Errorf("unresolved bare ref should have been rewritten: %v", key)
		}
	}
}

// Wildcard + static imports do NOT provide a bare-name binding — bare
// parents with no matching single-class import land as unresolved.
func TestJavaGrammar_UnresolvedBareNameFlagged(t *testing.T) {
	src := []byte(`package com.example;

import com.acme.util.*;
import static java.util.Collections.emptyList;

public class Unknown extends MystificationBase {}
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Unknown.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)
	r, ok := refs[[2]string{"com.example.Unknown", "MystificationBase"}]
	if !ok {
		t.Fatalf("expected bare MystificationBase target (got %+v)", refs)
	}
	v, _ := r.Metadata["unresolved"].(bool)
	if !v {
		t.Errorf("expected unresolved=true on bare-target ref with no matching import; got metadata %+v", r.Metadata)
	}
}

// Explicit FQN in source: the Target already contains '.' so resolution is
// a no-op and `unresolved` must NOT be set.
func TestJavaGrammar_ExplicitFQNNotMarkedUnresolved(t *testing.T) {
	src := []byte(`package com.example;

public class Anchored extends com.other.Library.Base {}
`)
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Anchored.java", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	refs := inheritsRefs(doc)
	r, ok := refs[[2]string{"com.example.Anchored", "com.other.Library.Base"}]
	if !ok {
		t.Fatalf("expected explicit FQN target (got %+v)", refs)
	}
	if v, _ := r.Metadata["unresolved"].(bool); v {
		t.Errorf("explicit FQN should not be marked unresolved: %+v", r.Metadata)
	}
}

// Empty file must parse cleanly (no panic, no Units, no chunks).
func TestJavaGrammar_EmptyFile(t *testing.T) {
	h := code.New(code.NewJavaGrammar())
	doc, err := h.Parse("Empty.java", []byte{})
	if err != nil {
		t.Fatalf("Parse(empty): %v", err)
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

