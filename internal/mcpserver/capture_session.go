package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
)

var (
	nonAlnum  = regexp.MustCompile(`[^a-z0-9]+`)
	multiDash = regexp.MustCompile(`-{2,}`)
	// stripNL removes CR+LF, bare CR, and bare LF so user-supplied strings
	// never introduce YAML line breaks in frontmatter values or headings.
	stripNL = strings.NewReplacer("\r\n", "", "\r", "", "\n", "")
)

// slugify converts a title to a lowercase dash-separated identifier, trimmed
// to 50 characters so file names stay readable.
func slugify(title string) string {
	s := strings.ToLower(title)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = multiDash.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		// Truncate at word boundary.
		s = s[:50]
		if idx := strings.LastIndex(s, "-"); idx > 0 {
			s = s[:idx]
		}
	}
	return s
}

// categoryDir maps the category argument to a subdirectory name under DocsDir.
func categoryDir(category string) string {
	switch category {
	case "decisions":
		return "decisions"
	case "faqs":
		return "faqs"
	default:
		return "sessions"
	}
}

func registerCaptureSession(s *server.MCPServer, st *store.Store, cfg *config.Config, embedder embedding.Embedder) {
	tool := mcp.NewTool("capture_session",
		mcp.WithDescription("Capture AI session insights as a markdown file, index it, and return the chunk IDs so the capture is immediately searchable."),
		mcp.WithString("title",
			mcp.Required(),
			mcp.Description("Human-readable title for this capture (used as the document heading and file-name slug)."),
		),
		mcp.WithString("body",
			mcp.Required(),
			mcp.Description("Full markdown body of the capture. May use headings, lists, code blocks, etc."),
		),
		mcp.WithString("category",
			mcp.Description("Destination category. 'sessions' (default) for general session notes, 'decisions' for architectural decisions, 'faqs' for Q&A."),
			mcp.DefaultString("sessions"),
			mcp.Enum("sessions", "decisions", "faqs"),
		),
		mcp.WithString("session_id",
			mcp.Description("Optional session identifier written to frontmatter. Useful for correlating captures from the same conversation."),
		),
		mcp.WithString("author",
			mcp.Description("Optional author name written to frontmatter."),
		),
		mcp.WithString("docs_dir",
			mcp.Description("Target docs directory; must be one of the configured doc_dirs. Defaults to the first configured docs directory."),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		title, err := req.RequireString("title")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		body, err := req.RequireString("body")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		category := req.GetString("category", "sessions")
		sessionID := stripNL.Replace(req.GetString("session_id", ""))
		author := stripNL.Replace(req.GetString("author", ""))
		docsDirArg := req.GetString("docs_dir", "")
		title = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(title)

		// Determine the base docs directory for writing. The primary dir is
		// ResolvedDocDirs()[0]; an explicit docs_dir arg overrides it if it
		// matches one of the configured dirs.
		//
		// Validation normalises BOTH the caller-supplied arg AND each configured
		// dir to absolute on-disk paths before comparing, so relative config
		// entries (the normal case, e.g. "proto/docs") match an absolute arg
		// and vice-versa. On match we keep the ORIGINAL configured-dir string
		// as the baseDir so the stored key stays ProjectRoot-relative.
		resolvedDirs := cfg.ResolvedDocDirs()
		baseDir := resolvedDirs[0]
		if docsDirArg != "" {
			absArg, absErr := filepath.Abs(docsDirArg)
			if absErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid docs_dir %q: %v", docsDirArg, absErr)), nil
			}
			var matched bool
			for _, d := range resolvedDirs {
				// Anchor relative configured dirs at ProjectRoot when available.
				dirAbs := d
				if !filepath.IsAbs(d) && cfg.ProjectRoot != "" {
					dirAbs = filepath.Join(cfg.ProjectRoot, d)
				}
				dirAbs = filepath.Clean(dirAbs)
				if dirAbs == absArg {
					baseDir = d // keep original configured string (relative or absolute)
					matched = true
					break
				}
			}
			if !matched {
				return mcp.NewToolResultError(fmt.Sprintf(
					"docs_dir %q is not a configured docs directory; valid choices are: %s",
					docsDirArg, strings.Join(resolvedDirs, ", "),
				)), nil
			}
		}

		now := time.Now()
		slug := slugify(title)
		if slug == "" {
			slug = "capture"
		}
		filename := fmt.Sprintf("%s-%s.md", now.Format("2006-01-02"), slug)
		subDir := categoryDir(category)

		// Compute the absolute write path and the canonical stored key.
		// When ProjectRoot is set, anchor relative baseDirs there so the file
		// always lands under the workspace root regardless of CWD. When
		// ProjectRoot is empty (standalone / test flows) fall back to
		// CWD-relative resolution (filepath.Abs), preserving existing behaviour.
		var absFilePath string
		var relPath string // stored key passed to IndexSingleFile
		if cfg.ProjectRoot != "" {
			// Anchor at ProjectRoot.
			baseDirAbs := baseDir
			if !filepath.IsAbs(baseDir) {
				baseDirAbs = filepath.Join(cfg.ProjectRoot, baseDir)
			}
			absFilePath = filepath.Join(baseDirAbs, subDir, filename)
			// Store as slash-normalised path relative to ProjectRoot (§3.2).
			rel, relErr := filepath.Rel(cfg.ProjectRoot, absFilePath)
			if relErr != nil {
				return mcp.NewToolResultError(fmt.Sprintf("computing relative path: %v", relErr)), nil
			}
			relPath = filepath.ToSlash(rel)
		} else {
			// Legacy CWD-relative behaviour (no ProjectRoot configured).
			relPath = filepath.Join(baseDir, subDir, filename)
			var err error
			absFilePath, err = filepath.Abs(relPath)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid file path: %v", err)), nil
			}
		}

		// Build YAML frontmatter.
		var fm strings.Builder
		fm.WriteString("---\n")
		fmt.Fprintf(&fm, "type: %s\n", category)
		fm.WriteString("source: ai-capture\n")
		if sessionID != "" {
			fmt.Fprintf(&fm, "session_id: %q\n", sessionID)
		}
		if author != "" {
			fmt.Fprintf(&fm, "author: %q\n", author)
		}
		fmt.Fprintf(&fm, "date: %s\n", now.Format("2006-01-02"))
		fm.WriteString("---\n\n")

		content := fm.String() + "# " + title + "\n\n" + body

		if err := os.MkdirAll(filepath.Dir(absFilePath), 0755); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create directories: %v", err)), nil
		}
		if err := os.WriteFile(absFilePath, []byte(content), 0644); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to write file: %v", err)), nil
		}

		// Incremental reindex of the single new file.
		idx := indexer.New(st, cfg, embedder)
		result, err := idx.IndexSingleFile(relPath, absFilePath, true)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("re-indexing failed: %v", err)), nil
		}

		// Retrieve chunk IDs so the caller can search its own capture immediately.
		var chunkIDs []string
		var chunkLookupWarning string
		doc, docErr := st.GetDocumentByPath(relPath)
		if docErr != nil {
			chunkLookupWarning = fmt.Sprintf("warning: could not look up indexed document: %v", docErr)
		} else {
			chunks, chunksErr := st.GetChunksForDocument(doc.ID)
			if chunksErr != nil {
				chunkLookupWarning = fmt.Sprintf("warning: could not retrieve chunk IDs: %v", chunksErr)
			} else {
				for _, c := range chunks {
					chunkIDs = append(chunkIDs, c.ID)
				}
			}
		}

		var out strings.Builder
		fmt.Fprintf(&out, "Captured %s\n\n", relPath)
		fmt.Fprintf(&out, "Indexed:\n")
		fmt.Fprintf(&out, "  Documents: %d\n", result.DocumentsIndexed)
		fmt.Fprintf(&out, "  Chunks:    %d\n", result.ChunksCreated)
		if len(result.Errors) > 0 {
			fmt.Fprintf(&out, "  Errors:    %d\n", len(result.Errors))
			for _, e := range result.Errors {
				fmt.Fprintf(&out, "    - %s\n", e)
			}
		}
		if len(chunkIDs) > 0 {
			fmt.Fprintf(&out, "\nChunk IDs: %s\n", strings.Join(chunkIDs, ", "))
		}
		if chunkLookupWarning != "" {
			fmt.Fprintf(&out, "\n%s\n", chunkLookupWarning)
		}

		return mcp.NewToolResultText(out.String()), nil
	})
}
