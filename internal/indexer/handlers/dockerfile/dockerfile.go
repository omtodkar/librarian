// Package dockerfile implements a FileHandler for Dockerfile files.
//
// v1 is a docs-pass-only handler: it makes Dockerfile content queryable via
// search_docs and get_context without building a full instruction AST. Stage-boundary
// chunking (split at each FROM directive) keeps each build stage retrievable
// separately.
//
// v2 (lib-nf6) adds a graph-pass: Parse() also emits "stage" Units and References
// so the graph layer can answer queries like "what base image does auth use?" or
// "which port does orders expose?". Stage nodes land under sym:stage:<name>;
// external base images project to ext:<registry>/<image>:<tag> via import edges
// with node_kind="external"; stage-to-stage dependencies and COPY --from edges
// use inherits with relation="stage" / "copy-from".
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

// New returns a Dockerfile handler. Exported following the project handler convention
// (all handler constructors are exported) so tests can instantiate it directly.
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
//
// In addition to the v1 "section" Units (for the docs pass), Parse also emits
// "stage" Units and References for the graph pass (v2, lib-nf6). The docs-pass
// Chunk() method already skips non-"section" Units, so the graph data is
// transparently ignored during docs indexing.
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

	// Append graph-pass stage Units and References (v2).
	graphUnits, graphRefs := parseStageGraph(raw)
	doc.Units = append(doc.Units, graphUnits...)
	doc.Refs = append(doc.Refs, graphRefs...)

	return doc, nil
}

// Chunk converts each stage Unit into a SectionInput and delegates to ChunkSections.
// A single-stage Dockerfile yields one chunk via the ChunkSections no-sections fallback.
func (*Handler) Chunk(doc *indexer.ParsedDoc, opts indexer.ChunkOpts) ([]indexer.Chunk, error) {
	inputs := make([]indexer.SectionInput, 0, len(doc.Units))
	for _, u := range doc.Units {
		// splitIntoStageUnits always sets Kind="section", so this guard is unreachable
		// in practice — kept as a forward-compat guard in case future parse paths add
		// non-section units (e.g. a metadata unit in v2).
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

// parseStageGraph extracts the stage dependency graph from a Dockerfile and
// returns "stage" Units and References for the graph pass.
//
// Stage nodes use Path="stage:<name>" so they land under a distinct sym:stage:
// namespace in the graph, avoiding collisions with code symbols. External base
// images project to ext:<registry>/<image>:<tag> via import edges with
// Metadata["node_kind"]="external". Stage-to-stage FROM dependencies use
// inherits edges with Metadata["relation"]="stage". COPY --from edges use
// inherits with Metadata["relation"]="copy-from".
func parseStageGraph(raw string) ([]indexer.Unit, []indexer.Reference) {
	lines := strings.Split(raw, "\n")

	type stageInfo struct {
		name       string   // full path: "stage:<rawName>"
		rawName    string   // bare stage name or "stage-N" for unnamed
		baseImage  string   // raw image ref from FROM line
		lineIdx    int      // 0-indexed line number of the FROM directive
		expose     []string // ports from EXPOSE directives
		cmd        []string // CMD directive bodies
		entrypoint []string // ENTRYPOINT directive bodies
	}

	// First pass: collect FROM lines.
	var stages []stageInfo
	stageNum := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if !strings.HasPrefix(upper, "FROM ") && !strings.EqualFold(trimmed, "FROM") {
			continue
		}
		stageNum++
		baseImage, stageName := parseFromLine(trimmed)
		if stageName == "" {
			stageName = fmt.Sprintf("stage-%d", stageNum)
		}
		stages = append(stages, stageInfo{
			name:      "stage:" + stageName,
			rawName:   stageName,
			baseImage: baseImage,
			lineIdx:   i,
		})
	}

	if len(stages) == 0 {
		return nil, nil
	}

	// Build lookup tables for local stage resolution.
	// Keyed on lower-cased rawName so stage references are case-insensitive.
	stageByRawName := make(map[string]string, len(stages)) // lower(rawName) → path
	stageByIdx := make(map[string]string, len(stages))     // "0", "1", ... → path
	for i, s := range stages {
		stageByRawName[strings.ToLower(s.rawName)] = s.name
		stageByIdx[fmt.Sprintf("%d", i)] = s.name
	}

	type copyFromEdge struct {
		fromStage string
		toRef     string
	}
	var copyFromEdges []copyFromEdge

	// Second pass: collect EXPOSE/CMD/ENTRYPOINT/COPY per stage.
	currentStage := -1
	for lineIdx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		keyword := strings.ToUpper(fields[0])

		if keyword == "FROM" {
			for j := range stages {
				if stages[j].lineIdx == lineIdx {
					currentStage = j
					break
				}
			}
			continue
		}

		if currentStage < 0 {
			continue
		}

		// body is the directive argument (after the keyword), preserving original case.
		body := strings.TrimSpace(trimmed[len(fields[0]):])
		switch keyword {
		case "EXPOSE":
			stages[currentStage].expose = append(stages[currentStage].expose, strings.Fields(body)...)
		case "CMD":
			if body != "" {
				stages[currentStage].cmd = append(stages[currentStage].cmd, body)
			}
		case "ENTRYPOINT":
			if body != "" {
				stages[currentStage].entrypoint = append(stages[currentStage].entrypoint, body)
			}
		case "COPY":
			// Extract --from=<ref> flag.
			for _, f := range fields[1:] {
				if strings.HasPrefix(strings.ToLower(f), "--from=") {
					ref := f[len("--from="):]
					if ref != "" {
						copyFromEdges = append(copyFromEdges, copyFromEdge{
							fromStage: stages[currentStage].name,
							toRef:     ref,
						})
					}
				}
			}
		}
	}

	// Build stage Units and References.
	var units []indexer.Unit
	var refs []indexer.Reference

	for _, s := range stages {
		meta := map[string]any{
			"base_image": s.baseImage,
		}
		if len(s.expose) > 0 {
			meta["expose"] = s.expose
		}
		if len(s.cmd) > 0 {
			meta["cmd"] = s.cmd
		}
		if len(s.entrypoint) > 0 {
			meta["entrypoint"] = s.entrypoint
		}

		units = append(units, indexer.Unit{
			Kind:  "stage",
			Path:  s.name,
			Title: s.rawName,
			Loc: indexer.Location{
				Line: s.lineIdx + 1, // 1-indexed
			},
			Metadata: meta,
		})

		// FROM dependency edge — skip if no base image, special "scratch" base,
		// or ARG-substituted name (starts with "$"): unresolvable at parse time.
		if s.baseImage == "" || strings.EqualFold(s.baseImage, "scratch") || strings.HasPrefix(s.baseImage, "$") {
			continue
		}

		lowerBase := strings.ToLower(s.baseImage)
		// Strip digest and tag for stage name lookup. Digest must be stripped
		// first because its sha256: colon would fool the tag-stripping index.
		// e.g. "ubuntu@sha256:abc:tag" → strip "@..." → "ubuntu" (no tag here).
		baseNameOnly := lowerBase
		if i := strings.Index(baseNameOnly, "@"); i >= 0 {
			baseNameOnly = baseNameOnly[:i]
		}
		if colonIdx := strings.Index(baseNameOnly, ":"); colonIdx > 0 {
			baseNameOnly = baseNameOnly[:colonIdx]
		}

		if parentPath, ok := stageByRawName[lowerBase]; ok {
			// Exact match: lowerBase is the full image string including any tag
			// (e.g. "builder" or "builder:latest"). A stage name never includes
			// a tag, so this branch only fires when the FROM line references a
			// stage by its bare name — the common case for stage-to-stage deps.
			refs = append(refs, indexer.Reference{
				Kind:     "inherits",
				Source:   s.name,
				Target:   parentPath,
				Metadata: map[string]any{"relation": "stage"},
			})
		} else if parentPath, ok := stageByRawName[baseNameOnly]; ok && !strings.Contains(lowerBase, "/") {
			// Tag-stripped fallback: handles `FROM builder:latest AS x` where the
			// stage was declared as `FROM … AS builder` (no tag). The no-slash gate
			// is correct because Docker stage names cannot contain slashes — any
			// slash means the ref is a registry path, not a stage name.
			refs = append(refs, indexer.Reference{
				Kind:     "inherits",
				Source:   s.name,
				Target:   parentPath,
				Metadata: map[string]any{"relation": "stage"},
			})
		} else {
			// External image — normalize to include a registry prefix.
			extImage := normalizeDockerImage(s.baseImage)
			refs = append(refs, indexer.Reference{
				Kind:     "import",
				Source:   s.name,
				Target:   extImage,
				Metadata: map[string]any{"node_kind": "external"},
			})
		}
	}

	// COPY --from edges: resolve stage name or index to full stage path.
	for _, c := range copyFromEdges {
		lowerRef := strings.ToLower(c.toRef)
		var targetPath string
		if p, ok := stageByRawName[lowerRef]; ok {
			targetPath = p
		} else if p, ok := stageByIdx[lowerRef]; ok {
			targetPath = p
		}
		if targetPath == "" || targetPath == c.fromStage {
			// Skip missing targets and self-loops (e.g. FROM scratch + COPY --from=0
			// in a single-stage Dockerfile resolves to the stage copying from itself).
			continue
		}
		refs = append(refs, indexer.Reference{
			Kind:     "inherits",
			Source:   c.fromStage,
			Target:   targetPath,
			Metadata: map[string]any{"relation": "copy-from"},
		})
	}

	return units, refs
}

// parseFromLine extracts the base image and optional stage name from a FROM
// directive line. Returns ("", "") for the bare "FROM" form (no image).
//
// Examples:
//
//	"FROM ubuntu:22.04 AS base"               → ("ubuntu:22.04", "base")
//	"FROM --platform=linux/amd64 ubuntu AS x" → ("ubuntu", "x")
//	"FROM golang:1.22"                        → ("golang:1.22", "")
func parseFromLine(line string) (baseImage, stageName string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", ""
	}
	// Skip the FROM keyword itself.
	fields = fields[1:]

	// Skip --flags (--platform, --network, etc.).
	for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
		fields = fields[1:]
	}

	if len(fields) == 0 {
		return "", ""
	}

	baseImage = fields[0]
	fields = fields[1:]

	// Look for AS <name>.
	if len(fields) >= 2 && strings.EqualFold(fields[0], "AS") {
		stageName = fields[1]
	}

	return baseImage, stageName
}

// normalizeDockerImage normalizes a Docker image reference to include a registry
// prefix. Images without a registry prefix are assumed to be Docker Hub.
//
// Examples:
//
//	"ubuntu:22.04"           → "docker.io/library/ubuntu:22.04"
//	"myuser/myapp:1.0"       → "docker.io/myuser/myapp:1.0"
//	"gcr.io/distroless/base" → "gcr.io/distroless/base" (unchanged)
//	"localhost:5000/myapp"   → "localhost:5000/myapp" (unchanged)
func normalizeDockerImage(image string) string {
	// Use a slash-first approach: find the first "/" in the raw image string
	// (before any tag or digest) and inspect the segment before it. This avoids
	// the tag-separator colon in "localhost:5000/myapp" being mistaken for the
	// end of the name part, which the old namePart-extraction approach did.
	slashIdx := strings.Index(image, "/")
	if slashIdx < 0 {
		// No slash: official Docker Hub library image (e.g. "ubuntu:22.04").
		return "docker.io/library/" + image
	}

	// The prefix (before the first slash) is a registry when it contains a
	// dot ("gcr.io"), a colon ("localhost:5000"), or equals "localhost".
	prefix := image[:slashIdx]
	if strings.Contains(prefix, ".") || strings.Contains(prefix, ":") || prefix == "localhost" {
		return image
	}

	// Docker Hub user image (e.g. "myuser/myapp:1.0").
	return "docker.io/" + image
}
