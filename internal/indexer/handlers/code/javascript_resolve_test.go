package code

import (
	"path/filepath"
	"testing"

	"librarian/internal/indexer"
)

// fakeFS is a closure-returning helper so resolveJSImport tests stay pure —
// no real filesystem writes, deterministic across platforms. Paths in the
// map are canonicalised via filepath.Clean at lookup time to survive the
// same cleaning resolveJSImport applies.
func fakeFS(existing ...string) func(string) bool {
	have := map[string]bool{}
	for _, p := range existing {
		have[filepath.Clean(p)] = true
	}
	return func(p string) bool {
		return have[filepath.Clean(p)]
	}
}

func TestResolveJSImport_RelativeBareResolvesToTS(t *testing.T) {
	got, ok := resolveJSImport("./utils", "/proj/src/a.ts", fakeFS("/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v), want (/proj/src/utils.ts, true)", got, ok)
	}
}

func TestResolveJSImport_PrefersTSOverJS(t *testing.T) {
	got, ok := resolveJSImport("./utils", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.js", "/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v); .ts should win over .js", got, ok)
	}
}

func TestResolveJSImport_TSXFallback(t *testing.T) {
	got, ok := resolveJSImport("./Button", "/proj/src/a.tsx",
		fakeFS("/proj/src/Button.tsx"))
	if !ok || got != "/proj/src/Button.tsx" {
		t.Errorf("got (%q, %v), want /proj/src/Button.tsx", got, ok)
	}
}

func TestResolveJSImport_IndexResolution(t *testing.T) {
	got, ok := resolveJSImport("./utils", "/proj/src/a.ts",
		fakeFS("/proj/src/utils/index.ts"))
	if !ok || got != "/proj/src/utils/index.ts" {
		t.Errorf("got (%q, %v), want directory index resolution", got, ok)
	}
}

// TestResolveJSImport_ExplicitJSRewritesToTS pins the NodeNext rewrite:
// `import x from "./utils.js"` in a TS project where only `utils.ts` exists
// on disk resolves to the TS source, not the nonexistent .js file.
func TestResolveJSImport_ExplicitJSRewritesToTS(t *testing.T) {
	got, ok := resolveJSImport("./utils.js", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.ts"))
	if !ok || got != "/proj/src/utils.ts" {
		t.Errorf("got (%q, %v); .js-suffixed import should rewrite to .ts when only .ts exists", got, ok)
	}
}

func TestResolveJSImport_ExplicitJSStaysJSWhenNoTS(t *testing.T) {
	got, ok := resolveJSImport("./utils.js", "/proj/src/a.js",
		fakeFS("/proj/src/utils.js"))
	if !ok || got != "/proj/src/utils.js" {
		t.Errorf("got (%q, %v); pure-JS project should resolve .js literally", got, ok)
	}
}

func TestResolveJSImport_ParentDirectory(t *testing.T) {
	got, ok := resolveJSImport("../lib/foo", "/proj/src/a.ts",
		fakeFS("/proj/lib/foo.tsx"))
	if !ok || got != "/proj/lib/foo.tsx" {
		t.Errorf("got (%q, %v), want /proj/lib/foo.tsx (parent-dir resolution)", got, ok)
	}
}

func TestResolveJSImport_NoCandidateReturnsFalse(t *testing.T) {
	_, ok := resolveJSImport("./missing", "/proj/src/a.ts", fakeFS())
	if ok {
		t.Error("expected no resolution for missing file")
	}
}

func TestResolveJSImport_MJSResolvesToMTS(t *testing.T) {
	got, ok := resolveJSImport("./utils.mjs", "/proj/src/a.ts",
		fakeFS("/proj/src/utils.mts"))
	if !ok || got != "/proj/src/utils.mts" {
		t.Errorf("got (%q, %v), want .mts rewrite for .mjs import", got, ok)
	}
}

// TestResolveJSImport_DirectoryCandidateRejectedByStatFn pins that the
// production statFn (statIsFile) filters out directories — if a bare
// `./utils` on disk is a directory, Tier 2 (append extension) and Tier 3
// (index fallback) should be responsible for picking the right file, not
// the directory itself. The fake statFn here simulates a directory by
// returning false for the exact candidate path but true for an index file.
func TestResolveJSImport_DirectoryCandidateRejectedByStatFn(t *testing.T) {
	// Fake: no ./utils.* file exists, but ./utils/ is a directory with
	// index.ts. The directory-as-file probe must fail; index.ts must win.
	stat := func(p string) bool {
		return p == "/proj/src/utils/index.ts"
	}
	got, ok := resolveJSImport("./utils", "/proj/src/a.ts", stat)
	if !ok || got != "/proj/src/utils/index.ts" {
		t.Errorf("got (%q, %v); directory candidate must not match as a file, index.ts should win", got, ok)
	}
}


func TestResolveJSImport_TSExplicitFallsBackToTSXSibling(t *testing.T) {
	got, ok := resolveJSImport("./Button.ts", "/proj/src/a.ts",
		fakeFS("/proj/src/Button.tsx"))
	if !ok || got != "/proj/src/Button.tsx" {
		t.Errorf("got (%q, %v); .ts-suffixed import should fall through to .tsx sibling", got, ok)
	}
}

// TestResolveImports_NoContextIsNoop verifies the backward-compat path: a
// ParseCtx with empty AbsPath / ProjectRoot returns refs untouched so the
// grammar-level Parse tests (which use an empty context) continue to see
// raw specifiers.
func TestResolveImports_NoContextIsNoop(t *testing.T) {
	g := &jsLikeGrammar{}
	in := []indexer.Reference{
		{Kind: "import", Target: "./utils"},
		{Kind: "import", Target: "lodash"},
	}
	out := g.ResolveImports(in, "a.ts", indexer.ParseContext{})
	for i := range in {
		if out[i].Target != in[i].Target {
			t.Errorf("ResolveImports mutated %q → %q without context", in[i].Target, out[i].Target)
		}
		if out[i].Metadata != nil {
			t.Errorf("ResolveImports added metadata without context: %+v", out[i].Metadata)
		}
	}
}

// TestTagNodeKind_DoesNotOverwrite pins the no-overwrite guarantee the
// godoc advertises: if a caller (or a future second resolver pass) has
// already stamped a node_kind, the second tagNodeKind call is a no-op.
// Prevents a category of regression where a later branch silently
// redirects edges that an earlier branch had already claimed.
func TestTagNodeKind_DoesNotOverwrite(t *testing.T) {
	ref := &indexer.Reference{
		Kind:     "import",
		Target:   "foo",
		Metadata: map[string]any{"node_kind": "code_file", "member": "x"},
	}
	tagNodeKind(ref, "external")
	if got := ref.Metadata["node_kind"]; got != "code_file" {
		t.Errorf("node_kind overwritten: got %q, want code_file", got)
	}
	if got := ref.Metadata["member"]; got != "x" {
		t.Errorf("unrelated metadata clobbered: got %v, want x", got)
	}
}

// TestResolveImports_BareSpecifierTaggedExternal pins that bare npm
// specifiers survive the resolver with their Target intact but pick up a
// node_kind=external tag so graphTargetID routes them to ext: nodes.
func TestResolveImports_BareSpecifierTaggedExternal(t *testing.T) {
	g := &jsLikeGrammar{}
	in := []indexer.Reference{
		{Kind: "import", Target: "lodash"},
		{Kind: "import", Target: "@scope/pkg"},
	}
	out := g.ResolveImports(in, "src/a.ts", indexer.ParseContext{
		AbsPath:     "/proj/src/a.ts",
		ProjectRoot: "/proj",
	})
	for i, r := range out {
		if r.Target != in[i].Target {
			t.Errorf("bare specifier mutated: %q → %q", in[i].Target, r.Target)
		}
		if r.Metadata == nil || r.Metadata["node_kind"] != "external" {
			t.Errorf("bare specifier %q missing external tag: %+v", r.Target, r.Metadata)
		}
	}
}
