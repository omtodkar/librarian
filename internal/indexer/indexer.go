package indexer

import (
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/store"
)

type Indexer struct {
	store    *store.Store
	cfg      *config.Config
	embedder embedding.Embedder
	registry *Registry

	// progressOverride forces a specific progress reporting mode on both
	// passes (docs + graph), bypassing the file-count + TTY auto-select
	// and any value in cfg.Graph.ProgressMode. Empty = use config / auto.
	// Set via SetProgressOverride from the CLI; decoupled from cfg so
	// transient per-run overrides don't mutate the shared *config.Config.
	progressOverride string
}

// SetProgressOverride forces the progress-reporting mode for subsequent
// IndexDirectory / IndexProjectGraph calls on this Indexer. Empty string
// clears the override, falling back to cfg.Graph.ProgressMode.
//
// Valid values: "verbose", "bar", "quiet", "silent" — see progress.go.
// Used by the CLI's --verbose / --quiet / --json flags.
func (idx *Indexer) SetProgressOverride(mode string) {
	idx.progressOverride = mode
}

// progressMode returns the effective progress mode for this run: the
// runtime override if set, otherwise the config value (which may itself
// be empty, meaning auto-select in newIndexProgress).
func (idx *Indexer) progressMode() string {
	if idx.progressOverride != "" {
		return idx.progressOverride
	}
	return idx.cfg.Graph.ProgressMode
}

type IndexResult struct {
	DocumentsIndexed int
	ChunksCreated    int
	CodeFilesFound   int
	Skipped          int
	Errors           []string
}

// GraphResult summarises the code-graph pass — the second pass that walks the
// project root and projects code-symbol Units into graph_nodes + edges. Counts
// are disjoint from IndexResult (files here are code files, not docs).
//
// Invariant: FilesScanned + FilesSkipped + FilesSkippedGenerated +
// FilesErrored == total files the walker returned. A file that errors
// before indexGraphFile completes (read failure, parse failure) is
// counted on FilesErrored only — not FilesScanned — so the four
// counters always sum to the walker output.
type GraphResult struct {
	FilesScanned          int // files the graph pass actually parsed and projected
	FilesSkipped          int // unchanged content_hash, skipped
	FilesSkippedGenerated int // skipped because a canonical generated-file banner was detected
	FilesErrored          int // attempted but errored (os.ReadFile / Parse / store write failures)
	SymbolsAdded          int // symbol nodes upserted
	EdgesAdded            int // contains + imports/calls/extends/implements edges
	Errors                []string
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

	// Share progress mode with the graph pass so `--json` / `--quiet` /
	// `--verbose` affect both loops consistently. The helper auto-selects
	// verbose below 500 files (today's default behaviour) unless overridden.
	progress := newIndexProgress(len(files), idx.progressMode())
	for _, file := range files {
		err := idx.indexFile(file, result, force)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
		}
		progress.tick(file.FilePath, err != nil)
	}
	progress.finish()

	// Populate graph: document and code-file nodes + mentions and shared_code_ref edges.
	idx.buildGraphEdges(files)

	return result, nil
}

// IndexProjectGraph runs the code-graph pass over rootDir, projecting code
// symbols and import/call/extends/implements edges into graph_nodes /
// graph_edges. Unlike IndexDirectory it produces no chunks or vectors —
// structural-only, so the knowledge base stays scoped to docs_dir.
//
// Formats handled by the docs pass (markdown, docx, xlsx, pptx, pdf) are
// skipped here so they aren't double-indexed when docs_dir sits under the
// project root. Per-file content hash gates incremental re-runs: unchanged
// files are counted as skipped and their existing symbols / edges stay
// untouched.
func (idx *Indexer) IndexProjectGraph(rootDir string, force bool) (*GraphResult, error) {
	result := &GraphResult{}

	walkCfg := GraphWalkConfig{
		HonorGitignore:  idx.cfg.Graph.HonorGitignore,
		ExcludePatterns: idx.cfg.Graph.ExcludePatterns,
		Roots:           idx.cfg.Graph.Roots,
		SkipFormats:     docsPassFormats,
	}

	files, err := WalkGraph(rootDir, walkCfg, idx.registry)
	if err != nil {
		return nil, fmt.Errorf("walking project root: %w", err)
	}
	if len(files) == 0 {
		return result, nil
	}

	progress := newIndexProgress(len(files), idx.progressMode())

	configuredWorkers := idx.cfg.Graph.MaxWorkers
	switch {
	case configuredWorkers >= 1:
		// Explicit worker count from config/CLI.
		idx.runGraphWorkers(files, result, force, configuredWorkers, progress)
	case len(files) < graphParallelismThreshold:
		// Small project: serial, no sampling overhead.
		idx.runGraphWorkers(files, result, force, 1, progress)
	default:
		// Auto mode on a non-trivial file count: sample the first few
		// files to measure per-file wall time, then parallelize the rest
		// with a worker count sized to the measured cost profile.
		idx.runAdaptiveGraphPass(files, result, force, progress)
	}
	progress.finish()
	return result, nil
}

// graphParallelismThreshold is the file-count below which the graph pass
// stays serial regardless of MaxWorkers=0 auto-mode — goroutine + channel
// setup costs more than the parallel parse savings for a handful of files.
const graphParallelismThreshold = 20

// graphWorkerCap bounds auto-selected worker count. SQLite writes serialize
// via the *sql.DB mutex, so beyond this the extra goroutines just queue
// on the DB lock and gain nothing while adding scheduling overhead.
const graphWorkerCap = 8

// adaptiveSampleSize is how many files the auto-worker path processes
// serially up-front to estimate per-file wall time before committing to a
// worker count for the remainder. Large enough to average out variance
// (one unusually big file won't dominate the measurement) but small enough
// that the sample-phase cost is negligible on projects with hundreds of
// files.
const adaptiveSampleSize = 10

// adaptiveWorkersForAvg maps a measured per-file average wall time to a
// worker-pool size. The buckets encode a simple rule: fast files (cheap
// parse) are overhead-bound so fewer workers win; slow files (big classes,
// heavy grammars) are compute-bound so more workers win. All results are
// capped by min(GOMAXPROCS, graphWorkerCap).
func adaptiveWorkersForAvg(avg time.Duration) int {
	switch {
	case avg < 2*time.Millisecond:
		return capWorkers(2)
	case avg < 10*time.Millisecond:
		return capWorkers(4)
	default:
		return capWorkers(graphWorkerCap)
	}
}

// capWorkers clamps a requested worker count to [1, min(GOMAXPROCS,
// graphWorkerCap)]. Keeps the auto-mode from ever running away even on
// hosts with very many cores — SQLite write contention makes the extra
// workers counterproductive beyond the cap.
func capWorkers(requested int) int {
	n := runtime.GOMAXPROCS(0)
	if n > graphWorkerCap {
		n = graphWorkerCap
	}
	if requested > n {
		requested = n
	}
	if requested < 1 {
		requested = 1
	}
	return requested
}

// runAdaptiveGraphPass processes the first adaptiveSampleSize files
// serially while timing them, then uses the measured average to pick a
// worker count for the remaining files. Sample files are real indexing
// work (tick'd through progress, counted on result) — no wasted compute.
// The worker-count decision is tested directly against adaptiveWorkersForAvg
// / capWorkers in progress_test.go; exposing the choice through this
// function's signature would add surface with no caller using it.
//
// Two correctness details:
//  1. The sample uses a local *GraphResult that merges into `shared` at
//     the end — same pattern as runGraphWorkers's parallel path. Keeping
//     result accumulation uniform across sample + parallel phases means
//     future changes to the merge logic (e.g. deduplication) don't have
//     to special-case the sample.
//  2. Files that hit the hash gate (FilesSkipped++) contribute no real
//     work to the measurement — a second incremental run where most
//     files are unchanged would average ≈ 0 ms and wrongly select 2
//     workers for the remainder. Only include actually-parsed files
//     when computing the per-file average; if none parsed, fall back to
//     the full worker cap because we have no cost signal to go on.
func (idx *Indexer) runAdaptiveGraphPass(files []WalkResult, shared *GraphResult, force bool, progress *indexProgress) {
	sampleSize := adaptiveSampleSize
	if sampleSize > len(files) {
		sampleSize = len(files)
	}

	sample := &GraphResult{}
	// Time only the indexGraphFile calls, not the progress.tick / accounting
	// work around them. The tick takes a mutex + Fprintf which on a TTY
	// with slow pipe can add milliseconds per iteration and skew the
	// measured per-file avg enough to nudge adaptiveWorkersForAvg into
	// the wrong bucket. Sum per-call elapsed instead.
	var parsedElapsed time.Duration
	for i := 0; i < sampleSize; i++ {
		file := files[i]
		t0 := time.Now()
		err := idx.indexGraphFile(file, sample, force)
		parsedElapsed += time.Since(t0)
		if err != nil {
			sample.Errors = append(sample.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
			sample.FilesErrored++
		}
		progress.tick(file.FilePath, err != nil)
	}
	mergeGraphResult(shared, sample)

	if sampleSize == len(files) {
		return
	}

	// Use only the files that actually parsed for the average — skipped
	// ones cost a GetCodeFileByPath + hash compare, not a Parse, so they
	// misrepresent the per-file cost of the remaining work.
	var workers int
	if sample.FilesScanned == 0 {
		workers = capWorkers(graphWorkerCap)
	} else {
		avgPerFile := parsedElapsed / time.Duration(sample.FilesScanned)
		workers = adaptiveWorkersForAvg(avgPerFile)
	}
	idx.runGraphWorkers(files[sampleSize:], shared, force, workers, progress)
}

// mergeGraphResult adds src's counters and errors into dst. Shared between
// the adaptive sample phase and the parallel worker merge so both paths
// aggregate identically.
func mergeGraphResult(dst, src *GraphResult) {
	dst.FilesScanned += src.FilesScanned
	dst.FilesSkipped += src.FilesSkipped
	dst.FilesSkippedGenerated += src.FilesSkippedGenerated
	dst.FilesErrored += src.FilesErrored
	dst.SymbolsAdded += src.SymbolsAdded
	dst.EdgesAdded += src.EdgesAdded
	dst.Errors = append(dst.Errors, src.Errors...)
}

// runGraphWorkers dispatches files across `workers` goroutines that each
// call indexGraphFile. Each worker accumulates counters and errors into a
// LOCAL *GraphResult to avoid lock contention on the hot path; after all
// files drain the local results are merged into the caller's shared result
// under one lock. The progress reporter is shared across workers; its
// internal mutex serialises ticks.
//
// Falls through to a straight serial loop when workers <= 1 — same cost as
// the pre-parallel code path, no goroutine overhead.
//
// Thread-safety notes:
//   - idx.store uses *sql.DB, goroutine-safe; SQLite WAL serialises writes.
//   - idx.registry.HandlerFor is read-only after init.
//   - CodeHandler.Parse creates a fresh sitter.NewParser per call — no
//     shared AST state across workers.
//   - os.ReadFile, sha256.Sum256, regexp.Regexp.Match (isGeneratedFile)
//     are all stateless.
func (idx *Indexer) runGraphWorkers(files []WalkResult, shared *GraphResult, force bool, workers int, progress *indexProgress) {
	if workers <= 1 {
		for _, file := range files {
			err := idx.indexGraphFile(file, shared, force)
			if err != nil {
				shared.Errors = append(shared.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
				shared.FilesErrored++
			}
			progress.tick(file.FilePath, err != nil)
		}
		return
	}

	jobs := make(chan WalkResult, workers)
	locals := make([]*GraphResult, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		local := &GraphResult{}
		locals[w] = local
		go func(local *GraphResult) {
			defer wg.Done()
			for file := range jobs {
				err := idx.indexGraphFile(file, local, force)
				if err != nil {
					local.Errors = append(local.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
					local.FilesErrored++
				}
				progress.tick(file.FilePath, err != nil)
			}
		}(local)
	}

	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	// Merge locals into the shared result. Per-counter sums are correct
	// because each worker only touched its own local; the shared result
	// carried zeroes in (caller checks after we return).
	for _, local := range locals {
		mergeGraphResult(shared, local)
	}
}

// docsPassFormats is the set of handler.Name() values the graph pass skips
// because the docs pass already indexes them. Keep this aligned with the
// extensions the markdown / office / pdf handlers register.
var docsPassFormats = map[string]bool{
	"markdown": true,
	"docx":     true,
	"xlsx":     true,
	"pptx":     true,
	"pdf":      true,
}

// indexGraphFile runs one file through the graph pass:
//  1. Compute content hash, skip if unchanged (incremental gate).
//  2. Parse the file via its registered handler.
//  3. Upsert the code_file node and refresh the stored hash.
//  4. Delete prior symbol nodes sourced to this file (so renamed/removed
//     symbols from the previous parse don't linger).
//  5. Project each symbol Unit into a symbol node + a contains edge from
//     the code_file node.
//  6. Project outbound parsed.Refs into import/call/extends/implements
//     edges, with the file node as the edge source.
//
// Errors during UpsertNode / UpsertEdge are treated as per-item failures
// (logged as per-file errors, loop continues) rather than fatal so one bad
// symbol doesn't abandon the rest of a large file.
func (idx *Indexer) indexGraphFile(file WalkResult, result *GraphResult, force bool) error {
	handler := idx.registry.HandlerFor(file.FilePath)
	if handler == nil {
		return nil
	}

	content, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}

	hash := computeHash(string(content))

	existing, _ := idx.store.GetCodeFileByPath(file.FilePath)
	if existing != nil && !force && existing.ContentHash == hash {
		result.FilesSkipped++
		return nil
	}

	// Banner-based generated-file detection. Runs after the hash gate (so
	// we read the file bytes only once — we already have `content` from
	// above) and before Parse (so we skip the tree-sitter cost on files
	// we're going to throw away anyway).
	//
	// If a file that was previously indexed has acquired a generator banner
	// since the last run, its stale symbols + code_files row need cleanup
	// so the graph doesn't keep claiming symbols that no longer belong.
	if idx.cfg.Graph.DetectGenerated && isGeneratedFile(content) {
		if existing != nil {
			// One transaction for all three deletes (symbols, code_file
			// graph node, code_files row) so a crash between them can't
			// leave partial state. Cascade on graph_edges cleans up
			// incident mentions / shared_code_ref / contains edges.
			if err := idx.store.DeleteGeneratedFile(file.FilePath); err != nil {
				return fmt.Errorf("cleaning up state for newly-generated file: %w", err)
			}
		}
		result.FilesSkippedGenerated++
		return nil
	}

	parsed, err := handler.Parse(file.FilePath, content)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	fileNodeID := store.CodeFileNodeID(file.FilePath)
	if err := idx.store.UpsertNode(store.Node{
		ID:         fileNodeID,
		Kind:       store.NodeKindCodeFile,
		Label:      file.FilePath,
		SourcePath: file.FilePath,
	}); err != nil {
		return fmt.Errorf("upserting code file node: %w", err)
	}

	if _, err := idx.store.AddOrUpdateCodeFile(file.FilePath, handler.Name(), hash); err != nil {
		return fmt.Errorf("updating code file row: %w", err)
	}

	// Wipe stale symbols before projecting fresh ones. Scoped to
	// kind='symbol' so docs-pass mentions/shared_code_ref edges survive.
	if err := idx.store.DeleteSymbolsForFile(file.FilePath); err != nil {
		return fmt.Errorf("deleting stale symbols: %w", err)
	}

	// Project code-symbol Units. Non-symbol kinds (section/key-path/page/…)
	// are skipped — those belong to the docs pass or non-code formats.
	for _, u := range parsed.Units {
		if !isSymbolKind(u.Kind) {
			continue
		}
		symID := store.SymbolNodeID(u.Path)
		if err := idx.store.UpsertNode(store.Node{
			ID:         symID,
			Kind:       store.NodeKindSymbol,
			Label:      u.Title,
			SourcePath: file.FilePath,
		}); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("symbol %s: %s", u.Path, err))
			continue
		}
		if err := idx.store.UpsertEdge(store.Edge{
			From: fileNodeID,
			To:   symID,
			Kind: store.EdgeKindContains,
		}); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("contains edge %s: %s", u.Path, err))
			continue
		}
		result.SymbolsAdded++
		result.EdgesAdded++
	}

	// Project outbound references. graphTargetID / graphNodeKindFromRef are
	// reused from the docs pass and handle import/call/extends/implements
	// via SymbolNodeID, code-file via CodeFileNodeID, config-key via
	// ConfigKeyNodeID — unsupported ref kinds skip silently.
	for _, ref := range parsed.Refs {
		targetID := graphTargetID(ref)
		if targetID == "" {
			continue
		}
		if err := idx.store.UpsertNode(store.Node{
			ID:         targetID,
			Kind:       graphNodeKindFromRef(ref),
			Label:      ref.Target,
			SourcePath: ref.Target,
		}); err != nil {
			continue
		}
		if err := idx.store.UpsertEdge(store.Edge{
			From: fileNodeID,
			To:   targetID,
			Kind: ref.Kind,
		}); err != nil {
			continue
		}
		result.EdgesAdded++
	}

	result.FilesScanned++
	return nil
}

// isSymbolKind reports whether a Unit.Kind represents a code symbol worth
// projecting into the graph. Matches the symbol-bearing kinds listed in
// Unit's godoc; section/paragraph/page/row/table/key-path are non-code
// shapes and stay out of graph_nodes{kind='symbol'}.
func isSymbolKind(kind string) bool {
	switch kind {
	case "function", "method", "constructor",
		"class", "interface", "enum", "record",
		"type", "field":
		return true
	}
	return false
}

func (idx *Indexer) IndexSingleFile(filePath, absPath string, force bool) (*IndexResult, error) {
	result := &IndexResult{}

	file := WalkResult{
		FilePath: filePath,
		AbsPath:  absPath,
	}
	if err := idx.indexFile(file, result, force); err != nil {
		return nil, err
	}

	// Refresh graph edges over all indexed documents so shared_code_ref edges
	// stay consistent after a single-file update. Cheaper than selectively
	// invalidating the subset that references files this doc touched.
	docs, err := idx.store.ListDocuments()
	if err != nil {
		return nil, fmt.Errorf("refresh graph after single-file index: %w", err)
	}
	files := make([]WalkResult, 0, len(docs))
	for _, d := range docs {
		files = append(files, WalkResult{FilePath: d.FilePath})
	}
	idx.buildGraphEdges(files)

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

	// Skip-if-unchanged (unless --force). In both cases, any prior version of
	// the document is deleted before re-insert so we never leave orphans.
	if existing, _ := idx.store.GetDocumentByPath(file.FilePath); existing != nil {
		if !force && existing.ContentHash == contentHash {
			result.Skipped++
			return nil
		}
		idx.store.DeleteDocument(existing.ID)
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

	doc, err := idx.store.AddDocument(store.AddDocumentInput{
		FilePath:    file.FilePath,
		Title:       parsed.Title,
		DocType:     parsed.DocType,
		Summary:     parsed.Summary,
		Headings:    HeadingsToJSON(parsed.Headings),
		Frontmatter: FrontmatterToJSON(parsed.Frontmatter),
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
		// GetCodeFileByPath returns (nil, nil) when not found (post round
		// 2 semantic). The docs pass inserts on first reference.
		cf, err := idx.store.GetCodeFileByPath(ref.FilePath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("code file lookup %s: %s", ref.FilePath, err))
			continue
		}
		if cf == nil {
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

	// Handler-emitted structured refs (tree-sitter call/import edges, config-key
	// references, etc.) flow straight into graph_edges. These bypass the
	// refs/code_files tables because their targets (symbols, config keys) don't
	// fit that file-centric schema. For a markdown file where the handler only
	// populates codeRefs via the regex pass above, parsed.Refs is empty and
	// this loop is a no-op.
	for _, ref := range parsed.Refs {
		targetID := graphTargetID(ref)
		if targetID == "" {
			continue
		}
		idx.store.UpsertNode(store.Node{
			ID:         targetID,
			Kind:       graphNodeKindFromRef(ref),
			Label:      ref.Target,
			SourcePath: ref.Target,
		})
		idx.store.UpsertEdge(store.Edge{
			From: store.DocNodeID(doc.ID),
			To:   targetID,
			Kind: ref.Kind,
		})
	}

	result.DocumentsIndexed++
	return nil
}

// graphTargetID maps a Reference to a namespaced graph node id via the store's
// node-id constructors. Returns "" for reference kinds that don't yet have a
// node-kind mapping (the edge is skipped rather than invented).
func graphTargetID(ref Reference) string {
	switch ref.Kind {
	case "code-file":
		return store.CodeFileNodeID(ref.Target)
	case "import", "call", "extends", "implements":
		return store.SymbolNodeID(ref.Target)
	case "config-key":
		return store.ConfigKeyNodeID(ref.Target)
	}
	return ""
}

// graphNodeKindFromRef maps a Reference kind to the store NodeKind of its
// target. Must agree with graphTargetID on which kinds are supported.
func graphNodeKindFromRef(ref Reference) string {
	switch ref.Kind {
	case "code-file":
		return store.NodeKindCodeFile
	case "import", "call", "extends", "implements":
		return store.NodeKindSymbol
	case "config-key":
		return store.NodeKindConfigKey
	}
	return "unknown"
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
