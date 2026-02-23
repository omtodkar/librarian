package indexer

import (
	"path/filepath"
	"regexp"
	"strings"
)

type CodeReference struct {
	FilePath string
	Language string
	Context  string
}

var codeFileRegex = regexp.MustCompile(
	"(?:^|[\\s`\"'(])([a-zA-Z0-9_/.-]+\\.(?:go|ts|tsx|js|jsx|py|rs|java|rb|c|cpp|h|hpp|cs|swift|kt|scala|sh|bash|zsh|yaml|yml|toml|json))(?:$|[\\s`\"')\\]:,])",
)

func ExtractCodeReferences(content string, patterns []string) []CodeReference {
	var refs []CodeReference
	seen := make(map[string]bool)

	lines := strings.Split(content, "\n")
	for _, line := range lines {
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
				Context:  strings.TrimSpace(line),
			})
		}
	}

	return refs
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
