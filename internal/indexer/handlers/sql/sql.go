// Package sql implements a FileHandler for .sql, .psql, and .ddl files.
//
// v1 is a docs-pass-only handler: it makes SQL content queryable via search_docs
// and get_context without building a full SQL AST. Statement-boundary chunking
// keeps semantically related lines (CREATE TABLE + its columns + trailing comment)
// in the same chunk. Structural extraction (table/column symbol nodes, FK edges)
// is deferred to v2.
package sql

import (
	"path/filepath"
	"strconv"
	"strings"

	"librarian/internal/indexer"
)

// Handler indexes SQL files for the docs pass.
type Handler struct{}

// New returns a SQL handler.
func New() *Handler { return &Handler{} }

var _ indexer.FileHandler = (*Handler)(nil)

func init() {
	indexer.RegisterDefault(New())
}

func (*Handler) Name() string { return "sql" }

func (*Handler) Extensions() []string { return []string{".sql", ".psql", ".ddl"} }

// Parse converts raw SQL bytes to a ParsedDoc. Each SQL statement (terminated by
// a top-level semicolon) becomes one Unit, with any leading comments folded into
// the same Unit so they stay co-located in search results.
func (*Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)
	base := filepath.Base(path)

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "sql",
		Title:      base,
		DocType:    "sql",
		RawContent: raw,
		Metadata:   map[string]any{},
	}

	units := splitIntoStatementUnits(raw)
	doc.Units = units
	doc.Signals = indexer.ExtractRationaleSignals(raw)
	return doc, nil
}

// Chunk converts each statement Unit into a SectionInput and delegates to the
// shared ChunkSections. Statement Units that exceed MaxTokens fall back to the
// shared paragraph splitter automatically.
func (*Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		if u.Kind != "section" {
			continue
		}
		inputs = append(inputs, indexer.SectionInput{
			Heading:    u.Title,
			Hierarchy:  []string{doc.Title, u.Title},
			Content:    u.Content,
			SignalLine: indexer.SignalLineFromSignals(u.Signals),
			SignalMeta: indexer.SignalsToJSON(u.Signals),
		})
	}
	chunks := indexer.ChunkSections(doc.Title, doc.RawContent, inputs, opts)
	return chunks, nil
}

// splitIntoStatementUnits splits SQL source into one Unit per statement.
// A statement ends at a semicolon that appears outside a string literal or
// block comment. Leading line and block comments are attached to the following
// statement so they stay co-located in search results.
//
// If no statement boundaries are found (e.g. a file with a single long stored
// procedure that doesn't end in a semicolon), the whole file is returned as one
// Unit so ChunkSections can apply its paragraph splitter.
func splitIntoStatementUnits(src string) []indexer.Unit {
	stmts := splitStatements(src)
	if len(stmts) == 0 {
		return nil
	}

	units := make([]indexer.Unit, 0, len(stmts))
	for i, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		title := statementTitle(stmt, i+1)
		units = append(units, indexer.Unit{
			Kind:    "section",
			Path:    title,
			Title:   title,
			Content: stmt,
			Signals: indexer.ExtractRationaleSignals(stmt),
		})
	}
	return units
}

// splitStatements splits src into a slice of statement strings. Each element
// contains the statement text plus any leading comments that belong to it.
// The split boundary is a semicolon that is not inside a string literal or
// block comment, followed by optional whitespace and a newline (or end of input).
func splitStatements(src string) []string {
	type parseState int
	const (
		stateNormal parseState = iota
		stateLineComment
		stateBlockComment
		stateSingleQuote
		stateDoubleQuote
		stateDollarQuote // PostgreSQL $$...$$
	)

	var (
		state     = stateNormal
		stmts     []string
		pending   strings.Builder // current accumulated text including pending comments
		dollarTag string          // captures the opening $tag$ of a dollar-quoted string
	)

	runes := []rune(src)
	n := len(runes)

	for i := 0; i < n; i++ {
		ch := runes[i]

		switch state {
		case stateLineComment:
			pending.WriteRune(ch)
			if ch == '\n' {
				state = stateNormal
			}

		case stateBlockComment:
			pending.WriteRune(ch)
			if ch == '*' && i+1 < n && runes[i+1] == '/' {
				pending.WriteRune(runes[i+1])
				i++
				state = stateNormal
			}

		case stateSingleQuote:
			pending.WriteRune(ch)
			if ch == '\'' {
				if i+1 < n && runes[i+1] == '\'' {
					// escaped quote — consume both
					pending.WriteRune(runes[i+1])
					i++
				} else {
					state = stateNormal
				}
			}

		case stateDoubleQuote:
			pending.WriteRune(ch)
			if ch == '"' {
				state = stateNormal
			}

		case stateDollarQuote:
			pending.WriteRune(ch)
			// check if we're at the closing $tag$
			if ch == '$' {
				tail := string(runes[i:])
				if strings.HasPrefix(tail, dollarTag) {
					for j := 1; j < len([]rune(dollarTag)); j++ {
						pending.WriteRune(runes[i+j])
					}
					i += len([]rune(dollarTag)) - 1
					state = stateNormal
					dollarTag = ""
				}
			}

		case stateNormal:
			switch {
			case ch == '-' && i+1 < n && runes[i+1] == '-':
				state = stateLineComment
				pending.WriteRune(ch)
				pending.WriteRune(runes[i+1])
				i++

			case ch == '/' && i+1 < n && runes[i+1] == '*':
				state = stateBlockComment
				pending.WriteRune(ch)
				pending.WriteRune(runes[i+1])
				i++

			case ch == '\'':
				state = stateSingleQuote
				pending.WriteRune(ch)

			case ch == '"':
				state = stateDoubleQuote
				pending.WriteRune(ch)

			case ch == '$':
				// Detect PostgreSQL dollar-quote: $tag$ where tag is [A-Za-z0-9_]*
				tag := scanDollarTag(runes, i)
				if tag != "" {
					dollarTag = tag
					state = stateDollarQuote
					for _, r := range []rune(tag) {
						pending.WriteRune(r)
					}
					i += len([]rune(tag)) - 1
				} else {
					pending.WriteRune(ch)
				}

			case ch == ';':
				pending.WriteRune(ch)
				stmt := pending.String()
				stmts = append(stmts, stmt)
				pending.Reset()

			default:
				pending.WriteRune(ch)
			}
		}
	}

	// Anything remaining after the last semicolon (or with no semicolons at all).
	if tail := strings.TrimSpace(pending.String()); tail != "" {
		stmts = append(stmts, pending.String())
	}

	return stmts
}

// scanDollarTag checks whether runes[i:] begins a PostgreSQL dollar-quoted string
// opening tag ($tag$ or $$) and returns the full tag string (including both
// dollar signs) if so, or "" if not.
func scanDollarTag(runes []rune, i int) string {
	if runes[i] != '$' {
		return ""
	}
	j := i + 1
	for j < len(runes) && (runes[j] == '_' || (runes[j] >= 'a' && runes[j] <= 'z') ||
		(runes[j] >= 'A' && runes[j] <= 'Z') || (runes[j] >= '0' && runes[j] <= '9')) {
		j++
	}
	if j < len(runes) && runes[j] == '$' {
		return string(runes[i : j+1])
	}
	return ""
}

// statementTitle derives a short label for a statement Unit. It skips leading
// comments and returns the first keyword(s) (e.g. "CREATE TABLE users" or
// "INSERT INTO orders") capped at 60 characters, falling back to
// "statement N" when the statement body is empty or comment-only.
func statementTitle(stmt string, n int) string {
	// Strip leading comments.
	text := strings.TrimSpace(stripLeadingComments(stmt))
	if text == "" {
		return "statement " + strconv.Itoa(n)
	}

	// Take first line, normalise whitespace, cap length.
	line := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	// Strip trailing semicolon from the title.
	line = strings.TrimRight(line, "; \t")
	runes := []rune(line)
	if len(runes) > 60 {
		line = string(runes[:60]) + "…"
	}
	if line == "" {
		return "statement " + strconv.Itoa(n)
	}
	return line
}

// stripLeadingComments removes leading -- line comments and /* block comments */
// from text, returning the remainder.
func stripLeadingComments(text string) string {
	for {
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "--") {
			if idx := strings.Index(text, "\n"); idx >= 0 {
				text = text[idx+1:]
			} else {
				return ""
			}
		} else if strings.HasPrefix(text, "/*") {
			if idx := strings.Index(text, "*/"); idx >= 0 {
				text = text[idx+2:]
			} else {
				return ""
			}
		} else {
			break
		}
	}
	return text
}
