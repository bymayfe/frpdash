package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SessionRepo persists panel login sessions, stored only as token hashes.
type SessionRepo struct{ base }

// Create stores a login session token hash.
func (r *SessionRepo) Create(ctx context.Context, tokenHash string, userID int64, expires time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, expires.Unix(), r.unixNow())
	return err
}

// UserID returns the user ID of a live session, or ErrNotFound.
func (r *SessionRepo) UserID(ctx context.Context, tokenHash string) (int64, error) {
	var userID int64
	err := r.q.QueryRowContext(ctx,
		`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, r.unixNow()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

// Delete ends a session.
func (r *SessionRepo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// PurgeExpired deletes expired sessions.
func (r *SessionRepo) PurgeExpired(ctx context.Context) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, r.unixNow())
	return err
}
