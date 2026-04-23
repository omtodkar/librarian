package config

import (
	"path/filepath"
	"regexp"
	"strings"

	"librarian/internal/indexer"
)

// XMLHandler indexes .xml files (pom.xml, persistence.xml, logback.xml, ...).
//
// For v1, XML is treated as a single opaque Unit per file — typical enterprise
// XML (Maven POMs, Spring configs) is short enough to fit comfortably in one
// chunk, and semantic search over the whole file is useful for "where is the
// jackson dependency?" style queries.
//
// Comments (<!-- ... -->) are scanned for rationale markers so TODO/FIXME/NOTE
// comments still surface as signals.
type XMLHandler struct{}

// NewXML returns an XML handler.
func NewXML() *XMLHandler { return &XMLHandler{} }

var _ indexer.FileHandler = (*XMLHandler)(nil)

func (*XMLHandler) Name() string         { return "xml" }
func (*XMLHandler) Extensions() []string { return []string{".xml"} }

var xmlCommentRegex = regexp.MustCompile(`(?s)<!--(.*?)-->`)

func (*XMLHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	// Collect rationale markers from XML comments.
	var commentBodies []string
	for _, m := range xmlCommentRegex.FindAllStringSubmatch(raw, -1) {
		commentBodies = append(commentBodies, m[1])
	}
	signals := indexer.ExtractRationaleSignals(strings.Join(commentBodies, "\n"))

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "xml",
		Title:      filepath.Base(path),
		DocType:    "xml",
		RawContent: raw,
		Metadata:   map[string]any{},
		Signals:    signals,
	}

	doc.Units = []indexer.Unit{{
		Kind:    "key-path",
		Path:    filepath.Base(path),
		Title:   filepath.Base(path),
		Content: raw,
		Signals: signals,
	}}
	return doc, nil
}

func (*XMLHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}
