// Package web serves the panel: login, clients, services and settings. It
// listens on loopback only; the operator reaches it over an SSH tunnel.
package web

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bymayfe/frpdash/internal/frpbin"
	"github.com/bymayfe/frpdash/internal/frpconf"
	"github.com/bymayfe/frpdash/internal/protocol"
	"github.com/bymayfe/frpdash/internal/server/auth"
	"github.com/bymayfe/frpdash/internal/server/fleet"
	"github.com/bymayfe/frpdash/internal/server/store"
	assets "github.com/bymayfe/frpdash/web"
)

const sessionCookie = "frpdash_session"

// Presence reports which agents are connected.
type Presence interface {
	Online(clientID int64) bool
}

// ProcessStatus reports on the frps child process.
type ProcessStatus interface {
	Running() bool
	Logs() []string
}

// Config is what the UI shows about the agent listener.
type Config struct {
	AgentAddr    string // host:port agents dial
	Fingerprint  string
	PublicKeyPin string   // for the one-line install command
	Firewall     Firewall // nil: no firewall section in Settings
}

// UI is the panel's HTTP handler.
type UI struct {
	auth     *auth.Service
	fleet    *fleet.Service
	presence Presence
	frps     ProcessStatus
	cfg      Config
	log      *slog.Logger
	pages    map[string]*template.Template
}

// New parses the embedded templates and builds the UI.
func New(a *auth.Service, f *fleet.Service, p Presence, frps ProcessStatus, cfg Config, log *slog.Logger) (*UI, error) {
	u := &UI{auth: a, fleet: f, presence: p, frps: frps, cfg: cfg, log: log.With("component", "ui"),
		pages: map[string]*template.Template{}}
	for _, name := range []string{"login", "clients", "client-new", "pair", "client", "settings"} {
		t, err := parsePage(name)
		if err != nil {
			return nil, err
		}
		u.pages[name] = t
	}
	return u, nil
}

// Handler returns the panel's routes wrapped in security middleware.
func (u *UI) Handler() http.Handler {
	static, _ := fs.Sub(assets.Static, "static") // embedded path: cannot fail
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /login", u.loginPage)
	mux.HandleFunc("POST /login", u.login)
	mux.HandleFunc("POST /logout", u.logout)

	authed := map[string]func(http.ResponseWriter, *http.Request, *pageData){
		"GET /{$}":                                 u.clientsPage,
		"GET /clients/table":                       u.clientsTable,
		"GET /clients/new":                         u.newClientPage,
		"POST /clients":                            u.createClient,
		"GET /clients/{id}":                        u.clientPage,
		"POST /clients/{id}/delete":                u.deleteClient,
		"POST /clients/{id}/pair":                  u.pairClient,
		"GET /clients/{id}/pair-status":            u.pairStatus,
		"POST /clients/{id}/proxies":               u.addProxy,
		"POST /proxies/{id}/delete":                u.deleteProxy,
		"POST /proxies/{id}/visitors":              u.grantVisitor,
		"POST /proxies/{id}/visitors/{cid}/delete": u.revokeVisitor,
		"GET /settings":                            u.settingsPage,
		"POST /settings/firewall":                  u.openFirewall,
	}
	for pattern, h := range authed {
		mux.HandleFunc(pattern, u.requireUser(h))
	}
	return securityHeaders(sameOrigin(mux))
}

// pageData is the template model; each page uses the fields it needs.
type pageData struct {
	User         *store.User
	Error, Flash string

	Clients    []clientView
	Client     clientView
	Proxies    []proxyView
	Others     []store.Client
	Global     fleet.Global
	PublicHost string
	AgentAddr  string

	Code       string
	Token      string // pairing token, for polling its state
	PairState  string // fleet.Pairing*
	InstallCmd string
	Expires    time.Time
	TTL        time.Duration

	Fingerprint string
	FRPSRunning bool
	FRPSLogs    []string
	FRPVersion  string
	Firewall    *firewallView
}

type clientView struct {
	store.Client
	Online     bool
	ProxyCount int
}

type proxyView struct {
	store.Proxy
	Visitors []store.VisitorView
}

func (u *UI) render(w http.ResponseWriter, page, tmpl string, status int, d *pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := u.pages[page].ExecuteTemplate(w, tmpl, d); err != nil {
		u.log.Error("render", "page", page, "err", err)
	}
}

// redirect works for plain forms and htmx requests alike.
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// fail shows user-input errors on the current page and hides internal ones.
func (u *UI) fail(w http.ResponseWriter, r *http.Request, err error, retry func(msg string)) {
	switch {
	case errors.Is(err, fleet.ErrInvalid) && retry != nil:
		retry(strings.TrimPrefix(err.Error(), fleet.ErrInvalid.Error()+": "))
	case errors.Is(err, fleet.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	default:
		u.log.Error("request failed", "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

func (u *UI) requireUser(h func(http.ResponseWriter, *http.Request, *pageData)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		user, err := u.auth.SessionUser(r.Context(), token)
		if err != nil {
			if !errors.Is(err, auth.ErrNoSession) {
				u.log.Error("session lookup", "err", err)
			}
			redirect(w, r, "/login")
			return
		}
		h(w, r, &pageData{User: &user, Flash: flashes[r.URL.Query().Get("ok")]})
	}
}

// flashes maps fixed keys to messages, so the query string cannot inject text.
var flashes = map[string]string{
	"proxy-added":     "Service added. The agent picks it up within seconds.",
	"proxy-deleted":   "Service deleted.",
	"visitor-granted": "Access granted.",
	"visitor-revoked": "Access revoked.",
	"client-deleted":  "Client deleted.",
	"client-paired":   "✓ Connected. Add a service below to open a tunnel to this machine.",
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func formInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.FormValue(name))
	return n
}

// --- auth ---

func (u *UI) loginPage(w http.ResponseWriter, _ *http.Request) {
	u.render(w, "login", "base", http.StatusOK, &pageData{})
}

func (u *UI) login(w http.ResponseWriter, r *http.Request) {
	user, err := u.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			u.log.Error("login", "err", err)
		}
		u.log.Warn("failed login", "username", r.FormValue("username"))
		u.render(w, "login", "base", http.StatusUnauthorized, &pageData{Error: "Invalid username or password."})
		return
	}
	token, expires, err := u.auth.StartSession(r.Context(), user.ID)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	// Not Secure: the panel is plain HTTP on loopback behind an SSH tunnel, and
	// some browsers drop Secure cookies on http://localhost.
	//nolint:gosec // see above
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	redirect(w, r, "/")
}

func (u *UI) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = u.auth.EndSession(r.Context(), c.Value)
	}
	//nolint:gosec // not Secure, see login
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode})
	redirect(w, r, "/login")
}

// --- clients ---

func (u *UI) loadClients(ctx context.Context) ([]clientView, error) {
	cs, err := u.fleet.Clients(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]clientView, 0, len(cs))
	for _, c := range cs {
		ps, err := u.fleet.Proxies(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, clientView{Client: c, Online: u.presence.Online(c.ID), ProxyCount: len(ps)})
	}
	return out, nil
}

func (u *UI) clientsPage(w http.ResponseWriter, r *http.Request, d *pageData) {
	var err error
	if d.Clients, err = u.loadClients(r.Context()); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	u.render(w, "clients", "base", http.StatusOK, d)
}

func (u *UI) clientsTable(w http.ResponseWriter, r *http.Request, d *pageData) {
	var err error
	if d.Clients, err = u.loadClients(r.Context()); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	u.render(w, "clients", "client-table", http.StatusOK, d)
}

func (u *UI) newClientPage(w http.ResponseWriter, r *http.Request, d *pageData) {
	var err error
	if d.Global, err = u.fleet.Global(r.Context()); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	u.render(w, "client-new", "base", http.StatusOK, d)
}

func (u *UI) createClient(w http.ResponseWriter, r *http.Request, d *pageData) {
	in := fleet.ClientInput{Name: r.FormValue("name")}
	if pmin, pmax := formInt(r, "port_min"), formInt(r, "port_max"); pmin != 0 || pmax != 0 {
		in.PortRange = &frpconf.PortRange{Min: pmin, Max: pmax}
	}
	c, err := u.fleet.CreateClient(r.Context(), in)
	if err != nil {
		u.fail(w, r, err, func(msg string) {
			d.Error = msg
			d.Global, _ = u.fleet.Global(r.Context())
			u.render(w, "client-new", "base", http.StatusUnprocessableEntity, d)
		})
		return
	}
	u.showPairing(w, r, d, c)
}

func (u *UI) pairClient(w http.ResponseWriter, r *http.Request, d *pageData) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := u.fleet.Client(r.Context(), id)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	u.showPairing(w, r, d, c)
}

func (u *UI) showPairing(w http.ResponseWriter, r *http.Request, d *pageData, c store.Client) {
	token, expires, err := u.fleet.CreatePairingToken(r.Context(), c.ID)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	d.Client = clientView{Client: c}
	d.Code = protocol.PairingCode{Addr: u.cfg.AgentAddr, Fingerprint: u.cfg.Fingerprint, Token: token}.Encode()
	d.InstallCmd = protocol.InstallCommand(u.cfg.AgentAddr, u.cfg.PublicKeyPin, token)
	d.Token, d.PairState = token, fleet.PairingWaiting
	d.Expires, d.TTL, d.AgentAddr = expires, fleet.PairingTTL, u.cfg.AgentAddr
	u.render(w, "pair", "base", http.StatusOK, d)
}

// pairStatus is polled by the pairing page: it shows how far the client got
// and, once its agent is connected, moves on to the client's page.
func (u *UI) pairStatus(w http.ResponseWriter, r *http.Request, d *pageData) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	c, err := u.fleet.Client(r.Context(), id)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	state, err := u.fleet.PairingState(r.Context(), id, r.URL.Query().Get("t"))
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	if state == fleet.PairingUsed && u.presence.Online(id) {
		redirect(w, r, clientURL(id, "client-paired"))
		return
	}
	d.Client, d.PairState = clientView{Client: c}, state
	status := http.StatusOK
	if state == fleet.PairingExpired {
		status = htmxStopPolling
	}
	u.render(w, "pair", "pair-status", status, d)
}

// htmxStopPolling is the status that makes htmx stop an "every Ns" trigger.
const htmxStopPolling = 286

func (u *UI) clientPage(w http.ResponseWriter, r *http.Request, d *pageData) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	u.renderClient(w, r, d, id, http.StatusOK)
}

func (u *UI) renderClient(w http.ResponseWriter, r *http.Request, d *pageData, id int64, status int) {
	ctx := r.Context()
	c, err := u.fleet.Client(ctx, id)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	d.Client = clientView{Client: c, Online: u.presence.Online(c.ID)}
	d.PublicHost = u.fleet.PublicHost()
	ps, err := u.fleet.Proxies(ctx, id)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	for _, p := range ps {
		pv := proxyView{Proxy: p}
		if p.Mode == store.ModePrivate {
			if pv.Visitors, err = u.fleet.VisitorsOfProxy(ctx, p.ID); err != nil {
				u.fail(w, r, err, nil)
				return
			}
		}
		d.Proxies = append(d.Proxies, pv)
	}
	all, err := u.fleet.Clients(ctx)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	for _, o := range all {
		if o.ID != id {
			d.Others = append(d.Others, o)
		}
	}
	u.render(w, "client", "base", status, d)
}

func (u *UI) deleteClient(w http.ResponseWriter, r *http.Request, _ *pageData) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := u.fleet.DeleteClient(r.Context(), id); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	redirect(w, r, "/?ok=client-deleted")
}

// --- services ---

func (u *UI) addProxy(w http.ResponseWriter, r *http.Request, d *pageData) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, err := u.fleet.AddProxy(r.Context(), id, fleet.ProxyInput{
		Name: r.FormValue("name"), Mode: r.FormValue("mode"), Type: r.FormValue("type"),
		LocalIP: r.FormValue("local_ip"), LocalPort: formInt(r, "local_port"), RemotePort: formInt(r, "remote_port"),
	})
	if err != nil {
		u.fail(w, r, err, func(msg string) {
			d.Error = msg
			u.renderClient(w, r, d, id, http.StatusUnprocessableEntity)
		})
		return
	}
	redirect(w, r, "/clients/"+strconv.FormatInt(id, 10)+"?ok=proxy-added")
}

// proxyOwner loads a proxy for the {id} path value.
func (u *UI) proxyOwner(w http.ResponseWriter, r *http.Request) (store.Proxy, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return store.Proxy{}, false
	}
	p, err := u.fleet.Proxy(r.Context(), id)
	if err != nil {
		u.fail(w, r, err, nil)
		return store.Proxy{}, false
	}
	return p, true
}

func clientURL(id int64, ok string) string {
	return "/clients/" + strconv.FormatInt(id, 10) + "?ok=" + ok
}

func (u *UI) deleteProxy(w http.ResponseWriter, r *http.Request, _ *pageData) {
	p, ok := u.proxyOwner(w, r)
	if !ok {
		return
	}
	if err := u.fleet.RemoveProxy(r.Context(), p.ID); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	redirect(w, r, clientURL(p.ClientID, "proxy-deleted"))
}

func (u *UI) grantVisitor(w http.ResponseWriter, r *http.Request, d *pageData) {
	p, ok := u.proxyOwner(w, r)
	if !ok {
		return
	}
	visitor, _ := strconv.ParseInt(r.FormValue("client_id"), 10, 64)
	if err := u.fleet.GrantVisitor(r.Context(), p.ID, visitor, formInt(r, "bind_port")); err != nil {
		u.fail(w, r, err, func(msg string) {
			d.Error = msg
			u.renderClient(w, r, d, p.ClientID, http.StatusUnprocessableEntity)
		})
		return
	}
	redirect(w, r, clientURL(p.ClientID, "visitor-granted"))
}

func (u *UI) revokeVisitor(w http.ResponseWriter, r *http.Request, _ *pageData) {
	p, ok := u.proxyOwner(w, r)
	if !ok {
		return
	}
	visitor, ok := pathID(r, "cid")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := u.fleet.RevokeVisitor(r.Context(), p.ID, visitor); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	redirect(w, r, clientURL(p.ClientID, "visitor-revoked"))
}

// --- settings ---

func (u *UI) settingsPage(w http.ResponseWriter, r *http.Request, d *pageData) {
	var err error
	if d.Firewall, err = firewallStatus(r.Context(), u.cfg.Firewall); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	u.renderSettings(w, r, d)
}

func (u *UI) renderSettings(w http.ResponseWriter, r *http.Request, d *pageData) {
	var err error
	if d.Global, err = u.fleet.Global(r.Context()); err != nil {
		u.fail(w, r, err, nil)
		return
	}
	d.PublicHost, d.AgentAddr, d.Fingerprint = u.fleet.PublicHost(), u.cfg.AgentAddr, u.cfg.Fingerprint
	d.FRPSRunning, d.FRPSLogs, d.FRPVersion = u.frps.Running(), u.frps.Logs(), frpbin.Version
	u.render(w, "settings", "base", http.StatusOK, d)
}

// --- middleware ---

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site state-changing requests (CSRF), on top of the
// SameSite=Strict session cookie. Browsers send Sec-Fetch-Site; it is
// checked first because Origin can legitimately be "null" (e.g. under some
// referrer policies). Without it (old browsers), Origin must match the host.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			var bad bool
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
				bad = site != "same-origin"
			} else if origin := r.Header.Get("Origin"); origin != "" {
				bad = origin != "http://"+r.Host && origin != "https://"+r.Host
			}
			if bad {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
