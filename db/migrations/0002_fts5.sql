-- +goose Up
-- +goose StatementBegin
CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunks_fts USING fts5(content);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS doc_chunks_ai AFTER INSERT ON doc_chunks BEGIN
    INSERT INTO doc_chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS doc_chunks_ad AFTER DELETE ON doc_chunks BEGIN
    DELETE FROM doc_chunks_fts WHERE rowid = old.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS doc_chunks_au AFTER UPDATE OF content ON doc_chunks BEGIN
    DELETE FROM doc_chunks_fts WHERE rowid = old.id;
    INSERT INTO doc_chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
-- +goose StatementEnd

-- Populate the FTS index from any rows already in doc_chunks (handles upgrades from v1).
-- +goose StatementBegin
INSERT INTO doc_chunks_fts(rowid, content) SELECT id, content FROM doc_chunks;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS doc_chunks_au;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS doc_chunks_ad;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS doc_chunks_ai;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS doc_chunks_fts;
-- +goose StatementEnd
