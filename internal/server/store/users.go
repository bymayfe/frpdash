package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is a stored panel account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
}

// UserRepo persists panel users. It stores hashes only; hashing and password
// policy live in the auth package.
type UserRepo struct{ base }

// Create inserts a user and returns it with ID and CreatedAt set.
func (r *UserRepo) Create(ctx context.Context, username, passwordHash string) (User, error) {
	u := User{Username: username, PasswordHash: passwordHash, CreatedAt: time.Unix(r.unixNow(), 0)}
	res, err := r.q.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		u.Username, u.PasswordHash, u.CreatedAt.Unix())
	if err != nil {
		return User{}, mapUnique(err, map[string]error{"users.username": ErrUsernameTaken})
	}
	u.ID, err = res.LastInsertId()
	return u, err
}

// FindByUsername returns the user or ErrNotFound.
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (User, error) {
	return r.find(ctx, `username = ?`, username)
}

// Get returns the user by ID or ErrNotFound.
func (r *UserRepo) Get(ctx context.Context, id int64) (User, error) {
	return r.find(ctx, `id = ?`, id)
}

func (r *UserRepo) find(ctx context.Context, where string, arg any) (User, error) {
	var (
		u       User
		created int64
	)
	err := r.q.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE `+where, arg).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// UpdatePasswordHash replaces a user's hash, or returns ErrNotFound.
func (r *UserRepo) UpdatePasswordHash(ctx context.Context, username, passwordHash string) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE username = ?`, passwordHash, username))
}

// Count returns the number of users.
func (r *UserRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}
