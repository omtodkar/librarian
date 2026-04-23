// Package config implements FileHandlers for structured config formats:
// YAML, JSON, TOML, XML, .properties, and .env.
//
// Design principle: all config formats share a common conceptual model of
// "key-path + value + optional leading comment". Handlers produce ParsedDoc
// with Kind="key-path" Units, one per top-level group (for nested formats)
// or a single Unit for the whole file (for flat formats like .properties).
//
// TODO/FIXME/HACK/WHY/NOTE markers found in comments become Signals.
package config

import (
	"regexp"
	"strings"

	"librarian/internal/indexer"
)

// rationaleRegex catches common rationale markers in comments. Matches the
// marker at the start of a trimmed line or after whitespace following a comment
// leader (#, //, <!--).
var rationaleRegex = regexp.MustCompile(`(?i)\b(TODO|FIXME|HACK|WHY|NOTE)\b:?\s*(.*)$`)

// extractSignals scans a block of comment lines for rationale markers and
// returns the corresponding Signals. The caller is responsible for stripping
// comment leaders (# / // / <!-- -->) before passing the text in.
func extractSignals(comments string) []indexer.Signal {
	if comments == "" {
		return nil
	}
	var out []indexer.Signal
	for _, line := range strings.Split(comments, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := rationaleRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		kind := strings.ToLower(m[1])
		switch kind {
		case "todo", "fixme", "hack":
			out = append(out, indexer.Signal{Kind: "todo", Value: kind, Detail: strings.TrimSpace(m[2])})
		case "why", "note":
			out = append(out, indexer.Signal{Kind: "rationale", Value: kind, Detail: strings.TrimSpace(m[2])})
		}
	}
	return out
}

// registerConfigHandlers is called by init() in each per-format file to wire a
// single handler into the default registry. Consolidated here so the call
// shape stays consistent.
func registerConfigHandlers(handlers ...indexer.FileHandler) {
	for _, h := range handlers {
		indexer.RegisterDefault(h)
	}
}

// chunkFromUnits is the shared chunker for config handlers. Each top-level Unit
// becomes one SectionInput; ChunkSections handles token-aware splitting.
func chunkFromUnits(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) []indexer.Chunk {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		if u.Kind != "key-path" {
			continue
		}
		inputs = append(inputs, indexer.SectionInput{
			Heading:   u.Title,
			Hierarchy: []string{u.Title},
			Content:   u.Content,
		})
	}
	return indexer.ChunkSections(doc.Title, doc.RawContent, inputs, opts)
}
