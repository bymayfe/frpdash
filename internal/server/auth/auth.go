// Package auth owns panel accounts: username/password policy, password
// hashing and credential checks. Persistence is behind the UserStore interface.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bymayfe/frpdash/internal/server/store"
)

// Password policy. bcrypt reads only the first 72 bytes, so longer passwords
// are rejected rather than silently truncated.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 72
)

// Errors returned by Service.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrInvalidUsername    = errors.New("username must be 3-32 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit")
	ErrWeakPassword       = fmt.Errorf("password must be %d-%d bytes", MinPasswordLen, MaxPasswordLen)
	ErrUserNotFound       = errors.New("user not found")
	ErrNoSession          = errors.New("not logged in")
	ErrUsernameTaken      = store.ErrUsernameTaken
)

// UserStore is the persistence the service needs; *store.UserRepo satisfies it.
type UserStore interface {
	Create(ctx context.Context, username, passwordHash string) (store.User, error)
	FindByUsername(ctx context.Context, username string) (store.User, error)
	UpdatePasswordHash(ctx context.Context, username, passwordHash string) error
	Count(ctx context.Context) (int, error)
	Get(ctx context.Context, id int64) (store.User, error)
}

// SessionStore persists login sessions; *store.SessionRepo satisfies it.
type SessionStore interface {
	Create(ctx context.Context, tokenHash string, userID int64, expires time.Time) error
	UserID(ctx context.Context, tokenHash string) (int64, error)
	Delete(ctx context.Context, tokenHash string) error
}

// SessionTTL is how long a panel login lasts.
const SessionTTL = 12 * time.Hour

// Service manages panel accounts.
type Service struct {
	users    UserStore
	sessions SessionStore
	hasher   PasswordHasher
	// dummy is compared against when a user does not exist, so a failed login
	// costs the same whether or not the username is real.
	dummy string
}

// NewService builds a Service.
func NewService(users UserStore, sessions SessionStore, hasher PasswordHasher) (*Service, error) {
	dummy, err := hasher.Hash("frpdash-timing-equalizer")
	if err != nil {
		return nil, err
	}
	return &Service{users: users, sessions: sessions, hasher: hasher, dummy: dummy}, nil
}

// NormalizeUsername lowercases and trims a username.
func NormalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// ValidateUsername checks a normalized username.
func ValidateUsername(u string) error {
	if len(u) < 3 || len(u) > 32 {
		return ErrInvalidUsername
	}
	for i, r := range u {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !alnum && (i == 0 || (r != '.' && r != '_' && r != '-')) {
			return ErrInvalidUsername
		}
	}
	return nil
}

// ValidatePassword checks the password policy.
func ValidatePassword(p string) error {
	if len(p) < MinPasswordLen || len(p) > MaxPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// HasUsers reports whether any account exists.
func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	n, err := s.users.Count(ctx)
	return n > 0, err
}

// CreateUser validates and stores a new account.
func (s *Service) CreateUser(ctx context.Context, username, password string) (store.User, error) {
	username = NormalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return store.User{}, err
	}
	hash, err := s.hash(password)
	if err != nil {
		return store.User{}, err
	}
	return s.users.Create(ctx, username, hash)
}

// Authenticate checks credentials. Unknown user and wrong password both
// return ErrInvalidCredentials.
func (s *Service) Authenticate(ctx context.Context, username, password string) (store.User, error) {
	u, err := s.users.FindByUsername(ctx, NormalizeUsername(username))
	if errors.Is(err, store.ErrNotFound) {
		_ = s.hasher.Compare(s.dummy, password)
		return store.User{}, ErrInvalidCredentials
	}
	if err != nil {
		return store.User{}, err
	}
	if len(password) > MaxPasswordLen || s.hasher.Compare(u.PasswordHash, password) != nil {
		return store.User{}, ErrInvalidCredentials
	}
	return u, nil
}

// SetPassword replaces an account's password.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	err = s.users.UpdatePasswordHash(ctx, NormalizeUsername(username), hash)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUserNotFound
	}
	return err
}

func (s *Service) hash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	return s.hasher.Hash(password)
}

// StartSession creates a login session and returns its token.
func (s *Service) StartSession(ctx context.Context, userID int64) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(b)
	expires := time.Now().Add(SessionTTL)
	return token, expires, s.sessions.Create(ctx, hashToken(token), userID, expires)
}

// SessionUser returns the user of a live session token.
func (s *Service) SessionUser(ctx context.Context, token string) (store.User, error) {
	if token == "" {
		return store.User{}, ErrNoSession
	}
	id, err := s.sessions.UserID(ctx, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrNoSession
	}
	if err != nil {
		return store.User{}, err
	}
	u, err := s.users.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, ErrNoSession
	}
	return u, err
}

// EndSession logs a session out.
func (s *Service) EndSession(ctx context.Context, token string) error {
	return s.sessions.Delete(ctx, hashToken(token))
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}
