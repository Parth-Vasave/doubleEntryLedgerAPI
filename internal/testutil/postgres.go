// Package testutil provides a disposable PostgreSQL instance for integration tests.
package testutil

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/internal/store"
)

// StartPostgres starts a PostgreSQL 16 container (or uses TEST_DATABASE_URL when
// set), applies migrations and returns a connected pool plus a cleanup func.
// Intended to be called once from TestMain.
func StartPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return connectAndMigrate(ctx, url, func() {})
	}
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("ledger"),
		postgres.WithUsername("ledger"),
		postgres.WithPassword("ledger"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithEnv(map[string]string{"TZ": "UTC"}),
	)
	if err != nil {
		return nil, nil, err
	}
	terminate := func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = ctr.Terminate(c)
	}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		terminate()
		return nil, nil, err
	}
	return connectAndMigrate(ctx, url, terminate)
}

func connectAndMigrate(ctx context.Context, url string, cleanup func()) (*pgxpool.Pool, func(), error) {
	pool, err := store.Connect(ctx, url, 50)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := store.Migrate(ctx, pool); err != nil {
		pool.Close()
		cleanup()
		return nil, nil, err
	}
	return pool, func() { pool.Close(); cleanup() }, nil
}

// SkipIfShort skips integration tests under `go test -short`.
func SkipIfShort(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
}

// RunMain is a TestMain helper: it starts Postgres (unless -short), stores the
// pool in *pool, runs the tests and exits with their status.
func RunMain(m *testing.M, pool **pgxpool.Pool) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	p, cleanup, err := StartPostgres(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres:", err)
		os.Exit(1)
	}
	*pool = p
	code := m.Run()
	cleanup()
	os.Exit(code)
}
