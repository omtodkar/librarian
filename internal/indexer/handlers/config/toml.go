package config

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/pelletier/go-toml/v2"

	"librarian/internal/indexer"
)

// TOMLHandler parses .toml files.
//
// Each top-level key or table becomes a Unit. Unlike YAML there's no
// first-class comment API in go-toml/v2's high-level Unmarshal, so comments
// aren't extracted as signals in this first pass — can be revisited with the
// lower-level tokenizer if rationale extraction proves valuable.
type TOMLHandler struct{}

// NewTOML returns a TOML handler.
func NewTOML() *TOMLHandler { return &TOMLHandler{} }

var _ indexer.FileHandler = (*TOMLHandler)(nil)

func (*TOMLHandler) Name() string         { return "toml" }
func (*TOMLHandler) Extensions() []string { return []string{".toml"} }

func (*TOMLHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "toml",
		Title:      filepath.Base(path),
		DocType:    "toml",
		RawContent: raw,
		Metadata:   map[string]any{},
	}

	var m map[string]any
	if err := toml.Unmarshal(content, &m); err != nil {
		doc.Units = []indexer.Unit{{
			Kind:    "key-path",
			Path:    filepath.Base(path),
			Title:   filepath.Base(path),
			Content: raw,
			Metadata: map[string]any{
				"parse_error": err.Error(),
			},
		}}
		return doc, nil
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := m[k]
		body, err := toml.Marshal(map[string]any{k: v})
		var content string
		if err != nil {
			content = fmt.Sprintf("%s = %v", k, v)
		} else {
			content = string(body)
		}
		doc.Units = append(doc.Units, indexer.Unit{
			Kind:    "key-path",
			Path:    k,
			Title:   k,
			Content: content,
		})
	}
	return doc, nil
}

func (*TOMLHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}

func init() { registerConfigHandlers(NewTOML()) }
