package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/clock"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func Open(ctx context.Context, cfg config.Database) (*gorm.DB, *sql.DB, error) {
	// Let pgx decode URL parameters before GORM sees the connection. GORM's
	// own DSN timezone matcher treats an encoded slash as part of the zone.
	connection, err := pgx.ParseConfig(cfg.DSN("postgres"))
	if err != nil {
		return nil, nil, errors.New("cannot configure PostgreSQL connection")
	}
	pool := stdlib.OpenDB(*connection)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{
		NowFunc:              clock.Now,
		Logger:               gormlogger.Discard,
		DisableAutomaticPing: true,
	})
	if err != nil {
		_ = pool.Close()
		return nil, nil, errors.New("cannot configure PostgreSQL connection")
	}
	pool.SetMaxOpenConns(cfg.MaxOpen)
	pool.SetMaxIdleConns(cfg.MaxIdle)
	pool.SetConnMaxLifetime(30 * time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, nil, errors.New("cannot connect to PostgreSQL; check configuration and permissions")
	}
	var timezone string
	if err := pool.QueryRowContext(ctx, "SHOW TimeZone").Scan(&timezone); err != nil || timezone != config.Timezone {
		_ = pool.Close()
		return nil, nil, errors.New("PostgreSQL session must use Asia/Shanghai")
	}
	return db, pool, nil
}
