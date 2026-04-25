-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS documents (
    id TEXT PRIMARY KEY,
    file_path TEXT UNIQUE NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    doc_type TEXT NOT NULL DEFAULT 'guide',
    summary TEXT NOT NULL DEFAULT '',
    headings TEXT NOT NULL DEFAULT '[]',
    frontmatter TEXT NOT NULL DEFAULT '{}',
    content_hash TEXT NOT NULL DEFAULT '',
    chunk_count INTEGER NOT NULL DEFAULT 0,
    indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS doc_chunks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_path TEXT NOT NULL,
    section_heading TEXT NOT NULL DEFAULT '',
    section_hierarchy TEXT NOT NULL DEFAULT '[]',
    chunk_index INTEGER NOT NULL DEFAULT 0,
    content TEXT NOT NULL DEFAULT '',
    token_count INTEGER NOT NULL DEFAULT 0,
    doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    signal_meta TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS code_files (
    id TEXT PRIMARY KEY,
    file_path TEXT UNIQUE NOT NULL,
    language TEXT NOT NULL DEFAULT '',
    ref_type TEXT NOT NULL DEFAULT 'file',
    content_hash TEXT NOT NULL DEFAULT '',
    last_referenced_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS refs (
    doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    code_file_id TEXT NOT NULL REFERENCES code_files(id) ON DELETE CASCADE,
    context TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (doc_id, code_file_id)
);

-- Graph spine: generic nodes + edges. Every kind of thing librarian indexes
-- (documents, code files, code symbols, config keys, ...) projects into a
-- graph_node with a stable namespaced id (e.g., "doc:{uuid}", "file:{path}",
-- "sym:com.acme.Auth.validate", "key:spring.datasource.url"). Typed edges
-- connect them for structural queries via recursive CTE traversal.
CREATE TABLE IF NOT EXISTS graph_nodes (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    label       TEXT NOT NULL DEFAULT '',
    source_path TEXT,
    metadata    TEXT NOT NULL DEFAULT '{}',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_graph_nodes_kind        ON graph_nodes(kind);
CREATE INDEX IF NOT EXISTS idx_graph_nodes_source_path ON graph_nodes(source_path);

CREATE TABLE IF NOT EXISTS graph_edges (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    from_node  TEXT NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    to_node    TEXT NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    weight     REAL NOT NULL DEFAULT 1.0,
    metadata   TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(from_node, to_node, kind)
);

CREATE INDEX IF NOT EXISTS idx_graph_edges_from ON graph_edges(from_node, kind);
CREATE INDEX IF NOT EXISTS idx_graph_edges_to   ON graph_edges(to_node, kind);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_graph_edges_to;
DROP INDEX IF EXISTS idx_graph_edges_from;
DROP TABLE IF EXISTS graph_edges;
DROP INDEX IF EXISTS idx_graph_nodes_source_path;
DROP INDEX IF EXISTS idx_graph_nodes_kind;
DROP TABLE IF EXISTS graph_nodes;
DROP TABLE IF EXISTS refs;
DROP TABLE IF EXISTS code_files;
DROP TABLE IF EXISTS doc_chunks;
DROP TABLE IF EXISTS documents;
-- +goose StatementEnd
