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

