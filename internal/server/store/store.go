// Package store is the server's SQLite persistence layer. DB owns the
// connection and migrations; tables are reached through repositories, either
// directly (db.Repos()) or inside a transaction (db.InTx).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Errors returned by repositories.
var (
	ErrNotFound      = errors.New("not found")
	ErrUsernameTaken = errors.New("username already exists")
	ErrNameTaken     = errors.New("name already exists")
	ErrPortTaken     = errors.New("port already in use")
)

// querier is satisfied by *sql.DB and *sql.Tx, so repositories work the same
// inside and outside a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Repos groups the repositories bound to one querier.
type Repos struct {
	Users    *UserRepo
	Settings *SettingsRepo
	Clients  *ClientRepo
	Proxies  *ProxyRepo
	Pairing  *PairingRepo
	Sessions *SessionRepo
}

func newRepos(q querier, now func() time.Time) *Repos {
	b := base{q: q, now: now}
	return &Repos{
		Users:    &UserRepo{b},
		Settings: &SettingsRepo{b},
		Clients:  &ClientRepo{b},
		Proxies:  &ProxyRepo{b},
		Pairing:  &PairingRepo{b},
		Sessions: &SessionRepo{b},
	}
}

// base is embedded by every repository.
type base struct {
	q   querier
	now func() time.Time
}

func (b base) unixNow() int64 { return b.now().Unix() }

// DB is an open frpdash database.
type DB struct {
	sql   *sql.DB
	now   func() time.Time
	repos *Repos
}

// Open opens (creating if needed) the database at path and applies pending
// migrations. The file is 0600 and its directory 0700: it holds password and
// agent secret hashes.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := prepareFile(path); err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate") // take the write lock at BEGIN

	conn, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection serializes access: panel traffic is tiny, and a
	// transaction holds the only connection, so read-check-write sequences
	// inside InTx cannot interleave.
	conn.SetMaxOpenConns(1)

	db := &DB{sql: conn, now: time.Now}
	db.repos = newRepos(conn, db.now)

	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.migrate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.sql.Close() }

// Repos returns repositories that run each call on its own.
func (db *DB) Repos() *Repos { return db.repos }

// Users returns the users repository.
func (db *DB) Users() *UserRepo { return db.repos.Users }

// InTx runs fn with repositories bound to one transaction, committing if fn
// returns nil.
func (db *DB) InTx(ctx context.Context, fn func(r *Repos) error) error {
	return db.tx(ctx, func(tx *sql.Tx) error { return fn(newRepos(tx, db.now)) })
}

func (db *DB) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (db *DB) unixNow() int64 { return db.now().Unix() }

func prepareFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // tighten a pre-existing file too
}

// uniqueViolation reports whether err is a UNIQUE/PRIMARY KEY violation and
// returns SQLite's message, which names the conflicting columns.
func uniqueViolation(err error) (string, bool) {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return "", false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return se.Error(), true
	}
	return "", false
}

// mapUnique converts a unique violation on any of the given column lists into
// the paired error. Other errors pass through.
func mapUnique(err error, pairs map[string]error) error {
	msg, ok := uniqueViolation(err)
	if !ok {
		return err
	}
	for cols, mapped := range pairs {
		if strings.Contains(msg, cols) {
			return mapped
		}
	}
	return err
}

func nullTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0)
}

func rowsAffectedOrNotFound(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
