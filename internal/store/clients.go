package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Client is a machine running the agent.
type Client struct {
	ID              int64
	Name            string
	AgentSecretHash string // empty until pairing completes
	PortMin         int    // 0 when the client has no own range
	PortMax         int
	Overrides       string // JSON, decoded by the fleet service
	DesiredRev      int64
	AppliedRev      int64
	LastSeenAt      time.Time // zero if never seen
	CreatedAt       time.Time
}

// Paired reports whether an agent has completed pairing.
func (c Client) Paired() bool { return c.AgentSecretHash != "" }

// ClientRepo persists clients.
type ClientRepo struct{ base }

const clientCols = `id, name, COALESCE(agent_secret_hash, ''), COALESCE(port_range_min, 0),
	COALESCE(port_range_max, 0), overrides_json, desired_rev, applied_rev, last_seen_at, created_at`

func scanClient(row interface{ Scan(...any) error }) (Client, error) {
	var (
		c        Client
		lastSeen sql.NullInt64
		created  int64
	)
	err := row.Scan(&c.ID, &c.Name, &c.AgentSecretHash, &c.PortMin, &c.PortMax,
		&c.Overrides, &c.DesiredRev, &c.AppliedRev, &lastSeen, &created)
	c.LastSeenAt, c.CreatedAt = nullTime(lastSeen), time.Unix(created, 0)
	return c, err
}

// Create inserts a client. Overrides defaults to "{}".
func (r *ClientRepo) Create(ctx context.Context, c Client) (Client, error) {
	if c.Overrides == "" {
		c.Overrides = "{}"
	}
	c.CreatedAt = time.Unix(r.unixNow(), 0)
	var pmin, pmax any
	if c.PortMin != 0 {
		pmin, pmax = c.PortMin, c.PortMax
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO clients
		(name, port_range_min, port_range_max, overrides_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.Name, pmin, pmax, c.Overrides, c.CreatedAt.Unix())
	if err != nil {
		return Client{}, mapUnique(err, map[string]error{"clients.name": ErrNameTaken})
	}
	c.ID, err = res.LastInsertId()
	return c, err
}

// Get returns a client or ErrNotFound.
func (r *ClientRepo) Get(ctx context.Context, id int64) (Client, error) {
	c, err := scanClient(r.q.QueryRowContext(ctx, `SELECT `+clientCols+` FROM clients WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	return c, err
}

// List returns all clients ordered by name.
func (r *ClientRepo) List(ctx context.Context) ([]Client, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+clientCols+` FROM clients ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes a client and, by cascade, its proxies, visitor grants and
// pairing codes.
func (r *ClientRepo) Delete(ctx context.Context, id int64) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id))
}

// BumpDesiredRev increments a client's desired revision and returns it.
func (r *ClientRepo) BumpDesiredRev(ctx context.Context, id int64) (int64, error) {
	var rev int64
	err := r.q.QueryRowContext(ctx,
		`UPDATE clients SET desired_rev = desired_rev + 1 WHERE id = ? RETURNING desired_rev`, id).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return rev, err
}

// SetAppliedRev records the revision the agent reports as applied.
func (r *ClientRepo) SetAppliedRev(ctx context.Context, id, rev int64) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx,
		`UPDATE clients SET applied_rev = ? WHERE id = ?`, rev, id))
}

// SetAgentSecretHash stores the agent credential hash (completes pairing).
func (r *ClientRepo) SetAgentSecretHash(ctx context.Context, id int64, hash string) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx,
		`UPDATE clients SET agent_secret_hash = ? WHERE id = ?`, hash, id))
}

// TouchLastSeen records when the agent was last connected.
func (r *ClientRepo) TouchLastSeen(ctx context.Context, id int64) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx,
		`UPDATE clients SET last_seen_at = ? WHERE id = ?`, r.unixNow(), id))
}
