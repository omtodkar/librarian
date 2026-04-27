package mcpserver

import "librarian/internal/store"

// storeReader is the read-only surface of the store that MCP tools consume.
// Using the interface instead of *store.Store directly lets tool helpers be
// unit-tested without a real SQLite workspace: tests inject a narrow mock
// that satisfies the interface.
type storeReader interface {
	GetNode(id string) (*store.Node, error)
	Neighbors(nodeID, direction string, kinds ...string) ([]store.Edge, error)
	ListSymbolNodesWithMetadataContaining(substring string) ([]store.Node, error)
	ListNodesByKindWithMetadataContaining(kind, substring string) ([]store.Node, error)
}
