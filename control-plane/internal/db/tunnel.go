package db

import (
	"context"
	"time"
)

// Tunnel is the persisted form of a public exposure record.
type Tunnel struct {
	ID           string
	UserID       string
	Subdomain    string
	Protocol     string
	LocalAddr    string
	Status       string
	ExpiresAt    time.Time
	CreatedAt    time.Time
	LastActiveAt time.Time
	Enabled      bool
}

// LogEvent appends to the monthly-partitioned events table. Partitioned rows
// are pruned by simply dropping old partitions.
func (d *DB) LogEvent(ctx context.Context, userID, kind string, payload any) error {
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO events (user_id, kind, payload) VALUES ($1, $2, $3)`,
		userID, kind, payload)
	return err
}

// Event is a single audit entry returned by the events API.
type Event struct {
	ID        string
	Kind      string
	Payload   []byte
	CreatedAt time.Time
}

// RecentEvents returns the newest `limit` events for a user.
func (d *DB) RecentEvents(ctx context.Context, userID string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := d.Pool.Query(ctx, `
		SELECT id, kind, payload, created_at
		FROM events
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
