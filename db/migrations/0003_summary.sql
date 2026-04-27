-- +goose Up
-- +goose StatementBegin
ALTER TABLE doc_chunks ADD COLUMN summary TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS summary_cache (
    content_hash TEXT PRIMARY KEY,
    summary      TEXT NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS summary_cache;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE doc_chunks DROP COLUMN summary;
-- +goose StatementEnd
