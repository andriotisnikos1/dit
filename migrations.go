// Package dit is the module root. It exists so that the SQL migrations can be
// embedded from migrations/ — go:embed cannot reach outside its own directory,
// so the embedded filesystem lives here and is passed to internal/store.
package dit

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// MigrationsDir is the directory inside the module holding the .sql files.
const MigrationsDir = "migrations"

// Migrations is the embedded migration set, rooted at the directory holding
// the .sql files so that a plain fs.ReadDir(".") lists them.
var Migrations fs.FS = mustSub(migrationFiles, MigrationsDir)

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		// Only reachable if the embed directive and MigrationsDir disagree,
		// which is a build-time mistake rather than a runtime condition.
		panic(fmt.Sprintf("dit: embedded migrations directory %q is missing: %v", dir, err))
	}
	return sub
}
