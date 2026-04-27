-- +goose Up
-- +goose StatementBegin
ALTER TABLE graph_nodes ADD COLUMN line_number INTEGER NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- DROP COLUMN requires SQLite >= 3.35.0 (2021); mattn/go-sqlite3 bundles a modern SQLite.
ALTER TABLE graph_nodes DROP COLUMN line_number;
-- +goose StatementEnd
