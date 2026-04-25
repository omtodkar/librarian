package indexer

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

// gitignoreMatcher evaluates paths against every .gitignore file discovered
// under a root directory, mirroring git's layered semantics: a pattern in
// `a/b/.gitignore` applies only to paths at or below `a/b/`, patterns are
// evaluated in document order across the tree (shallowest-first), and a
// deeper `!pattern` negation can un-ignore something a shallower file
// ignored.
//
// Rather than maintain a separate sabhiram matcher per file (which can't
// correctly compose negations across files — a negation in a nested file
// can't distinguish "explicit un-ignore" from "silent about this path"),
// loadGitignores translates every nested pattern to root-scoped form and
// feeds them to one sabhiram matcher in shallowest-first order. That one
// matcher then evaluates patterns sequentially, which is exactly how git
// resolves layered .gitignore semantics.
type gitignoreMatcher struct {
	matcher *ignore.GitIgnore
}

// loadGitignores walks rootDir once, collects every .gitignore, translates
// their patterns to root-scoped form, and compiles a single matcher.
// Heavyweight / plumbing directories (.git, node_modules, vendor) are pruned
// from the pre-walk so we don't load thousands of dependency .gitignore files
// the graph walker will never visit.
//
// Symlinked directories are not followed (filepath.WalkDir's documented
// behaviour), so symlink loops are not a risk. Permission errors on opaque
// subdirs are tolerated.
func loadGitignores(rootDir string) (*gitignoreMatcher, error) {
	type gitignoreFile struct {
		baseDir string // relative to rootDir, slash-separated, no leading/trailing slash
		lines   []string
	}
	var files []gitignoreFile

	err := filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			// Prune heavyweight / plumbing directories — their .gitignore
			// files aren't relevant to a graph pass that already skips
			// them. Reuses the walker's hard-exclude and default-dir
			// maps so any additions propagate here automatically.
			name := d.Name()
			if hardGraphExcludes[name] || defaultGraphExcludeDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != ".gitignore" {
			return nil
		}

		lines, err := readGitignoreLines(path)
		if err != nil {
			return fmt.Errorf("reading gitignore at %s: %w", path, err)
		}

		baseDir := filepath.Dir(path)
		rel, err := filepath.Rel(rootDir, baseDir)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}

		files = append(files, gitignoreFile{baseDir: rel, lines: lines})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading gitignores: %w", err)
	}

	// Shallowest-first so sabhiram evaluates ancestor patterns before deeper
	// overrides — last rule that matches wins, which is git's semantic.
	sort.SliceStable(files, func(i, j int) bool {
		return depth(files[i].baseDir) < depth(files[j].baseDir)
	})

	var translated []string
	for _, f := range files {
		for _, line := range f.lines {
			if t := translateGitignoreLine(line, f.baseDir); t != "" {
				translated = append(translated, t)
			}
		}
	}

	return &gitignoreMatcher{matcher: ignore.CompileIgnoreLines(translated...)}, nil
}

// Matches reports whether relPath (relative to the matcher's root, any
// separator) is ignored by the composed gitignore ruleset. Nil receiver
// reports false so callers can skip `loadGitignores` when HonorGitignore
// is off without branching at the call site.
func (m *gitignoreMatcher) Matches(relPath string) bool {
	if m == nil || m.matcher == nil {
		return false
	}
	return m.matcher.MatchesPath(filepath.ToSlash(relPath))
}

// readGitignoreLines reads a .gitignore file into a slice of raw lines,
// preserving order and discarding trailing CR / whitespace. Comments and
// blanks are returned as-is — translation handles skipping them.
func readGitignoreLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, strings.TrimRight(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// translateGitignoreLine rewrites one .gitignore pattern so it can be
// evaluated in the root matcher with the same effect as if the original file
// were evaluated in place. Returns "" for blanks and comments.
//
// Translation rules (baseDir is the .gitignore file's directory, relative to
// root, no leading/trailing slash, forward slashes):
//
//	baseDir == ""  → pattern passes through unchanged (root .gitignore).
//	baseDir != ""  → the pattern is prefixed to restrict its scope:
//	    /pattern         → /baseDir/pattern       (rooted to baseDir)
//	    sub/foo (slash)  → baseDir/sub/foo        (rooted relative to baseDir)
//	    foo    (no slash)→ baseDir/** /foo        (matches at any depth below baseDir)
//	    !pattern         → ! applied to translated non-negated form
//
// Trailing slash (foo/) is a directory marker, not a rooted indicator, and is
// preserved on the translated pattern.
func translateGitignoreLine(line, baseDir string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	if baseDir == "" {
		return trimmed
	}

	// `\!foo` and `\#foo` are gitignore's escape for literal ! / # at the
	// start of a pattern. The `\` escape is meaningful ONLY at position 0
	// of the pattern sabhiram sees; once we prepend `baseDir/`, the `\` is
	// mid-pattern and sabhiram doesn't interpret it. Fortunately after
	// the prefix, the literal `!` / `#` isn't at position 0 either —
	// sabhiram won't confuse them with negation / comment syntax — so
	// dropping the `\` yields the correct match.
	//
	// This branch must short-circuit (not fall through) because the main
	// path would re-interpret the now-bare `!trap` as a negation and emit
	// `!baseDir/**/trap`, which matches completely different files.
	//
	// Narrow case (files named literal `!foo` or `#foo` in a nested
	// directory with their own .gitignore).
	if strings.HasPrefix(trimmed, "\\!") || strings.HasPrefix(trimmed, "\\#") {
		literal := trimmed[1:] // "!trap" or "#trap"
		stripped := strings.TrimSuffix(literal, "/")
		if strings.Contains(stripped, "/") {
			return baseDir + "/" + literal
		}
		return baseDir + "/**/" + literal
	}

	negated := strings.HasPrefix(trimmed, "!")
	if negated {
		trimmed = trimmed[1:]
	}
	rooted := strings.HasPrefix(trimmed, "/")
	if rooted {
		trimmed = trimmed[1:]
	}

	// A pattern with a slash (other than a trailing directory marker) is
	// rooted relative to the .gitignore's directory. A pattern without any
	// slash applies at every depth below that directory — git's default.
	stripped := strings.TrimSuffix(trimmed, "/")
	hasInternalSlash := strings.Contains(stripped, "/")

	var result string
	if rooted || hasInternalSlash {
		result = baseDir + "/" + trimmed
	} else {
		result = baseDir + "/**/" + trimmed
	}
	if negated {
		result = "!" + result
	}
	return result
}

// depth returns the number of "/"-separated segments in a slash-normalised
// path, used to sort gitignore files shallowest-first. Empty string → 0.
func depth(relDir string) int {
	if relDir == "" {
		return 0
	}
	return strings.Count(relDir, "/") + 1
}
