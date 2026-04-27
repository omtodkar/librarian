package indexer

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/store"
	"librarian/internal/summarizer"
)

type Indexer struct {
	store      *store.Store
	cfg        *config.Config
	embedder   embedding.Embedder
	summarizer summarizer.Summarizer
	registry   *Registry

	// progressOverride forces a specific progress reporting mode on both
	// passes (docs + graph), bypassing the file-count + TTY auto-select
	// and any value in cfg.Graph.ProgressMode. Empty = use config / auto.
	// Set via SetProgressOverride from the CLI; decoupled from cfg so
	// transient per-run overrides don't mutate the shared *config.Config.
	progressOverride string

	// pythonSrcRoots is cfg.Python.SrcRoots resolved to absolute, cleaned
	// paths once at construction time so per-file handler dispatch doesn't
	// repeat the join/clean work. Consumed by parseFile when building
	// ParseContext. Nil when no roots are configured.
	pythonSrcRoots []string

	// pythonPackageCache memoizes directory → package-parts lookups within a
	// single IndexProjectGraph pass. Reset at the start of every pass so
	// content added mid-session (fixture files in tests, user edits between
	// re-indexes) is observed afresh. Nil pointer semantics: parseFile
	// passes &idx.pythonPackageCache through ParseContext, and the resolver
	// treats a nil inner map as no-op (no caching). A fresh sync.Map per
	// run is cheaper than threading allocation decisions through the call
	// graph and makes concurrent access safe by default.
	pythonPackageCache sync.Map

	// currentBufManifest holds the per-run buf codegen manifest while the
	// post-graph-pass resolvers run. Populated by IndexProjectGraph just
	// before buildImplementsRPCEdges and cleared immediately after so no
	// stale data leaks into a later call. Nil when no buf.gen.yaml was
	// discovered — the resolver treats nil as "no manifest" and falls
	// back to name-only matching.
	//
	// Kept here rather than added to buildImplementsRPCEdges' signature so
	// lib-6wz's API stays unchanged — per the lib-4kb scope discipline.
	currentBufManifest *BufManifest
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
		store:          s,
		cfg:            cfg,
		embedder:       embedder,
		summarizer:     summarizer.Noop{},
		registry:       reg,
		pythonSrcRoots: resolvePythonSrcRoots(cfg),
	}
}

// SetSummarizer installs an optional summarizer for per-chunk summary
// generation. Call before IndexDirectory / IndexSingleFile. The default
// is summarizer.Noop (no summarization).
func (idx *Indexer) SetSummarizer(s summarizer.Summarizer) {
	idx.summarizer = s
}

// resolvePythonSrcRoots produces the absolute, cleaned src-root list the
// Python import resolver matches against. Two sources are merged:
//
//  1. Explicit cfg.Python.SrcRoots (user opt-in, matched first — if a user
//     bothered to configure something, they expect it to win).
//  2. pyproject.toml auto-detection (setuptools / Poetry / Hatch layouts),
//     appended after explicit entries. Dedupes on cleaned absolute path so
//     a user redundantly listing what the TOML already declares doesn't
//     double-count.
//
// Malformed pyproject.toml emits one stderr warning and drops auto-detect
// for this run — explicit config still applies. Missing pyproject.toml is
// silent (not every indexed project is Python).
//
// Returns nil when ProjectRoot is empty (tests without a workspace) or
// neither source yields anything.
func resolvePythonSrcRoots(cfg *config.Config) []string {
	if cfg == nil || cfg.ProjectRoot == "" {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(abs string) {
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}

	for _, r := range cfg.Python.SrcRoots {
		if r == "" {
			continue
		}
		if filepath.IsAbs(r) {
			add(filepath.Clean(r))
			continue
		}
		add(filepath.Clean(filepath.Join(cfg.ProjectRoot, r)))
	}

	detected, err := detectPythonSrcRootsFromPyproject(cfg.ProjectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "librarian: %s; python src-root auto-detect disabled\n", err)
	}
	for _, d := range detected {
		add(d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseFile dispatches file parsing through FileHandlerCtx when the handler
// implements it (Python today for relative-import resolution), passing AbsPath
// and cached per-language config. Falls back to the legacy Parse for
// handlers that haven't opted in.
func (idx *Indexer) parseFile(handler FileHandler, file WalkResult, content []byte) (*ParsedDoc, error) {
	if ctxh, ok := handler.(FileHandlerCtx); ok {
		return ctxh.ParseCtx(file.FilePath, content, ParseContext{
			AbsPath:        file.AbsPath,
			ProjectRoot:    idx.cfg.ProjectRoot,
			PythonSrcRoots: idx.pythonSrcRoots,
			PackageCache:   &idx.pythonPackageCache,
		})
	}
	return handler.Parse(file.FilePath, content)
}

// resetPythonPackageCache clears the memo for a fresh graph pass. Called from
// IndexProjectGraph before any file is walked so the cache reflects the
// on-disk state at pass-start. Swapping the whole sync.Map is preferable to
// iterating + deleting since fresh passes are expected to populate most
// entries from scratch anyway.
func (idx *Indexer) resetPythonPackageCache() {
	idx.pythonPackageCache = sync.Map{}
}

func (idx *Indexer) IndexDirectory(docsDir string, force bool) (*IndexResult, error) {
	result := &IndexResult{}

	// Fail fast on a model swap — otherwise every chunk pays a full embedding
	// API call before AddChunk's per-chunk check would surface the mismatch.
	if err := idx.store.VerifyActiveEmbedder(idx.embedder.Model()); err != nil {
		return nil, err
	}

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

	idx.resetPythonPackageCache()

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

	// affected accumulates source_paths of files that held cross-file edges
	// into any reindexed file's symbols. After the parallel pass completes,
	// those files are force-reindexed serially in runGraphWorkers to reconstitute
	// edges that DeleteSymbolsForFile's FK cascade destroyed. Using a sync.Map
	// lets workers write concurrently without a mutex in the hot path.
	var affected sync.Map

	configuredWorkers := idx.cfg.Graph.MaxWorkers
	switch {
	case configuredWorkers >= 1:
		// Explicit worker count from config/CLI.
		idx.runGraphWorkers(files, result, force, configuredWorkers, progress, &affected)
	case len(files) < graphParallelismThreshold:
		// Small project: serial, no sampling overhead.
		idx.runGraphWorkers(files, result, force, 1, progress, &affected)
	default:
		// Auto mode on a non-trivial file count: sample the first few
		// files to measure per-file wall time, then parallelize the rest
		// with a worker count sized to the measured cost profile.
		idx.runAdaptiveGraphPass(files, result, force, progress, &affected)
	}
	progress.finish()

	// Buf codegen manifest: harvest buf.gen.yaml plugin lists and proto
	// file-level options (both persisted on code_file graph_node metadata
	// during the per-file pass) to compute per-proto-file codegen path
	// prefixes. Runs before the implements_rpc resolver so the resolver can
	// consult it via idx.currentBufManifest for per-candidate source-path
	// tightening (lib-4kb). nil manifest means no buf.gen.yaml in the
	// project — the resolver falls back to lib-6wz's name-only matching
	// without losing edges.
	//
	// The manifest is stashed on the Indexer rather than threaded through
	// buildImplementsRPCEdges' signature so lib-6wz's resolver API stays
	// unchanged (scope discipline from the lib-4kb bead). Cleared after
	// the resolver runs so no stale data leaks into a later invocation.
	idx.currentBufManifest = idx.buildBufManifest(result)
	defer func() { idx.currentBufManifest = nil }()

	// Cross-language resolver pass: connect generated-code symbols back to
	// their proto rpc declarations via naming conventions. Runs after all
	// per-file projection has landed, so every candidate sym: node we probe
	// exists in the store if its file was indexed this run or a prior run.
	// Works off persisted rpc nodes (no proto-file re-parse), so incremental
	// re-indexes where the .proto was hash-skipped still link newly added
	// Go/Dart/TS implementers. Counted as edges on the shared result;
	// per-file counters are unchanged.
	idx.buildImplementsRPCEdges(result)

	// Call-site detector: emit call_rpc edges from TS/JS call sites to their
	// proto rpc declarations. Runs after buildImplementsRPCEdges so all
	// connect-es stub symbol nodes exist when the call-site resolver probes them.
	idx.buildCallRPCEdges(result)

	// Dart call-site detector: emit call_rpc edges from Flutter/Dart call sites
	// to their proto rpc declarations. Runs after buildCallRPCEdges so the
	// TS detector's edges are already committed (ordering within the post-pass
	// resolvers is otherwise arbitrary, but keeping Dart after TS is consistent
	// with the bead dependency order lib-4g2.3 → lib-4g2.4).
	idx.buildDartCallRPCEdges(result)

	// Test-subject linker: emit tests edges from test files to their likely
	// subject files via path-naming conventions (lib-8bg). Runs after all
	// per-file projection so every code_file node exists in the store.
	if idx.cfg.Graph.TestEdges.Enabled {
		idx.buildTestEdges(result)
	}

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
func (idx *Indexer) runAdaptiveGraphPass(files []WalkResult, shared *GraphResult, force bool, progress *indexProgress, affected *sync.Map) {
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
		err := idx.indexGraphFile(file, sample, force, affected)
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
	idx.runGraphWorkers(files[sampleSize:], shared, force, workers, progress, affected)
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
// After the parallel pass, a serial reconstitution post-pass iterates the
// paths collected in `affected` and force-reindexes each one via
// indexGraphFileDirect. This restores cross-file edges that
// DeleteSymbolsForFile's FK cascade destroyed during the parallel walk.
// Affected paths from the adaptive sample phase (populated before this
// function is called) are included in the same sweep.
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
//   - affected is a *sync.Map; concurrent Store calls from workers are safe.
func (idx *Indexer) runGraphWorkers(files []WalkResult, shared *GraphResult, force bool, workers int, progress *indexProgress, affected *sync.Map) {
	if workers <= 1 {
		for _, file := range files {
			err := idx.indexGraphFile(file, shared, force, affected)
			if err != nil {
				shared.Errors = append(shared.Errors, fmt.Sprintf("%s: %s", file.FilePath, err))
				shared.FilesErrored++
			}
			progress.tick(file.FilePath, err != nil)
		}
	} else {
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
					err := idx.indexGraphFile(file, local, force, affected)
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

	// Serial reconstitution post-pass: force-reindex every file that held a
	// cross-file edge into a reindexed file's symbols. All parallel work has
	// landed at this point, so the DB reflects the freshly projected symbols
	// and the reconstitution writes are safe to do sequentially.
	// indexGraphFileDirect is used (not indexGraphFile) so this pass does not
	// itself collect new affected paths or trigger another reconstitution round.
	affected.Range(func(key, _ any) bool {
		affPath := key.(string)
		affFile := WalkResult{
			FilePath: affPath,
			AbsPath:  filepath.Join(idx.cfg.ProjectRoot, affPath),
		}
		dummy := &GraphResult{}
		if err := idx.indexGraphFileDirect(affFile, dummy, true); err != nil {
			shared.Errors = append(shared.Errors, fmt.Sprintf("reconstitute %s: %s", affPath, err))
		}
		mergeGraphResult(shared, dummy)
		return true
	})
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

// indexGraphFile runs one file through the graph pass and records any
// source_paths that hold cross-file edges into this file's symbols in
// `affected`. The caller (runGraphWorkers) drains `affected` in a serial
// post-pass after all parallel workers complete, force-reindexing those files
// to reconstitute edges that DeleteSymbolsForFile's FK cascade destroyed.
//
// Affected paths are collected only when the file will actually be reindexed
// (i.e., after the hash gate and generated-file detection). `affected` is a
// *sync.Map so concurrent workers can write to it without a mutex.
func (idx *Indexer) indexGraphFile(file WalkResult, result *GraphResult, force bool, affected *sync.Map) error {
	// Read content and compute hash up front to short-circuit without a store
	// query for files that will clearly be skipped.
	handler := idx.registry.HandlerFor(file.FilePath)
	if handler == nil {
		return nil
	}
	content, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}
	hash := computeHash(string(content))

	existing, err := idx.store.GetCodeFileByPath(file.FilePath)
	if err != nil {
		return fmt.Errorf("checking existing code file: %w", err)
	}
	willSkip := existing != nil && !force && existing.ContentHash == hash
	willSkipGenerated := !willSkip && idx.cfg.Graph.DetectGenerated && isGeneratedFile(content)

	// Collect affected paths only when we'll actually reindex (after skip gates)
	// so we don't pay the query cost for unchanged or generated files.
	if !willSkip && !willSkipGenerated {
		if paths, qerr := idx.store.AffectedSourcePathsForFile(file.FilePath); qerr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: affected-paths query: %s", file.FilePath, qerr))
		} else {
			for _, p := range paths {
				affected.Store(p, struct{}{})
			}
		}
	}

	return idx.indexGraphFileDirect(file, result, force)
}

// indexGraphFileDirect runs one file through the graph pass without triggering
// cross-file edge reconstitution. This is the core implementation; callers
// that need reconstitution should use indexGraphFile instead.
//
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
func (idx *Indexer) indexGraphFileDirect(file WalkResult, result *GraphResult, force bool) error {
	handlers := idx.registry.HandlersFor(file.FilePath)
	if len(handlers) == 0 {
		return nil
	}
	primary := handlers[0]

	content, err := os.ReadFile(file.AbsPath)
	if err != nil {
		return fmt.Errorf("reading: %w", err)
	}

	hash := computeHash(string(content))

	existing, err := idx.store.GetCodeFileByPath(file.FilePath)
	if err != nil {
		return fmt.Errorf("checking existing code file: %w", err)
	}
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

	// Parse with the primary handler: its ParsedDoc drives the code_file node
	// metadata (e.g. buf_gen / proto options) and the base set of Units/Refs.
	parsed, err := idx.parseFile(primary, file, content)
	if err != nil {
		return fmt.Errorf("parsing: %w", err)
	}

	fileNodeID := store.CodeFileNodeID(file.FilePath)
	if err := idx.store.UpsertNode(store.Node{
		ID:         fileNodeID,
		Kind:       store.NodeKindCodeFile,
		Label:      file.FilePath,
		SourcePath: file.FilePath,
		Metadata:   codeFileNodeMetadataJSON(parsed),
	}); err != nil {
		return fmt.Errorf("upserting code file node: %w", err)
	}

	if _, err := idx.store.AddOrUpdateCodeFile(file.FilePath, primary.Name(), hash); err != nil {
		return fmt.Errorf("updating code file row: %w", err)
	}

	// Wipe stale symbols before projecting fresh ones. Scoped to
	// kind='symbol' so docs-pass mentions/shared_code_ref edges survive.
	if err := idx.store.DeleteSymbolsForFile(file.FilePath); err != nil {
		return fmt.Errorf("deleting stale symbols: %w", err)
	}

	// Collect Units and Refs from additional handlers (e.g. connect-es stub
	// handler for *_connect.ts files). Additional handlers run on the same
	// content bytes already read; parse errors are non-fatal (logged on
	// result.Errors, remaining handlers still run).
	allUnits := parsed.Units
	allRefs := parsed.Refs
	for _, h := range handlers[1:] {
		extra, parseErr := idx.parseFile(h, file, content)
		if parseErr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("handler %s for %s: %s", h.Name(), file.FilePath, parseErr))
			continue
		}
		allUnits = append(allUnits, extra.Units...)
		allRefs = append(allRefs, extra.Refs...)
	}

	// Project code-symbol Units. Non-symbol kinds (section/key-path/page/…)
	// are skipped — those belong to the docs pass or non-code formats.
	//
	// Unit.Metadata (populated by grammars implementing
	// symbolMetadataExtractor — today Kotlin's extension-function receiver
	// type) serialises into graph_nodes.metadata so downstream queries
	// ("find all extensions of String") can filter on the JSON. Empty /
	// nil metadata serialises to "{}" via UpsertNode's own default.
	for _, u := range allUnits {
		if !isSymbolKind(u.Kind) {
			continue
		}
		symID := store.SymbolNodeID(u.Path)
		if err := idx.store.UpsertNode(store.Node{
			ID:         symID,
			Kind:       store.NodeKindSymbol,
			Label:      u.Title,
			SourcePath: file.FilePath,
			Metadata:   unitMetadataJSON(u.Metadata),
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
	// reused from the docs pass and handle import/call/inherits (+ legacy
	// extends/implements) via SymbolNodeID, code-file via CodeFileNodeID,
	// config-key via ConfigKeyNodeID — unsupported ref kinds skip silently.
	//
	// Edge From is ref.Source (via refEdgeSource) when the grammar emitted a
	// symbol-scoped reference — today that's inherits edges anchored at the
	// child class's sym: node. File-scoped refs (imports etc.) keep
	// fileNodeID as From.
	//
	// Target-node creation uses UpsertPlaceholderNode (ON CONFLICT DO NOTHING)
	// rather than UpsertNode (ON CONFLICT DO UPDATE). Rationale: when the
	// target already exists as a real indexed symbol (its defining file's
	// symbol-projection loop ran first), the ref-loop MUST NOT overwrite
	// that row's label/source_path/metadata with placeholder values, which
	// would happen under file-walker ordering where Child.java is processed
	// before Base.java (lexicographic) or the reverse for mixed-case
	// filenames. UpsertPlaceholderNode leaves existing rows untouched so
	// a Reference.Metadata with unresolved=true can't poison a resolved
	// node's metadata downstream.
	for _, ref := range allRefs {
		targetID := graphTargetID(ref)
		if targetID == "" {
			continue
		}
		if err := idx.store.UpsertPlaceholderNode(store.Node{
			ID:         targetID,
			Kind:       graphNodeKindFromRef(ref),
			Label:      ref.Target,
			SourcePath: ref.Target,
			Metadata:   targetNodeMetadataJSON(ref),
		}); err != nil {
			continue
		}
		if err := idx.store.UpsertEdge(store.Edge{
			From:     refEdgeSource(ref, fileNodeID),
			To:       targetID,
			Kind:     ref.Kind,
			Metadata: refMetadataJSON(ref),
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
		"type", "field",
		"object",         // Kotlin object / companion object declarations
		"property",       // Kotlin property (val / var declarations)
		"struct",         // Swift struct declarations
		"extension",      // Swift extension declarations (target type as Title)
		"protocol",       // Swift protocol declarations
		"mixin",          // Dart mixin declarations (lib-wji.3)
		"extension_type", // Dart extension type declarations (lib-wji.3)
		"service",        // Proto service declarations (lib-cym)
		"rpc",            // Proto service RPC methods (lib-cym)
		"message",        // Proto message declarations (lib-cym)
		"oneof":          // Proto oneof declarations (lib-cym)
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

	parsed, err := idx.parseFile(handler, file, content)
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
		if err := idx.store.DeleteDocument(existing.ID); err != nil {
			return fmt.Errorf("deleting stale doc row: %w", err)
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

	// Batch-embed every chunk's text in one shot (provider slices into waves
	// of batch_size internally). Previously this was one HTTP call per chunk
	// which dominated indexing wall-clock and tripped rate limits quickly.
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.EmbeddingText
	}
	vectors, err := idx.embedder.EmbedBatch(texts)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("batch embed (%d chunks): %s", len(chunks), err))
		return nil
	}

	// Resolve per-chunk summaries via summary_cache (keyed on SHA-256 of
	// chunk content). Cache misses are generated one at a time via the
	// configured summarizer; unchanged chunks skip the API call entirely.
	summaries := idx.resolveSummaries(chunks, result)

	model := idx.embedder.Model()
	for i, chunk := range chunks {
		_, err := idx.store.AddChunk(store.AddChunkInput{
			Vector:           vectors[i],
			Content:          chunk.EmbeddingText,
			Summary:          summaries[i],
			FilePath:         file.FilePath,
			SectionHeading:   chunk.SectionHeading,
			SectionHierarchy: HierarchyToJSON(chunk.SectionHierarchy),
			ChunkIndex:       chunk.ChunkIndex,
			TokenCount:       chunk.TokenCount,
			DocID:            doc.ID,
			SignalMeta:       chunk.SignalMeta,
			Model:            model,
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
	//
	// Symbol-scoped refs (ref.Source populated) would anchor at sym:<Source>
	// instead of the doc node — not expected from the docs-pass handlers
	// today (markdown/office/pdf don't emit inherits), but the same
	// refEdgeSource helper keeps the semantics consistent if that changes.
	//
	// Target nodes use UpsertPlaceholderNode (see graph pass comment for the
	// same call — doesn't clobber resolved symbols' metadata on ordering).
	for _, ref := range parsed.Refs {
		targetID := graphTargetID(ref)
		if targetID == "" {
			continue
		}
		idx.store.UpsertPlaceholderNode(store.Node{
			ID:         targetID,
			Kind:       graphNodeKindFromRef(ref),
			Label:      ref.Target,
			SourcePath: ref.Target,
			Metadata:   targetNodeMetadataJSON(ref),
		})
		idx.store.UpsertEdge(store.Edge{
			From:     refEdgeSource(ref, store.DocNodeID(doc.ID)),
			To:       targetID,
			Kind:     ref.Kind,
			Metadata: refMetadataJSON(ref),
		})
	}

	result.DocumentsIndexed++
	return nil
}

// resolveSummaries returns one summary string per chunk in order. It checks
// summary_cache first (keyed on SHA-256 of chunk.EmbeddingText); only cache
// misses are passed to the summarizer. Errors from the summarizer are
// recorded in result.Errors but never abort the indexing run — the
// affected chunk simply gets an empty summary.
func (idx *Indexer) resolveSummaries(chunks []Chunk, result *IndexResult) []string {
	summaries := make([]string, len(chunks))

	hashes := make([]string, len(chunks))
	for i, c := range chunks {
		hashes[i] = computeHash(c.EmbeddingText)
	}

	cached, err := idx.store.GetChunkSummariesByHashes(hashes)
	if err != nil {
		// Non-fatal: fall through to generate all summaries.
		result.Errors = append(result.Errors, fmt.Sprintf("summary cache lookup: %s", err))
	}

	for i, h := range hashes {
		if s, ok := cached[h]; ok {
			summaries[i] = s
			continue
		}
		// Cache miss: generate via summarizer (Noop returns "" instantly).
		s, serr := idx.summarizer.Summarize(chunks[i].EmbeddingText)
		if serr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("summarize chunk %d: %s", chunks[i].ChunkIndex, serr))
			continue
		}
		summaries[i] = s
		// Cache the result even for empty strings — prevents repeat API calls
		// for content the model consistently returns nothing for.
		if uerr := idx.store.UpsertChunkSummary(h, s); uerr != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("cache summary chunk %d: %s", chunks[i].ChunkIndex, uerr))
		}
	}

	return summaries
}

// refEdgeSource returns the graph node ID that should be the `from_node` of
// the edge materialised from this Reference. Symbol-scoped refs (Source
// populated by a grammar's inheritanceExtractor/…) anchor at sym:<Source>.
// File-scoped refs (the long-standing shape for imports / mentions / code-file
// / config-key / doc-link) fall back to defaultNodeID, preserving existing
// behaviour end-to-end.
func refEdgeSource(ref Reference, defaultNodeID string) string {
	if ref.Source != "" {
		return store.SymbolNodeID(ref.Source)
	}
	return defaultNodeID
}

// refMetadataJSON serialises Reference.Metadata to a JSON string suitable for
// Edge.Metadata / Node.Metadata. Returns "" (which UpsertEdge/UpsertNode
// interpret as "{}") when the metadata is empty, so zero-value refs continue
// to produce unchanged on-disk edge rows. Keys whose values don't survive
// json.Marshal (channels, functions) drop from the result — none of the
// conventional keys (relation, type_args, unresolved, alias, static,
// node_kind) hit that case.
//
// Keys are emitted in sorted order because json.Marshal on map[string]any is
// non-deterministic, and graph_edges uses INSERT OR REPLACE — non-deterministic
// serialisation would cause the same logical metadata to produce a different
// row byte-for-byte on every reindex, churning the SQLite WAL and anything
// downstream that watches edge rows. Sorted order costs a keys-slice sort per
// edge; negligible vs the Marshal itself.
func refMetadataJSON(ref Reference) string {
	if len(ref.Metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(ref.Metadata))
	for k := range ref.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		// Skip any key whose value can't be marshalled (channel / function /
		// cyclic map). Don't abort the whole map — losing one key is
		// recoverable, but returning "" drops every key including
		// "relation", which is the primary discriminator for inherits-edge
		// flavor. No current grammar produces un-marshallable values; this
		// is defensive against future additions.
		vb, err := json.Marshal(ref.Metadata[k])
		if err != nil {
			continue
		}
		kb, err := json.Marshal(k)
		if err != nil {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// codeFileNodeMetadataJSON filters a parsed.Metadata map down to the keys the
// graph-pass's code_file node is allowed to carry, then serialises to a
// sorted-key JSON string. Non-empty only for the handful of files whose
// handlers stash cross-pass state on ParsedDoc.Metadata:
//
//   - "buf_gen"  — buf.gen.yaml plugin list (lib-4kb, config/yaml.go)
//   - "options"  — proto file-level *_package options (lib-cym, code/proto.go)
//
// Both feed the buf manifest builder (buildBufManifest). Any other parsed
// metadata keys stay off the graph_nodes row — the column is a narrow
// resolver-input channel, not a dumping ground for handler state that would
// churn the DB on every reindex.
//
// Returns "" (UpsertNode interprets as "{}") when neither key is present so
// files without relevant metadata don't write a non-trivial metadata string.
func codeFileNodeMetadataJSON(parsed *ParsedDoc) string {
	if parsed == nil || len(parsed.Metadata) == 0 {
		return ""
	}
	sub := map[string]any{}
	for _, k := range []string{"buf_gen", "options"} {
		if v, ok := parsed.Metadata[k]; ok {
			sub[k] = v
		}
	}
	if len(sub) == 0 {
		return ""
	}
	return unitMetadataJSON(sub)
}

// unitMetadataJSON serialises a Unit.Metadata map to the sorted-key JSON
// string form used by graph_nodes.metadata. Returns "" (which UpsertNode
// interprets as "{}") when the map is empty or nil — so non-extension
// symbols don't get their metadata column churned on reindex.
//
// Deterministic ordering matters for the same reason refMetadataJSON
// sorts its keys: INSERT OR REPLACE writes every time, so non-deterministic
// JSON would trigger real disk writes even when the logical metadata is
// unchanged. Per-value marshal errors skip the key rather than dropping
// the whole map (matching refMetadataJSON's defensive posture).
func unitMetadataJSON(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		vb, err := json.Marshal(meta[k])
		if err != nil {
			continue
		}
		kb, err := json.Marshal(k)
		if err != nil {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.String()
}

// targetNodeMetadataJSON returns the JSON metadata for the target node of a
// Reference — currently only the `unresolved=true` marker bubbles up, so
// downstream queries / orphan sweeps can tell placeholder nodes (bare Java
// class names without a matching import, Python expression bases) apart from
// real symbols. Other Reference.Metadata keys are edge-level, not node-level,
// and stay on the edge.
func targetNodeMetadataJSON(ref Reference) string {
	if ref.Metadata == nil {
		return ""
	}
	if v, ok := ref.Metadata["unresolved"].(bool); ok && v {
		return `{"unresolved":true}`
	}
	return ""
}

// graphTargetID maps a Reference to a namespaced graph node id via the store's
// node-id constructors. Returns "" for reference kinds that don't yet have a
// node-kind mapping (the edge is skipped rather than invented).
//
// For "import" refs, a per-reference Metadata["node_kind"] tag can override
// the default sym: namespace — used by the JS/TS resolver to route resolved
// relative specifiers onto file: nodes (matching CodeFileNodeID) and bare
// specifiers onto ext: nodes (npm packages that aren't in-project). Refs
// without the tag continue to land on sym:, preserving the lib-o8m behaviour.
//
// "inherits" is the canonical new kind for class/interface/protocol parent
// relationships (lib-wji.1). "extends" and "implements" are retained as
// legacy aliases routing identically to sym: nodes so existing hand-authored
// fixtures and pre-lib-wji.1 data in on-disk DBs keep working. New grammars
// emit "inherits" with Metadata["relation"] instead.
//
// "implements_rpc" (lib-6wz) is a codegen derivation from a proto rpc to the
// generated-code method that implements it (symbol → symbol). Kept distinct
// from "inherits" because the relationship isn't subtype/parent — it's
// derived-from-codegen — so "all parents of X" queries stay clean.
func graphTargetID(ref Reference) string {
	switch ref.Kind {
	case "code-file":
		return store.CodeFileNodeID(ref.Target)
	case "import":
		switch nodeKindFromMetadata(ref) {
		case store.NodeKindCodeFile:
			return store.CodeFileNodeID(ref.Target)
		case store.NodeKindExternal:
			return store.ExternalPackageNodeID(ref.Target)
		}
		return store.SymbolNodeID(ref.Target)
	case "call", "inherits", "requires", "implements_rpc", "extends", "implements":
		return store.SymbolNodeID(ref.Target)
	case "part":
		return store.CodeFileNodeID(ref.Target)
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
	case "import":
		if k := nodeKindFromMetadata(ref); k != "" {
			return k
		}
		return store.NodeKindSymbol
	case "call", "inherits", "requires", "implements_rpc", "extends", "implements":
		return store.NodeKindSymbol
	case "part":
		return store.NodeKindCodeFile
	case "config-key":
		return store.NodeKindConfigKey
	}
	return "unknown"
}

// nodeKindFromMetadata reads the resolver-set Metadata["node_kind"] tag off a
// Reference, returning the canonical kind constant or "" if unset.
func nodeKindFromMetadata(ref Reference) string {
	if ref.Metadata == nil {
		return ""
	}
	v, ok := ref.Metadata["node_kind"].(string)
	if !ok {
		return ""
	}
	switch v {
	case store.NodeKindCodeFile, store.NodeKindExternal, store.NodeKindSymbol:
		return v
	}
	return ""
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
