// Package faq extracts frequently-asked questions from git commit history and
// bd issue history, clusters near-duplicates by embedding similarity, and
// writes each cluster as a searchable markdown FAQ file under docs/faqs/.
package faq

import (
	"fmt"
	"path/filepath"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
	"librarian/internal/summarizer"
)

// RunConfig controls a single FAQ extraction run.
type RunConfig struct {
	// GitCommits is the number of recent git commits to scan. Default 100.
	GitCommits int
	// Threshold is the cosine similarity threshold for clustering. Default 0.85.
	Threshold float64
	// FAQDir is the output directory for generated FAQ files, relative to CWD.
	// Defaults to <DocsDir>/faqs.
	FAQDir string
	// Cfg is the librarian workspace config (embedding provider, DB path, etc.).
	Cfg *config.Config

	// ScanGit overrides the git scanner for testing. Nil uses ScanGitLog.
	ScanGit func(n int) ([]Source, error)
	// ScanIssues overrides the bd scanner for testing. Nil uses ScanBDIssues.
	ScanIssues func() ([]Source, error)
}

// Result summarises a completed FAQ extraction run.
type Result struct {
	GitSources    int
	IssueSources  int
	Clusters      int
	FilesWritten  []string
	ChunksCreated int
}

// Run executes the full FAQ extraction pipeline:
//  1. Scan git log (last GitCommits commits) for question-shaped subjects.
//  2. Scan bd closed issues for question-shaped titles.
//  3. Cluster near-duplicates using embedding cosine similarity.
//  4. Write each cluster (that has a real answer) as docs/faqs/<slug>.md.
//  5. Re-index each written file into the librarian store.
func Run(rc RunConfig) (*Result, error) {
	if rc.GitCommits <= 0 {
		rc.GitCommits = 100
	}
	if rc.Threshold <= 0 {
		rc.Threshold = 0.85
	}
	if rc.FAQDir == "" {
		rc.FAQDir = filepath.Join(rc.Cfg.DocsDir, "faqs")
	}
	if rc.ScanGit == nil {
		rc.ScanGit = ScanGitLog
	}
	if rc.ScanIssues == nil {
		rc.ScanIssues = ScanBDIssues
	}

	// 1. Scan sources.
	gitSrcs, err := rc.ScanGit(rc.GitCommits)
	if err != nil {
		return nil, fmt.Errorf("scanning git log: %w", err)
	}

	issueSrcs, err := rc.ScanIssues()
	if err != nil {
		// bd may not be installed in all environments; treat as non-fatal.
		issueSrcs = nil
	}

	all := append(gitSrcs, issueSrcs...)
	if len(all) == 0 {
		return &Result{GitSources: len(gitSrcs), IssueSources: len(issueSrcs)}, nil
	}

	// 2. Cluster near-duplicates.
	embedder, err := embedding.NewEmbedder(rc.Cfg.Embedding)
	if err != nil {
		return nil, fmt.Errorf("creating embedder: %w", err)
	}

	clusters, err := Cluster(all, embedder, rc.Threshold)
	if err != nil {
		return nil, fmt.Errorf("clustering: %w", err)
	}

	// 3. Build FAQ entries (one per cluster). Drop entries without a real
	// answer to avoid fabricating FAQs without a clear source.
	entries := make([]FAQEntry, 0, len(clusters))
	for _, cluster := range clusters {
		e := EntryFromCluster(cluster)
		if e.Answer != "" {
			entries = append(entries, e)
		}
	}

	// 4. Write FAQ files.
	written, err := WriteEntries(entries, rc.FAQDir)
	if err != nil {
		return nil, fmt.Errorf("writing faq files: %w", err)
	}

	result := &Result{
		GitSources:   len(gitSrcs),
		IssueSources: len(issueSrcs),
		Clusters:     len(clusters),
		FilesWritten: written,
	}

	if len(written) == 0 {
		return result, nil
	}

	// 5. Re-index each written file.
	s, err := store.Open(rc.Cfg.DBPath, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	sum, err := summarizer.New(rc.Cfg.Summarization)
	if err != nil {
		return nil, fmt.Errorf("creating summarizer: %w", err)
	}

	idx := indexer.New(s, rc.Cfg, embedder)
	idx.SetSummarizer(sum)

	for _, path := range written {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolving path %s: %w", path, err)
		}
		// Pass absPath for both args: the first arg is the DB canonical key;
		// using absolute path avoids environment-dependent relative path lookups.
		r, err := idx.IndexSingleFile(absPath, absPath, true)
		if err != nil {
			return nil, fmt.Errorf("indexing %s: %w", path, err)
		}
		result.ChunksCreated += r.ChunksCreated
	}

	return result, nil
}
