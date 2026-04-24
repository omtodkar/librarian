package indexer

import (
	"path/filepath"
	"regexp"
	"strings"
)

type CodeReference struct {
	FilePath string
	Language string
	RefType  string // "file", "directory", or "pattern"
	Context  string
}

var codeFileRegex = regexp.MustCompile(
	"(?:^|[\\s`\"'(])([a-zA-Z0-9_/.-]+\\.(?:go|ts|tsx|js|jsx|py|rs|java|rb|c|cpp|h|hpp|cs|swift|kt|scala|sh|bash|zsh|yaml|yml|toml|json))(?:$|[\\s`\"')\\]:,])",
)

// dirRegex matches directory paths ending with / that have at least two segments (e.g., cmd/, internal/config/)
var dirRegex = regexp.MustCompile(
	`(?:^|[\s` + "`" + `"'(])([a-zA-Z0-9_][a-zA-Z0-9_/.-]*/)(?:$|[\s` + "`" + `"')\]:,])`,
)

// globRegex matches glob patterns containing * (e.g., *.go, **/*.ts, internal/auth/*.go)
var globRegex = regexp.MustCompile(
	`(?:^|[\s` + "`" + `"'(])([a-zA-Z0-9_*/.{,}-]*\*[a-zA-Z0-9_*/.{,}-]*)(?:$|[\s` + "`" + `"')\]:,])`,
)

func ExtractCodeReferences(content string, patterns []string) []CodeReference {
	var refs []CodeReference
	seen := make(map[string]bool)

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		// Extract concrete file references
		matches := codeFileRegex.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			filePath := match[1]

			if seen[filePath] {
				continue
			}

			if !matchesPatterns(filePath, patterns) {
				continue
			}

			seen[filePath] = true
			refs = append(refs, CodeReference{
				FilePath: filePath,
				Language: languageFromExt(filepath.Ext(filePath)),
				RefType:  "file",
				Context:  strings.TrimSpace(line),
			})
		}

		// Extract directory references
		dirMatches := dirRegex.FindAllStringSubmatch(line, -1)
		for _, match := range dirMatches {
			if len(match) < 2 {
				continue
			}
			dirPath := match[1]

			if seen[dirPath] {
				continue
			}

			// Skip URLs
			if strings.Contains(line, "http://"+dirPath) || strings.Contains(line, "https://"+dirPath) {
				continue
			}

			// Skip single-segment dirs (too ambiguous, e.g., "the/")
			// A multi-segment dir has at least one / before the trailing /
			trimmed := strings.TrimSuffix(dirPath, "/")
			if !strings.Contains(trimmed, "/") && len(trimmed) < 3 {
				continue
			}

			seen[dirPath] = true
			refs = append(refs, CodeReference{
				FilePath: dirPath,
				Language: "",
				RefType:  "directory",
				Context:  strings.TrimSpace(line),
			})
		}

		// Extract glob patterns
		globMatches := globRegex.FindAllStringSubmatch(line, -1)
		for _, match := range globMatches {
			if len(match) < 2 {
				continue
			}
			pattern := match[1]

			if seen[pattern] {
				continue
			}

			// Reject markdown emphasis syntax (`**bold**`, `*italic*`,
			// `**Foo/Bar**`) that looks like a glob to the regex because
			// of the asterisks. Legit globs starting with `**` continue
			// with `/` (e.g. `**/*.go`); anything else after the leading
			// asterisks is formatting, not a path.
			if isMarkdownEmphasis(pattern) {
				continue
			}

			// Require a known extension or a path separator so bare `*`
			// and single-segment fragments don't land as references.
			if !strings.Contains(pattern, "/") && !matchesPatterns(pattern, patterns) {
				continue
			}

			seen[pattern] = true
			refs = append(refs, CodeReference{
				FilePath: pattern,
				Language: "",
				RefType:  "pattern",
				Context:  strings.TrimSpace(line),
			})
		}
	}

	return refs
}

// isMarkdownEmphasis reports whether a glob-regex match is really markdown
// bold/italic formatting rather than a path pattern. The glob regex allows
// `*` in both halves of its capture, so `**word**` and `*word*` look like
// globs. Legit globs starting with `**` continue with `/` (`**/*.go`); any
// other character immediately after the leading asterisks means the
// asterisks are formatting runes. `*foo*` (italic) is caught the same way.
func isMarkdownEmphasis(pattern string) bool {
	switch {
	case strings.HasPrefix(pattern, "**"):
		// `**` alone, or `**word…` where the next char isn't `/`.
		return len(pattern) < 3 || pattern[2] != '/'
	case strings.HasPrefix(pattern, "*") && len(pattern) > 1:
		// `*x` where x is a letter → italic prefix, not a glob.
		// `*.go`, `*{a,b}`, `**/` are fine; a letter/digit right after
		// the single `*` is the italic case.
		c := pattern[1]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

func matchesPatterns(filePath string, patterns []string) bool {
	ext := filepath.Ext(filePath)
	for _, pattern := range patterns {
		patExt := filepath.Ext(pattern)
		if patExt == ext {
			return true
		}
	}
	return false
}

func languageFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx":
		return "cpp"
	case ".h", ".hpp":
		return "c/cpp"
	case ".cs":
		return "csharp"
	case ".swift":
		return "swift"
	case ".kt":
		return "kotlin"
	case ".scala":
		return "scala"
	case ".sh", ".bash", ".zsh":
		return "shell"
	default:
		return "unknown"
	}
}
