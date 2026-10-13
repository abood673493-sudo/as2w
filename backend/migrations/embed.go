// Package migrations embeds the SQL migration files so that the compiled
// binary can bring a database up to date without shipping loose .sql files.
//
// File naming convention:
//
//	<version>_<name>.up.sql     applied in ascending version order
//	<version>_<name>.down.sql   reverts the matching .up.sql
//
// Versions must be numeric and unique.
package migrations

import "embed"

// FS holds every migration file in this directory.
//
//go:embed *.sql
var FS embed.FS