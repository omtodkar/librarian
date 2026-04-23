package config

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"librarian/internal/indexer"
)

// YAMLHandler parses .yml / .yaml files via gopkg.in/yaml.v3's Node API, which
// preserves head / line / foot comments for Signal extraction.
//
// For mapping-root documents (the common case: Spring config, k8s manifests,
// CI workflows) each top-level key becomes a Unit with the head/inline comments
// merged into the content. For sequence / scalar roots the whole file is one
// Unit.
type YAMLHandler struct{}

// NewYAML returns a YAML handler.
func NewYAML() *YAMLHandler { return &YAMLHandler{} }

var _ indexer.FileHandler = (*YAMLHandler)(nil)

func (*YAMLHandler) Name() string         { return "yaml" }
func (*YAMLHandler) Extensions() []string { return []string{".yaml", ".yml"} }

func (*YAMLHandler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "yaml",
		Title:      filepath.Base(path),
		DocType:    "yaml",
		RawContent: raw,
		Metadata:   map[string]any{},
	}

	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
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

	// Unwrap the document node.
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = *root.Content[0]
	}

	// Mapping root: per top-level key Units. Other roots: single Unit.
	if root.Kind != yaml.MappingNode || len(root.Content)%2 != 0 {
		doc.Units = []indexer.Unit{{
			Kind:    "key-path",
			Path:    filepath.Base(path),
			Title:   filepath.Base(path),
			Content: raw,
		}}
		return doc, nil
	}

	var allComments []string
	for i := 0; i < len(root.Content); i += 2 {
		keyNode := root.Content[i]
		valNode := root.Content[i+1]

		key := keyNode.Value
		comments := gatherComments(keyNode, valNode)
		if comments != "" {
			allComments = append(allComments, comments)
		}

		// Serialize the (key, value) pair back to YAML text as a mini-mapping.
		mini := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{keyNode, valNode}}
		out, err := yaml.Marshal(mini)
		var body string
		if err != nil {
			body = keyNode.Value + ": <marshal error: " + err.Error() + ">"
		} else {
			body = string(out)
		}

		unit := indexer.Unit{
			Kind:    "key-path",
			Path:    key,
			Title:   key,
			Content: body,
			Loc:     indexer.Location{Line: keyNode.Line, Column: keyNode.Column},
			Signals: indexer.ExtractRationaleSignals(comments),
		}
		doc.Units = append(doc.Units, unit)
	}

	doc.Signals = indexer.ExtractRationaleSignals(strings.Join(allComments, "\n"))
	return doc, nil
}

func (*YAMLHandler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	return chunkFromUnits(doc, opts), nil
}


// gatherComments concatenates head / line / foot comments from a key/value pair
// into a single string for signal extraction. Empty if no comments.
func gatherComments(keyNode, valNode *yaml.Node) string {
	var parts []string
	for _, c := range []string{keyNode.HeadComment, keyNode.LineComment, keyNode.FootComment,
		valNode.HeadComment, valNode.LineComment, valNode.FootComment} {
		if c = strings.TrimSpace(c); c != "" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, "\n")
}
