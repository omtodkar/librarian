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

	// Populate graph: document and code-file nodes + mentions and shared_code_ref edges.
	idx.buildGraphEdges(files)

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

// buildGraphEdges is the post-indexing pass that projects document/code-file/refs
// data into the generic graph_nodes + graph_edges tables.
//
// Emits:
//   - a "document" node per indexed doc
//   - a "code_file" node per referenced code file
//   - a "mentions" edge from each doc to every code file it references
//   - a "shared_code_ref" edge between docs that reference the same code file
//     (one direction; symmetric semantics handled at query time)
//
// Future handlers (code via tree-sitter, config via YAML, etc.) will add their
// own node kinds and edge kinds to this same table pair.
func (idx *Indexer) buildGraphEdges(files []WalkResult) {
	codeFileToDocIDs := make(map[string][]string)

	for _, file := range files {
		doc, err := idx.store.GetDocumentByPath(file.FilePath)
		if err != nil {
			continue
		}

		if err := idx.store.UpsertNode(store.Node{
			ID:         store.DocNodeID(doc.ID),
			Kind:       store.NodeKindDocument,
			Label:      doc.Title,
			SourcePath: doc.FilePath,
		}); err != nil {
			continue
		}

		codeFiles, err := idx.store.GetReferencedCodeFiles(doc.ID)
		if err != nil {
			continue
		}
		for _, cf := range codeFiles {
			if err := idx.store.UpsertNode(store.Node{
				ID:         store.CodeFileNodeID(cf.FilePath),
				Kind:       store.NodeKindCodeFile,
				Label:      cf.FilePath,
				SourcePath: cf.FilePath,
			}); err != nil {
				continue
			}
			idx.store.UpsertEdge(store.Edge{
				From: store.DocNodeID(doc.ID),
				To:   store.CodeFileNodeID(cf.FilePath),
				Kind: store.EdgeKindMentions,
			})
			codeFileToDocIDs[cf.FilePath] = append(codeFileToDocIDs[cf.FilePath], doc.ID)
		}
	}

	linked := make(map[string]bool)
	for _, docIDs := range codeFileToDocIDs {
		if len(docIDs) < 2 {
			continue
		}
		for i := 0; i < len(docIDs); i++ {
			for j := i + 1; j < len(docIDs); j++ {
				a, b := docIDs[i], docIDs[j]
				if a == b {
					continue
				}
				key := a + "|" + b
				if linked[key] {
					continue
				}
				linked[key] = true
				idx.store.UpsertEdge(store.Edge{
					From: store.DocNodeID(a),
					To:   store.DocNodeID(b),
					Kind: store.EdgeKindSharedCodeRef,
				})
			}
		}
	}
}

func computeHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}
