// Package server is the VDS side. Server wires it together: frps, the agent
// listener (TLS, public) and the panel (loopback only). File prefixes mark the
// parts: auth* (accounts, sessions), fleet* (clients and services),
// setup* (first run), agentapi_* (agent endpoints), web_* (panel and setup
// pages), firewall* and fwsync* (host firewall), ports* (VDS port choice).
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/bymayfe/frpdash/internal/frp"
	"github.com/bymayfe/frpdash/internal/store"
)

const (
	shutdownTimeout = 10 * time.Second
	purgeInterval   = time.Hour
)

// Config holds the server's addresses.
type Config struct {
	DataDir    string
	UIAddr     string // must be loopback
	AgentAddr  string // listen address of the agent endpoint
	PublicHost string // VDS address agents and frpc dial; empty: keep the stored one (or ask in setup)
}

// Validate enforces that the panel is never exposed and the rest parses.
func (c Config) Validate() error {
	host, _, err := net.SplitHostPort(c.UIAddr)
	if err != nil {
		return fmt.Errorf("ui-addr: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("ui-addr must be a loopback address (127.0.0.1:PORT); reach the panel over SSH")
	}
	if _, err := c.agentPort(); err != nil {
		return err
	}
	if c.PublicHost != "" {
		if err := ValidateHost(c.PublicHost); err != nil {
			return fmt.Errorf("--public-host: %w", err)
		}
	}
	return nil
}

func (c Config) agentPort() (int, error) {
	_, p, err := net.SplitHostPort(c.AgentAddr)
	if err != nil {
		return 0, fmt.Errorf("agent-addr: %w", err)
	}
	return strconv.Atoi(p)
}

// Server is the running VDS side.
type Server struct {
	cfg      Config
	db       *store.DB
	accounts *AuthService
	setup    *SetupService
	fw       *FirewallSync
	log      *slog.Logger
}

// New builds a Server over an open database.
func New(cfg Config, db *store.DB, accounts *AuthService, log *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	agentPort, _ := cfg.agentPort() // validated above
	return &Server{cfg: cfg, db: db, accounts: accounts, setup: NewSetupService(db, accounts, cfg.DataDir),
		fw: NewFirewallSync(db, NewFirewallManager(), agentPort, log), log: log}, nil
}

// Run serves the panel at once, runs first-time setup in it if needed, then
// starts frps and the agent listener. It blocks until ctx ends or one fails.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg.PublicHost != "" {
		if err := s.setup.SetPublicHost(ctx, s.cfg.PublicHost); err != nil {
			return err
		}
	}
	panel := &swapHandler{}
	panel.Set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "frpdash is starting; reload in a few seconds.", http.StatusServiceUnavailable)
	}))
	uiSrv := &http.Server{Addr: s.cfg.UIAddr, Handler: panel, ReadHeaderTimeout: 10 * time.Second}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return serve(gctx, uiSrv, uiSrv.ListenAndServe) })
	g.Go(func() error {
		if err := s.runSetup(gctx, panel); err != nil || gctx.Err() != nil {
			return err
		}
		return s.start(gctx, g, panel)
	})
	return g.Wait()
}

// runSetup shows the setup page until it is completed (or ctx ends).
func (s *Server) runSetup(ctx context.Context, panel *swapHandler) error {
	needed, err := s.setup.Needed(ctx)
	if err != nil || !needed {
		return err
	}
	code, err := s.setup.Code()
	if err != nil {
		return err
	}
	done := make(chan struct{})
	page, err := NewSetup(s.setup, Addresses(), s.fw, func() { close(done) }, s.log)
	if err != nil {
		return err
	}
	panel.Set(page.Handler())
	s.log.Info("setup required: open the panel and enter the setup code", "panel", "http://"+s.cfg.UIAddr,
		"setup_code", code)
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// start brings up frps, the agent listener and the panel proper.
func (s *Server) start(ctx context.Context, g *errgroup.Group, panel *swapHandler) error {
	host, err := s.setup.PublicHost(ctx)
	if err != nil {
		return err
	}
	agentPort, _ := s.cfg.agentPort() // validated in New
	cert, err := LoadOrCreateAgentCert(s.cfg.DataDir, host)
	if err != nil {
		return err
	}

	_, uiPort, _ := net.SplitHostPort(s.cfg.UIAddr) // validated in New
	panelPort, _ := strconv.Atoi(uiPort)
	fl := NewFleetService(s.db, host, agentPort, panelPort)
	fl.SetHostPorts(hostPorts{})
	hub := NewHub(fl, s.log)
	fl.SetNotifier(notifiers{hub, s.fw}) // agents get the new config; the firewall follows public ports

	frps, err := s.prepareFRPS(ctx, fl)
	if err != nil {
		return err
	}
	agentAddr := net.JoinHostPort(host, strconv.Itoa(agentPort))
	hub.EnableInstall(InstallConfig{Addr: agentAddr, Fingerprint: cert.Fingerprint,
		PublicKeyPin: cert.PublicKeyPin, Binary: s.agentBinary})
	ui, err := NewUI(s.accounts, fl, hub, frps, UIConfig{AgentAddr: agentAddr, Fingerprint: cert.Fingerprint,
		PublicKeyPin: cert.PublicKeyPin, Firewall: s.fw}, s.log)
	if err != nil {
		return err
	}
	agentSrv := &http.Server{Addr: s.cfg.AgentAddr, Handler: hub.Handler(), ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert.TLS}, MinVersion: tls.VersionTLS13}}

	s.log.Info("frpdash server starting", "panel", "http://"+s.cfg.UIAddr, "agents", s.cfg.AgentAddr,
		"public_host", host, "fingerprint", cert.Fingerprint)

	g.Go(func() error { frps.Run(ctx); return nil })
	g.Go(func() error { return serve(ctx, agentSrv, func() error { return agentSrv.ListenAndServeTLS("", "") }) })
	g.Go(func() error { s.purgeLoop(ctx); return nil })
	g.Go(func() error { s.fw.Run(ctx); return nil })
	panel.Set(ui.Handler())
	return nil
}

// notifiers fans fleet changes out to several listeners.
type notifiers []Notifier

func (ns notifiers) ClientChanged(id int64) {
	for _, n := range ns {
		n.ClientChanged(id)
	}
}

// swapHandler lets one listener serve the setup page and then the panel,
// with no gap between them.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func (s *swapHandler) Set(h http.Handler) { s.h.Store(&h) }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*s.h.Load()).ServeHTTP(w, r)
}

// DistDir is where, under the data dir, frpdash builds for other
// architectures go (frpdash-linux-arm64, ...) so the installer can serve them.
const DistDir = "dist"

// agentBinary returns the frpdash build the install script downloads: this
// very executable for its own architecture, otherwise dist/frpdash-linux-<arch>.
func (s *Server) agentBinary(arch string) (string, error) {
	if runtime.GOOS == "linux" && arch == runtime.GOARCH {
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(self)
	}
	p := filepath.Join(s.cfg.DataDir, DistDir, "frpdash-linux-"+arch)
	if _, err := os.Stat(p); err != nil { //nolint:gosec // arch is checked against installArches before this is called
		return "", err
	}
	return p, nil
}

// prepareFRPS installs frps, writes its config and returns the process.
func (s *Server) prepareFRPS(ctx context.Context, fl *FleetService) (*frp.Process, error) {
	bin, err := frp.NewInstaller(filepath.Join(s.cfg.DataDir, "bin")).Ensure(ctx, "frps")
	if err != nil {
		return nil, err
	}
	sc, err := fl.ServerConfig(ctx)
	if err != nil {
		return nil, err
	}
	body, err := sc.TOML()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.cfg.DataDir, "frps.toml")
	if err := frp.WriteFileAtomic(path, body, 0o600); err != nil {
		return nil, err
	}
	if err := frp.Verify(ctx, bin, path); err != nil {
		return nil, err
	}
	return frp.NewProcess("frps", bin, s.log, "-c", path), nil
}

// serve runs an http.Server until ctx ends, then shuts it down.
func serve(ctx context.Context, srv *http.Server, listen func() error) error {
	errc := make(chan error, 1)
	go func() { errc <- listen() }()
	select {
	case err := <-errc:
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = srv.Shutdown(sctx)
		return nil
	}
}

func (s *Server) purgeLoop(ctx context.Context) {
	t := time.NewTicker(purgeInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := s.db.Repos()
			if err := r.Sessions.PurgeExpired(ctx); err != nil {
				s.log.Error("purge sessions", "err", err)
			}
			if err := r.Pairing.PurgeExpired(ctx); err != nil {
				s.log.Error("purge pairing codes", "err", err)
			}
		}
	}
}
