package indexer

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	meta "github.com/yuin/goldmark-meta"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

type ParsedDocument struct {
	Title       string
	DocType     string
	Summary     string
	Headings    []string
	Frontmatter map[string]interface{}
	Sections    []Section
	Diagrams    []DiagramInfo
	Tables      []TableInfo
	RawContent  string
}

type Section struct {
	Heading   string
	Hierarchy []string
	Level     int
	Content   string
	Signals   *EmphasisSignals
}

func ParseMarkdown(filePath string) (*ParsedDocument, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	return ParseMarkdownBytes(filePath, content)
}

// ParseMarkdownBytes parses a markdown document from in-memory bytes. The filePath
// parameter is retained for downstream metadata and error messages; the file is not
// re-read. This is the bytes entry point the MarkdownHandler uses.
func ParseMarkdownBytes(_ string, content []byte) (*ParsedDocument, error) {
	md := goldmark.New(
		goldmark.WithExtensions(
			meta.Meta,
			extension.Table,
		),
	)

	ctx := parser.NewContext()
	reader := text.NewReader(content)
	doc := md.Parser().Parse(reader, parser.WithContext(ctx))

	frontmatter := meta.Get(ctx)

	parsed := &ParsedDocument{
		Frontmatter: frontmatter,
		RawContent:  string(content),
	}

	// Extract metadata from frontmatter
	if title, ok := frontmatter["title"].(string); ok {
		parsed.Title = title
	}
	if docType, ok := frontmatter["type"].(string); ok {
		parsed.DocType = docType
	}
	if desc, ok := frontmatter["description"].(string); ok {
		parsed.Summary = desc
	}

	// Walk AST to extract headings and sections
	var headings []string
	var sections []Section
	var currentHierarchy []string
	var currentSection *Section

	flushSection := func() {
		if currentSection != nil && strings.TrimSpace(currentSection.Content) != "" {
			sections = append(sections, *currentSection)
		}
	}

	ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		if heading, ok := node.(*ast.Heading); ok {
			headingText := extractText(node, content)
			headings = append(headings, headingText)

			if heading.Level == 1 && parsed.Title == "" {
				parsed.Title = headingText
			}

			flushSection()

			// Update hierarchy: pop entries at same or deeper level
			for len(currentHierarchy) >= heading.Level {
				currentHierarchy = currentHierarchy[:len(currentHierarchy)-1]
			}
			currentHierarchy = append(currentHierarchy, headingText)

			hierarchyCopy := make([]string, len(currentHierarchy))
			copy(hierarchyCopy, currentHierarchy)

			currentSection = &Section{
				Heading:   headingText,
				Hierarchy: hierarchyCopy,
				Level:     heading.Level,
			}

			return ast.WalkSkipChildren, nil
		}

		// Accumulate content for current section
		switch n := node.(type) {
		case *ast.FencedCodeBlock:
			lang := string(n.Language(content))
			nodeText := extractBlockText(n, content)
			info, summary, isDiagram := ProcessDiagramBlock(lang, nodeText)
			if isDiagram {
				parsed.Diagrams = append(parsed.Diagrams, *info)
				if currentSection != nil {
					currentSection.Content += summary + "\n"
				}
			} else {
				if currentSection != nil {
					currentSection.Content += nodeText + "\n"
				} else if nodeText != "" && parsed.Summary == "" {
					parsed.Summary = strings.TrimSpace(nodeText)
				}
			}
			return ast.WalkSkipChildren, nil

		case *east.Table:
			info, summary := ProcessTableNode(n, content)
			parsed.Tables = append(parsed.Tables, *info)
			if currentSection != nil {
				currentSection.Content += summary + "\n"
			} else if parsed.Summary == "" {
				parsed.Summary = strings.TrimSpace(summary)
			}
			return ast.WalkSkipChildren, nil

		case *ast.HTMLBlock:
			nodeText := extractBlockText(n, content)
			if isHTMLTable(nodeText) {
				if info, summary, ok := ProcessHTMLTable(nodeText); ok {
					parsed.Tables = append(parsed.Tables, *info)
					if currentSection != nil {
						currentSection.Content += summary + "\n"
					}
					return ast.WalkSkipChildren, nil
				}
			}
			// fallthrough to regular handling
			if currentSection != nil {
				currentSection.Content += nodeText + "\n"
			} else if nodeText != "" && parsed.Summary == "" {
				parsed.Summary = strings.TrimSpace(nodeText)
			}
			return ast.WalkSkipChildren, nil

		case *ast.Paragraph, *ast.List:
			nodeText := extractBlockText(n, content)
			if currentSection != nil {
				currentSection.Content += nodeText + "\n"
				if sig := ExtractEmphasisSignals(n, content); sig != nil {
					mergeSignals(currentSection, sig)
				}
			} else if nodeText != "" && parsed.Summary == "" {
				parsed.Summary = strings.TrimSpace(nodeText)
			}
			return ast.WalkSkipChildren, nil

		case *ast.CodeBlock, *ast.Blockquote, *ast.ThematicBreak:
			nodeText := extractBlockText(n, content)
			if currentSection != nil {
				currentSection.Content += nodeText + "\n"
			} else if nodeText != "" && parsed.Summary == "" {
				parsed.Summary = strings.TrimSpace(nodeText)
			}
			return ast.WalkSkipChildren, nil
		}

		return ast.WalkContinue, nil
	})

	flushSection()

	parsed.Headings = headings
	parsed.Sections = sections

	if parsed.DocType == "" {
		parsed.DocType = "guide"
	}

	return parsed, nil
}

func extractText(node ast.Node, source []byte) string {
	var buf bytes.Buffer
	extractInlineText(node, source, &buf)
	return strings.TrimSpace(buf.String())
}

func extractInlineText(node ast.Node, source []byte, buf *bytes.Buffer) {
	// Only block-level nodes support Lines(); inline nodes panic
	if node.Type() == ast.TypeBlock {
		for i := 0; i < node.Lines().Len(); i++ {
			line := node.Lines().At(i)
			buf.Write(line.Value(source))
		}
	}

	// Recurse into children
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if t, ok := child.(*ast.Text); ok {
			buf.Write(t.Segment.Value(source))
			if t.SoftLineBreak() {
				buf.WriteByte('\n')
			}
		} else {
			extractInlineText(child, source, buf)
		}
	}
}

func extractBlockText(node ast.Node, source []byte) string {
	var buf bytes.Buffer

	// Only block-level nodes support Lines(); inline nodes panic
	if node.Type() == ast.TypeBlock {
		for i := 0; i < node.Lines().Len(); i++ {
			line := node.Lines().At(i)
			buf.Write(line.Value(source))
		}
	}

	if buf.Len() > 0 {
		return buf.String()
	}

	// For composite nodes, extract text from children
	extractInlineText(node, source, &buf)
	return buf.String()
}

func HeadingsToJSON(headings []string) string {
	b, _ := json.Marshal(headings)
	return string(b)
}

func FrontmatterToJSON(fm map[string]interface{}) string {
	if fm == nil {
		return "{}"
	}
	b, _ := json.Marshal(fm)
	return string(b)
}
