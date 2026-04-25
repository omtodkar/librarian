package db

import "embed"

// MigrationsFS holds every numbered goose migration under db/migrations/.
// Consumed by internal/store.Open, which hands it to goose.SetBaseFS before
// calling goose.Up("migrations"). Keeping migrations inside the binary means
// a single `go install librarian` ships the schema — no separate deploy step.
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
