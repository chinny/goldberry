// Package migrations embeds the two goose migration trees. They are kept in
// lockstep: the same numbered files with the same intent (plan §9.1), which
// scripts/check-migrations.sh enforces in CI.
package migrations

import "embed"

//go:embed sqlite/*.sql
var SQLite embed.FS

//go:embed postgres/*.sql
var Postgres embed.FS
