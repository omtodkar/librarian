package store

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) AddChunk(input AddChunkInput) (*DocChunk, error) {
	signalMeta := input.SignalMeta
	if signalMeta == "" {
		signalMeta = "{}"
	}

	// Run the (model, dim) mismatch check BEFORE the chunk row is inserted.
	// Otherwise a config-level model swap would leave every doc's first
	// chunk row committed with no vector — invisible to search but visible
	// to GetChunksForDocument.
	if err := s.ensureVecTable(input.Model, len(input.Vector)); err != nil {
		return nil, fmt.Errorf("add_chunk ensure_vec_table: %w", err)
	}

	res, err := s.db.Exec(`
		INSERT INTO doc_chunks (file_path, section_heading, section_hierarchy, chunk_index, content, summary, token_count, doc_id, signal_meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		input.FilePath, input.SectionHeading, input.SectionHierarchy,
		input.ChunkIndex, input.Content, input.Summary, input.TokenCount, input.DocID, signalMeta,
	)
	if err != nil {
		return nil, fmt.Errorf("add_chunk: %w", err)
	}

	chunkID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add_chunk last_insert_id: %w", err)
	}

	vecBytes := float64sToFloat32Bytes(input.Vector)
	_, err = s.db.Exec(`INSERT INTO doc_chunk_vectors (chunk_id, embedding) VALUES (?, ?)`,
		chunkID, vecBytes)
	if err != nil {
		return nil, fmt.Errorf("add_chunk vector: %w", err)
	}

	return &DocChunk{
		ID:               strconv.FormatInt(chunkID, 10),
		FilePath:         input.FilePath,
		SectionHeading:   input.SectionHeading,
		SectionHierarchy: input.SectionHierarchy,
		ChunkIndex:       input.ChunkIndex,
		Content:          input.Content,
		Summary:          input.Summary,
		TokenCount:       input.TokenCount,
		SignalMeta:       signalMeta,
	}, nil
}

type scoredChunk struct {
	chunk      DocChunk
	distance   float64
	finalScore float64
}

// vectorSearch fetches up to fetchLimit candidates ordered by cosine distance.
// Internal helper shared by SearchChunks and HybridSearch.
func (s *Store) vectorSearch(vector []float64, fetchLimit int) ([]scoredChunk, error) {
	vecBytes := float64sToFloat32Bytes(vector)
	rows, err := s.db.Query(`
		SELECT c.id, c.file_path, c.section_heading, c.section_hierarchy, c.chunk_index, c.content, c.summary, c.token_count, c.signal_meta, v.distance
		FROM doc_chunk_vectors v
		JOIN doc_chunks c ON c.id = v.chunk_id
		WHERE v.embedding MATCH ?
		  AND k = ?
		ORDER BY v.distance, c.id`, vecBytes, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("search_chunks: %w", err)
	}
	defer rows.Close()

	var candidates []scoredChunk
	for rows.Next() {
		var sc scoredChunk
		var id int64
		if err := rows.Scan(&id, &sc.chunk.FilePath, &sc.chunk.SectionHeading,
			&sc.chunk.SectionHierarchy, &sc.chunk.ChunkIndex, &sc.chunk.Content,
			&sc.chunk.Summary, &sc.chunk.TokenCount, &sc.chunk.SignalMeta, &sc.distance); err != nil {
			return nil, fmt.Errorf("search_chunks scan: %w", err)
		}
		sc.chunk.ID = strconv.FormatInt(id, 10)
		candidates = append(candidates, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// ftsSearch runs an FTS5 BM25 query and returns up to fetchLimit candidates
// ordered by relevance (best first). Returns nil without error when queryText
// is empty after sanitization or when the FTS table has no matching rows.
func (s *Store) ftsSearch(queryText string, fetchLimit int) ([]scoredChunk, error) {
	ftsQuery := buildFTSQuery(queryText)
	if ftsQuery == "" {
		return nil, nil
	}

	rows, err := s.db.Query(`
		SELECT c.id, c.file_path, c.section_heading, c.section_hierarchy, c.chunk_index, c.content, c.summary, c.token_count, c.signal_meta
		FROM doc_chunks_fts f
		JOIN doc_chunks c ON c.id = f.rowid
		WHERE doc_chunks_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, ftsQuery, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("fts_search: %w", err)
	}
	defer rows.Close()

	var candidates []scoredChunk
	for rows.Next() {
		var sc scoredChunk
		var id int64
		if err := rows.Scan(&id, &sc.chunk.FilePath, &sc.chunk.SectionHeading,
			&sc.chunk.SectionHierarchy, &sc.chunk.ChunkIndex, &sc.chunk.Content,
			&sc.chunk.Summary, &sc.chunk.TokenCount, &sc.chunk.SignalMeta); err != nil {
			return nil, fmt.Errorf("fts_search scan: %w", err)
		}
		sc.chunk.ID = strconv.FormatInt(id, 10)
		candidates = append(candidates, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// rerankDefaultTopK is the number of signal-reranked candidates fed to the
// cross-encoder when the caller does not specify a topK override.
const rerankDefaultTopK = 20

// SearchChunks performs vector KNN search, signal-weighted re-ranking, and
// optionally cross-encoder reranking when a Reranker is configured.
func (s *Store) SearchChunks(query string, vector []float64, limit int) ([]DocChunk, error) {
	// vec0 may be absent on a fresh DB, between ClearVectorState and the
	// first AddChunk of a reindex, or in a long-lived process (MCP server)
	// that held an open Store while another process dropped the table.
	// Returning an empty result matches "no matches" semantics instead of
	// surfacing a sqlite "no such table" error.
	if !s.vecTableReady {
		return nil, nil
	}

	fetchLimit := limit * 3
	if fetchLimit < 10 {
		fetchLimit = 10
	}

	candidates, err := s.vectorSearch(vector, fetchLimit)
	if err != nil {
		return nil, err
	}

	if s.reranker == nil {
		return rerankWithSignals(candidates, limit), nil
	}

	// Cross-encoder path: signal-rerank to topK, then cross-encoder rerank.
	topK := s.rerankTopK
	if topK <= 0 {
		topK = rerankDefaultTopK
	}
	if topK < limit {
		topK = limit
	}

	signalTop := signalRankToK(candidates, topK)

	docs := make([]string, len(signalTop))
	for i, sc := range signalTop {
		docs[i] = sc.chunk.Content
	}

	scores, rerankErr := s.reranker.Rerank(query, docs)
	if rerankErr != nil {
		slog.Debug("reranker fallback", "error", rerankErr)
		if len(signalTop) > limit {
			signalTop = signalTop[:limit]
		}
		return toDocChunks(signalTop), nil
	}
	if len(scores) != len(signalTop) {
		slog.Debug("reranker score count mismatch, falling back", "want", len(signalTop), "got", len(scores))
		if len(signalTop) > limit {
			signalTop = signalTop[:limit]
		}
		return toDocChunks(signalTop), nil
	}

	// Apply cross-encoder scores, re-sort descending, truncate to limit.
	for i := range signalTop {
		signalTop[i].finalScore = scores[i]
	}
	sort.Slice(signalTop, func(i, j int) bool {
		return signalTop[i].finalScore > signalTop[j].finalScore
	})
	if len(signalTop) > limit {
		signalTop = signalTop[:limit]
	}
	return toDocChunks(signalTop), nil
}

// HybridSearch combines vector KNN and FTS5 BM25 results via Reciprocal Rank
// Fusion (RRF), then applies signal-weighted re-ranking over the merged set.
// This surfaces exact literal matches (e.g. specific identifiers) that pure
// vector search may rank below semantically similar but lexically different chunks.
func (s *Store) HybridSearch(vector []float64, queryText string, limit int) ([]DocChunk, error) {
	if !s.vecTableReady {
		return nil, nil
	}

	fetchLimit := limit * 3
	if fetchLimit < 10 {
		fetchLimit = 10
	}

	vecCandidates, err := s.vectorSearch(vector, fetchLimit)
	if err != nil {
		return nil, err
	}

	ftsCandidates, err := s.ftsSearch(queryText, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("hybrid_search: %w", err)
	}

	merged := mergeRRF(vecCandidates, ftsCandidates)
	return hybridRerankWithSignals(merged, limit), nil
}

// mergeRRF applies Reciprocal Rank Fusion over two ranked lists, returning a
// merged list with finalScore = sum of 1/(k+rank) contributions. k=60 is the
// standard constant that dampens the impact of very-high-rank outliers.
func mergeRRF(vecResults, ftsResults []scoredChunk) []scoredChunk {
	const k = 60.0
	scores := make(map[string]*scoredChunk, len(vecResults)+len(ftsResults))

	for rank, sc := range vecResults {
		rrfScore := 1.0 / (k + float64(rank+1))
		entry := sc
		entry.finalScore = rrfScore
		scores[sc.chunk.ID] = &entry
	}

	for rank, sc := range ftsResults {
		rrfScore := 1.0 / (k + float64(rank+1))
		if existing, ok := scores[sc.chunk.ID]; ok {
			existing.finalScore += rrfScore
		} else {
			entry := sc
			entry.finalScore = rrfScore
			scores[sc.chunk.ID] = &entry
		}
	}

	result := make([]scoredChunk, 0, len(scores))
	for _, sc := range scores {
		result = append(result, *sc)
	}
	return result
}

// hybridRerankWithSignals applies signal boosting on top of pre-computed RRF
// scores and returns the top-limit chunks. Analogous to rerankWithSignals but
// treats finalScore as the base (RRF output) rather than recomputing from distance.
func hybridRerankWithSignals(candidates []scoredChunk, limit int) []DocChunk {
	for i := range candidates {
		boost := computeMetadataBoost(candidates[i].chunk.SignalMeta)
		candidates[i].finalScore = 0.90*candidates[i].finalScore + 0.10*boost
	}

	sort.Slice(candidates, func(i, j int) bool {
		return rankLess(candidates[i], candidates[j])
	})

	candidates = deduplicateByContent(candidates)

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	chunks := make([]DocChunk, len(candidates))
	for i, sc := range candidates {
		chunks[i] = sc.chunk
	}
	return chunks
}

// signalRankToK applies signal-weighted scoring, sorts, deduplicates, and
// truncates to the top-k candidates. Returns []scoredChunk so callers that
// need to re-rank further (e.g. cross-encoder) can work with the raw scores.
func signalRankToK(candidates []scoredChunk, k int) []scoredChunk {
	for i := range candidates {
		vectorScore := 1.0 - candidates[i].distance
		boost := computeMetadataBoost(candidates[i].chunk.SignalMeta)
		candidates[i].finalScore = 0.90*vectorScore + 0.10*boost
	}
	sort.Slice(candidates, func(i, j int) bool {
		return rankLess(candidates[i], candidates[j])
	})
	candidates = deduplicateByContent(candidates)
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates
}

// toDocChunks extracts the chunk field from a slice of scoredChunks.
func toDocChunks(candidates []scoredChunk) []DocChunk {
	chunks := make([]DocChunk, len(candidates))
	for i, sc := range candidates {
		chunks[i] = sc.chunk
	}
	return chunks
}

func rerankWithSignals(candidates []scoredChunk, limit int) []DocChunk {
	return toDocChunks(signalRankToK(candidates, limit))
}

// dedupWindow is the maximum number of top-ranked candidates scanned for
// byte-identical content. Only chunks within this window are deduplicated.
const dedupWindow = 20

// deduplicateByContent removes byte-identical chunks within the first
// dedupWindow entries of a sorted candidates slice. The highest-ranked
// instance (index 0 = best) is kept as the representative; its Duplicates
// field is set to the file paths of all removed copies. Singletons pass
// through unchanged. Candidates beyond dedupWindow are appended as-is.
func deduplicateByContent(candidates []scoredChunk) []scoredChunk {
	topN := len(candidates)
	if topN > dedupWindow {
		topN = dedupWindow
	}

	// Pass 1: record the representative index and duplicate paths per hash.
	type hashEntry struct {
		repIdx   int
		dupPaths []string
	}
	seen := make(map[[32]byte]*hashEntry, topN)
	for i := 0; i < topN; i++ {
		h := sha256.Sum256([]byte(candidates[i].chunk.Content))
		if e, ok := seen[h]; ok {
			e.dupPaths = append(e.dupPaths, candidates[i].chunk.FilePath)
		} else {
			seen[h] = &hashEntry{repIdx: i}
		}
	}

	// Short-circuit: if no hash appears ≥2 times, nothing to do.
	hasDups := false
	for _, e := range seen {
		if len(e.dupPaths) > 0 {
			hasDups = true
			break
		}
	}
	if !hasDups {
		return candidates
	}

	// Pass 2: build a set of representative indices → their duplicate paths.
	repSet := make(map[int][]string, len(seen))
	for _, e := range seen {
		repSet[e.repIdx] = e.dupPaths
	}

	result := make([]scoredChunk, 0, len(candidates))
	for i := 0; i < topN; i++ {
		dupPaths, isRep := repSet[i]
		if !isRep {
			continue // duplicate — drop it
		}
		c := candidates[i]
		if len(dupPaths) > 0 {
			c.chunk.Duplicates = dupPaths
		}
		result = append(result, c)
	}
	// Candidates beyond the dedup window pass through unchanged.
	result = append(result, candidates[topN:]...)
	return result
}

// rankLess defines a total order for scored chunks: descending by finalScore,
// ascending by numeric chunk id on ties. A total order makes sort.Slice output
// deterministic regardless of input permutation, which keeps Claude's prompt-cache
// prefix identical across repeated identical queries (cache TTL = 5 min).
func rankLess(a, b scoredChunk) bool {
	if a.finalScore != b.finalScore {
		return a.finalScore > b.finalScore
	}
	idA, _ := strconv.ParseInt(a.chunk.ID, 10, 64)
	idB, _ := strconv.ParseInt(b.chunk.ID, 10, 64)
	return idA < idB
}

func computeMetadataBoost(signalMetaJSON string) float64 {
	if signalMetaJSON == "" || signalMetaJSON == "{}" {
		return 0.0
	}

	// Fields mirror the JSON shape emitted by SignalsToJSON (signals.go).
	// Extending this struct is safe; unknown keys are ignored by Unmarshal.
	var signals struct {
		InlineLabels []string `json:"inline_labels"`
		RiskMarkers  []string `json:"risk_markers"`
		Todos        []string `json:"todos"`
		Rationale    []string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(signalMetaJSON), &signals); err != nil {
		return 0.0
	}

	boost := 0.0
	highValueLabels := map[string]bool{
		"warning":   true,
		"decision":  true,
		"important": true,
	}

	for _, label := range signals.InlineLabels {
		if highValueLabels[label] {
			boost += 0.3
		} else {
			boost += 0.1
		}
	}
	for range signals.RiskMarkers {
		boost += 0.2
	}
	// TODOs and rationale (WHY/NOTE) carry caveats or design intent the author
	// flagged; a small boost surfaces them without drowning out real warnings.
	for range signals.Todos {
		boost += 0.05
	}
	for range signals.Rationale {
		boost += 0.05
	}

	if boost > 1.0 {
		boost = 1.0
	}
	return boost
}

func (s *Store) GetChunksForDocument(docID string) ([]DocChunk, error) {
	rows, err := s.db.Query(`
		SELECT id, file_path, section_heading, section_hierarchy, chunk_index, content, summary, token_count, signal_meta
		FROM doc_chunks WHERE doc_id = ? ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("get_chunks_for_document: %w", err)
	}
	defer rows.Close()

	var chunks []DocChunk
	for rows.Next() {
		var chunk DocChunk
		var id int64
		if err := rows.Scan(&id, &chunk.FilePath, &chunk.SectionHeading,
			&chunk.SectionHierarchy, &chunk.ChunkIndex, &chunk.Content, &chunk.Summary,
			&chunk.TokenCount, &chunk.SignalMeta); err != nil {
			return nil, fmt.Errorf("get_chunks_for_document scan: %w", err)
		}
		chunk.ID = strconv.FormatInt(id, 10)
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// buildFTSQuery converts a natural language query string into an FTS5 OR
// query: each token is double-quoted and joined with OR so BM25 ranks on any
// token match. Double-quoting prevents FTS5 reserved words (AND, OR, NOT,
// NEAR) from being interpreted as operators — they are treated as literals.
// Returns "" when no valid terms remain after sanitization.
func buildFTSQuery(query string) string {
	clean := sanitizeFTSQuery(query)
	words := strings.Fields(clean)
	if len(words) == 0 {
		return ""
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = `"` + w + `"`
	}
	return strings.Join(quoted, " OR ")
}

// sanitizeFTSQuery removes characters that carry special meaning in the FTS5
// query language to prevent syntax errors on arbitrary user input.
func sanitizeFTSQuery(query string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '*', '(', ')', '^', '{', '}', '[', ']':
			return -1
		}
		return r
	}, query)
}

// float64sToFloat32Bytes converts a []float64 vector to little-endian []byte
// of float32 values, as expected by sqlite-vec.
func float64sToFloat32Bytes(vec []float64) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return buf
}
