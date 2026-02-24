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
    last_referenced_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS refs (
    doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    code_file_id TEXT NOT NULL REFERENCES code_files(id) ON DELETE CASCADE,
    context TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (doc_id, code_file_id)
);

CREATE TABLE IF NOT EXISTS related_docs (
    from_doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    to_doc_id TEXT NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (from_doc_id, to_doc_id)
);
