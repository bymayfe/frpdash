package web

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/bymayfe/frpdash/internal/server/setup"
	"github.com/bymayfe/frpdash/internal/server/store"
	assets "github.com/bymayfe/frpdash/web"
)

// SetupService is what the setup page needs from setup.Service.
type SetupService interface {
	HasAdmin(ctx context.Context) (bool, error)
	Complete(ctx context.Context, in setup.Input) error
}

// Setup serves the first-run page until setup is complete, then a short
// "starting" notice until the server swaps in the panel.
type Setup struct {
	svc       SetupService
	addresses []setup.Address
	firewall  Firewall // nil: no firewall step
	done      func()
	log       *slog.Logger
	page      *template.Template
	finished  atomic.Bool
}

// NewSetup builds the setup page. done is called once, after a successful
// submit has been answered.
func NewSetup(svc SetupService, addresses []setup.Address, fw Firewall, done func(), log *slog.Logger) (*Setup, error) {
	page, err := parsePage("setup")
	if err != nil {
		return nil, err
	}
	return &Setup{svc: svc, addresses: addresses, firewall: fw, done: done, log: log.With("component", "setup"),
		page: page}, nil
}

// setupData is the setup page's template model.
type setupData struct {
	User      *store.User // always nil: base.html hides the menu
	Error     string
	Flash     string
	HasAdmin  bool
	Addresses []setup.Address
	Username  string
	Host      string
	Finished  bool
	Firewall  *firewallView
	// OpenPorts is the firewall checkbox; on by default when it can do something.
	OpenPorts bool
}

// Handler serves the setup page with the panel's security middleware.
func (s *Setup) Handler() http.Handler {
	static, _ := fs.Sub(assets.Static, "static") // embedded path: cannot fail
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.form)
	mux.HandleFunc("POST /setup", s.submit)
	mux.HandleFunc("/", s.elsewhere)
	return securityHeaders(sameOrigin(mux))
}

func (s *Setup) form(w http.ResponseWriter, r *http.Request) {
	if s.finished.Load() {
		s.starting(w)
		return
	}
	d, err := s.data(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	d.Username = "admin"
	d.OpenPorts = d.Firewall != nil && d.Firewall.Status.Supported()
	if len(s.addresses) > 0 && !s.addresses[0].Private {
		d.Host = s.addresses[0].IP
	}
	s.render(w, http.StatusOK, d)
}

func (s *Setup) submit(w http.ResponseWriter, r *http.Request) {
	if s.finished.Load() {
		s.starting(w)
		return
	}
	in := setup.Input{
		Code:       r.FormValue("code"),
		Username:   r.FormValue("username"),
		Password:   r.FormValue("password"),
		Password2:  r.FormValue("password2"),
		PublicHost: r.FormValue("public_host"),
	}
	err := s.svc.Complete(r.Context(), in)
	if errors.Is(err, setup.ErrInvalid) {
		s.log.Warn("setup rejected", "err", err)
		d, derr := s.data(r.Context())
		if derr != nil {
			s.internal(w, derr)
			return
		}
		d.Error = strings.TrimPrefix(err.Error(), setup.ErrInvalid.Error()+": ")
		d.Username, d.Host = in.Username, in.PublicHost
		d.OpenPorts = r.FormValue("open_ports") != ""
		s.render(w, http.StatusUnprocessableEntity, d)
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if !s.finished.CompareAndSwap(false, true) {
		s.starting(w)
		return
	}
	s.log.Info("setup complete", "public_host", strings.TrimSpace(in.PublicHost))
	done := &setupData{Finished: true, Host: strings.TrimSpace(in.PublicHost)}
	if done.Firewall, err = firewallStatus(r.Context(), s.firewall); err != nil {
		s.log.Error("firewall rules", "err", err)
	}
	if done.Firewall != nil && r.FormValue("open_ports") != "" && done.Firewall.Status.Supported() {
		syncFirewall(r.Context(), s.firewall, done.Firewall)
		if done.Firewall.Err != "" {
			s.log.Warn("firewall", "err", done.Firewall.Err)
		} else {
			s.log.Info("firewall ports opened", "kind", done.Firewall.Status.Kind)
		}
	}
	s.render(w, http.StatusOK, done)
	s.done()
}

// elsewhere sends every other path to the setup page.
func (s *Setup) elsewhere(w http.ResponseWriter, r *http.Request) {
	if s.finished.Load() {
		s.starting(w)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// starting answers between a finished setup and the panel taking over.
func (s *Setup) starting(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	http.Error(w, "frpdash is starting; reload in a few seconds.", http.StatusServiceUnavailable)
}

func (s *Setup) data(ctx context.Context) (*setupData, error) {
	has, err := s.svc.HasAdmin(ctx)
	if err != nil {
		return nil, err
	}
	fw, err := firewallStatus(ctx, s.firewall)
	if err != nil {
		return nil, err
	}
	return &setupData{HasAdmin: has, Addresses: s.addresses, Firewall: fw}, nil
}

func (s *Setup) render(w http.ResponseWriter, status int, d *setupData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.page.ExecuteTemplate(w, "base", d); err != nil {
		s.log.Error("render setup", "err", err)
	}
}

func (s *Setup) internal(w http.ResponseWriter, err error) {
	s.log.Error("setup", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
