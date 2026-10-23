// Package migrations embeds the goose SQL migrations in the binary.
//
// Embedded rather than read from disk so the production image — a distroless one with no shell and
// no copy of the source — carries exactly the migrations it was built with, and the migrate job
// (`/app/api migrate up`) runs the same image the service does. A migration that exists in the
// repository but not in the image cannot happen.
//
// sqlc reads this directory as its schema (sqlc.yaml). It parses the `-- +goose Up` sections and
// ignores the Down ones, so the queries are type-checked against exactly what the migrations build.
package migrations

import "embed"

// FS holds every migration, in the order goose applies them: by the numeric prefix.
//
//go:embed *.sql
var FS embed.FS
