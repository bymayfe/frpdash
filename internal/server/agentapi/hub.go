// Package agentapi serves the agent-facing endpoints (pairing and the
// desired-state WebSocket) and tracks which agents are online.
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/bymayfe/frpdash/internal/frpconf"
	"github.com/bymayfe/frpdash/internal/protocol"
	"github.com/bymayfe/frpdash/internal/server/fleet"
	"github.com/bymayfe/frpdash/internal/server/store"
)

const (
	helloTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
	writeTimeout = 10 * time.Second
	maxMessage   = 1 << 20
)

// Fleet is what the hub needs from the fleet service.
type Fleet interface {
	Pair(ctx context.Context, token string) (store.Client, string, error)
	CheckPairingToken(ctx context.Context, token string) (store.Client, error)
	AuthenticateAgent(ctx context.Context, clientID int64, secret string) (store.Client, error)
	DesiredConfig(ctx context.Context, clientID int64) (int64, frpconf.ClientConfig, error)
	RecordApplied(ctx context.Context, clientID, rev int64) error
	RecordSeen(ctx context.Context, clientID int64) error
}

// Hub tracks connected agents and pushes desired state to them.
type Hub struct {
	fleet   Fleet
	log     *slog.Logger
	install *InstallConfig // nil: install endpoints are off

	mu       sync.Mutex
	sessions map[int64]*session
}

type session struct {
	notify chan struct{}
	cancel context.CancelFunc
}

// NewHub builds a Hub.
func NewHub(f Fleet, log *slog.Logger) *Hub {
	return &Hub{fleet: f, log: log.With("component", "agentapi"), sessions: map[int64]*session{}}
}

// Handler serves only the agent endpoints; everything else is 404.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+protocol.PathPair, h.servePair)
	mux.HandleFunc("GET "+protocol.PathConnect, h.serveConnect)
	mux.HandleFunc("GET "+protocol.PathInstall+"{token}", h.serveInstallScript)
	mux.HandleFunc("GET "+protocol.PathInstall+"{token}/{file}", h.serveInstallBinary)
	return mux
}

// ClientChanged implements fleet.Notifier.
func (h *Hub) ClientChanged(clientID int64) {
	h.mu.Lock()
	s := h.sessions[clientID]
	h.mu.Unlock()
	if s == nil {
		return
	}
	select {
	case s.notify <- struct{}{}:
	default: // a push is already pending
	}
}

// Online reports whether a client's agent is connected.
func (h *Hub) Online(clientID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[clientID] != nil
}

func (h *Hub) servePair(w http.ResponseWriter, r *http.Request) {
	var req protocol.PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	c, secret, err := h.fleet.Pair(r.Context(), req.Token)
	if errors.Is(err, fleet.ErrPairingFailed) {
		h.log.Warn("pairing rejected", "remote", r.RemoteAddr)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		h.log.Error("pairing", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.log.Info("client paired", "client", c.Name, "remote", r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	//nolint:gosec // issuing the secret is the point of pairing
	_ = json.NewEncoder(w).Encode(protocol.PairResponse{ClientID: c.ID, ClientName: c.Name, Secret: secret})
}

func (h *Hub) serveConnect(w http.ResponseWriter, r *http.Request) {
	id, secret, err := protocol.ParseAuthHeader(r.Header.Get("Authorization"))
	if err == nil {
		_, err = h.fleet.AuthenticateAgent(r.Context(), id, secret)
	}
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow() //nolint:errcheck // best effort
	conn.SetReadLimit(maxMessage)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s := &session{notify: make(chan struct{}, 1), cancel: cancel}
	h.register(id, s)
	defer h.unregister(id, s)

	log := h.log.With("client_id", id)
	if err := h.run(ctx, conn, id, s, log); err != nil && ctx.Err() == nil {
		log.Info("agent disconnected", "err", err)
	}
}

func (h *Hub) run(ctx context.Context, conn *websocket.Conn, id int64, s *session, log *slog.Logger) error {
	helloCtx, cancelHello := context.WithTimeout(ctx, helloTimeout)
	var hello protocol.Message
	err := wsjson.Read(helloCtx, conn, &hello)
	cancelHello()
	if err != nil || hello.Type != protocol.TypeHello || hello.Hello == nil {
		return errors.New("expected hello")
	}
	log.Info("agent connected", "applied_rev", hello.Hello.AppliedRev, "version", hello.Hello.Version)

	readErr := make(chan error, 1)
	go func() { readErr <- h.readAcks(ctx, conn, id, log) }()

	sent := hello.Hello.AppliedRev
	push := func() error {
		rev, cfg, err := h.fleet.DesiredConfig(ctx, id)
		if err != nil || rev <= sent {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		if err := wsjson.Write(wctx, conn, protocol.Message{Type: protocol.TypeDesired,
			Desired: &protocol.Desired{Rev: rev, Config: cfg}}); err != nil {
			return err
		}
		sent = rev
		log.Info("desired state sent", "rev", rev)
		return nil
	}
	if err := push(); err != nil {
		return err
	}

	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-s.notify:
			if err := push(); err != nil {
				return err
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (h *Hub) readAcks(ctx context.Context, conn *websocket.Conn, id int64, log *slog.Logger) error {
	for {
		var m protocol.Message
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			return err
		}
		if m.Type != protocol.TypeAck || m.Ack == nil {
			continue
		}
		if m.Ack.Error != "" {
			log.Warn("agent failed to apply", "rev", m.Ack.Rev, "err", m.Ack.Error)
			continue
		}
		if err := h.fleet.RecordApplied(ctx, id, m.Ack.Rev); err != nil {
			log.Error("record applied", "err", err)
		}
	}
}

// register adds a session, replacing (and closing) an older one for the
// same client.
func (h *Hub) register(id int64, s *session) {
	h.mu.Lock()
	old := h.sessions[id]
	h.sessions[id] = s
	h.mu.Unlock()
	if old != nil {
		old.cancel()
	}
}

func (h *Hub) unregister(id int64, s *session) {
	h.mu.Lock()
	if h.sessions[id] == s {
		delete(h.sessions, id)
	}
	h.mu.Unlock()
	if err := h.fleet.RecordSeen(context.Background(), id); err != nil {
		h.log.Error("record last seen", "err", err)
	}
}
