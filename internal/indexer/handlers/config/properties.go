package config

import (
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// PropertiesHandler parses Java-style .properties files.
//
// Supported syntax: KEY=VALUE or KEY:VALUE lines, # and ! as comment leaders,
// backslash line-continuations are preserved as-is in the raw content (we
// don't expand them — the indexer's goal is semantic search, not evaluation).
//
// Like EnvHandler this emits a single per-file Unit: properties files tend to
// be short and flat, so per-key chunking would produce sub-MinTokens noise.
type PropertiesHandler struct{}

// NewProperties returns a .properties handler.
func NewProperties() *PropertiesHandler { return &PropertiesHandler{} }

var _ indexer.FileHandler = (*PropertiesHandler)(nil)

func (*PropertiesHandler) Name() string         { return "properties" }
func (*PropertiesHandler) Extensions() []string { return []string{".properties"} }

func (*PropertiesHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	var commentLines []string
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "!") {
			commentLines = append(commentLines, strings.TrimLeft(trim, "#!"))
		}
	}

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "properties",
		Title:      filepath.Base(path),
		DocType:    "properties",
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

func (*PropertiesHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}
