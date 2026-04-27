package faq

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// bdIssue is the partial JSON shape of a beads issue (only the fields we need).
type bdIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	CloseReason string `json:"close_reason"`
}

// parseGitLog parses the output of `git log --format=%H\x1f%s\x1f%b\x1e`
// into question-shaped Sources. Exported for unit-testing without forking git.
func parseGitLog(data []byte) ([]Source, error) {
	var sources []Source
	for _, record := range strings.Split(string(data), "\x1e") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\x1f", 3)
		if len(parts) < 2 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		subject := strings.TrimSpace(parts[1])
		if sha == "" || subject == "" {
			continue
		}
		body := ""
		if len(parts) == 3 {
			body = strings.TrimSpace(parts[2])
		}

		shortSHA := sha
		if len(shortSHA) > 8 {
			shortSHA = shortSHA[:8]
		}

		if isQuestionShaped(subject) {
			sources = append(sources, Source{
				Kind:   "git",
				ID:     shortSHA,
				Text:   subject,
				Detail: body,
			})
			continue
		}

		// Also check body lines for question headings (lines starting with # containing ?)
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") && strings.Contains(line, "?") {
				question := strings.TrimLeft(line, "# ")
				question = strings.TrimSpace(question)
				if question != "" {
					sources = append(sources, Source{
						Kind:   "git",
						ID:     shortSHA,
						Text:   question,
						Detail: body,
					})
					break
				}
			}
		}
	}
	return sources, nil
}

// parseBDIssues parses `bd list --json` output into question-shaped Sources.
// Exported for unit-testing without forking bd.
func parseBDIssues(data []byte) ([]Source, error) {
	var issues []bdIssue
	if err := json.Unmarshal(data, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd output: %w", err)
	}
	var sources []Source
	for _, issue := range issues {
		if !isQuestionShaped(issue.Title) {
			continue
		}
		detail := issue.CloseReason
		if detail == "" {
			detail = issue.Description
		}
		sources = append(sources, Source{
			Kind:   "issue",
			ID:     issue.ID,
			Text:   issue.Title,
			Detail: detail,
		})
	}
	return sources, nil
}

// ScanGitLog scans the last n git commits for question-shaped subjects/bodies.
func ScanGitLog(n int) ([]Source, error) {
	out, err := exec.Command("git", "log", fmt.Sprintf("-n%d", n), "--format=%H\x1f%s\x1f%b\x1e").Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	return parseGitLog(out)
}

// ScanBDIssues scans closed bd issues for question-shaped titles.
func ScanBDIssues() ([]Source, error) {
	out, err := exec.Command("bd", "list", "--status=closed", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}
	return parseBDIssues(out)
}
