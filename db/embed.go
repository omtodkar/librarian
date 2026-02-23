package db

import _ "embed"

//go:embed migrations.sql
var MigrationsSQL string
