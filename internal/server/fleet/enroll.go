package fleet

import (
	"context"
	"errors"
	"time"

	"github.com/bymayfe/frpdash/internal/protocol"
	"github.com/bymayfe/frpdash/internal/server/store"
)

// PairingTTL is how long an "Add Client" code stays valid.
const PairingTTL = 15 * time.Minute

// Agent authentication errors.
var (
	ErrPairingFailed = errors.New("pairing code is invalid, expired or already used")
	ErrUnauthorized  = errors.New("unknown agent or wrong secret")
)

// CreatePairingToken issues a one-time token for a client. Only its hash is
// stored.
func (s *Service) CreatePairingToken(ctx context.Context, clientID int64) (string, time.Time, error) {
	if _, err := s.Client(ctx, clientID); err != nil {
		return "", time.Time{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(PairingTTL)
	if err := s.db.Repos().Pairing.CreateCode(ctx, clientID, protocol.HashSecret(token), expires); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// CheckPairingToken reports which client a still valid token belongs to,
// without redeeming it; the install script uses it to gate downloads.
func (s *Service) CheckPairingToken(ctx context.Context, token string) (store.Client, error) {
	id, err := s.db.Repos().Pairing.ValidCode(ctx, protocol.HashSecret(token))
	if errors.Is(err, store.ErrNotFound) {
		return store.Client{}, ErrPairingFailed
	}
	if err != nil {
		return store.Client{}, err
	}
	return s.Client(ctx, id)
}

// Pairing states shown while the panel waits for a client to connect.
const (
	PairingWaiting = "waiting" // the install command has not been run yet
	PairingUsed    = "used"    // the client paired; its agent may still be connecting
	PairingExpired = "expired" // the command expired unused (or is unknown)
)

// PairingState reports what became of a client's pairing token.
func (s *Service) PairingState(ctx context.Context, clientID int64, token string) (string, error) {
	id, used, expired, err := s.db.Repos().Pairing.CodeState(ctx, protocol.HashSecret(token))
	switch {
	case errors.Is(err, store.ErrNotFound) || (err == nil && id != clientID):
		return PairingExpired, nil
	case err != nil:
		return "", err
	case used:
		return PairingUsed, nil
	case expired:
		return PairingExpired, nil
	}
	return PairingWaiting, nil
}

// Pair redeems a token and issues the agent's permanent secret. Pairing an
// already paired client replaces its secret (machine reinstalled).
func (s *Service) Pair(ctx context.Context, token string) (store.Client, string, error) {
	var (
		c      store.Client
		secret string
	)
	err := s.db.InTx(ctx, func(r *store.Repos) error {
		id, err := r.Pairing.ConsumeCode(ctx, protocol.HashSecret(token))
		if errors.Is(err, store.ErrNotFound) {
			return ErrPairingFailed
		}
		if err != nil {
			return err
		}
		if secret, err = randomHex(32); err != nil {
			return err
		}
		if err := r.Clients.SetAgentSecretHash(ctx, id, protocol.HashSecret(secret)); err != nil {
			return err
		}
		c, err = r.Clients.Get(ctx, id)
		return err
	})
	return c, secret, err
}

// AuthenticateAgent checks an agent's credentials.
func (s *Service) AuthenticateAgent(ctx context.Context, clientID int64, secret string) (store.Client, error) {
	c, err := s.Client(ctx, clientID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !c.Paired()) {
		return store.Client{}, ErrUnauthorized
	}
	if err != nil {
		return store.Client{}, err
	}
	if !protocol.EqualHashes(c.AgentSecretHash, protocol.HashSecret(secret)) {
		return store.Client{}, ErrUnauthorized
	}
	return c, nil
}

// RecordApplied stores the revision an agent reports as running.
func (s *Service) RecordApplied(ctx context.Context, clientID, rev int64) error {
	return s.db.Repos().Clients.SetAppliedRev(ctx, clientID, rev)
}

// RecordSeen stores when an agent was last connected.
func (s *Service) RecordSeen(ctx context.Context, clientID int64) error {
	return s.db.Repos().Clients.TouchLastSeen(ctx, clientID)
}
