package faq

import "strings"

// Source is a single question candidate extracted from git or bd history.
type Source struct {
	Kind   string // "git" or "issue"
	ID     string // short commit SHA or issue ID
	Text   string // the question text (commit subject or issue title)
	Detail string // commit body or issue close_reason / description
}

// isQuestionShaped reports whether text is question-shaped: starts with a
// question word (how/what/why/where/when) or contains a '?' character.
func isQuestionShaped(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"how ", "what ", "why ", "where ", "when "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return strings.Contains(text, "?")
}
