package indexer

import (
	"crypto/sha256"
	"fmt"
	"os"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/store"
)

type Indexer struct {
	store    *store.Store
	cfg      *config.Config
	embedder embedding.Embedder
	registry *Registry
}

type IndexResult struct {
	DocumentsIndexed int
	ChunksCreated    int
	CodeFilesFound   int
	Skipped          int
	Errors           []string
}

// New returns an Indexer that dispatches files through the default handler registry.
func New(s *store.Store, cfg *config.Config, embedder embedding.Embedder) *Indexer {
	return NewWithRegistry(s, cfg, embedder, DefaultRegistry())
}

// NewWithRegistry returns an Indexer that dispatches files through the given registry.
// Use this when tests need isolated registration or when a custom handler set is
// required; most callers should use New.
func NewWithRegistry(s *store.Store, cfg *config.Config, embedder embedding.Embedder, reg *Registry) *Indexer {
	return &Indexer{
		store:    s,
		cfg:      cfg,
		embedder: embedder,
		registry: reg,
	}
}

func (idx *Indexer) IndexDirectory(docsDir string, force bool) (*IndexResult, error) {
	result := &IndexResult{}

	files, err := WalkDocs(docsDir, idx.cfg.ExcludePatterns, idx.registry)
	if err != nil {
		return nil, fmt.Errorf("walking docs directory: %w", err)
	}

	if len(files) == 0 {
		return result, nil
	}

	fmt.Printf("Found %d files to index\n", len(files))

	for i, file := range files {
		fmt.Printf("  [%d/%d] %s", i+1, len(files), file.FilePath)
		err := idx.indexFile(file, result, force)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
			fmt.Printf(" ERROR\n")
		} else {
			fmt.Printf(" OK\n")
		}
	}

	// Build RelatedDoc edges for documents that share code references
	idx.buildRelatedDocEdges(files)

	return result, nil
}

func (idx *Indexer) IndexSingleFile(filePath, absPath string, force bool) (*IndexResult, error) {
	result := &IndexResult{}

	file := WalkResult{
		FilePath: filePath,
		AbsPath:  absPath,
	}
	err := idx.indexFile(file, result, force)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (idx *Indexer) indexFile(file WalkResult, result *IndexResult, force bool) error {
	handler := idx.registry.HandlerFor(file.FilePath)
	if handler == nil {
		return fmt.Errorf("no handler registered for %s", file.FilePath)
	}

	content, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}

	parsed, err := handler.Parse(file.FilePath, content)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	contentHash := computeHash(parsed.RawContent)

	// Check if document already exists and hasn't changed
	if !force {
		existing, err := idx.store.GetDocumentByPath(file.FilePath)
		if err == nil && existing != nil && existing.ContentHash == contentHash {
			result.Skipped++
			return nil
		}
		if err == nil && existing != nil {
			idx.store.DeleteDocument(existing.ID)
		}
	} else {
		existing, err := idx.store.GetDocumentByPath(file.FilePath)
		if err == nil && existing != nil {
			idx.store.DeleteDocument(existing.ID)
		}
	}

	chunkCfg := ChunkConfig{
		MaxTokens:    idx.cfg.Chunking.MaxTokens,
		OverlapLines: idx.cfg.Chunking.OverlapLines,
		MinTokens:    idx.cfg.Chunking.MinTokens,
	}
	chunks, err := handler.Chunk(parsed, chunkCfg)
	if err != nil {
		return fmt.Errorf("chunking: %w", err)
	}

	var headings []string
	if h, ok := parsed.Metadata["headings"].([]string); ok {
		headings = h
	}
	var frontmatter map[string]interface{}
	if fm, ok := parsed.Metadata["frontmatter"].(map[string]interface{}); ok {
		frontmatter = fm
	}

	doc, err := idx.store.AddDocument(store.AddDocumentInput{
		FilePath:    file.FilePath,
		Title:       parsed.Title,
		DocType:     parsed.DocType,
		Summary:     parsed.Summary,
		Headings:    HeadingsToJSON(headings),
		Frontmatter: FrontmatterToJSON(frontmatter),
		ContentHash: contentHash,
		ChunkCount:  uint32(len(chunks)),
	})
	if err != nil {
		return fmt.Errorf("creating document: %w", err)
	}

	for _, chunk := range chunks {
		vector, err := idx.embedder.Embed(chunk.EmbeddingText)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("chunk %d embed: %s", chunk.ChunkIndex, err))
			continue
		}
		_, err = idx.store.AddChunk(store.AddChunkInput{
			Vector:           vector,
			Content:          chunk.EmbeddingText,
			FilePath:         file.FilePath,
			SectionHeading:   chunk.SectionHeading,
			SectionHierarchy: HierarchyToJSON(chunk.SectionHierarchy),
			ChunkIndex:       chunk.ChunkIndex,
			TokenCount:       chunk.TokenCount,
			DocID:            doc.ID,
			SignalMeta:       chunk.SignalMeta,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("chunk %d: %s", chunk.ChunkIndex, err))
			continue
		}
		result.ChunksCreated++
	}

	codeRefs := ExtractCodeReferences(parsed.RawContent, idx.cfg.CodeFilePatterns)
	for _, ref := range codeRefs {
		cf, err := idx.store.GetCodeFileByPath(ref.FilePath)
		if err != nil {
			cf, err = idx.store.AddCodeFile(ref.FilePath, ref.Language, ref.RefType)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("code file %s: %s", ref.FilePath, err))
				continue
			}
		}

		err = idx.store.AddReference(doc.ID, cf.ID, ref.Context)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("reference %s: %s", ref.FilePath, err))
			continue
		}
		result.CodeFilesFound++
	}

	result.DocumentsIndexed++
	return nil
}

func (idx *Indexer) buildRelatedDocEdges(files []WalkResult) {
	// Build a map of code file path -> list of document file paths that reference it
	codeFileToDocPaths := make(map[string][]string)

	for _, file := range files {
		doc, err := idx.store.GetDocumentByPath(file.FilePath)
		if err != nil {
			continue
		}
		codeFiles, err := idx.store.GetReferencedCodeFiles(doc.ID)
		if err != nil {
			continue
		}
		for _, cf := range codeFiles {
			codeFileToDocPaths[cf.FilePath] = append(codeFileToDocPaths[cf.FilePath], file.FilePath)
		}
	}

	// For each shared code file, create RelatedDoc edges between the documents
	linked := make(map[string]bool)
	for _, docPaths := range codeFileToDocPaths {
		if len(docPaths) < 2 {
			continue
		}
		for i := 0; i < len(docPaths); i++ {
			for j := i + 1; j < len(docPaths); j++ {
				key := docPaths[i] + "|" + docPaths[j]
				if linked[key] {
					continue
				}
				linked[key] = true

				fromDoc, err := idx.store.GetDocumentByPath(docPaths[i])
				if err != nil {
					continue
				}
				toDoc, err := idx.store.GetDocumentByPath(docPaths[j])
				if err != nil {
					continue
				}
				idx.store.AddRelatedDoc(fromDoc.ID, toDoc.ID, "shared_code_references")
			}
		}
	}
}

func computeHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}
