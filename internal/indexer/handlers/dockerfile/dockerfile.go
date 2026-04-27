// Package dockerfile implements a FileHandler for Dockerfile files.
//
// v1 is a docs-pass-only handler: it makes Dockerfile content queryable via
// search_docs and get_context without building a full instruction AST. Stage-boundary
// chunking (split at each FROM directive) keeps each build stage retrievable
// separately. Structural extraction (stage graph, base-image nodes,
// EXPOSE/CMD/ENTRYPOINT metadata) is deferred to v2 (lib-nf6).
//
// Detected by both extension (.dockerfile) and filename pattern (Dockerfile,
// Dockerfile.*), registered via RegisterByFilenameGlob alongside the standard
// extension-keyed registration.
package dockerfile

import (
	"fmt"
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// Handler indexes Dockerfile files for the docs pass.
type Handler struct{}

// New returns a Dockerfile handler.
func New() *Handler { return &Handler{} }

var _ indexer.FileHandler = (*Handler)(nil)

func init() {
	h := New()
	indexer.RegisterDefault(h)
	indexer.RegisterDefaultByFilenameGlob(h, "Dockerfile", "Dockerfile.*")
}

func (*Handler) Name() string { return "dockerfile" }

// Extensions covers the extension form (e.g. auth.dockerfile). The filename forms
// (Dockerfile, Dockerfile.prod) are registered separately via RegisterByFilenameGlob.
func (*Handler) Extensions() []string { return []string{".dockerfile"} }

// Parse converts raw Dockerfile bytes to a ParsedDoc with one Unit per build stage.
// Each FROM directive starts a new stage; any content before the first FROM
// (e.g. # syntax= directives) is included with stage 1.
func (*Handler) Parse(path string, content []byte) (*indexer.ParsedDoc, error) {
	raw := string(content)
	base := filepath.Base(path)

	doc := &indexer.ParsedDoc{
		Path:       path,
		Format:     "dockerfile",
		Title:      base,
		DocType:    "dockerfile",
		RawContent: raw,
		Metadata:   map[string]any{},
	}

	doc.Units = splitIntoStageUnits(raw)
	doc.Signals = indexer.ExtractRationaleSignals(raw)
	return doc, nil
}

// Chunk converts each stage Unit into a SectionInput and delegates to ChunkSections.
// A single-stage Dockerfile yields one chunk via the ChunkSections no-sections fallback.
func (*Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		if u.Kind != "section" {
			continue
		}
		inputs = append(inputs, indexer.SectionInput{
			Heading:    u.Title,
			Hierarchy:  []string{doc.Title, u.Title},
			Content:    u.Content,
			SignalLine: indexer.SignalLineFromSignals(u.Signals),
			SignalMeta: indexer.SignalsToJSON(u.Signals),
		})
	}
	chunks := indexer.ChunkSections(doc.Title, doc.RawContent, inputs, opts)
	return chunks, nil
}

// splitIntoStageUnits splits a Dockerfile into one Unit per build stage.
// The boundary is any FROM instruction at the start of a non-empty line
// (case-insensitive). Any preamble before the first FROM is folded into stage 1.
// A file with no FROM returns the whole content as a single "stage 1" unit.
func splitIntoStageUnits(src string) []indexer.Unit {
	lines := strings.Split(src, "\n")

	type stageBoundary struct {
		lineIdx int
		title   string
	}

	var boundaries []stageBoundary
	stageNum := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") || strings.EqualFold(trimmed, "FROM") {
			stageNum++
			boundaries = append(boundaries, stageBoundary{lineIdx: i, title: stageTitle(trimmed, stageNum)})
		}
	}

	if len(boundaries) == 0 {
		return []indexer.Unit{{
			Kind:    "section",
			Path:    "stage 1",
			Title:   "stage 1",
			Content: src,
		}}
	}

	units := make([]indexer.Unit, 0, len(boundaries))
	for i, b := range boundaries {
		startLine := b.lineIdx
		if i == 0 {
			startLine = 0 // include any preamble before the first FROM
		}

		endLine := len(lines)
		if i+1 < len(boundaries) {
			endLine = boundaries[i+1].lineIdx
		}

		content := strings.TrimSpace(strings.Join(lines[startLine:endLine], "\n"))
		if content == "" {
			// Defensive guard: in practice this branch is unreachable in v1 because
			// every stage slice includes at least its own FROM line (always non-empty).
			// Kept for robustness against future refactors that might separate the FROM
			// line from the stage body. If all units are dropped by this path, Chunk()
			// receives a doc with no units and ChunkSections falls back to a single
			// raw-content chunk — intentional v1 behavior, tested by
			// TestHandler_ChunkFallback_EmptyUnits.
			continue
		}

		units = append(units, indexer.Unit{
			Kind:    "section",
			Path:    b.title,
			Title:   b.title,
			Content: content,
			Signals: indexer.ExtractRationaleSignals(content),
		})
	}

	return units
}

// stageTitle derives a human-readable title from a FROM line.
//
//	"FROM python:3.12 AS app"    → "stage: app"
//	"FROM ubuntu:22.04"          → "stage 1" (or "stage N")
//	"FROM --platform=... img AS build" → "stage: build"
func stageTitle(fromLine string, n int) string {
	upper := strings.ToUpper(fromLine)
	if idx := strings.Index(upper, " AS "); idx >= 0 {
		name := strings.TrimSpace(fromLine[idx+4:])
		if name != "" {
			return "stage: " + name
		}
	}
	return fmt.Sprintf("stage %d", n)
}
