package faq

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// FAQEntry is a generated FAQ with question, answer text, and source reference.
type FAQEntry struct {
	Question   string
	Answer     string
	SourceID   string
	SourceKind string
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a question to a filename-safe slug (≤60 chars).
func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// EntryFromCluster builds a FAQEntry from a non-empty cluster of Sources.
// The first source is the representative question; the best detail text
// across the cluster becomes the answer.
func EntryFromCluster(cluster []Source) FAQEntry {
	if len(cluster) == 0 {
		return FAQEntry{}
	}
	rep := cluster[0]
	answer := chooseAnswer(cluster)
	return FAQEntry{
		Question:   rep.Text,
		Answer:     answer,
		SourceID:   rep.ID,
		SourceKind: rep.Kind,
	}
}

// chooseAnswer picks the most informative detail from a cluster. Prefers
// close_reason / commit body (non-empty Detail) and truncates to ~300 chars
// at a sentence boundary to keep FAQ answers concise.
func chooseAnswer(cluster []Source) string {
	for _, src := range cluster {
		detail := strings.TrimSpace(src.Detail)
		if detail == "" {
			continue
		}
		if len(detail) <= 300 {
			return detail
		}
		// Truncate at a sentence boundary near 300 chars.
		cutoff := 300
		for cutoff > 200 && detail[cutoff] != '.' && detail[cutoff] != '\n' {
			cutoff--
		}
		return strings.TrimSpace(detail[:cutoff+1])
	}
	return "See source for details."
}

// Markdown renders the FAQEntry as a markdown document suitable for indexing.
func (e FAQEntry) Markdown() string {
	var sb strings.Builder
	sb.WriteString("# " + e.Question + "\n\n")
	sb.WriteString(e.Answer + "\n\n")
	sb.WriteString("---\n\n")
	switch e.SourceKind {
	case "git":
		sb.WriteString(fmt.Sprintf("*Source: git commit `%s`*\n", e.SourceID))
	case "issue":
		sb.WriteString(fmt.Sprintf("*Source: issue `%s`*\n", e.SourceID))
	default:
		sb.WriteString(fmt.Sprintf("*Source: `%s`*\n", e.SourceID))
	}
	sb.WriteString(fmt.Sprintf("\n*Generated: %s*\n", time.Now().UTC().Format("2006-01-02")))
	return sb.String()
}

// WriteEntries writes FAQ entries as markdown files under dir.
// Returns the list of written absolute file paths (relative to dir).
func WriteEntries(entries []FAQEntry, dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating faq directory: %w", err)
	}
	var paths []string
	seen := map[string]int{} // slug → count, for de-duplication
	for _, entry := range entries {
		slug := slugify(entry.Question)
		if slug == "" {
			continue
		}
		// Disambiguate when two questions share the same slug.
		if n := seen[slug]; n > 0 {
			slug = fmt.Sprintf("%s-%d", slug, n+1)
		}
		seen[slug]++

		filename := slug + ".md"
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, []byte(entry.Markdown()), 0o644); err != nil {
			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}
