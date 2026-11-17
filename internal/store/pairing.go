package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PairingRepo persists one-time pairing codes, stored only as hashes.
type PairingRepo struct{ base }

// CreateCode stores a pairing code hash for a client.
func (r *PairingRepo) CreateCode(ctx context.Context, clientID int64, codeHash string, expires time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO pairing_codes (code_hash, client_id, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		codeHash, clientID, expires.Unix(), r.unixNow())
	return err
}

// ConsumeCode marks a valid (unused, unexpired) code as used and returns its
// client ID, or ErrNotFound.
func (r *PairingRepo) ConsumeCode(ctx context.Context, codeHash string) (int64, error) {
	var clientID int64
	err := r.q.QueryRowContext(ctx, `UPDATE pairing_codes SET used_at = ?
		WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?
		RETURNING client_id`, r.unixNow(), codeHash, r.unixNow()).Scan(&clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return clientID, err
}

// ValidCode returns the client ID of a valid (unused, unexpired) code
// without using it up, or ErrNotFound.
func (r *PairingRepo) ValidCode(ctx context.Context, codeHash string) (int64, error) {
	var clientID int64
	err := r.q.QueryRowContext(ctx, `SELECT client_id FROM pairing_codes
		WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?`, codeHash, r.unixNow()).Scan(&clientID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return clientID, err
}

// CodeState reports what became of a code: used, expired, or neither (still
// valid). ErrNotFound if it is unknown (or already purged).
func (r *PairingRepo) CodeState(ctx context.Context, codeHash string) (clientID int64, used, expired bool, err error) {
	var usedAt sql.NullInt64
	var expiresAt int64
	err = r.q.QueryRowContext(ctx, `SELECT client_id, used_at, expires_at FROM pairing_codes WHERE code_hash = ?`,
		codeHash).Scan(&clientID, &usedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false, ErrNotFound
	}
	return clientID, usedAt.Valid, !usedAt.Valid && expiresAt <= r.unixNow(), err
}

// PurgeExpired deletes expired or used pairing codes.
func (r *PairingRepo) PurgeExpired(ctx context.Context) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM pairing_codes WHERE expires_at <= ? OR used_at IS NOT NULL`, r.unixNow())
	return err
}
