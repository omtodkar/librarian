package indexer

import (
	"fmt"

	"librarian/internal/store"
)

// buildTestEdges is the post-graph-pass resolver that emits tests edges from
// test files to their likely subject files via path-naming conventions.
// Runs after per-file projection so all code_file nodes exist in the store.
// Guarded by cfg.Graph.TestEdges.Enabled (default true).
func (idx *Indexer) buildTestEdges(result *GraphResult) {
	codeFileNodes, err := idx.store.ListNodesByKind(store.NodeKindCodeFile)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("test_edges: list code_file nodes: %s", err))
		return
	}

	knownPaths := make(map[string]struct{}, len(codeFileNodes))
	for _, n := range codeFileNodes {
		if n.SourcePath != "" {
			knownPaths[n.SourcePath] = struct{}{}
		}
	}

	for _, n := range codeFileNodes {
		if n.SourcePath == "" {
			continue
		}
		subjects := testSubjectLinker(n.SourcePath, knownPaths)
		for _, sub := range subjects {
			if err := idx.store.UpsertEdge(store.Edge{
				From:     store.CodeFileNodeID(n.SourcePath),
				To:       store.CodeFileNodeID(sub),
				Kind:     store.EdgeKindTests,
				Metadata: `{"heuristic":"path-convention"}`,
			}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("test_edges: %s→%s: %s", n.SourcePath, sub, err))
				continue
			}
			result.EdgesAdded++
		}
	}
}
