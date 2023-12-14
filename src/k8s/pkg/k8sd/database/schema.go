package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"

	"github.com/canonical/lxd/lxd/db/schema"
	"github.com/canonical/microcluster/cluster"
)

var (
	SchemaExtensions = map[int]schema.Update{
		1: schemaApplyMigration("kubernetes-auth-tokens", "000-create.sql"),
	}

	//go:embed sql/migrations
	sqlMigrations embed.FS

	//go:embed sql/queries
	sqlQueries embed.FS
)

func schemaApplyMigration(migrationPath ...string) schema.Update {
	path := filepath.Join(append([]string{"sql", "migrations"}, migrationPath...)...)
	b, err := sqlMigrations.ReadFile(path)
	if err != nil {
		panic(fmt.Errorf("invalid migration file %s: %s", path, err))
	}
	return func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, string(b)); err != nil {
			return fmt.Errorf("failed to apply migration %s: %w", path, err)
		}
		return nil
	}
}

func mustPrepareStatement(queryPath ...string) int {
	path := filepath.Join(append([]string{"sql", "queries"}, queryPath...)...)
	b, err := sqlQueries.ReadFile(path)
	if err != nil {
		panic(fmt.Errorf("invalid query file %s: %s", path, err))
	}
	return cluster.RegisterStmt(string(b))
}