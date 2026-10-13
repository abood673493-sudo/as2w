// Package database provides PostgreSQL connectivity (pgx) and the embedded
// migration runner used by the panel.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options describes how to connect to PostgreSQL.
type Options struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	ConnTimeout     time.Duration
	ApplicationName string
}

// DB wraps a pgx connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// Open creates the connection pool and verifies that the database is reachable.
func Open(ctx context.Context, opts Options) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	if opts.MaxConns > 0 {
		poolCfg.MaxConns = opts.MaxConns
	}
	if opts.MinConns >= 0 {
		poolCfg.MinConns = opts.MinConns
	}
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 30 * time.Minute
	poolCfg.HealthCheckPeriod = time.Minute

	name := opts.ApplicationName
	if name == "" {
		name = "ssh-manager-panel"
	}
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = make(map[string]string, 1)
	}
	poolCfg.ConnConfig.RuntimeParams["application_name"] = name

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	timeout := opts.ConnTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	return &DB{pool: pool}, nil
}

// Pool exposes the underlying pgx pool for repositories.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping verifies that the database is still reachable.
func (db *DB) Ping(ctx context.Context) error {
	if db.pool == nil {
		return fmt.Errorf("database is not initialised")
	}
	return db.pool.Ping(ctx)
}

// Stats returns pool statistics for the health endpoint.
func (db *DB) Stats() *pgxpool.Stat {
	if db.pool == nil {
		return nil
	}
	return db.pool.Stat()
}

// Close releases every pooled connection.
func (db *DB) Close() {
	if db.pool != nil {
		db.pool.Close()
	}
}