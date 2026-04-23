package indexer

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Chunk struct {
	Content          string
	SectionHeading   string
	SectionHierarchy []string
	ChunkIndex       uint32
	TokenCount       uint32
	EmbeddingText    string
	SignalMeta       string
}

type ChunkConfig struct {
	MaxTokens    int
	OverlapLines int
	MinTokens    int
}

func DefaultChunkConfig() ChunkConfig {
	return ChunkConfig{
		MaxTokens:    512,
		OverlapLines: 3,
		MinTokens:    50,
	}
}

// SectionInput is the format-agnostic input to ChunkSections. Handlers convert their
// own section-like units (markdown sections, code methods, YAML key-paths, etc.)
// into SectionInput before delegating to the shared chunker. SignalLine and
// SignalMeta are pre-formatted by the handler so the chunker doesn't depend on any
// format-specific signal representation.
type SectionInput struct {
	Heading    string
	Hierarchy  []string
	Content    string
	SignalLine string // prepended to embedding text (empty string = omit)
	SignalMeta string // stored verbatim on Chunk.SignalMeta; empty string is normalised to "{}"
}

// ChunkSections splits a sequence of section-like units into chunks suitable for
// embedding and storage. Empty sections list triggers a no-sections fallback that
// chunks the rawContent as a single unit (keyed on docTitle).
//
// Handlers call this after converting their parsed units into []SectionInput; the
// legacy ChunkDocument is a thin wrapper for callers still working with
// ParsedDocument.
func ChunkSections(docTitle, rawContent string, sections []SectionInput, cfg ChunkConfig) []Chunk {
	var chunks []Chunk
	var chunkIndex uint32

	if len(sections) == 0 {
		content := strings.TrimSpace(rawContent)
		if content == "" {
			return nil
		}
		tokens := estimateTokens(content)
		if tokens < cfg.MinTokens {
			return nil
		}
		embeddingText := fmt.Sprintf("Document: %s\n\n%s", docTitle, content)
		chunks = append(chunks, Chunk{
			Content:          content,
			SectionHeading:   docTitle,
			SectionHierarchy: []string{docTitle},
			ChunkIndex:       0,
			TokenCount:       uint32(tokens),
			EmbeddingText:    embeddingText,
		})
		return chunks
	}

	for _, sec := range sections {
		content := strings.TrimSpace(sec.Content)
		if content == "" {
			continue
		}

		tokens := estimateTokens(content)
		if tokens < cfg.MinTokens {
			continue
		}

		hierarchyStr := strings.Join(sec.Hierarchy, " > ")
		contextHeader := fmt.Sprintf("Document: %s | Section: %s", docTitle, hierarchyStr)

		meta := sec.SignalMeta
		if meta == "" {
			meta = "{}"
		}

		if tokens <= cfg.MaxTokens {
			embeddingText := contextHeader + "\n\n" + content
			if sec.SignalLine != "" {
				embeddingText += "\n" + sec.SignalLine
			}
			chunks = append(chunks, Chunk{
				Content:          content,
				SectionHeading:   sec.Heading,
				SectionHierarchy: sec.Hierarchy,
				ChunkIndex:       chunkIndex,
				TokenCount:       uint32(tokens),
				EmbeddingText:    embeddingText,
				SignalMeta:       meta,
			})
			chunkIndex++
		} else {
			subChunks := splitByParagraphs(content, cfg)
			for _, sub := range subChunks {
				subTokens := estimateTokens(sub)
				if subTokens < cfg.MinTokens {
					continue
				}
				embeddingText := contextHeader + "\n\n" + sub
				if sec.SignalLine != "" {
					embeddingText += "\n" + sec.SignalLine
				}
				chunks = append(chunks, Chunk{
					Content:          sub,
					SectionHeading:   sec.Heading,
					SectionHierarchy: sec.Hierarchy,
					ChunkIndex:       chunkIndex,
					TokenCount:       uint32(subTokens),
					EmbeddingText:    embeddingText,
					SignalMeta:       meta,
				})
				chunkIndex++
			}
		}
	}

	if cfg.OverlapLines > 0 && len(chunks) > 1 {
		chunks = applyOverlap(chunks, cfg.OverlapLines)
	}

	return chunks
}

// ChunkDocument is the legacy entry point for the markdown-specific ParsedDocument
// shape. It converts Sections to []SectionInput and delegates to ChunkSections so
// the chunking logic has a single source of truth. New handler code should call
// ChunkSections directly.
func ChunkDocument(doc *ParsedDocument, cfg ChunkConfig) []Chunk {
	inputs := make([]SectionInput, 0, len(doc.Sections))
	for _, s := range doc.Sections {
		line := ""
		meta := "{}"
		if s.Signals != nil {
			line = s.Signals.SignalLine()
			meta = s.Signals.ToJSON()
		}
		inputs = append(inputs, SectionInput{
			Heading:    s.Heading,
			Hierarchy:  s.Hierarchy,
			Content:    s.Content,
			SignalLine: line,
			SignalMeta: meta,
		})
	}
	return ChunkSections(doc.Title, doc.RawContent, inputs, cfg)
}

func splitByParagraphs(content string, cfg ChunkConfig) []string {
	paragraphs := strings.Split(content, "\n\n")
	var result []string
	var current strings.Builder

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		candidateText := current.String()
		if candidateText != "" {
			candidateText += "\n\n" + para
		} else {
			candidateText = para
		}

		if estimateTokens(candidateText) > cfg.MaxTokens && current.Len() > 0 {
			result = append(result, strings.TrimSpace(current.String()))
			current.Reset()
			current.WriteString(para)
		} else {
			if current.Len() > 0 {
				current.WriteString("\n\n")
			}
			current.WriteString(para)
		}
	}

	if current.Len() > 0 {
		result = append(result, strings.TrimSpace(current.String()))
	}

	return result
}

func applyOverlap(chunks []Chunk, overlapLines int) []Chunk {
	for i := 1; i < len(chunks); i++ {
		prevLines := strings.Split(chunks[i-1].Content, "\n")
		if len(prevLines) > overlapLines {
			overlap := strings.Join(prevLines[len(prevLines)-overlapLines:], "\n")
			chunks[i].Content = overlap + "\n" + chunks[i].Content
			chunks[i].TokenCount = uint32(estimateTokens(chunks[i].Content))
		}
	}
	return chunks
}

func estimateTokens(text string) int {
	words := len(strings.Fields(text))
	tokens := int(float64(words) / 0.75)
	if tokens == 0 && len(text) > 0 {
		tokens = 1
	}
	return tokens
}

func HierarchyToJSON(hierarchy []string) string {
	b, _ := json.Marshal(hierarchy)
	return string(b)
}
