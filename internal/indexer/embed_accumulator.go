package indexer

import (
	"fmt"
	"os"

	"librarian/internal/embedding"
)

// pendingDoc holds the per-document state buffered before its chunks have all
// been embedded. It is kept alive in the embedAccumulator until every chunk
// slot has been populated (either with a real vector or a nil indicating
// per-item failure), at which point finalizeDoc drains it into the store.
type pendingDoc struct {
	file    WalkResult
	parsed  *parsedDocMeta // lightweight metadata snapshot (no bulk content)
	chunks  []Chunk

	// vectors is pre-allocated to len(chunks); slots are filled as flush waves
	// complete. A nil slot means the embedding failed for that chunk position.
	vectors [][]float64

	// filledCount is the number of vector slots that have been populated
	// (either with a real embedding or with nil on per-item failure).
	// Used to confirm "all chunks received" without scanning the whole slice.
	filledCount int
}

// parsedDocMeta holds only the fields from ParsedDoc needed by finalizeDoc.
// Keeping this struct separate avoids retaining the full Units tree in memory
// while chunks from other docs are still in-flight.
type parsedDocMeta struct {
	title       string
	docType     string
	summary     string
	headings    []string
	frontmatter map[string]any
	contentHash string
	rawContent  string
	refs        []Reference
}

// chunkRef is the back-pointer stored in the flat pending buffer. It maps a
// single buffer slot (one chunk's EmbeddingText) back to its owning pendingDoc
// and the index within that doc's vectors slice.
type chunkRef struct {
	doc      *pendingDoc
	chunkIdx int // index within doc.vectors / doc.chunks
}

// embedAccumulator collects chunks from multiple files, flushes them in
// batchSize-sized waves via EmbedBatch, then finalizes each doc once all of
// its chunks have been scattered back. This is the core mechanism that lets
// IndexDocs pack chunks from different doc_dirs into the same EmbedBatch call.
//
// Usage pattern:
//
//	acc := newEmbedAccumulator(embedder, batchSize, idx, result, progress)
//	for _, file := range files {
//	    if err := acc.addFile(file, force); err != nil {
//	        result.Errors = append(...)
//	    }
//	}
//	acc.finalize()  // partial flush + finalize remaining pending docs
//
// Thread-safety: not thread-safe. All calls must be from a single goroutine.
type embedAccumulator struct {
	embedder  embedding.Embedder
	batchSize int
	idx       *Indexer
	result    *IndexResult
	progress  *indexProgress

	// pending collects chunks whose embeddings have not yet been requested.
	// Flushed when len(pending) >= batchSize.
	pending []chunkRef
	// pendingTexts mirrors pending with the actual embedding texts.
	pendingTexts []string

	// docs collects all pendingDoc entries in creation order. A doc is removed
	// (set to nil and memory released) once finalizeDoc runs for it, but the
	// slice is never compacted — iteration looks for non-nil entries.
	docs []*pendingDoc
}

const defaultAccumulatorBatchSize = 100

// newEmbedAccumulator creates an accumulator. batchSize <= 0 falls back to
// defaultAccumulatorBatchSize (mirrors the provider default so the accumulator
// aligns with the provider's internal wave size).
func newEmbedAccumulator(
	embedder embedding.Embedder,
	batchSize int,
	idx *Indexer,
	result *IndexResult,
	progress *indexProgress,
) *embedAccumulator {
	bs := batchSize
	if bs <= 0 {
		bs = defaultAccumulatorBatchSize
	}
	return &embedAccumulator{
		embedder:     embedder,
		batchSize:    bs,
		idx:          idx,
		result:       result,
		progress:     progress,
		pending:      make([]chunkRef, 0, bs),
		pendingTexts: make([]string, 0, bs),
	}
}

// addFile performs the collect phase for a single file:
//  1. Read → parse → computeHash.
//  2. Stale-row skip/delete (unchanged hash → result.Skipped++, return nil).
//  3. Chunk.
//  4. Append each chunk to the pending buffer; flush when full.
//
// Returns an error only for hard failures (read, parse, chunk, delete). Embed
// errors are soft — they land in result.Errors via flush / finalizeDoc.
func (a *embedAccumulator) addFile(file WalkResult, force bool) error {
	handler := a.idx.registry.HandlerFor(file.FilePath)
	if handler == nil {
		return fmt.Errorf("no handler registered for %s", file.FilePath)
	}

	content, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}

	parsed, err := a.idx.parseFile(handler, file, content)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	contentHash := computeHash(parsed.RawContent)

	// Skip-if-unchanged (unless --force).
	if existing, _ := a.idx.store.GetDocumentByPath(file.FilePath); existing != nil {
		if !force && existing.ContentHash == contentHash {
			a.result.Skipped++
			if a.progress != nil {
				a.progress.tick(file.FilePath, false)
			}
			return nil
		}
		if err := a.idx.store.DeleteDocument(existing.ID); err != nil {
			return fmt.Errorf("deleting stale doc row: %w", err)
		}
	}

	chunkCfg := ChunkConfig{
		MaxTokens:    a.idx.cfg.Chunking.MaxTokens,
		OverlapLines: a.idx.cfg.Chunking.OverlapLines,
		MinTokens:    a.idx.cfg.Chunking.MinTokens,
	}
	chunks, err := handler.Chunk(parsed, chunkCfg)
	if err != nil {
		return fmt.Errorf("chunking: %w", err)
	}

	meta := &parsedDocMeta{
		title:       parsed.Title,
		docType:     parsed.DocType,
		summary:     parsed.Summary,
		headings:    parsed.Headings,
		frontmatter: parsed.Frontmatter,
		contentHash: contentHash,
		rawContent:  parsed.RawContent,
		refs:        parsed.Refs,
	}

	doc := &pendingDoc{
		file:    file,
		parsed:  meta,
		chunks:  chunks,
		vectors: make([][]float64, len(chunks)),
	}
	a.docs = append(a.docs, doc)

	for i, c := range chunks {
		a.pending = append(a.pending, chunkRef{doc: doc, chunkIdx: i})
		a.pendingTexts = append(a.pendingTexts, c.EmbeddingText)

		if len(a.pending) >= a.batchSize {
			if flushErr := a.flush(); flushErr != nil {
				// Record but continue; don't abort the whole run.
				a.result.Errors = append(a.result.Errors, fmt.Sprintf("flush: %s", flushErr))
			}
		}
	}

	return nil
}

// flush calls EmbedBatch on the current pending buffer, scatters results back
// to their owning docs, then finalizes any doc whose last chunk was just filled.
//
// If EmbedBatch returns a hard error, all pending slots for the affected docs
// are left nil (embedding failed) and those docs will be skipped in finalizeDoc.
func (a *embedAccumulator) flush() error {
	if len(a.pending) == 0 {
		return nil
	}

	texts := a.pendingTexts
	refs := a.pending

	// Reset before the API call so concurrent (future) or re-entrant callers
	// don't double-add. Currently single-goroutine but good practice.
	a.pending = a.pending[:0]
	a.pendingTexts = a.pendingTexts[:0]

	vectors, err := a.embedder.EmbedBatch(texts)
	if err != nil {
		// Hard batch error: record an error per affected doc+chunk range, but
		// leave vectors nil (zero value) — finalizeDoc will see all-nil and skip.
		a.result.Errors = append(a.result.Errors, fmt.Sprintf("batch embed (%d chunks): %s", len(texts), err))
		// nil-fill for each doc affected by this batch
		for _, ref := range refs {
			ref.doc.filledCount++
			// vectors[ref.chunkIdx] is already nil (zero value for []float64)
		}
		a.tryFinalizeReadyDocs()
		return nil // not a hard return error; already recorded in result.Errors
	}

	// Scatter vectors back to their owning docs.
	for i, ref := range refs {
		var v []float64
		if i < len(vectors) {
			v = vectors[i]
		}
		ref.doc.vectors[ref.chunkIdx] = v
		ref.doc.filledCount++
	}

	a.tryFinalizeReadyDocs()
	return nil
}

// tryFinalizeReadyDocs scans all pending docs and finalizes those whose
// filledCount == len(chunks) (all chunks have been assigned a vector or nil).
func (a *embedAccumulator) tryFinalizeReadyDocs() {
	for i, doc := range a.docs {
		if doc == nil {
			continue
		}
		if doc.filledCount == len(doc.chunks) {
			a.finalizeDoc(doc)
			a.docs[i] = nil // release memory
		}
	}
}

// finalize flushes any remaining pending chunks and finalizes all docs that
// are still pending. Must be called exactly once after all addFile calls.
func (a *embedAccumulator) finalize() {
	if len(a.pending) > 0 {
		if err := a.flush(); err != nil {
			a.result.Errors = append(a.result.Errors, fmt.Sprintf("final flush: %s", err))
		}
	}
	// After final flush, all docs should be ready. Sweep once more.
	a.tryFinalizeReadyDocs()
}

// finalizeDoc runs the store-write tail for a single doc once all its chunk
// vectors have been resolved (some may be nil = per-item failure). Delegates
// to storeIndexedDoc which owns the shared logic; this wrapper records any
// AddDocument failure in result.Errors (the progress tick is handled inside
// storeIndexedDoc via the progress parameter).
func (a *embedAccumulator) finalizeDoc(doc *pendingDoc) {
	if err := a.idx.storeIndexedDoc(doc.file, doc.parsed, doc.chunks, doc.vectors, a.result, a.progress); err != nil {
		a.result.Errors = append(a.result.Errors, fmt.Sprintf("%s: creating document: %s", doc.file.FilePath, err))
	}
}

