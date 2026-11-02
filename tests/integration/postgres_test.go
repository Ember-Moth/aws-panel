package integration

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/herta-network/aws-panel/internal/app/migration/schema"
	"github.com/herta-network/aws-panel/internal/config"
	"github.com/herta-network/aws-panel/internal/infra/postgres"
	"github.com/herta-network/aws-panel/internal/module/platform"
	"github.com/herta-network/aws-panel/internal/repository/kernel"
)

func TestPostgresFoundation(t *testing.T) {
	portText := os.Getenv("AWS_PANEL_TEST_DB_PORT")
	if portText == "" {
		t.Skip("set AWS_PANEL_TEST_DB_PORT to test against an isolated PostgreSQL instance")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal("invalid test database port")
	}
	cfg := config.Defaults().Database
	cfg.Port = port
	cfg.Name = "aws_panel_test"
	if name := os.Getenv("AWS_PANEL_TEST_DB_NAME"); name != "" {
		cfg.Name = name
	}
	if !strings.HasPrefix(cfg.Name, "aws_panel_test") {
		t.Fatal("test database name must start with aws_panel_test")
	}
	cfg.Password = os.Getenv("AWS_PANEL_TEST_DB_PASSWORD")
	migrationConfig := cfg
	if user := os.Getenv("AWS_PANEL_TEST_MIGRATION_USER"); user != "" {
		migrationConfig.User = user
		migrationConfig.Password = os.Getenv("AWS_PANEL_TEST_MIGRATION_PASSWORD")
	}
	if err := schema.Up(migrationConfig); err != nil {
		t.Fatal(err)
	}
	if err := schema.Up(migrationConfig); err != nil {
		t.Fatal("migration is not repeatable:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := schema.Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if migrationConfig.User != cfg.User {
		_, err := pool.ExecContext(ctx, "CREATE TABLE runtime_must_not_create_tables (id integer)")
		if err == nil {
			_, _ = pool.ExecContext(ctx, "DROP TABLE runtime_must_not_create_tables")
			t.Fatal("runtime role unexpectedly has DDL privileges")
		}
	}

	// Hold two connections simultaneously, proving timezone is set for each
	// new connection instead of issuing SET once on an arbitrary pooled one.
	var connections []*sql.Conn
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range 2 {
		connection, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		var timezone string
		if err := connection.QueryRowContext(ctx, "SHOW TimeZone").Scan(&timezone); err != nil {
			t.Fatal(err)
		}
		if timezone != config.Timezone {
			t.Fatal("incorrect PostgreSQL session timezone")
		}
	}

	store := platform.NewRepository(kernel.Connection{DB: db})
	runtime, err := store.Runtime(ctx)
	if err != nil || runtime.Name != "AWS Panel" {
		t.Fatalf("runtime settings: %v", err)
	}
	instant := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
	if err := store.TouchHeartbeat(ctx, "integration", instant); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Heartbeat(ctx, "integration")
	if err != nil || loaded == nil || !loaded.Equal(instant) {
		t.Fatalf("timestamp roundtrip failed: %v", err)
	}
	var date string
	if err := pool.QueryRowContext(ctx, "SELECT observed_at::date::text FROM platform_heartbeats WHERE component = 'integration'").Scan(&date); err != nil {
		t.Fatal(err)
	}
	if date != "2026-11-02" {
		t.Fatalf("wrong Beijing date boundary: %s", date)
	}
}
