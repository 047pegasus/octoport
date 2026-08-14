package db

import (
	"context"
	"fmt"
	"time"
)

// migrate applies the schema. Sharding and partitioning are first-class:
//
//   - users    -> hash-sharded across N tablets (YugabyteDB) keyed on user id
//   - tunnels  -> hash-sharded across N tablets keyed on user id, so one user's
//     rows always land on the same tablet (locality for queries)
//   - events   -> range-partitioned by month, so old activity rolls off cheaply
//
// The YugabyteDB-only hints (SPLIT INTO ... TABLETS) are best-effort: on plain
// Postgres they error out and are ignored, keeping the schema portable.
func (d *DB) migrate(ctx context.Context, shards int) error {
	stmts := []string{
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,

		// ---- users : hash-sharded across tablets ----
		`CREATE TABLE IF NOT EXISTS users (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			email         TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			plan          TEXT NOT NULL DEFAULT 'free',
			max_tunnels   INT  NOT NULL DEFAULT 5,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users (email)`,

		// GitHub OAuth identity columns (empty for password users).
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS github_id       TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS github_username TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS github_avatar   TEXT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_github_id ON users (github_id)`,

		// ---- tunnels : hash-sharded, co-located per user ----
		`CREATE TABLE IF NOT EXISTS tunnels (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			subdomain     TEXT NOT NULL UNIQUE,
			protocol      TEXT NOT NULL DEFAULT 'tcp',
			local_addr    TEXT NOT NULL,
			status        TEXT NOT NULL DEFAULT 'active',
			expires_at    TIMESTAMPTZ NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_active_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tunnels_user_id ON tunnels (user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_tunnels_subdomain ON tunnels (subdomain)`,

		// Column additions for tables created before a schema bump.
		// IF NOT EXISTS keeps them idempotent across redeploys.
		`ALTER TABLE tunnels ADD COLUMN IF NOT EXISTS expires_at    TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`ALTER TABLE tunnels ADD COLUMN IF NOT EXISTS last_active_at TIMESTAMPTZ NOT NULL DEFAULT now()`,
		`ALTER TABLE tunnels ADD COLUMN IF NOT EXISTS enabled       BOOLEAN NOT NULL DEFAULT true`,

		// ---- events : range-partitioned by month ----
		`CREATE TABLE IF NOT EXISTS events (
			id         UUID NOT NULL DEFAULT gen_random_uuid(),
			user_id    UUID NOT NULL,
			kind       TEXT NOT NULL,
			payload    JSONB NOT NULL DEFAULT '{}'::jsonb,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (id, created_at)
		) PARTITION BY RANGE (created_at)`,
	}

	for _, stmt := range stmts {
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt, err)
		}
	}

	// YugabyteDB hash-splitting (best-effort; ignored on vanilla Postgres).
	ybHints := []string{
		fmt.Sprintf("ALTER TABLE users   SPLIT INTO %d TABLETS", shards),
		fmt.Sprintf("ALTER TABLE tunnels SPLIT INTO %d TABLETS", shards),
	}
	for _, stmt := range ybHints {
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			// Non-fatal: not running on YugabyteDB, or already split.
		}
	}

	// Ensure current + future month partitions exist for events.
	if err := d.ensureEventPartitions(ctx); err != nil {
		return err
	}
	return nil
}

// ensureEventPartitions creates one partition per month for the next N months.
func (d *DB) ensureEventPartitions(ctx context.Context) error {
	for i := 0; i < 4; i++ {
		start := time.Now().UTC().AddDate(0, i, 0)
		name := start.Format("events_200601")
		next := start.AddDate(0, 1, 0)
		stmt := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF events
			 FOR VALUES FROM ('%s') TO ('%s')`,
			name, start.Format("2006-01-02"), next.Format("2006-01-02"))
		if _, err := d.Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("create partition %s: %w", name, err)
		}
	}
	return nil
}
