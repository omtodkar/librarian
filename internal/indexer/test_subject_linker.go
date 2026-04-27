package indexer

import (
	"path/filepath"
	"strings"
)

// testSubjectLinker returns the known file paths that are likely the
// subject-under-test for testFilePath, based on path-naming conventions.
// Multiple candidates are returned when the heuristic produces more than one
// match; false positives are cheap (extra edges answer "possibly tests this"
// correctly). Returns nil when testFilePath doesn't match any supported test
// pattern.
//
// Supported conventions:
//   - Go:     foo_test.go         → foo.go (same dir)
//   - Python: test_foo.py         → foo.py (same dir)
//             tests/test_foo.py   → ../foo.py (one level up)
//             fooTests.py         → foo.py (same dir; skipped when name also
//             starts with test_, i.e. test_fooTests.py is handled by the
//             test_ branch only, avoiding a duplicate probe for test_foo.py)
//   - Java:   FooTest.java        → Foo.java (same dir)
//   - JS/TS:  foo.test.ts         → foo.ts (same dir)
//             foo.spec.ts         → foo.ts (same dir)
//             __tests__/foo.test.ts → ../foo.ts (one level up)
//             __tests__/auth.ts   → ../auth.ts (plain-named files inside
//             __tests__/ map one level up; no .test./.spec. infix required)
//
// knownPaths is the set of all indexed code-file paths against which
// candidate paths are validated. Only paths present in the set are returned.
func testSubjectLinker(testFilePath string, knownPaths map[string]struct{}) []string {
	dir := filepath.Dir(testFilePath)
	base := filepath.Base(testFilePath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	var out []string
	probe := func(relPath string) {
		cleaned := filepath.Clean(relPath)
		if _, ok := knownPaths[cleaned]; ok {
			out = append(out, cleaned)
		}
	}

	switch ext {
	case ".go":
		if strings.HasSuffix(stem, "_test") {
			probe(filepath.Join(dir, strings.TrimSuffix(stem, "_test")+".go"))
		}

	case ".py":
		if strings.HasPrefix(stem, "test_") {
			subject := strings.TrimPrefix(stem, "test_") + ".py"
			probe(filepath.Join(dir, subject))
			// tests/ or test/ directories: also check one level up.
			parentBase := filepath.Base(dir)
			if parentBase == "tests" || parentBase == "test" {
				probe(filepath.Join(filepath.Dir(dir), subject))
			}
		}
		// fooTests.py → foo.py. Guard: skip when the stem also starts with
		// "test_" — those names (e.g. test_authTests.py) are already handled
		// by the branch above, and the "Tests" suffix refers to the test
		// prefix pattern, not a standalone capitalized suffix.
		if strings.HasSuffix(stem, "Tests") && !strings.HasPrefix(stem, "test_") {
			probe(filepath.Join(dir, strings.TrimSuffix(stem, "Tests")+".py"))
		}

	case ".java":
		if strings.HasSuffix(stem, "Test") {
			probe(filepath.Join(dir, strings.TrimSuffix(stem, "Test")+".java"))
		}

	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".mjs", ".cts", ".cjs":
		subjectStem, matched := tsTestStem(stem)
		if matched {
			for _, se := range tsSubjectExts(ext) {
				probe(filepath.Join(dir, subjectStem+se))
			}
			// __tests__/ directory: also check one level up.
			if filepath.Base(dir) == "__tests__" {
				for _, se := range tsSubjectExts(ext) {
					probe(filepath.Join(filepath.Dir(dir), subjectStem+se))
				}
			}
		}
		// Files directly inside __tests__/ without a .test./.spec. infix
		// map to the same-named file one level up
		// (e.g. __tests__/auth.ts → ../auth.ts). Covers test suites that
		// place plain-named files inside __tests__/.
		if !matched && filepath.Base(dir) == "__tests__" {
			for _, se := range tsSubjectExts(ext) {
				probe(filepath.Join(filepath.Dir(dir), stem+se))
			}
		}
	}

	return out
}

// tsTestStem strips a ".test" or ".spec" infix from a JS/TS file stem (the
// part before the final extension). Returns ("", false) when neither suffix
// is found. Checks the exact tail so "jest.setup.test" → "jest.setup",
// not "jest".
func tsTestStem(stem string) (string, bool) {
	for _, infix := range []string{".test", ".spec"} {
		if strings.HasSuffix(stem, infix) {
			return strings.TrimSuffix(stem, infix), true
		}
	}
	return "", false
}

// tsSubjectExts returns the ordered list of candidate extensions to try for a
// JS/TS subject file. The test file's own extension is tried first (highest
// likelihood), then the natural partner (.ts↔.tsx, .js↔.jsx).
func tsSubjectExts(testExt string) []string {
	switch testExt {
	case ".ts":
		return []string{".ts", ".tsx"}
	case ".tsx":
		return []string{".tsx", ".ts"}
	case ".js":
		return []string{".js", ".jsx"}
	case ".jsx":
		return []string{".jsx", ".js"}
	default:
		return []string{testExt}
	}
}
