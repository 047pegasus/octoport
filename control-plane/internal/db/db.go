package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB wraps a connection pool. YugabyteDB speaks the PostgreSQL wire protocol,
// so we talk to it with pgx exactly as we would a vanilla Postgres; the only
// difference is the sharding/partitioning DDL below.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect opens a pool and applies the schema. retries keeps startup resilient
// while the database container is still becoming healthy.
func Connect(ctx context.Context, dsn string, poolMax int, shards int) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = int32(poolMax)

	var pool *pgxpool.Pool
	for attempt := 1; attempt <= 10; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = pool.Ping(pingCtx)
			cancel()
		}
		if err == nil {
			break
		}
		pool.Close()
		if attempt == 10 {
			return nil, fmt.Errorf("connect to database: %w", err)
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	d := &DB{Pool: pool}
	if err := d.migrate(ctx, shards); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// Close releases the pool.
func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}
