// Package agent runs on client machines: it pairs with a server, keeps a
// WebSocket to it, and applies the desired frpc config the server sends.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/bymayfe/frpdash/internal/frp"
)

const (
	maxBackoff  = time.Minute
	dialTimeout = 15 * time.Second
	maxMessage  = 1 << 20
)

// Credentials are written by `frpdash connect` and read by the agent.
type Credentials struct {
	Server      string `json:"server"` // host:port of the agent listener
	Fingerprint string `json:"fingerprint"`
	ClientID    int64  `json:"clientId"`
	ClientName  string `json:"clientName"`
	Secret      string `json:"secret"`
}

// LoadCredentials reads a credentials file.
func LoadCredentials(path string) (Credentials, error) {
	var c Credentials
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return c, fmt.Errorf("read credentials (run `frpdash connect` first): %w", err)
	}
	return c, json.Unmarshal(b, &c)
}

// Save writes the credentials with mode 0600.
func (c Credentials) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ") //nolint:gosec // the secret belongs in this 0600 file
	if err != nil {
		return err
	}
	return frp.WriteFileAtomic(path, b, 0o600)
}

func httpClient(fingerprint string) *http.Client {
	return &http.Client{
		Timeout:   dialTimeout,
		Transport: &http.Transport{TLSClientConfig: frp.PinnedTLSConfig(fingerprint)},
	}
}

// Pair redeems a pairing code and returns the agent's credentials.
func Pair(ctx context.Context, code string) (Credentials, error) {
	pc, err := frp.DecodePairingCode(code)
	if err != nil {
		return Credentials{}, err
	}
	body, _ := json.Marshal(frp.PairRequest{Token: pc.Token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+pc.Addr+frp.PathPair, bytes.NewReader(body))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient(pc.Fingerprint).Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("contact server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, fmt.Errorf("server refused pairing: %s", resp.Status)
	}
	var pr frp.PairResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return Credentials{}, err
	}
	return Credentials{Server: pc.Addr, Fingerprint: pc.Fingerprint, ClientID: pr.ClientID,
		ClientName: pr.ClientName, Secret: pr.Secret}, nil
}

// Agent applies desired state from the server to a local frpc.
type Agent struct {
	creds     Credentials
	dataDir   string
	version   string // reported to the server in Hello
	installer *frp.Installer
	log       *slog.Logger

	frpcBin string
	frpc    *frp.Process
	started bool
	applied int64
}

// New builds an Agent storing its frpc binary and config under dataDir.
// version is the frpdash build it reports to the server.
func New(creds Credentials, dataDir, version string, log *slog.Logger) *Agent {
	return &Agent{creds: creds, dataDir: dataDir, version: version, installer: frp.NewInstaller(filepath.Join(dataDir, "bin")),
		log: log.With("component", "agent", "client", creds.ClientName)}
}

func (a *Agent) configPath() string { return filepath.Join(a.dataDir, "frpc.toml") }
func (a *Agent) statePath() string  { return filepath.Join(a.dataDir, "state.json") }

// Run installs frpc, restores the last applied config and keeps a
// connection to the server until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		return err
	}
	var err error
	if a.frpcBin, err = a.ensureFRPC(ctx); err != nil {
		return err
	}
	a.frpc = frp.NewProcess("frpc", a.frpcBin, a.log, "-c", a.configPath())

	// Restart frpc with the last applied state, so tunnels survive while the
	// server is unreachable.
	if st, err := a.loadState(); err == nil {
		a.applied = st.Rev
		a.startFRPC(ctx)
		a.log.Info("restored last config", "rev", st.Rev)
	}

	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		a.log.Warn("server connection lost", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
	return nil
}

func (a *Agent) ensureFRPC(ctx context.Context) (string, error) {
	backoff := 5 * time.Second
	for {
		bin, err := a.installer.Ensure(ctx, "frpc")
		if err == nil {
			return bin, nil
		}
		a.log.Error("install frpc", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (a *Agent) startFRPC(ctx context.Context) {
	if a.started {
		a.frpc.Restart()
		return
	}
	a.started = true
	go a.frpc.Run(ctx)
}

// session runs one WebSocket connection.
func (a *Agent) session(ctx context.Context) error {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	//nolint:bodyclose // coder/websocket closes the handshake response body
	conn, _, err := websocket.Dial(dctx, "wss://"+a.creds.Server+frp.PathConnect, &websocket.DialOptions{
		HTTPClient: httpClient(a.creds.Fingerprint),
		HTTPHeader: http.Header{"Authorization": {frp.AuthHeader(a.creds.ClientID, a.creds.Secret)}},
	})
	cancel()
	if err != nil {
		return err
	}
	defer conn.CloseNow() //nolint:errcheck // best effort
	conn.SetReadLimit(maxMessage)

	if err := wsjson.Write(ctx, conn, frp.Message{Type: frp.TypeHello,
		Hello: &frp.Hello{AppliedRev: a.applied, Version: a.version}}); err != nil {
		return err
	}
	a.log.Info("connected to server", "applied_rev", a.applied)

	for {
		var m frp.Message
		if err := wsjson.Read(ctx, conn, &m); err != nil {
			return err
		}
		if m.Type != frp.TypeDesired || m.Desired == nil {
			continue
		}
		ack := frp.Ack{Rev: m.Desired.Rev}
		if err := a.apply(ctx, *m.Desired); err != nil {
			a.log.Error("apply desired state", "rev", m.Desired.Rev, "err", err)
			ack.Error = err.Error()
		} else {
			a.log.Info("applied desired state", "rev", m.Desired.Rev,
				"proxies", len(m.Desired.Config.Proxies), "visitors", len(m.Desired.Config.Visitors))
		}
		if err := wsjson.Write(ctx, conn, frp.Message{Type: frp.TypeAck, Ack: &ack}); err != nil {
			return err
		}
	}
}

// apply renders, verifies and installs a desired state, then (re)starts frpc.
// A config frpc rejects is never installed.
func (a *Agent) apply(ctx context.Context, d frp.Desired) error {
	body, err := d.Config.TOML()
	if err != nil {
		return err
	}
	candidate := a.configPath() + ".new"
	if err := frp.WriteFileAtomic(candidate, body, 0o600); err != nil {
		return err
	}
	if err := frp.Verify(ctx, a.frpcBin, candidate); err != nil {
		_ = os.Remove(candidate)
		return err
	}
	if err := os.Rename(candidate, a.configPath()); err != nil {
		return err
	}
	if err := a.saveState(d); err != nil {
		return err
	}
	a.applied = d.Rev
	a.startFRPC(ctx)
	return nil
}

func (a *Agent) loadState() (frp.Desired, error) {
	var st frp.Desired
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	if _, err := os.Stat(a.configPath()); err != nil {
		return st, errors.New("state without config file")
	}
	return st, nil
}

func (a *Agent) saveState(st frp.Desired) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return frp.WriteFileAtomic(a.statePath(), b, 0o600)
}
