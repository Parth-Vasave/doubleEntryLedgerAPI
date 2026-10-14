// Package store contains the PostgreSQL persistence layer.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/Parth-Vasave/doubleEntryLedgerAPI/migrations"
)

// DBTX is satisfied by *pgxpool.Pool, *pgxpool.Conn and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// PoolOption customises the pgx pool config before connecting.
type PoolOption func(*pgxpool.Config)

// Connect opens a pgx connection pool and verifies connectivity.
func Connect(ctx context.Context, url string, maxConns int32, opts ...PoolOption) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	for _, o := range opts {
		o(cfg)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Migrate applies all pending goose migrations embedded in the binary.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	return migrateDB(ctx, db)
}

func migrateDB(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Retryable reports whether err is a transient Postgres error
// (serialization failure or deadlock) that is safe to retry.
func Retryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01": // serialization_failure, deadlock_detected
			return true
		}
	}
	return false
}

// MaxTxAttempts bounds how many times InTx retries a transient failure.
const MaxTxAttempts = 5

// InTx runs fn inside a READ COMMITTED transaction, committing on success and
// retrying with jittered exponential backoff on serialization failures and
// deadlocks. fn must be safe to re-run from scratch.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 0; attempt < MaxTxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt) * 5 * time.Millisecond
			jitter := time.Duration(rand.Int64N(int64(backoff))) //nolint:gosec // retry jitter needs no CSPRNG
			select {
			case <-time.After(backoff/2 + jitter):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		err = pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, fn)
		if err == nil || !Retryable(err) {
			return err
		}
	}
	return err
}
