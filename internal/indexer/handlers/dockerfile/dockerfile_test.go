package dockerfile_test

import (
	"strings"
	"testing"

	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // wire all handlers including dockerfile
	dockerfilehandler "librarian/internal/indexer/handlers/dockerfile"
)

// compile-time interface check
var _ indexer.FileHandler = (*dockerfilehandler.Handler)(nil)

// TestHandler_Name checks the handler identifier.
func TestHandler_Name(t *testing.T) {
	h := dockerfilehandler.New()
	if h.Name() != "dockerfile" {
		t.Errorf("Name() = %q, want dockerfile", h.Name())
	}
}

// TestHandler_Extensions checks that .dockerfile is registered.
func TestHandler_Extensions(t *testing.T) {
	h := dockerfilehandler.New()
	exts := h.Extensions()
	if len(exts) != 1 || exts[0] != ".dockerfile" {
		t.Errorf("Extensions() = %v, want [.dockerfile]", exts)
	}
}

// TestHandler_ExtensionRegistration checks the extension form is in the default registry.
func TestHandler_ExtensionRegistration(t *testing.T) {
	reg := indexer.DefaultRegistry()
	if reg.HandlerFor("auth.dockerfile") == nil {
		t.Error("extension .dockerfile not registered in default registry")
	}
}

// TestHandler_FilenameRegistration checks that Dockerfile and Dockerfile.* are picked up
// via the filename glob mechanism (no extension).
func TestHandler_FilenameRegistration(t *testing.T) {
	reg := indexer.DefaultRegistry()

	cases := []string{
		"Dockerfile",
		"Dockerfile.prod",
		"Dockerfile.dev",
		"Dockerfile.staging",
		"services/auth/Dockerfile",
		"services/api/Dockerfile.prod",
	}
	for _, path := range cases {
		if reg.HandlerFor(path) == nil {
			t.Errorf("HandlerFor(%q) = nil, want dockerfile handler", path)
		}
	}
}

// TestHandler_FilenameRegistration_NegativeCases checks files that should NOT match
// the dockerfile handler. Uses two sub-checks:
//   - paths that have their own handler (e.g. .yml → yaml) must return a non-dockerfile handler
//   - paths with no handler at all must return nil — the filename-glob must not over-match
func TestHandler_FilenameRegistration_NegativeCases(t *testing.T) {
	reg := indexer.DefaultRegistry()

	// Paths that have a real handler but it must not be the dockerfile handler.
	hasOtherHandler := []string{
		"docker-compose.yml", // YAML handler
		"main.go",            // Go code handler
	}
	for _, path := range hasOtherHandler {
		h := reg.HandlerFor(path)
		if h != nil && h.Name() == "dockerfile" {
			t.Errorf("HandlerFor(%q) unexpectedly returned dockerfile handler", path)
		}
	}

	// Paths with no registered handler at all — must return nil.
	// These specifically exercise the filename-glob fallback path to confirm it
	// doesn't over-match (e.g. "Makefile" must not match the "Dockerfile*" globs).
	noHandler := []string{
		"Makefile",           // no handler registered
		"notadockerfile.txt", // .txt has no handler
	}
	for _, path := range noHandler {
		h := reg.HandlerFor(path)
		if h != nil {
			t.Fatalf("HandlerFor(%q) = %q, expected nil handler", path, h.Name())
		}
	}
}

// TestHandler_Parse_BasicFields checks that Parse sets the expected document fields.
func TestHandler_Parse_BasicFields(t *testing.T) {
	h := dockerfilehandler.New()
	content := []byte("FROM ubuntu:22.04\nRUN apt-get update\n")

	doc, err := h.Parse("services/auth/Dockerfile", content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "dockerfile" {
		t.Errorf("Format = %q, want dockerfile", doc.Format)
	}
	if doc.DocType != "dockerfile" {
		t.Errorf("DocType = %q, want dockerfile", doc.DocType)
	}
	if doc.Title != "Dockerfile" {
		t.Errorf("Title = %q, want Dockerfile", doc.Title)
	}
	if doc.Path != "services/auth/Dockerfile" {
		t.Errorf("Path = %q, want services/auth/Dockerfile", doc.Path)
	}
	if doc.RawContent != string(content) {
		t.Error("RawContent not preserved")
	}
}

// TestHandler_SingleStage_OneChunk verifies that a single-stage Dockerfile produces
// exactly one chunk covering the entire file. Uses enough content to exceed MinTokens.
func TestHandler_SingleStage_OneChunk(t *testing.T) {
	h := dockerfilehandler.New()
	// Realistic Python service Dockerfile — well above the MinTokens=50 threshold.
	src := `FROM python:3.12-slim

WORKDIR /app

# Install system dependencies required for building native Python extensions.
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    libpq-dev \
    && rm -rf /var/lib/apt/lists/*

# Install Python dependencies separately for better layer caching.
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application source.
COPY . .

EXPOSE 8000
CMD ["gunicorn", "--bind", "0.0.0.0:8000", "app:wsgi"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2 adds one "stage" Unit per stage alongside the "section" Unit.
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units (1 section + 1 stage) for single-stage Dockerfile, got %d", len(doc.Units))
	}

	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for single-stage Dockerfile, got %d", len(chunks))
		return
	}
	if !strings.Contains(chunks[0].Content, "FROM python") {
		t.Error("chunk content missing FROM directive at start")
	}
	if !strings.Contains(chunks[0].Content, `CMD ["gunicorn"`) {
		t.Error("chunk content missing CMD directive at end — stage content may be truncated")
	}
}

// TestHandler_MultiStage_ChunkedAtFromBoundaries verifies that a multi-stage Dockerfile
// is split into one chunk per stage. Each stage has enough content to exceed MinTokens.
func TestHandler_MultiStage_ChunkedAtFromBoundaries(t *testing.T) {
	h := dockerfilehandler.New()
	// Both stages have sufficient content to exceed the MinTokens=50 threshold.
	src := `FROM golang:1.22 AS builder
WORKDIR /app
# Download Go module dependencies in a separate layer for better Docker layer caching.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
# Build the application binary with all optimisation flags and stripped debug info.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

FROM ubuntu:22.04 AS runtime
# Install only the CA certificates needed for outbound TLS — keeps the image minimal.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && update-ca-certificates
# Run as a non-root user for defence-in-depth.
RUN useradd --system --uid 10001 appuser
COPY --from=builder /server /server
RUN chown appuser /server
USER appuser
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/server", "healthz"]
CMD ["/server"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2 adds one "stage" Unit per stage: 2 section + 2 stage = 4 total.
	if len(doc.Units) != 4 {
		t.Errorf("expected 4 Units (2 section + 2 stage) for 2-stage Dockerfile, got %d", len(doc.Units))
		return
	}

	// First two units are the "section" units named after the AS clause.
	if doc.Units[0].Title != "stage: builder" {
		t.Errorf("Units[0].Title = %q, want stage: builder", doc.Units[0].Title)
	}
	if doc.Units[1].Title != "stage: runtime" {
		t.Errorf("Units[1].Title = %q, want stage: runtime", doc.Units[1].Title)
	}

	// Stage 1 should contain the build instructions, not the runtime ones.
	if strings.Contains(doc.Units[0].Content, "EXPOSE") {
		t.Error("stage 1 content should not contain EXPOSE from stage 2")
	}
	if !strings.Contains(doc.Units[1].Content, "EXPOSE") {
		t.Error("stage 2 content should contain EXPOSE")
	}

	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks for 2-stage Dockerfile, got %d", len(chunks))
	}
}

// TestHandler_ThreeStages checks a three-stage Node.js pipeline with enough content
// in each stage to exceed MinTokens.
func TestHandler_ThreeStages(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM node:20 AS deps
WORKDIR /app
# Install production and development dependencies in a separate layer.
COPY package.json package-lock.json ./
RUN npm ci --include=dev && npm cache clean --force

FROM node:20 AS build
WORKDIR /app
COPY --from=deps /app/node_modules /app/node_modules
COPY tsconfig.json ./
COPY src/ ./src/
RUN npm run build && npm prune --production

FROM node:20-alpine AS runner
WORKDIR /app
RUN addgroup --system --gid 1001 nodejs && adduser --system --uid 1001 nextjs
COPY --from=build /app/dist /app/dist
COPY --from=build /app/node_modules /app/node_modules
USER nextjs
EXPOSE 3000
CMD ["node", "/app/dist/server.js"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 3 section + 3 stage = 6 total.
	if len(doc.Units) != 6 {
		t.Errorf("expected 6 Units (3 section + 3 stage), got %d", len(doc.Units))
	}
	for i, want := range []string{"stage: deps", "stage: build", "stage: runner"} {
		if doc.Units[i].Title != want {
			t.Errorf("Units[%d].Title = %q, want %q", i, doc.Units[i].Title, want)
		}
	}
}

// TestHandler_UnnamedStages checks that stages without AS get numeric titles.
func TestHandler_UnnamedStages(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22
RUN go build -o /server .

FROM alpine
COPY --from=0 /server /server
CMD ["/server"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 2 section + 2 stage = 4 total.
	if len(doc.Units) != 4 {
		t.Errorf("expected 4 Units (2 section + 2 stage), got %d", len(doc.Units))
	}
	if doc.Units[0].Title != "stage 1" {
		t.Errorf("Units[0].Title = %q, want stage 1", doc.Units[0].Title)
	}
	if doc.Units[1].Title != "stage 2" {
		t.Errorf("Units[1].Title = %q, want stage 2", doc.Units[1].Title)
	}
}

// TestHandler_CommentPreservation verifies that # comments stay in the chunk content.
// Tests both pre-FROM preamble comments and inline comments between instructions.
func TestHandler_CommentPreservation(t *testing.T) {
	h := dockerfilehandler.New()
	// Enough instructions to exceed MinTokens so Chunk() produces output.
	src := `# Use official Python runtime as a parent image
FROM python:3.12-slim

# Set the working directory in the container
WORKDIR /app

# Install OS-level build dependencies needed by some Python packages
RUN apt-get update && apt-get install -y --no-install-recommends gcc && rm -rf /var/lib/apt/lists/*

# Install Python dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy the application source code into the container
COPY . .

EXPOSE 8080
CMD ["python", "-m", "uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8080"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 1 section + 1 stage = 2 total.
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units (1 section + 1 stage), got %d", len(doc.Units))
	}

	content := doc.Units[0].Content
	if !strings.Contains(content, "# Use official Python runtime") {
		t.Error("comments before FROM should be preserved in stage content")
	}
	if !strings.Contains(content, "# Set the working directory in the container") {
		t.Error("inline comments should be preserved in stage content")
	}
}

// TestHandler_PreambleBeforeFrom verifies that # syntax= preamble is included in stage 1.
func TestHandler_PreambleBeforeFrom(t *testing.T) {
	h := dockerfilehandler.New()
	src := `# syntax=docker/dockerfile:1
# escape=\

FROM ubuntu:22.04
RUN echo hello`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 1 section + 1 stage = 2 total.
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units (1 section + 1 stage), got %d", len(doc.Units))
	}
	if !strings.Contains(doc.Units[0].Content, "# syntax=docker/dockerfile:1") {
		t.Error("preamble before first FROM should be included in stage 1 content")
	}
}

// TestHandler_FilenameVariants checks Parse with the common filename variants.
func TestHandler_FilenameVariants(t *testing.T) {
	h := dockerfilehandler.New()
	src := []byte("FROM alpine\nCMD [\"sh\"]\n")

	cases := []struct {
		path      string
		wantTitle string
	}{
		{"Dockerfile", "Dockerfile"},
		{"Dockerfile.prod", "Dockerfile.prod"},
		{"Dockerfile.dev", "Dockerfile.dev"},
		{"services/auth/Dockerfile", "Dockerfile"},
		{"auth.dockerfile", "auth.dockerfile"},
	}

	for _, tc := range cases {
		doc, err := h.Parse(tc.path, src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.path, err)
		}
		if doc.Title != tc.wantTitle {
			t.Errorf("Parse(%q).Title = %q, want %q", tc.path, doc.Title, tc.wantTitle)
		}
		if doc.Format != "dockerfile" {
			t.Errorf("Parse(%q).Format = %q, want dockerfile", tc.path, doc.Format)
		}
	}
}

// TestHandler_EmptyFile does not panic and returns empty chunks.
func TestHandler_EmptyFile(t *testing.T) {
	h := dockerfilehandler.New()
	doc, err := h.Parse("Dockerfile", []byte(""))
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk empty: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty Dockerfile, got %d", len(chunks))
	}
}

// TestHandler_HandlersForFilenameDispatch verifies that HandlersFor (used by the
// graph-pass walker) also resolves Dockerfile via the filename glob mechanism.
func TestHandler_HandlersForFilenameDispatch(t *testing.T) {
	reg := indexer.DefaultRegistry()

	cases := []string{
		"Dockerfile",
		"Dockerfile.prod",
		"services/api/Dockerfile",
		"auth.dockerfile",
	}
	for _, path := range cases {
		hs := reg.HandlersFor(path)
		if len(hs) == 0 {
			t.Errorf("HandlersFor(%q) returned no handlers, want dockerfile handler", path)
			continue
		}
		if hs[0].Name() != "dockerfile" {
			t.Errorf("HandlersFor(%q)[0].Name() = %q, want dockerfile", path, hs[0].Name())
		}
	}
}

// TestHandler_HandlersSliceNoDuplicates verifies that Handlers() does not return the
// dockerfile handler twice when it registers via both Register and RegisterByFilenameGlob.
func TestHandler_HandlersSliceNoDuplicates(t *testing.T) {
	reg := indexer.DefaultRegistry()
	counts := make(map[string]int)
	for _, h := range reg.Handlers() {
		counts[h.Name()]++
	}
	if counts["dockerfile"] > 1 {
		t.Errorf("dockerfile handler appears %d times in Handlers(), want 1", counts["dockerfile"])
	}
}

// TestHandler_BareFromLine exercises the bare-FROM branch (FROM with no image name),
// which is syntactically unusual but should not panic.
func TestHandler_BareFromLine(t *testing.T) {
	h := dockerfilehandler.New()
	// A FROM line with only the keyword — treated as a stage boundary.
	src := "FROM\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Should yield one unit — the bare FROM still counts as a stage start.
	if len(doc.Units) == 0 {
		t.Error("expected at least 1 Unit for bare FROM, got 0")
	}
}

// TestHandler_PlatformFlag checks that FROM lines with --platform=... still
// produce the correct stage title when an AS clause is present.
func TestHandler_PlatformFlag(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM --platform=$BUILDPLATFORM golang:1.22 AS cross-builder
WORKDIR /app
# Download all Go dependencies before copying source for better layer caching.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /server ./cmd/server

FROM --platform=$TARGETPLATFORM alpine:3.19 AS final
RUN apk add --no-cache ca-certificates tzdata
COPY --from=cross-builder /server /server
CMD ["/server"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 2 section + 2 stage = 4 total.
	if len(doc.Units) != 4 {
		t.Fatalf("expected 4 Units (2 section + 2 stage), got %d", len(doc.Units))
	}
	if doc.Units[0].Title != "stage: cross-builder" {
		t.Errorf("Units[0].Title = %q, want stage: cross-builder", doc.Units[0].Title)
	}
	if doc.Units[1].Title != "stage: final" {
		t.Errorf("Units[1].Title = %q, want stage: final", doc.Units[1].Title)
	}
}

// TestHandler_UnnamedStages_ChunkBehavior verifies Chunk() with stages that have
// enough content to clear MinTokens. (The Parse-only test uses short bodies.)
func TestHandler_UnnamedStages_ChunkBehavior(t *testing.T) {
	h := dockerfilehandler.New()
	// Both stages have enough content to exceed MinTokens=50.
	src := `FROM golang:1.22
WORKDIR /app
# Download all Go dependencies in a separate step for Docker layer caching.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

FROM alpine:3.19
# Install CA certificates and timezone data for production use.
RUN apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates \
    && rm -rf /var/cache/apk/*
# Create a non-root system user to run the server binary for defence in depth.
RUN addgroup -S appgroup && adduser -S -G appgroup -u 10001 appuser
COPY --from=0 /server /server
RUN chown appuser:appgroup /server
USER appuser
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD ["/server", "-healthz"]
CMD ["/server"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 2 section + 2 stage = 4 total.
	if len(doc.Units) != 4 {
		t.Fatalf("expected 4 Units (2 section + 2 stage), got %d", len(doc.Units))
	}
	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks for 2 unnamed stages, got %d", len(chunks))
	}
}

// TestHandler_FromScratch_NoBody verifies that a minimal Dockerfile with a single
// FROM line and no body instructions (empty stage body) does not panic and produces
// a valid ParsedDoc with at least one unit. Exercises the empty-body case directly
// via Parse(); the stage content is just the FROM line itself.
func TestHandler_FromScratch_NoBody(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM scratch\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if doc == nil {
		t.Fatal("Parse returned nil doc")
	}
	if doc.Format != "dockerfile" {
		t.Errorf("Format = %q, want dockerfile", doc.Format)
	}
	// v2: 1 section + 1 stage = 2 total. No inherits/import edge because "scratch" is special.
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units for single FROM-only Dockerfile, got %d", len(doc.Units))
	}
}

// TestHandler_ChunkFallback_EmptyUnits exercises the Chunk() raw-content fallback path:
// when doc.Units is empty (no stage sections), ChunkSections falls back to emitting a
// single chunk keyed on the whole raw content. This is intentional v1 behavior —
// documented in splitIntoStageUnits's defensive empty-content guard.
//
// Note: Parse() always produces at least one unit per FROM line (since each stage slice
// includes the FROM line itself). The only way to reach this Chunk() fallback via
// normal Parse() is an empty file (tested by TestHandler_EmptyFile). This test
// constructs a ParsedDoc directly with zero units to exercise the fallback path in
// isolation and confirm it produces exactly one raw-content chunk.
func TestHandler_ChunkFallback_EmptyUnits(t *testing.T) {
	h := dockerfilehandler.New()
	// Enough raw content to exceed MinTokens=50 so the fallback chunk is actually emitted.
	rawContent := "FROM golang:1.22 AS builder\n# Build stage with no explicit body lines beyond FROM\n" +
		"# This raw content is used to verify that Chunk() falls back to a single raw-content chunk\n" +
		"# when no section Units are provided. Intentional v1 behavior per splitIntoStageUnits.\n"

	doc := &indexer.ParsedDoc{
		Path:       "Dockerfile",
		Format:     "dockerfile",
		Title:      "Dockerfile",
		DocType:    "dockerfile",
		RawContent: rawContent,
		Units:      nil, // explicitly no units → triggers ChunkSections raw-content fallback
	}

	chunks, err := h.Chunk(doc, indexer.DefaultChunkConfig())
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 raw-content fallback chunk for empty units, got %d", len(chunks))
		return
	}
	if !strings.Contains(chunks[0].Content, "FROM golang") {
		t.Error("raw-content fallback chunk should contain the original raw content")
	}
}

// TestHandler_ChunkEmbeddingTextAndSectionHeading verifies that produced chunks have
// SectionHeading set to the stage title and EmbeddingText contains both the document
// title and the stage heading — analogous to TestHandler_ChunkEmbeddingTextContainsTitle
// in the SQL handler tests.
func TestHandler_ChunkEmbeddingTextAndSectionHeading(t *testing.T) {
	h := dockerfilehandler.New()
	// Multi-stage with named stages so we can verify headings by name.
	src := `FROM golang:1.22 AS build
WORKDIR /app
# Download Go module dependencies for Docker layer caching before copying source.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server ./cmd/server

FROM alpine:3.19 AS release
RUN apk add --no-cache ca-certificates tzdata && update-ca-certificates
COPY --from=build /server /server
RUN addgroup -S app && adduser -S -G app app && chown app /server
USER app
EXPOSE 8080
CMD ["/server"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.ChunkConfig{MaxTokens: 512, MinTokens: 1})
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// First chunk corresponds to the "build" stage.
	if chunks[0].SectionHeading != "stage: build" {
		t.Errorf("chunks[0].SectionHeading = %q, want stage: build", chunks[0].SectionHeading)
	}
	if !strings.Contains(chunks[0].EmbeddingText, "Dockerfile") {
		t.Errorf("EmbeddingText should contain document title 'Dockerfile': %q", chunks[0].EmbeddingText)
	}
	if !strings.Contains(chunks[0].EmbeddingText, "stage: build") {
		t.Errorf("EmbeddingText should contain section heading 'stage: build': %q", chunks[0].EmbeddingText)
	}
}

// TestHandler_SignalsPipeline verifies that ExtractRationaleSignals fires on Dockerfile
// TODO/FIXME comments and the signals surface on the produced Units.
func TestHandler_SignalsPipeline(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM python:3.12-slim
WORKDIR /app
# TODO: pin the requirements.txt version before going to production
COPY requirements.txt .
# FIXME: this RUN layer is too broad and busts the cache too often
RUN pip install --no-cache-dir -r requirements.txt && pip cache purge
COPY . .
EXPOSE 8000
CMD ["python", "-m", "uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Units) == 0 {
		t.Fatal("expected at least 1 unit")
	}

	// Signals should be extracted — at least the TODO and FIXME.
	allSignals := doc.Units[0].Signals
	if len(allSignals) == 0 {
		t.Error("expected at least one signal (TODO/FIXME) on stage unit, got none")
	}
	foundTODO := false
	for _, s := range allSignals {
		if strings.Contains(strings.ToUpper(s.Value), "TODO") || strings.Contains(strings.ToUpper(s.Detail), "TODO") {
			foundTODO = true
		}
	}
	if !foundTODO {
		t.Errorf("expected a TODO signal, got signals: %+v", allSignals)
	}
}

// TestHandler_LowercaseFromAs verifies case-insensitive FROM/AS detection so that
// `from node:20 as app` (all lowercase) is treated identically to `FROM node:20 AS app`.
func TestHandler_LowercaseFromAs(t *testing.T) {
	h := dockerfilehandler.New()
	// Use lowercase from/as; stage bodies must be long enough to exceed MinTokens.
	src := `from node:20 as deps
workdir /app
# Install only production dependencies in a locked layer for reproducible builds.
copy package.json package-lock.json ./
run npm ci --omit=dev && npm cache clean --force

from node:20-alpine as runner
workdir /app
# Copy the installed modules and built artefacts from the deps stage.
copy --from=deps /app/node_modules /app/node_modules
copy . .
expose 3000
cmd ["node", "server.js"]`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// v2: 2 section + 2 stage = 4 total.
	if len(doc.Units) != 4 {
		t.Fatalf("expected 4 Units (2 section + 2 stage) for lowercase from/as Dockerfile, got %d", len(doc.Units))
	}
	if doc.Units[0].Title != "stage: deps" {
		t.Errorf("Units[0].Title = %q, want stage: deps", doc.Units[0].Title)
	}
	if doc.Units[1].Title != "stage: runner" {
		t.Errorf("Units[1].Title = %q, want stage: runner", doc.Units[1].Title)
	}
}

// ── Graph-pass tests (v2, lib-nf6) ──────────────────────────────────────────

// stageUnits returns only the "stage" Kind units from a slice (graph-pass units).
func stageUnits(units []indexer.Unit) []indexer.Unit {
	var out []indexer.Unit
	for _, u := range units {
		if u.Kind == "stage" {
			out = append(out, u)
		}
	}
	return out
}

// refsOfKind returns refs filtered by Kind and, optionally, relation metadata.
func refsOfKind(refs []indexer.Reference, kind, relation string) []indexer.Reference {
	var out []indexer.Reference
	for _, r := range refs {
		if r.Kind != kind {
			continue
		}
		if relation != "" {
			if r.Metadata == nil {
				continue
			}
			if r.Metadata["relation"] != relation {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// TestGraphPass_MultiStage_StageNodes verifies that a two-stage Dockerfile emits
// exactly two "stage" Units (one per FROM) with the correct Path / Title and that
// a stage-to-stage inherits edge is emitted when the second stage uses the first
// stage as its base image.
func TestGraphPass_MultiStage_StageNodes(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM ubuntu:22.04 AS base\nRUN echo setup\n\nFROM base AS runtime\nRUN echo run\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stage Units, got %d", len(stages))
	}

	if stages[0].Path != "stage:base" {
		t.Errorf("stages[0].Path = %q, want stage:base", stages[0].Path)
	}
	if stages[0].Title != "base" {
		t.Errorf("stages[0].Title = %q, want base", stages[0].Title)
	}
	if stages[1].Path != "stage:runtime" {
		t.Errorf("stages[1].Path = %q, want stage:runtime", stages[1].Path)
	}
	if stages[1].Title != "runtime" {
		t.Errorf("stages[1].Title = %q, want runtime", stages[1].Title)
	}

	// Stage-to-stage inherits edge: runtime → base.
	stageEdges := refsOfKind(doc.Refs, "inherits", "stage")
	if len(stageEdges) != 1 {
		t.Fatalf("expected 1 stage inherits edge, got %d: %+v", len(stageEdges), stageEdges)
	}
	if stageEdges[0].Source != "stage:runtime" {
		t.Errorf("edge Source = %q, want stage:runtime", stageEdges[0].Source)
	}
	if stageEdges[0].Target != "stage:base" {
		t.Errorf("edge Target = %q, want stage:base", stageEdges[0].Target)
	}
}

// TestGraphPass_ExternalBaseImage verifies that a stage whose base is an external
// image (not a prior stage) emits an import Reference with node_kind="external" and
// the normalized docker.io/library/ prefix for official images.
func TestGraphPass_ExternalBaseImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM alpine:3.18\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}
	if stages[0].Path != "stage:stage-1" {
		t.Errorf("unnamed stage path = %q, want stage:stage-1", stages[0].Path)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref, got %d: %+v", len(extRefs), extRefs)
	}
	if extRefs[0].Target != "docker.io/library/alpine:3.18" {
		t.Errorf("external ref Target = %q, want docker.io/library/alpine:3.18", extRefs[0].Target)
	}
	if extRefs[0].Metadata["node_kind"] != "external" {
		t.Errorf("external ref node_kind = %q, want external", extRefs[0].Metadata["node_kind"])
	}
	if extRefs[0].Source != "stage:stage-1" {
		t.Errorf("external ref Source = %q, want stage:stage-1", extRefs[0].Source)
	}
}

// TestGraphPass_DockerHubUserImage verifies that a Docker Hub user image
// (single slash, no registry dot) gets the docker.io/ prefix without /library/.
func TestGraphPass_DockerHubUserImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM myuser/myapp:1.0\nCMD [\"./myapp\"]\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref, got %d", len(extRefs))
	}
	if extRefs[0].Target != "docker.io/myuser/myapp:1.0" {
		t.Errorf("Target = %q, want docker.io/myuser/myapp:1.0", extRefs[0].Target)
	}
}

// TestGraphPass_CustomRegistryImage verifies that an image with a registry prefix
// (contains a dot in the first path segment) is left unchanged.
func TestGraphPass_CustomRegistryImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM gcr.io/distroless/base:nonroot\nCMD [\"/app\"]\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref, got %d", len(extRefs))
	}
	if extRefs[0].Target != "gcr.io/distroless/base:nonroot" {
		t.Errorf("Target = %q, want gcr.io/distroless/base:nonroot", extRefs[0].Target)
	}
}

// TestGraphPass_RegistryWithPort verifies that an image whose registry component
// includes a port number (e.g. localhost:5000/myapp) is left unchanged by
// normalizeDockerImage — the port colon must not be confused with the tag separator.
func TestGraphPass_RegistryWithPort(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM localhost:5000/myapp:latest\nCMD [\"/myapp\"]\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref, got %d", len(extRefs))
	}
	if extRefs[0].Target != "localhost:5000/myapp:latest" {
		t.Errorf("Target = %q, want localhost:5000/myapp:latest (unchanged)", extRefs[0].Target)
	}
}

// TestGraphPass_ExposeCmdEntrypoint verifies that EXPOSE, CMD, and ENTRYPOINT
// directives are captured as metadata on the stage Unit.
func TestGraphPass_ExposeCmdEntrypoint(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM ubuntu:22.04 AS web
EXPOSE 8080 443
ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}
	meta := stages[0].Metadata

	expose, ok := meta["expose"].([]string)
	if !ok || len(expose) != 2 {
		t.Errorf("expose metadata = %v, want [\"8080\", \"443\"]", meta["expose"])
	} else {
		if expose[0] != "8080" || expose[1] != "443" {
			t.Errorf("expose = %v, want [8080 443]", expose)
		}
	}

	cmd, ok := meta["cmd"].([]string)
	if !ok || len(cmd) != 1 {
		t.Errorf("cmd metadata = %v, want 1 entry", meta["cmd"])
	} else if cmd[0] != `["nginx", "-g", "daemon off;"]` {
		t.Errorf("cmd[0] = %q, want [\"nginx\", \"-g\", \"daemon off;\"]", cmd[0])
	}

	ep, ok := meta["entrypoint"].([]string)
	if !ok || len(ep) != 1 {
		t.Errorf("entrypoint metadata = %v, want 1 entry", meta["entrypoint"])
	} else if ep[0] != `["/docker-entrypoint.sh"]` {
		t.Errorf("entrypoint[0] = %q, want [\"/docker-entrypoint.sh\"]", ep[0])
	}

	// base_image must always be present.
	if meta["base_image"] != "ubuntu:22.04" {
		t.Errorf("base_image = %q, want ubuntu:22.04", meta["base_image"])
	}
}

// TestGraphPass_CopyFromEdge verifies that COPY --from=<stage> emits an inherits
// edge with relation="copy-from" from the current stage to the source stage.
func TestGraphPass_CopyFromEdge(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN go build -o /server .

FROM alpine:3.19 AS runtime
COPY --from=builder /server /server
CMD ["/server"]
`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from edge, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:runtime" {
		t.Errorf("copy edge Source = %q, want stage:runtime", copyEdges[0].Source)
	}
	if copyEdges[0].Target != "stage:builder" {
		t.Errorf("copy edge Target = %q, want stage:builder", copyEdges[0].Target)
	}
}

// TestGraphPass_CopyFromIndex verifies that COPY --from=<N> (numeric index) resolves
// to the correct stage name.
func TestGraphPass_CopyFromIndex(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22
RUN go build -o /server .

FROM alpine
COPY --from=0 /server /server
CMD ["/server"]
`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from edge for index 0, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:stage-2" {
		t.Errorf("copy edge Source = %q, want stage:stage-2", copyEdges[0].Source)
	}
	if copyEdges[0].Target != "stage:stage-1" {
		t.Errorf("copy edge Target = %q, want stage:stage-1", copyEdges[0].Target)
	}
}

// TestGraphPass_ScratchBase verifies that a FROM scratch stage emits a stage Unit
// but no inherits or import edge (scratch is a special built-in with no real image).
func TestGraphPass_ScratchBase(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM scratch\nCOPY --from=0 /server /server\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	// No FROM dependency edge for scratch.
	stageEdges := refsOfKind(doc.Refs, "inherits", "stage")
	if len(stageEdges) != 0 {
		t.Errorf("expected no stage inherits edges for scratch base, got %d", len(stageEdges))
	}
	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 0 {
		t.Errorf("expected no external import refs for scratch, got %d", len(extRefs))
	}
	// COPY --from=0 in a single-stage Dockerfile resolves to a self-loop — must be dropped.
	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 0 {
		t.Errorf("expected no copy-from edges for scratch-base, got %d", len(copyEdges))
	}
}

// TestGraphPass_StageLineNumbers verifies that stage Units carry the correct
// 1-indexed line number of the FROM directive.
func TestGraphPass_StageLineNumbers(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM golang:1.22 AS build\nRUN go build .\n\nFROM alpine AS final\nCMD [\"/app\"]\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stage Units, got %d", len(stages))
	}
	if stages[0].Loc.Line != 1 {
		t.Errorf("stages[0] line = %d, want 1", stages[0].Loc.Line)
	}
	if stages[1].Loc.Line != 4 {
		t.Errorf("stages[1] line = %d, want 4", stages[1].Loc.Line)
	}
}

// TestGraphPass_MultipleExternalImages verifies that a Dockerfile with multiple
// FROM lines each using external images emits one external import edge per stage.
func TestGraphPass_MultipleExternalImages(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN go build -o /app .

FROM node:20 AS frontend
RUN npm run build

FROM nginx:alpine AS web
COPY --from=frontend /dist /usr/share/nginx/html
COPY --from=builder /app /app
`

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 3 {
		t.Fatalf("expected 3 external import refs, got %d: %+v", len(extRefs), extRefs)
	}

	// Two COPY --from edges from the web stage.
	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 2 {
		t.Fatalf("expected 2 copy-from edges, got %d: %+v", len(copyEdges), copyEdges)
	}
	for _, e := range copyEdges {
		if e.Source != "stage:web" {
			t.Errorf("copy edge Source = %q, want stage:web", e.Source)
		}
	}
}

// TestGraphPass_ARGBasedBaseImage verifies that FROM $BASE_IMAGE (ARG substitution)
// is skipped cleanly: no external import ref is emitted because the value is
// unresolvable at parse time.
func TestGraphPass_ARGBasedBaseImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "ARG BASE_IMAGE=ubuntu:22.04\nFROM $BASE_IMAGE AS app\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}
	if stages[0].Path != "stage:app" {
		t.Errorf("stage path = %q, want stage:app", stages[0].Path)
	}
	// ARG-substituted base is unresolvable at parse time — no edge should be emitted.
	if len(doc.Refs) != 0 {
		t.Errorf("expected no refs for ARG-based base, got %d: %+v", len(doc.Refs), doc.Refs)
	}
}

// TestGraphPass_DigestPinnedLocalStage verifies that a stage-to-stage reference that
// includes a digest pin (e.g. FROM base@sha256:abc123 AS runtime) still resolves to a
// stage inherits edge rather than an external import edge.
//
// This is the key regression for the lib-mcog bug: strings.Index(lowerBase, ':') used
// to find the colon inside 'sha256:abc' before the tag separator, producing
// baseNameOnly='base@sha256' instead of 'base'. The fix strips '@<digest>' first so
// the lookup correctly resolves to the local stage name.
func TestGraphPass_DigestPinnedLocalStage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM ubuntu:22.04 AS base\nRUN echo setup\n\nFROM base@sha256:abc123def456 AS runtime\nRUN echo run\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stage Units, got %d", len(stages))
	}

	// Stage-to-stage inherits edge: runtime → base (not an external import).
	stageEdges := refsOfKind(doc.Refs, "inherits", "stage")
	if len(stageEdges) != 1 {
		t.Fatalf("expected 1 stage inherits edge for digest-pinned local stage ref, got %d: %+v", len(stageEdges), doc.Refs)
	}
	if stageEdges[0].Source != "stage:runtime" {
		t.Errorf("edge Source = %q, want stage:runtime", stageEdges[0].Source)
	}
	if stageEdges[0].Target != "stage:base" {
		t.Errorf("edge Target = %q, want stage:base", stageEdges[0].Target)
	}

	// No external import ref should be emitted for the runtime stage.
	extRefs := refsOfKind(doc.Refs, "import", "")
	for _, r := range extRefs {
		if r.Source == "stage:runtime" {
			t.Errorf("unexpected external import ref for digest-pinned local stage: %+v", r)
		}
	}
}

// TestGraphPass_SpaceSeparatedPlatformFlag verifies that FROM --platform linux/amd64
// ubuntu:22.04 (space-separated flag, two tokens) correctly resolves ubuntu:22.04 as
// the base image rather than linux/amd64.
func TestGraphPass_SpaceSeparatedPlatformFlag(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM --platform linux/amd64 ubuntu:22.04\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}
	if stages[0].Metadata["base_image"] != "ubuntu:22.04" {
		t.Errorf("base_image = %q, want ubuntu:22.04 (linux/amd64 is the flag value, not the image)",
			stages[0].Metadata["base_image"])
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref, got %d: %+v", len(extRefs), extRefs)
	}
	if extRefs[0].Target != "docker.io/library/ubuntu:22.04" {
		t.Errorf("Target = %q, want docker.io/library/ubuntu:22.04", extRefs[0].Target)
	}
}

// TestGraphPass_SpaceSeparatedPlatformFlagWithAS verifies that FROM --platform linux/amd64
// ubuntu:22.04 AS myapp correctly extracts both the base image and the stage name.
func TestGraphPass_SpaceSeparatedPlatformFlagWithAS(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM --platform linux/amd64 ubuntu:22.04 AS myapp\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}
	if stages[0].Path != "stage:myapp" {
		t.Errorf("stage path = %q, want stage:myapp", stages[0].Path)
	}
	if stages[0].Metadata["base_image"] != "ubuntu:22.04" {
		t.Errorf("base_image = %q, want ubuntu:22.04", stages[0].Metadata["base_image"])
	}
}

// TestGraphPass_MixedFlagForms verifies that a Dockerfile mixing --key=value and
// --key value flag forms across stages parses all base images correctly.
func TestGraphPass_MixedFlagForms(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM --platform=linux/amd64 golang:1.22 AS builder
RUN go build -o /app .

FROM --platform linux/arm64 alpine:3.19 AS runner
COPY --from=builder /app /app
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stage Units, got %d", len(stages))
	}
	if stages[0].Metadata["base_image"] != "golang:1.22" {
		t.Errorf("stages[0] base_image = %q, want golang:1.22", stages[0].Metadata["base_image"])
	}
	if stages[1].Metadata["base_image"] != "alpine:3.19" {
		t.Errorf("stages[1] base_image = %q, want alpine:3.19", stages[1].Metadata["base_image"])
	}
}

// TestGraphPass_DigestPinnedBaseImage verifies that FROM ubuntu@sha256:abc123 emits
// an external import edge and that the digest ref is correctly normalized.
func TestGraphPass_DigestPinnedBaseImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := "FROM ubuntu@sha256:abc123def456\nRUN echo hello\n"

	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	if len(extRefs) != 1 {
		t.Fatalf("expected 1 external import ref for digest-pinned image, got %d: %+v", len(extRefs), extRefs)
	}
	// normalizeDockerImage should prepend docker.io/library/ for official images.
	if extRefs[0].Target != "docker.io/library/ubuntu@sha256:abc123def456" {
		t.Errorf("Target = %q, want docker.io/library/ubuntu@sha256:abc123def456", extRefs[0].Target)
	}
	if extRefs[0].Metadata["node_kind"] != "external" {
		t.Errorf("node_kind = %q, want external", extRefs[0].Metadata["node_kind"])
	}
}

// TestGraphPass_MultiLineCopyFrom_FlagOnContinuation is the primary regression
// test for the backslash-continuation COPY --from bug: when the --from flag
// appears on a continuation line (not the COPY line itself), it was silently
// dropped because fields[0] was "--from=..." rather than "COPY".
func TestGraphPass_MultiLineCopyFrom_FlagOnContinuation(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN go build -o /server .

FROM alpine:3.19 AS runtime
COPY \
  --from=builder \
  /server /server
CMD ["/server"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from edge for multi-line COPY --from, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:runtime" {
		t.Errorf("copy edge Source = %q, want stage:runtime", copyEdges[0].Source)
	}
	if copyEdges[0].Target != "stage:builder" {
		t.Errorf("copy edge Target = %q, want stage:builder", copyEdges[0].Target)
	}
}

// TestGraphPass_MultiLineCopyFrom_FlagOnFirstLine verifies that a COPY --from
// where the flag is already on the COPY line but the paths span continuations
// still emits the edge (pre-existing behavior, not broken by the fix).
func TestGraphPass_MultiLineCopyFrom_FlagOnFirstLine(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN go build -o /server .

FROM alpine:3.19 AS runtime
COPY --from=builder \
  /server \
  /server
CMD ["/server"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from edge, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:runtime" {
		t.Errorf("copy edge Source = %q, want stage:runtime", copyEdges[0].Source)
	}
	if copyEdges[0].Target != "stage:builder" {
		t.Errorf("copy edge Target = %q, want stage:builder", copyEdges[0].Target)
	}
}

// TestGraphPass_MultiLineCopyFrom_MultipleStages verifies that backslash
// continuation COPY --from lines are correctly resolved across multiple stages
// and that stage assignment is not confused by the line-merging.
func TestGraphPass_MultiLineCopyFrom_MultipleStages(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS build
RUN go build -o /app .

FROM node:20 AS assets
RUN npm run build

FROM nginx:alpine AS web
COPY \
  --from=assets \
  /app/dist /usr/share/nginx/html
COPY \
  --from=build \
  /app /app
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 2 {
		t.Fatalf("expected 2 copy-from edges, got %d: %+v", len(copyEdges), copyEdges)
	}
	for _, e := range copyEdges {
		if e.Source != "stage:web" {
			t.Errorf("copy edge Source = %q, want stage:web", e.Source)
		}
	}
	targets := map[string]bool{}
	for _, e := range copyEdges {
		targets[e.Target] = true
	}
	if !targets["stage:assets"] {
		t.Error("expected copy-from edge to stage:assets")
	}
	if !targets["stage:build"] {
		t.Error("expected copy-from edge to stage:build")
	}
}

// TestGraphPass_RunContinuation_NoCopyFromEdge verifies that a multi-line RUN
// instruction (backslash continuation) does not produce any spurious copy-from
// edges and that stage assignment remains correct for subsequent directives.
func TestGraphPass_RunContinuation_NoCopyFromEdge(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM ubuntu:22.04 AS base
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       ca-certificates \
    && rm -rf /var/lib/apt/lists/*
EXPOSE 8080

FROM base AS app
COPY --from=base /etc/ssl /etc/ssl
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Only one copy-from edge (the explicit COPY --from=base).
	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from edge, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:app" {
		t.Errorf("copy edge Source = %q, want stage:app", copyEdges[0].Source)
	}
	if copyEdges[0].Target != "stage:base" {
		t.Errorf("copy edge Target = %q, want stage:base", copyEdges[0].Target)
	}

	// EXPOSE 8080 on the base stage must still be captured despite the
	// multi-line RUN preceding it.
	stages := stageUnits(doc.Units)
	var baseStage *indexer.Unit
	for i := range stages {
		if stages[i].Path == "stage:base" {
			baseStage = &stages[i]
			break
		}
	}
	if baseStage == nil {
		t.Fatal("stage:base not found in stage units")
	}
	expose, _ := baseStage.Metadata["expose"].([]string)
	if len(expose) == 0 || expose[0] != "8080" {
		t.Errorf("base stage expose = %v, want [8080]", expose)
	}
}

// TestGraphPass_CopyFromExternalImage verifies that COPY --from=<external-image>
// (a ref that contains '/' or ':' and matches no local stage) emits an external
// import edge rather than being silently dropped.
func TestGraphPass_CopyFromExternalImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM alpine:3.19 AS runtime
COPY --from=gcr.io/distroless/base /lib/x86_64-linux-gnu /lib/x86_64-linux-gnu
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	// One for the FROM alpine base, one for the COPY --from external image.
	if len(extRefs) != 2 {
		t.Fatalf("expected 2 external import refs (FROM + COPY --from), got %d: %+v", len(extRefs), extRefs)
	}

	// Find the COPY --from ref specifically.
	var copyFromRef *indexer.Reference
	for i := range extRefs {
		if strings.Contains(extRefs[i].Target, "distroless") {
			copyFromRef = &extRefs[i]
		}
	}
	if copyFromRef == nil {
		t.Fatalf("expected external import ref for COPY --from=gcr.io/distroless/base, got refs: %+v", doc.Refs)
	}
	if copyFromRef.Target != "gcr.io/distroless/base" {
		t.Errorf("Target = %q, want gcr.io/distroless/base", copyFromRef.Target)
	}
	if copyFromRef.Metadata["node_kind"] != "external" {
		t.Errorf("node_kind = %q, want external", copyFromRef.Metadata["node_kind"])
	}
	if copyFromRef.Source != "stage:runtime" {
		t.Errorf("Source = %q, want stage:runtime", copyFromRef.Source)
	}

	// No spurious copy-from (inherits) edge should be emitted for an external ref.
	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 0 {
		t.Errorf("expected no copy-from inherits edges for external ref, got %d: %+v", len(copyEdges), copyEdges)
	}
}

// TestGraphPass_CopyFromExternalImage_Tagged verifies that a tagged external image
// in COPY --from (e.g. gcr.io/distroless/base:nonroot) is also handled.
func TestGraphPass_CopyFromExternalImage_Tagged(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN go build -o /app .

FROM gcr.io/distroless/base:nonroot AS final
COPY --from=builder /app /app
COPY --from=gcr.io/distroless/base:debug /busybox/sh /busybox/sh
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// One copy-from inherits edge (builder → final) and one external import for
	// the COPY --from=gcr.io/distroless/base:debug ref.
	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 1 {
		t.Fatalf("expected 1 copy-from inherits edge, got %d: %+v", len(copyEdges), copyEdges)
	}
	if copyEdges[0].Source != "stage:final" || copyEdges[0].Target != "stage:builder" {
		t.Errorf("copy-from edge = {%q→%q}, want {stage:final→stage:builder}", copyEdges[0].Source, copyEdges[0].Target)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	var debugRef *indexer.Reference
	for i := range extRefs {
		if strings.Contains(extRefs[i].Target, "debug") {
			debugRef = &extRefs[i]
		}
	}
	if debugRef == nil {
		t.Fatalf("expected external import ref for COPY --from=gcr.io/distroless/base:debug, refs: %+v", extRefs)
	}
	if debugRef.Target != "gcr.io/distroless/base:debug" {
		t.Errorf("Target = %q, want gcr.io/distroless/base:debug", debugRef.Target)
	}
	if debugRef.Metadata["node_kind"] != "external" {
		t.Errorf("node_kind = %q, want external", debugRef.Metadata["node_kind"])
	}
}

// TestGraphPass_CopyFromDockerHubImage verifies that a Docker Hub user image in
// COPY --from (e.g. myuser/mytools) gets the docker.io/ prefix.
func TestGraphPass_CopyFromDockerHubImage(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM alpine:3.19 AS runtime
COPY --from=myuser/mytools:latest /usr/local/bin/tool /usr/local/bin/tool
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	extRefs := refsOfKind(doc.Refs, "import", "")
	var toolRef *indexer.Reference
	for i := range extRefs {
		if strings.Contains(extRefs[i].Target, "mytools") {
			toolRef = &extRefs[i]
		}
	}
	if toolRef == nil {
		t.Fatalf("expected external import ref for COPY --from=myuser/mytools, refs: %+v", extRefs)
	}
	if toolRef.Target != "docker.io/myuser/mytools:latest" {
		t.Errorf("Target = %q, want docker.io/myuser/mytools:latest", toolRef.Target)
	}
	if toolRef.Metadata["node_kind"] != "external" {
		t.Errorf("node_kind = %q, want external", toolRef.Metadata["node_kind"])
	}
}

// TestGraphPass_CopyFromUnknownStageName verifies that COPY --from=<ref> where ref
// contains no '/' or ':' and matches no local stage or index is silently dropped
// (existing behavior preserved — no spurious edges).
func TestGraphPass_CopyFromUnknownStageName(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM alpine:3.19 AS runtime
COPY --from=nonexistent /file /file
CMD ["/app"]
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	copyEdges := refsOfKind(doc.Refs, "inherits", "copy-from")
	if len(copyEdges) != 0 {
		t.Errorf("expected no copy-from edges for unknown bare name, got %d: %+v", len(copyEdges), copyEdges)
	}
	// Also no spurious external import edges for the unknown name.
	for _, r := range doc.Refs {
		if r.Kind == "import" && strings.Contains(r.Target, "nonexistent") {
			t.Errorf("unexpected import edge for bare unknown name: %+v", r)
		}
	}
}

// ── BuildKit RUN --mount tests (lib-li3p) ───────────────────────────────────

// runMounts extracts the run_mounts metadata slice from a stage Unit.
func runMounts(u indexer.Unit) []map[string]string {
	v, _ := u.Metadata["run_mounts"].([]map[string]string)
	return v
}

// TestGraphPass_RunMount_CacheMount verifies that RUN --mount=type=cache,target=...
// captures mount metadata on the stage node.
func TestGraphPass_RunMount_CacheMount(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=type=cache,target=/root/.cache/go go build ./...
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 1 {
		t.Fatalf("expected 1 run_mount, got %d: %v", len(mounts), mounts)
	}
	if mounts[0]["type"] != "cache" {
		t.Errorf("mount type = %q, want cache", mounts[0]["type"])
	}
	if mounts[0]["target"] != "/root/.cache/go" {
		t.Errorf("mount target = %q, want /root/.cache/go", mounts[0]["target"])
	}
}

// TestGraphPass_RunMount_MultipleTypesInOneRun verifies that multiple --mount flags
// on a single RUN instruction all appear in run_mounts.
func TestGraphPass_RunMount_MultipleTypesInOneRun(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM ubuntu:22.04 AS app
RUN --mount=type=cache,target=/cache --mount=type=secret,id=mysecret,target=/run/secrets/token cat /run/secrets/token
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 2 {
		t.Fatalf("expected 2 run_mounts, got %d: %v", len(mounts), mounts)
	}

	types := map[string]bool{}
	for _, m := range mounts {
		types[m["type"]] = true
	}
	if !types["cache"] {
		t.Error("expected a cache mount")
	}
	if !types["secret"] {
		t.Error("expected a secret mount")
	}
}

// TestGraphPass_RunMount_MultipleRunInstructions verifies that mounts from multiple
// RUN instructions in the same stage are all accumulated.
func TestGraphPass_RunMount_MultipleRunInstructions(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=type=cache,target=/root/.cache/go go mod download
RUN --mount=type=cache,target=/root/.cache/go go build -o /app ./cmd/app
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 2 {
		t.Fatalf("expected 2 run_mounts (one per RUN), got %d: %v", len(mounts), mounts)
	}
	for i, m := range mounts {
		if m["type"] != "cache" {
			t.Errorf("mounts[%d] type = %q, want cache", i, m["type"])
		}
		if m["target"] != "/root/.cache/go" {
			t.Errorf("mounts[%d] target = %q, want /root/.cache/go", i, m["target"])
		}
	}
}

// TestGraphPass_RunMount_NoMountsProducesNoMetadata verifies that a RUN instruction
// without any --mount flags does not produce a run_mounts metadata key.
func TestGraphPass_RunMount_NoMountsProducesNoMetadata(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM alpine:3.19 AS app
RUN echo hello
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	if _, ok := stages[0].Metadata["run_mounts"]; ok {
		t.Error("expected no run_mounts metadata for a RUN without --mount flags")
	}
}

// TestGraphPass_RunMount_TargetAliases verifies that --mount=type=bind,dst=/src and
// --mount=type=bind,destination=/src are both normalised so the metadata key is "target".
func TestGraphPass_RunMount_TargetAliases(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=type=bind,dst=/workspace go build .
RUN --mount=type=bind,destination=/src go vet ./...
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 2 {
		t.Fatalf("expected 2 run_mounts, got %d: %v", len(mounts), mounts)
	}
	for i, m := range mounts {
		if _, hasDst := m["dst"]; hasDst {
			t.Errorf("mounts[%d] should not have 'dst' key (should be normalised to 'target')", i)
		}
		if _, hasDest := m["destination"]; hasDest {
			t.Errorf("mounts[%d] should not have 'destination' key (should be normalised to 'target')", i)
		}
		if m["target"] == "" {
			t.Errorf("mounts[%d] missing 'target' key after alias normalisation: %v", i, m)
		}
	}
}

// TestGraphPass_RunMount_DefaultTypeBind verifies that --mount= without a type= key
// defaults to type=bind per the BuildKit spec.
func TestGraphPass_RunMount_DefaultTypeBind(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=target=/workspace go build .
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 1 {
		t.Fatalf("expected 1 run_mount, got %d: %v", len(mounts), mounts)
	}
	if mounts[0]["type"] != "bind" {
		t.Errorf("mount type = %q, want bind (default)", mounts[0]["type"])
	}
}

// TestGraphPass_RunMount_MountsPerStage verifies that mounts are attributed to the
// correct stage in a multi-stage Dockerfile.
func TestGraphPass_RunMount_MountsPerStage(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=type=cache,target=/root/.cache/go go build ./...

FROM alpine:3.19 AS runtime
RUN echo no mounts here
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 2 {
		t.Fatalf("expected 2 stage Units, got %d", len(stages))
	}

	// Find builder and runtime stages by path.
	var builderStage, runtimeStage *indexer.Unit
	for i := range stages {
		switch stages[i].Path {
		case "stage:builder":
			builderStage = &stages[i]
		case "stage:runtime":
			runtimeStage = &stages[i]
		}
	}
	if builderStage == nil {
		t.Fatal("stage:builder not found")
	}
	if runtimeStage == nil {
		t.Fatal("stage:runtime not found")
	}

	builderMounts := runMounts(*builderStage)
	if len(builderMounts) != 1 || builderMounts[0]["type"] != "cache" {
		t.Errorf("builder stage: expected 1 cache mount, got %v", builderMounts)
	}

	if _, ok := runtimeStage.Metadata["run_mounts"]; ok {
		t.Error("runtime stage: expected no run_mounts metadata")
	}
}

// TestGraphPass_RunMount_MultiLineMountFlag verifies that a backslash-continued RUN
// instruction with --mount on a continuation line is handled correctly.
func TestGraphPass_RunMount_MultiLineMountFlag(t *testing.T) {
	h := dockerfilehandler.New()
	src := `FROM golang:1.22 AS builder
RUN --mount=type=cache,target=/root/.cache/go \
    go build \
    ./...
`
	doc, err := h.Parse("Dockerfile", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	stages := stageUnits(doc.Units)
	if len(stages) != 1 {
		t.Fatalf("expected 1 stage Unit, got %d", len(stages))
	}

	mounts := runMounts(stages[0])
	if len(mounts) != 1 {
		t.Fatalf("expected 1 run_mount for multi-line RUN, got %d: %v", len(mounts), mounts)
	}
	if mounts[0]["type"] != "cache" {
		t.Errorf("mount type = %q, want cache", mounts[0]["type"])
	}
	if mounts[0]["target"] != "/root/.cache/go" {
		t.Errorf("mount target = %q, want /root/.cache/go", mounts[0]["target"])
	}
}
