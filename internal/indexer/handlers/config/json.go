package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"librarian/internal/indexer"
)

// JSONHandler parses .json files.
//
// For object documents (the common case: package.json, tsconfig.json, API
// schemas), each top-level key becomes a Unit. For array or scalar root
// documents the entire content is one Unit.
//
// Strict JSON has no comments; we don't attempt JSONC / JSON5 parsing.
type JSONHandler struct{}

// NewJSON returns a JSON handler.
func NewJSON() *JSONHandler { return &JSONHandler{} }

var _ indexer.FileHandler = (*JSONHandler)(nil)

func (*JSONHandler) Name() string         { return "json" }
func (*JSONHandler) Extensions() []string { return []string{".json"} }

func (*JSONHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "json",
		Title:      filepath.Base(path),
		DocType:    "json",
		RawContent: raw,
		Metadata:   map[string]any{},
	}

	var decoded any
	if err := json.Unmarshal(content, &decoded); err != nil {
		// Fall back to a single opaque Unit for malformed JSON so the content
		// is still discoverable by search, just not structurally decomposed.
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

	obj, ok := decoded.(map[string]any)
	if !ok {
		// Array or scalar root — emit as one Unit.
		doc.Units = []indexer.Unit{{
			Kind:    "key-path",
			Path:    filepath.Base(path),
			Title:   filepath.Base(path),
			Content: raw,
		}}
		return doc, nil
	}

	// Object root: one Unit per top-level key. Stable key order for
	// deterministic chunk indices across runs.
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := obj[k]
		val, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			val = []byte(fmt.Sprintf("%v", v))
		}
		doc.Units = append(doc.Units, indexer.Unit{
			Kind:    "key-path",
			Path:    k,
			Title:   k,
			Content: fmt.Sprintf("%q: %s", k, val),
		})
	}
	return doc, nil
}

func (*JSONHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}
