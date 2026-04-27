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
	if len(doc.Units) != 1 {
		t.Errorf("expected 1 Unit for single-stage Dockerfile, got %d", len(doc.Units))
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
		t.Error("chunk content missing FROM directive")
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
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units for 2-stage Dockerfile, got %d", len(doc.Units))
		return
	}

	// First stage should be named after the AS clause.
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
	if len(doc.Units) != 3 {
		t.Errorf("expected 3 Units, got %d", len(doc.Units))
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
	if len(doc.Units) != 2 {
		t.Errorf("expected 2 Units, got %d", len(doc.Units))
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
	if len(doc.Units) != 1 {
		t.Fatalf("expected 1 Unit, got %d", len(doc.Units))
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
	if len(doc.Units) != 1 {
		t.Fatalf("expected 1 Unit, got %d", len(doc.Units))
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
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units, got %d", len(doc.Units))
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
	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 Units, got %d", len(doc.Units))
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
	// One unit expected: the stage whose content is the FROM line.
	if len(doc.Units) != 1 {
		t.Errorf("expected 1 Unit for single FROM-only Dockerfile, got %d", len(doc.Units))
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
