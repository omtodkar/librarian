package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/embedding"
	"librarian/internal/store"
)

func registerSearchDocs(s *server.MCPServer, st *store.Store, embedder embedding.Embedder) {
	tool := mcp.NewTool("search_docs",
		mcp.WithDescription("Semantic search across all indexed documentation. Returns relevant chunks with file paths and section context."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language search query"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return"),
			mcp.DefaultNumber(5),
			mcp.Min(1),
			mcp.Max(20),
		),
		mcp.WithBoolean("include_refs",
			mcp.Description("Include referenced code files for each result"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := req.GetInt("limit", 5)
		includeRefs := req.GetBool("include_refs", false)

		vector, err := embedder.Embed(query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embedding query: %v", err)), nil
		}

		chunks, err := st.SearchChunks(vector, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		if len(chunks) == 0 {
			return mcp.NewToolResultText("No results found for query: " + query), nil
		}

		var output string
		output = fmt.Sprintf("Found %d results for %q:\n\n", len(chunks), query)

		for i, chunk := range chunks {
			output += fmt.Sprintf("### Result %d\n", i+1)
			output += fmt.Sprintf("**File:** %s\n", chunk.FilePath)
			output += fmt.Sprintf("**Section:** %s\n", chunk.SectionHeading)
			output += fmt.Sprintf("**Content:**\n%s\n\n", chunk.Content)

			if includeRefs {
				doc, err := st.GetDocumentByPath(chunk.FilePath)
				if err != nil {
					continue
				}
				codeFiles, err := st.GetReferencedCodeFiles(doc.ID)
				if err != nil || len(codeFiles) == 0 {
					continue
				}
				output += "**Refs:** "
				for j, cf := range codeFiles {
					if j > 0 {
						output += ", "
					}
					output += cf.FilePath
				}
				output += "\n\n"
			}
		}

		return mcp.NewToolResultText(output), nil
	})
}
