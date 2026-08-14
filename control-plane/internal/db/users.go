package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound is returned when a query matches no rows.
	ErrNotFound = errors.New("db: not found")
	// ErrConflict is returned on a unique-violation (e.g. duplicate email).
	ErrConflict = errors.New("db: conflict")
)

// User mirrors the users table. Queries are shard-co-located on user id so a
// single user's reads/writes hit one tablet on YugabyteDB.
type User struct {
	ID           string
	Email        string
	PasswordHash string
	Plan         string
	MaxTunnels   int
	GitHubID     string
	GitHubAvatar string
	CreatedAt    time.Time
}

// CreateUser inserts a new account. Returns ErrConflict if the email is taken.
func (d *DB) CreateUser(ctx context.Context, email, passwordHash string, maxTunnels int) (*User, error) {
	u := &User{}
	err := d.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, plan, max_tunnels)
		VALUES ($1, $2, 'free', $3)
		RETURNING id, email, password_hash, plan, max_tunnels, COALESCE(github_id, ''), COALESCE(github_avatar, ''), created_at`,
		email, passwordHash, maxTunnels,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Plan, &u.MaxTunnels, &u.GitHubID, &u.GitHubAvatar, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, err
	}
	return u, nil
}

// GetUserByEmail fetches an account for the auth login path.
func (d *DB) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := d.Pool.QueryRow(ctx, `
		SELECT id, email, password_hash, plan, max_tunnels, COALESCE(github_id, ''), COALESCE(github_avatar, ''), created_at
		FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Plan, &u.MaxTunnels, &u.GitHubID, &u.GitHubAvatar, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByGitHubID fetches an account linked to a GitHub identity.
func (d *DB) GetUserByGitHubID(ctx context.Context, githubID string) (*User, error) {
	u := &User{}
	err := d.Pool.QueryRow(ctx, `
		SELECT id, email, password_hash, plan, max_tunnels, COALESCE(github_id, ''), COALESCE(github_avatar, ''), created_at
		FROM users WHERE github_id = $1`, githubID,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Plan, &u.MaxTunnels, &u.GitHubID, &u.GitHubAvatar, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpsertGitHubUser finds or creates the account behind a GitHub login:
//
//  1. A user already linked to this GitHub id wins (display fields refreshed).
//  2. Otherwise an account with the same email is linked to the GitHub id.
//  3. Otherwise a brand-new free account is created (passwordless).
//
// Returns ErrConflict if a race tries to create the same account twice.
func (d *DB) UpsertGitHubUser(ctx context.Context, email, githubID, githubUsername, githubAvatar string, maxTunnels int) (*User, error) {
	if u, err := d.GetUserByGitHubID(ctx, githubID); err == nil {
		_, err = d.Pool.Exec(ctx, `
			UPDATE users SET email = $2, github_username = $3, github_avatar = $4, updated_at = now()
			WHERE id = $1`, u.ID, email, githubUsername, githubAvatar)
		if err != nil {
			return nil, err
		}
		return d.GetUser(ctx, u.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	if u, err := d.GetUserByEmail(ctx, email); err == nil {
		_, err = d.Pool.Exec(ctx, `
			UPDATE users SET github_id = $2, github_username = $3, github_avatar = $4, updated_at = now()
			WHERE id = $1`, u.ID, githubID, githubUsername, githubAvatar)
		if err != nil {
			return nil, err
		}
		return d.GetUser(ctx, u.ID)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	u := &User{}
	err := d.Pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, github_id, github_username, github_avatar, plan, max_tunnels)
		VALUES ($1, '', $2, $3, $4, 'free', $5)
		RETURNING id, email, password_hash, plan, max_tunnels, COALESCE(github_id, ''), COALESCE(github_avatar, ''), created_at`,
		email, githubID, githubUsername, githubAvatar, maxTunnels,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Plan, &u.MaxTunnels, &u.GitHubID, &u.GitHubAvatar, &u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, err
	}
	return u, nil
}

// GetUser fetches an account by id.
func (d *DB) GetUser(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := d.Pool.QueryRow(ctx, `
		SELECT id, email, password_hash, plan, max_tunnels, COALESCE(github_id, ''), COALESCE(github_avatar, ''), created_at
		FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Plan, &u.MaxTunnels, &u.GitHubID, &u.GitHubAvatar, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// CountTunnels returns how many tunnels a user currently has open.
func (d *DB) CountTunnels(ctx context.Context, userID string) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM tunnels WHERE user_id = $1 AND status = 'active' AND expires_at > now()`,
		userID).Scan(&n)
	return n, err
}

// ExpireStale flips every tunnel whose window has lapsed to inactive. Cheap
// bulk hygiene so quotas never leak and audit rows stay truthful. It returns
// the distinct user ids affected so the sweeper can push a UI refresh.
func (d *DB) ExpireStale(ctx context.Context) ([]string, error) {
	rows, err := d.Pool.Query(ctx,
		`UPDATE tunnels SET status = 'inactive' WHERE status = 'active' AND expires_at <= now() RETURNING user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]struct{}{}
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return out, err
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// RecordTunnel persists a tunnel so the control plane can recover after a
// crash instead of relying purely on the cache.
func (d *DB) RecordTunnel(ctx context.Context, t *Tunnel) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO tunnels (id, user_id, subdomain, protocol, local_addr, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET
			status = EXCLUDED.status,
			expires_at = EXCLUDED.expires_at`,
		t.ID, t.UserID, t.Subdomain, t.Protocol, t.LocalAddr, t.Status, t.ExpiresAt,
	)
	return err
}

// TouchTunnel marks a tunnel as recently active (last_active_at, expires_at).
func (d *DB) TouchTunnel(ctx context.Context, id string, expiresAt time.Time) error {
	_, err := d.Pool.Exec(ctx, `
		UPDATE tunnels SET last_active_at = now(), expires_at = $2 WHERE id = $1`,
		id, expiresAt)
	return err
}

// DeactivateTunnel marks a tunnel inactive (expiry / agent disconnect).
func (d *DB) DeactivateTunnel(ctx context.Context, id string) error {
	_, err := d.Pool.Exec(ctx,
		`UPDATE tunnels SET status = 'inactive' WHERE id = $1`, id)
	return err
}

// GetTunnelByID fetches a tunnel row by its primary key (used by the delete
// path when the cache index has already aged out).
func (d *DB) GetTunnelByID(ctx context.Context, id string) (*Tunnel, error) {
	t := &Tunnel{}
	err := d.Pool.QueryRow(ctx, `
		SELECT id, user_id, subdomain, protocol, local_addr, status, expires_at, created_at, last_active_at, enabled
		FROM tunnels WHERE id = $1`, id,
	).Scan(&t.ID, &t.UserID, &t.Subdomain, &t.Protocol, &t.LocalAddr,
		&t.Status, &t.ExpiresAt, &t.CreatedAt, &t.LastActiveAt, &t.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ActiveTunnelsForUser returns the still-active, non-expired tunnels for a
// user so an agent can claim them when it connects.
func (d *DB) ActiveTunnelsForUser(ctx context.Context, userID string, now time.Time) ([]*Tunnel, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT id, user_id, subdomain, protocol, local_addr, status, expires_at, created_at, last_active_at, enabled
		FROM tunnels
		WHERE user_id = $1 AND status = 'active' AND expires_at > $2
		ORDER BY created_at ASC`, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Tunnel
	for rows.Next() {
		t := &Tunnel{}
		if err := rows.Scan(&t.ID, &t.UserID, &t.Subdomain, &t.Protocol, &t.LocalAddr,
			&t.Status, &t.ExpiresAt, &t.CreatedAt, &t.LastActiveAt, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTunnelEnabled flips the enabled flag on a tunnel row. Disabled tunnels
// keep their subdomain reserved (the row stays active until its hard deadline
// or an explicit delete) but stop routing traffic.
func (d *DB) SetTunnelEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := d.Pool.Exec(ctx,
		`UPDATE tunnels SET enabled = $2 WHERE id = $1`, id, enabled)
	return err
}
