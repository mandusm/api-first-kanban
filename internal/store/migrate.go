// Migrations support for the store package.
package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver
	"github.com/pressly/goose/v3"

	"api-first-kanban/migrations"
)

// MigrateUp applies all pending migrations using the embedded filesystem.
func MigrateUp(ctx context.Context, databaseURL string) error {
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}
	_, err = provider.Up(ctx)
	return err
}
