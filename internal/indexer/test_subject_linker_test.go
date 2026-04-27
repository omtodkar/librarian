package indexer

import (
	"sort"
	"testing"
)

func mkKnown(paths ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		m[p] = struct{}{}
	}
	return m
}

func TestTestSubjectLinker_Go(t *testing.T) {
	tests := []struct {
		name     string
		testFile string
		known    map[string]struct{}
		want     []string
	}{
		{
			name:     "foo_test.go → foo.go same dir",
			testFile: "pkg/auth/auth_test.go",
			known:    mkKnown("pkg/auth/auth.go", "pkg/auth/auth_test.go"),
			want:     []string{"pkg/auth/auth.go"},
		},
		{
			name:     "top-level foo_test.go → foo.go",
			testFile: "main_test.go",
			known:    mkKnown("main.go", "main_test.go"),
			want:     []string{"main.go"},
		},
		{
			name:     "no match when subject absent",
			testFile: "pkg/foo_test.go",
			known:    mkKnown("pkg/foo_test.go", "pkg/bar.go"),
			want:     nil,
		},
		{
			name:     "ambiguity: foo_test.go with foo.go AND bar.go → only foo.go",
			testFile: "pkg/foo_test.go",
			known:    mkKnown("pkg/foo.go", "pkg/foo_test.go", "pkg/bar.go"),
			want:     []string{"pkg/foo.go"},
		},
		{
			name:     "non-test .go file yields nothing",
			testFile: "pkg/auth.go",
			known:    mkKnown("pkg/auth.go"),
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TestSubjectLinker(tc.testFile, tc.known)
			assertStringSlicesEqual(t, tc.want, got)
		})
	}
}

func TestTestSubjectLinker_Python(t *testing.T) {
	tests := []struct {
		name     string
		testFile string
		known    map[string]struct{}
		want     []string
	}{
		{
			name:     "test_foo.py → foo.py same dir",
			testFile: "mypkg/test_auth.py",
			known:    mkKnown("mypkg/auth.py", "mypkg/test_auth.py"),
			want:     []string{"mypkg/auth.py"},
		},
		{
			name:     "tests/test_foo.py → sibling of tests dir",
			testFile: "mypkg/tests/test_auth.py",
			known:    mkKnown("mypkg/auth.py", "mypkg/tests/test_auth.py"),
			want:     []string{"mypkg/auth.py"},
		},
		{
			name:     "test/test_foo.py → sibling of test dir",
			testFile: "src/test/test_utils.py",
			known:    mkKnown("src/utils.py"),
			want:     []string{"src/utils.py"},
		},
		{
			name:     "fooTests.py → foo.py",
			testFile: "mypkg/authTests.py",
			known:    mkKnown("mypkg/auth.py"),
			want:     []string{"mypkg/auth.py"},
		},
		{
			name:     "test_foo.py no match when subject absent",
			testFile: "mypkg/test_missing.py",
			known:    mkKnown("mypkg/other.py"),
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TestSubjectLinker(tc.testFile, tc.known)
			assertStringSlicesEqual(t, tc.want, got)
		})
	}
}

func TestTestSubjectLinker_Java(t *testing.T) {
	tests := []struct {
		name     string
		testFile string
		known    map[string]struct{}
		want     []string
	}{
		{
			name:     "FooTest.java → Foo.java same dir",
			testFile: "src/AuthServiceTest.java",
			known:    mkKnown("src/AuthService.java"),
			want:     []string{"src/AuthService.java"},
		},
		{
			name:     "nested package path",
			testFile: "src/com/example/UserRepositoryTest.java",
			known:    mkKnown("src/com/example/UserRepository.java"),
			want:     []string{"src/com/example/UserRepository.java"},
		},
		{
			name:     "no match when subject absent",
			testFile: "src/FooTest.java",
			known:    mkKnown("src/Bar.java"),
			want:     nil,
		},
		{
			name:     "non-Test suffix yields nothing",
			testFile: "src/AuthTests.java",
			known:    mkKnown("src/Auth.java"),
			want:     nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TestSubjectLinker(tc.testFile, tc.known)
			assertStringSlicesEqual(t, tc.want, got)
		})
	}
}

func TestTestSubjectLinker_JSTS(t *testing.T) {
	tests := []struct {
		name     string
		testFile string
		known    map[string]struct{}
		want     []string
	}{
		{
			name:     "foo.test.ts → foo.ts same dir",
			testFile: "src/auth.test.ts",
			known:    mkKnown("src/auth.ts"),
			want:     []string{"src/auth.ts"},
		},
		{
			name:     "foo.spec.ts → foo.ts same dir",
			testFile: "src/auth.spec.ts",
			known:    mkKnown("src/auth.ts"),
			want:     []string{"src/auth.ts"},
		},
		{
			name:     "foo.test.tsx → foo.tsx same dir",
			testFile: "components/Button.test.tsx",
			known:    mkKnown("components/Button.tsx"),
			want:     []string{"components/Button.tsx"},
		},
		{
			name:     "foo.test.js → foo.js same dir",
			testFile: "lib/utils.test.js",
			known:    mkKnown("lib/utils.js"),
			want:     []string{"lib/utils.js"},
		},
		{
			name:     "__tests__/foo.test.ts → ../foo.ts one level up",
			testFile: "src/__tests__/auth.test.ts",
			known:    mkKnown("src/auth.ts"),
			want:     []string{"src/auth.ts"},
		},
		{
			name:     "__tests__ prefers same dir candidate before parent",
			testFile: "src/__tests__/auth.test.ts",
			known:    mkKnown("src/__tests__/auth.ts", "src/auth.ts"),
			want:     []string{"src/__tests__/auth.ts", "src/auth.ts"},
		},
		{
			name:     "no match when subject absent",
			testFile: "src/missing.test.ts",
			known:    mkKnown("src/other.ts"),
			want:     nil,
		},
		{
			name:     "ts falls back to tsx partner",
			testFile: "src/Form.test.ts",
			known:    mkKnown("src/Form.tsx"),
			want:     []string{"src/Form.tsx"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TestSubjectLinker(tc.testFile, tc.known)
			assertStringSlicesEqual(t, tc.want, got)
		})
	}
}

func TestTestSubjectLinker_NonTestFile(t *testing.T) {
	known := mkKnown("src/auth.ts", "src/utils.go", "src/Foo.java", "src/foo.py")
	nonTestFiles := []string{
		"src/auth.ts",
		"src/utils.go",
		"src/Foo.java",
		"src/foo.py",
	}
	for _, f := range nonTestFiles {
		if got := TestSubjectLinker(f, known); got != nil {
			t.Errorf("TestSubjectLinker(%q): expected nil, got %v", f, got)
		}
	}
}

// assertStringSlicesEqual compares two slices after sorting, so test order is
// irrelevant while the set of elements is checked precisely.
func assertStringSlicesEqual(t *testing.T, want, got []string) {
	t.Helper()
	sortedWant := append([]string(nil), want...)
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedWant)
	sort.Strings(sortedGot)

	if len(sortedWant) != len(sortedGot) {
		t.Errorf("want %v, got %v", sortedWant, sortedGot)
		return
	}
	for i := range sortedWant {
		if sortedWant[i] != sortedGot[i] {
			t.Errorf("want %v, got %v", sortedWant, sortedGot)
			return
		}
	}
}
