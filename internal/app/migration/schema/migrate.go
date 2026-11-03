package schema

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed *.sql
var files embed.FS

const Version uint = 10

func Up(cfg config.Database) error {
	source, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", source, cfg.DSN("pgx5"))
	if err != nil {
		_ = source.Close()
		return errors.New("cannot initialize PostgreSQL migrations; check connection and permissions")
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return errors.New("PostgreSQL migration failed; inspect schema version and migration permissions")
	}
	return nil
}

func Check(ctx context.Context, db *sql.DB) error {
	var version uint
	var dirty bool
	err := db.QueryRowContext(ctx, "SELECT version, dirty FROM public.schema_migrations LIMIT 1").Scan(&version, &dirty)
	if err != nil {
		return errors.New("database schema is not initialized; run aws-panel migrate up")
	}
	if dirty {
		return errors.New("database migration is dirty; resolve it before starting")
	}
	if version != Version {
		return fmt.Errorf("database schema version %d does not match required version %d", version, Version)
	}
	return nil
}
