package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// ensureEventPartitions creates one partition per month for the current
// month plus the next three, all aligned to month boundaries. It first
// repairs any misaligned partitions left behind by older builds, so a
// restart alone heals a database that already has squatters in it.
func (d *DB) ensureEventPartitions(ctx context.Context) error {
	if err := d.repairEventPartitions(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for i := 0; i < 4; i++ {
		name, from, to := eventPartitionRange(now, i)
		if err := d.ensureEventPartition(ctx, name, from, to); err != nil {
			return err
		}
	}
	return nil
}

// eventPartitionName identifies monthly events partitions: events_YYYYMM.
var eventPartitionName = regexp.MustCompile(`^events_([0-9]{4})([0-9]{2})$`)

// eventPartitionRange returns the table name and the [from, to) bounds for
// the month `offset` months after the month containing now. Bounds always
// land on month boundaries (the 1st at 00:00 UTC), no matter which day of
// the month now falls on.
//
// Older builds anchored bounds to the current day instead
// (time.Now().UTC().AddDate(0, i, 0) formatted with day precision), so a
// run on e.g. Sep 15 created events_202612 as [2026-12-15, 2027-01-15) — a
// "December" partition sprawling halfway into January. CREATE TABLE IF NOT
// EXISTS dedupes by table name only, so the squatter survived every later
// run until a correctly-aligned neighbor (events_202701) collided with it
// and every startup migration failed with 42P17.
func eventPartitionRange(now time.Time, offset int) (name, from, to string) {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := first.AddDate(0, offset, 0)
	end := start.AddDate(0, 1, 0)
	const dayFmt = "2006-01-02"
	return start.Format("events_200601"), start.Format(dayFmt), end.Format(dayFmt)
}

// canonicalEventRange returns the correct [from, to) instants for a
// YYYYMM month name, or ok=false when name isn't a monthly partition.
func canonicalEventRange(name string) (from, to time.Time, ok bool) {
	m := eventPartitionName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, time.Time{}, false
	}
	year, err1 := strconv.Atoi(m[1])
	month, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || month < 1 || month > 12 {
		return time.Time{}, time.Time{}, false
	}
	from = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return from, from.AddDate(0, 1, 0), true
}

// eventPartition describes one existing partition of the events table.
type eventPartition struct {
	name    string
	from    time.Time
	to      time.Time
	bounded bool // false when bounds couldn't be parsed (e.g. DEFAULT)
}

// listEventPartitions reads every partition of the events table with its
// parsed [from, to) bounds. Partitions whose bounds can't be parsed are
// reported with bounded=false and are never touched.
func (d *DB) listEventPartitions(ctx context.Context) ([]eventPartition, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT c.relname, pg_get_expr(c.relpartbound, c.oid)
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'events' AND c.relispartition
		ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("list events partitions: %w", err)
	}
	defer rows.Close()
	var out []eventPartition
	for rows.Next() {
		var name string
		var expr *string
		if err := rows.Scan(&name, &expr); err != nil {
			return nil, fmt.Errorf("list events partitions: %w", err)
		}
		p := eventPartition{name: name}
		if expr != nil {
			if from, to, ok := parsePartitionBound(*expr); ok {
				p.from, p.to, p.bounded = from, to, true
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

var boundLiterals = regexp.MustCompile(`'([^']*)'`)

// parsePartitionBound extracts [from, to) from pg_get_expr output like
// "FOR VALUES FROM ('2026-12-01 00:00:00+00') TO ('2027-01-01 00:00:00+00')".
func parsePartitionBound(expr string) (from, to time.Time, ok bool) {
	lits := boundLiterals.FindAllStringSubmatch(expr, -1)
	if len(lits) < 2 {
		return time.Time{}, time.Time{}, false
	}
	from, err1 := parseBoundTime(lits[0][1])
	to, err2 := parseBoundTime(lits[1][1])
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func parseBoundTime(s string) (time.Time, error) {
	// pg_get_expr renders timestamptz bounds with a numeric offset, but be
	// liberal: session TimeZone settings change the exact rendering.
	layouts := []string{
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07",
		"2006-01-02",
		time.RFC3339,
	}
	s = strings.TrimSpace(s)
	var err error
	for _, l := range layouts {
		var t time.Time
		if t, err = time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

// repairEventPartitions rebuilds every misaligned monthly partition — one
// named events_YYYYMM whose bounds don't cover exactly that month. All rows
// are staged aside inside one transaction before anything is dropped, so
// nothing is lost; empty partitions are simply dropped and recreated.
func (d *DB) repairEventPartitions(ctx context.Context) error {
	parts, err := d.listEventPartitions(ctx)
	if err != nil {
		return err
	}
	var bad []eventPartition
	for _, p := range parts {
		wantFrom, wantTo, ok := canonicalEventRange(p.name)
		if !ok || !p.bounded {
			continue // not a monthly partition (or DEFAULT-like): never touch
		}
		if p.from.Equal(wantFrom) && p.to.Equal(wantTo) {
			continue
		}
		bad = append(bad, p)
	}
	if len(bad) == 0 {
		return nil
	}

	for _, p := range bad {
		slog.Warn("repairing misaligned events partition",
			"partition", p.name,
			"actual", p.from.UTC().Format("2006-01-02")+".."+p.to.UTC().Format("2006-01-02"))
	}

	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("repair events partitions: begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	// Stage every row from every bad partition before dropping anything.
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE events_repair_stage (
		id UUID, user_id UUID, kind TEXT, payload JSONB, created_at TIMESTAMPTZ
	)`); err != nil {
		return fmt.Errorf("repair events partitions: stage: %w", err)
	}
	for _, p := range bad {
		if _, err := tx.Exec(ctx, fmt.Sprintf(
			`INSERT INTO events_repair_stage (id, user_id, kind, payload, created_at)
			 SELECT id, user_id, kind, payload, created_at FROM %s`, quoteIdent(p.name))); err != nil {
			return fmt.Errorf("repair events partitions: stage %s: %w", p.name, err)
		}
	}
	for _, p := range bad {
		if _, err := tx.Exec(ctx, `DROP TABLE `+quoteIdent(p.name)); err != nil {
			return fmt.Errorf("repair events partitions: drop %s: %w", p.name, err)
		}
	}
	// Recreate canonical partitions for every month the staged rows touch,
	// so the re-insert below always has a home.
	months, err := repairMonths(ctx, tx)
	if err != nil {
		return err
	}
	for _, name := range months {
		wantFrom, wantTo, ok := canonicalEventRange(name)
		if !ok {
			continue
		}
		if err := createEventPartition(ctx, tx, name,
			wantFrom.Format("2006-01-02"), wantTo.Format("2006-01-02")); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (id, user_id, kind, payload, created_at)
		SELECT id, user_id, kind, payload, created_at FROM events_repair_stage`); err != nil {
		return fmt.Errorf("repair events partitions: reinsert: %w", err)
	}
	if _, err := tx.Exec(ctx, `DROP TABLE events_repair_stage`); err != nil {
		return fmt.Errorf("repair events partitions: cleanup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("repair events partitions: commit: %w", err)
	}
	slog.Info("repaired misaligned events partitions", "count", len(bad))
	return nil
}

// repairMonths returns the canonical partition names for every calendar
// month touched by the staged rows, oldest first.
func repairMonths(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT to_char(date_trunc('month', created_at), '"events_"YYYYMM')
		FROM events_repair_stage
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("repair events partitions: months: %w", err)
	}
	var months []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, fmt.Errorf("repair events partitions: months: %w", err)
		}
		months = append(months, name)
	}
	rows.Close()
	return months, rows.Err()
}

// ensureEventPartition creates the named partition with exactly [from, to)
// bounds. Tolerates losing a creation race with another control plane
// starting at the same time, but refuses to silently keep a partition whose
// bounds don't match — that is what caused the 42P17 crash loop.
func (d *DB) ensureEventPartition(ctx context.Context, name, from, to string) error {
	if !eventPartitionName.MatchString(name) {
		return fmt.Errorf("refusing to manage unexpected partition name %q", name)
	}
	err := createEventPartition(ctx, d.Pool, name, from, to)
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
		// Another control plane won the creation race; make sure it built
		// the range we want instead of assuming.
		found, matches, verr := d.partitionBoundsMatch(ctx, name, from, to)
		if verr != nil {
			return verr
		}
		if found && matches {
			return nil
		}
	}
	return fmt.Errorf("create partition %s [%s, %s): %w", name, from, to, err)
}

// partitionBoundsMatch reports whether a partition with this name exists
// and whether its bounds equal [from, to).
func (d *DB) partitionBoundsMatch(ctx context.Context, name, from, to string) (found, matches bool, err error) {
	wantFrom, werr1 := parseBoundTime(from)
	wantTo, werr2 := parseBoundTime(to)
	if werr1 != nil || werr2 != nil {
		return false, false, fmt.Errorf("bad wanted bounds [%s, %s)", from, to)
	}
	var expr *string
	err = d.Pool.QueryRow(ctx, `
		SELECT pg_get_expr(c.relpartbound, c.oid)
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'events' AND c.relispartition AND c.relname = $1`, name).Scan(&expr)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read partition %s bounds: %w", name, err)
	}
	if expr == nil {
		return true, false, nil
	}
	haveFrom, haveTo, ok := parsePartitionBound(*expr)
	if !ok {
		return true, false, nil
	}
	return true, haveFrom.Equal(wantFrom) && haveTo.Equal(wantTo), nil
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// createEventPartition runs the bare CREATE without IF NOT EXISTS so name
// collisions and bound overlaps surface as errors instead of being hidden.
func createEventPartition(ctx context.Context, db execer, name, from, to string) error {
	if !eventPartitionName.MatchString(name) {
		return fmt.Errorf("refusing to create unexpected partition name %q", name)
	}
	_, err := db.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF events FOR VALUES FROM ('%s') TO ('%s')`,
		quoteIdent(name), from, to))
	return err
}

// quoteIdent quotes an identifier that has already passed the strict
// events_YYYYMM allowlist check above.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
