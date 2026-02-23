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

func ChunkDocument(doc *ParsedDocument, cfg ChunkConfig) []Chunk {
	var chunks []Chunk
	var chunkIndex uint32

	if len(doc.Sections) == 0 {
		content := strings.TrimSpace(doc.RawContent)
		if content == "" {
			return nil
		}
		tokens := estimateTokens(content)
		if tokens < cfg.MinTokens {
			return nil
		}
		embeddingText := fmt.Sprintf("Document: %s\n\n%s", doc.Title, content)
		chunks = append(chunks, Chunk{
			Content:          content,
			SectionHeading:   doc.Title,
			SectionHierarchy: []string{doc.Title},
			ChunkIndex:       0,
			TokenCount:       uint32(tokens),
			EmbeddingText:    embeddingText,
		})
		return chunks
	}

	for _, section := range doc.Sections {
		content := strings.TrimSpace(section.Content)
		if content == "" {
			continue
		}

		tokens := estimateTokens(content)
		if tokens < cfg.MinTokens {
			continue
		}

		hierarchyStr := strings.Join(section.Hierarchy, " > ")
		contextHeader := fmt.Sprintf("Document: %s | Section: %s", doc.Title, hierarchyStr)

		if tokens <= cfg.MaxTokens {
			embeddingText := contextHeader + "\n\n" + content
			chunks = append(chunks, Chunk{
				Content:          content,
				SectionHeading:   section.Heading,
				SectionHierarchy: section.Hierarchy,
				ChunkIndex:       chunkIndex,
				TokenCount:       uint32(tokens),
				EmbeddingText:    embeddingText,
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
				chunks = append(chunks, Chunk{
					Content:          sub,
					SectionHeading:   section.Heading,
					SectionHierarchy: section.Hierarchy,
					ChunkIndex:       chunkIndex,
					TokenCount:       uint32(subTokens),
					EmbeddingText:    embeddingText,
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
