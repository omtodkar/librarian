package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/embedding"
	helixclient "librarian/internal/helix"
)

func registerGetContext(s *server.MCPServer, client *helixclient.Client, embedder embedding.Embedder) {
	tool := mcp.NewTool("get_context",
		mcp.WithDescription("Comprehensive briefing: semantic search combined with graph traversal for related docs and code references. Use this for understanding a topic in depth."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language query for the topic you want context on"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Number of initial search results"),
			mcp.DefaultNumber(5),
			mcp.Min(1),
			mcp.Max(10),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := req.GetInt("limit", 5)

		// Step 1: Embed query and run semantic search
		vector, err := embedder.Embed(query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embedding query: %v", err)), nil
		}

		chunks, err := client.SearchChunks(vector, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		output := fmt.Sprintf("=== BRIEFING: %q ===\n\n", query)

		// Step 2: Primary sources
		output += "## Primary Sources (direct matches):\n\n"
		if len(chunks) == 0 {
			output += "No direct matches found.\n\n"
		} else {
			for _, chunk := range chunks {
				output += fmt.Sprintf("### %s > %s\n", chunk.FilePath, chunk.SectionHeading)
				output += chunk.Content + "\n\n"
			}
		}

		// Step 3: Collect unique source documents
		seenDocs := make(map[string]bool)
		var docIDs []string

		output += "## Source Documents:\n"
		for _, chunk := range chunks {
			if seenDocs[chunk.FilePath] {
				continue
			}
			seenDocs[chunk.FilePath] = true
			doc, err := client.GetDocumentByPath(chunk.FilePath)
			if err != nil {
				continue
			}
			docIDs = append(docIDs, doc.ID)
			output += fmt.Sprintf("- %s (%s) - %q\n", doc.FilePath, doc.DocType, doc.Title)
		}
		output += "\n"

		// Step 4: Referenced code files
		output += "## Referenced Code Files:\n"
		seenCodeFiles := make(map[string]bool)
		for _, docID := range docIDs {
			codeFiles, err := client.GetReferencedCodeFiles(docID)
			if err != nil {
				continue
			}
			for _, cf := range codeFiles {
				if seenCodeFiles[cf.FilePath] {
					continue
				}
				seenCodeFiles[cf.FilePath] = true
				output += fmt.Sprintf("- %s (%s)\n", cf.FilePath, cf.Language)
			}
		}
		if len(seenCodeFiles) == 0 {
			output += "None found.\n"
		}
		output += "\n"

		// Step 5: Related documents
		output += "## Related Documentation:\n"
		seenRelated := make(map[string]bool)
		for _, docID := range docIDs {
			related, err := client.GetRelatedDocuments(docID)
			if err != nil {
				continue
			}
			for _, rel := range related {
				if seenRelated[rel.FilePath] || seenDocs[rel.FilePath] {
					continue
				}
				seenRelated[rel.FilePath] = true
				output += fmt.Sprintf("- %s - %q\n", rel.FilePath, rel.Title)
			}
		}
		if len(seenRelated) == 0 {
			output += "None found.\n"
		}

		return mcp.NewToolResultText(output), nil
	})
}
