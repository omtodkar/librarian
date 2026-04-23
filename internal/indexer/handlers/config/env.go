package config

import (
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// EnvHandler parses .env files (one KEY=VALUE per line, # for comments).
//
// The entire file produces a single Unit — .env is flat and small enough that
// per-key chunking would shred the content into sub-MinTokens noise.
type EnvHandler struct{}

// NewEnv returns an .env handler.
func NewEnv() *EnvHandler { return &EnvHandler{} }

var _ indexer.FileHandler = (*EnvHandler)(nil)

func (*EnvHandler) Name() string         { return "env" }
func (*EnvHandler) Extensions() []string { return []string{".env"} }

func (*EnvHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	// Collect leading-comment signals by scanning each comment line.
	var commentLines []string
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			commentLines = append(commentLines, strings.TrimPrefix(trim, "#"))
		}
	}

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "env",
		Title:      filepath.Base(path),
		DocType:    "env",
		RawContent: raw,
		Metadata:   map[string]any{},
		Signals:    indexer.ExtractRationaleSignals(strings.Join(commentLines, "\n")),
	}

	doc.Units = []indexer.Unit{{
		Kind:    "key-path",
		Path:    filepath.Base(path),
		Title:   filepath.Base(path),
		Content: raw,
		Signals: doc.Signals,
	}}
	return doc, nil
}

func (*EnvHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}
