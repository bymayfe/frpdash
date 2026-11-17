// Package fleet holds the business rules for clients and their services:
// naming, port allocation, visitor grants, the global settings layer and the
// desired frpc config each agent should run. Every change bumps the affected
// clients' desired revision and notifies the agent hub.
package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"

	"github.com/bymayfe/frpdash/internal/frpconf"
	"github.com/bymayfe/frpdash/internal/server/ports"
	"github.com/bymayfe/frpdash/internal/server/store"
)

const (
	settingsKeyGlobal = "global"
	maxNameLen        = 32
	defaultLocalIP    = "127.0.0.1"
)

// ErrInvalid wraps every user input error so handlers can show it as-is.
var ErrInvalid = errors.New("invalid input")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Global is the top settings layer.
type Global struct {
	BindPort  int               `json:"bindPort"`
	Token     string            `json:"token"`
	PortRange frpconf.PortRange `json:"portRange"`
	Overrides frpconf.Overrides `json:"overrides"`
}

// DefaultGlobal is written on first start; the token is generated.
var DefaultGlobal = Global{BindPort: 7000, PortRange: frpconf.PortRange{Min: 20000, Max: 20999}}

// Notifier is told which clients' desired state changed.
type Notifier interface {
	ClientChanged(clientID int64)
}

// Service manages the fleet.
type Service struct {
	db         *store.DB
	publicHost string
	reserved   []int
	notifier   Notifier
	host       HostPorts // nil: no check for ports other programs use
}

// HostPorts reports ports other programs on the VDS listen on, so a public
// service is not given one of them.
type HostPorts interface {
	InUse(proto string, port int) bool
}

// SetHostPorts enables the check for ports already used on the VDS.
func (s *Service) SetHostPorts(h HostPorts) { s.host = h }

// NewService builds a Service. publicHost is the VDS address agents and frpc
// dial; reserved ports are never handed to proxies.
func NewService(db *store.DB, publicHost string, reserved ...int) *Service {
	return &Service{db: db, publicHost: publicHost, reserved: reserved}
}

// SetNotifier registers the change listener (the agent hub).
func (s *Service) SetNotifier(n Notifier) { s.notifier = n }

// PublicHost returns the VDS address.
func (s *Service) PublicHost() string { return s.publicHost }

func (s *Service) notify(ids ...int64) {
	if s.notifier == nil {
		return
	}
	for _, id := range ids {
		s.notifier.ClientChanged(id)
	}
}

// Global returns the global layer, creating it with a fresh token on first use.
func (s *Service) Global(ctx context.Context) (Global, error) {
	var g Global
	err := s.db.Repos().Settings.Get(ctx, settingsKeyGlobal, &g)
	if !errors.Is(err, store.ErrNotFound) {
		return g, err
	}
	g = DefaultGlobal
	if g.Token, err = randomHex(24); err != nil {
		return g, err
	}
	return g, s.db.Repos().Settings.Set(ctx, settingsKeyGlobal, g)
}

// ServerConfig builds the frps config from the global layer.
func (s *Service) ServerConfig(ctx context.Context) (frpconf.ServerConfig, error) {
	g, err := s.Global(ctx)
	if err != nil {
		return frpconf.ServerConfig{}, err
	}
	return frpconf.ServerConfig{
		BindAddr: "0.0.0.0", BindPort: g.BindPort, Token: g.Token,
		AllowPorts: []frpconf.PortRange{g.PortRange},
	}, nil
}

func (s *Service) allocator(g Global) *ports.Allocator {
	return ports.NewAllocator(g.PortRange, append([]int{g.BindPort}, s.reserved...)...)
}

func validateName(kind, name string) error {
	if len(name) > maxNameLen || frpconf.ValidateName(kind, name) != nil {
		return invalidf("%s name must be 1-%d characters: a-z, 0-9, '-' or '_'", kind, maxNameLen)
	}
	return nil
}

func clientRange(c store.Client) *frpconf.PortRange {
	if c.PortMin == 0 {
		return nil
	}
	return &frpconf.PortRange{Min: c.PortMin, Max: c.PortMax}
}

// ClientInput describes a new client.
type ClientInput struct {
	Name      string
	PortRange *frpconf.PortRange // optional, narrows the global range
}

// CreateClient adds a client (unpaired until its agent connects).
func (s *Service) CreateClient(ctx context.Context, in ClientInput) (store.Client, error) {
	if err := validateName("client", in.Name); err != nil {
		return store.Client{}, err
	}
	c := store.Client{Name: in.Name}
	if in.PortRange != nil {
		if err := in.PortRange.Validate(); err != nil {
			return store.Client{}, invalidf("%v", err)
		}
		c.PortMin, c.PortMax = in.PortRange.Min, in.PortRange.Max
	}
	c, err := s.db.Repos().Clients.Create(ctx, c)
	if errors.Is(err, store.ErrNameTaken) {
		return c, invalidf("a client named %q already exists", in.Name)
	}
	return c, err
}

// Client returns one client.
func (s *Service) Client(ctx context.Context, id int64) (store.Client, error) {
	return s.db.Repos().Clients.Get(ctx, id)
}

// Clients returns all clients.
func (s *Service) Clients(ctx context.Context) ([]store.Client, error) {
	return s.db.Repos().Clients.List(ctx)
}

// DeleteClient removes a client with its services; clients that visited
// those services, and owners of services it visited, get a new desired state.
func (s *Service) DeleteClient(ctx context.Context, id int64) error {
	var changed []int64
	err := s.db.InTx(ctx, func(r *store.Repos) error {
		proxies, err := r.Proxies.ListByClient(ctx, id)
		if err != nil {
			return err
		}
		for _, p := range proxies {
			vs, err := r.Proxies.VisitorsOfProxy(ctx, p.ID)
			if err != nil {
				return err
			}
			for _, v := range vs {
				changed = append(changed, v.VisitorClientID)
			}
		}
		// Owners of services this client visited lose it from allowUsers.
		visited, err := r.Proxies.VisitorsOfClient(ctx, id)
		if err != nil {
			return err
		}
		for _, v := range visited {
			p, err := r.Proxies.Get(ctx, v.ProxyID)
			if err != nil {
				return err
			}
			changed = append(changed, p.ClientID)
		}
		if err := r.Clients.Delete(ctx, id); err != nil {
			return err
		}
		return bumpAll(ctx, r, changed)
	})
	if err == nil {
		s.notify(append(changed, id)...)
	}
	return err
}

func bumpAll(ctx context.Context, r *store.Repos, ids []int64) error {
	for _, id := range ids {
		if _, err := r.Clients.BumpDesiredRev(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// ProxyInput describes a new service.
type ProxyInput struct {
	Name       string
	Mode       string // store.ModePublic or store.ModePrivate
	Type       string // tcp or udp for public; ignored for private (stcp)
	LocalIP    string // default 127.0.0.1
	LocalPort  int
	RemotePort int // public only; 0 picks a free port
}

// AddProxy validates and stores a service, allocating a remote port for
// public ones, and bumps the client's desired revision.
func (s *Service) AddProxy(ctx context.Context, clientID int64, in ProxyInput) (store.Proxy, error) {
	if err := validateName("service", in.Name); err != nil {
		return store.Proxy{}, err
	}
	if in.LocalIP == "" {
		in.LocalIP = defaultLocalIP
	}
	if net.ParseIP(in.LocalIP) == nil {
		return store.Proxy{}, invalidf("local IP %q is not an IP address", in.LocalIP)
	}
	if in.LocalPort < 1 || in.LocalPort > 65535 {
		return store.Proxy{}, invalidf("local port must be 1-65535")
	}
	p := store.Proxy{ClientID: clientID, Name: in.Name, Mode: in.Mode, LocalIP: in.LocalIP,
		LocalPort: in.LocalPort, Enabled: true}
	switch in.Mode {
	case store.ModePublic:
		if in.Type != store.TypeTCP && in.Type != store.TypeUDP {
			return store.Proxy{}, invalidf("public services are tcp or udp")
		}
		p.Type = in.Type
	case store.ModePrivate:
		p.Type = store.TypeSTCP
		sk, err := randomHex(16)
		if err != nil {
			return store.Proxy{}, err
		}
		p.SecretKey = sk
	default:
		return store.Proxy{}, invalidf("mode must be public or private")
	}

	g, err := s.Global(ctx)
	if err != nil {
		return store.Proxy{}, err
	}
	err = s.db.InTx(ctx, func(r *store.Repos) error {
		c, err := r.Clients.Get(ctx, clientID)
		if err != nil {
			return err
		}
		if p.Mode == store.ModePublic {
			used, err := r.Proxies.UsedRemotePorts(ctx, p.Type)
			if err != nil {
				return err
			}
			alloc := s.allocator(g)
			if s.host != nil {
				alloc.WithBusy(func(port int) bool { return s.host.InUse(p.Type, port) })
			}
			if in.RemotePort == 0 {
				p.RemotePort, err = alloc.Pick(clientRange(c), used)
			} else {
				p.RemotePort, err = in.RemotePort, alloc.Check(in.RemotePort, clientRange(c), used)
			}
			if err != nil {
				return invalidf("%v", err)
			}
		}
		if p, err = r.Proxies.Create(ctx, p); err != nil {
			return err
		}
		_, err = r.Clients.BumpDesiredRev(ctx, clientID)
		return err
	})
	switch {
	case errors.Is(err, store.ErrNameTaken):
		return p, invalidf("this client already has a service named %q", in.Name)
	case errors.Is(err, store.ErrPortTaken):
		return p, invalidf("%v", ports.ErrInUse)
	case err != nil:
		return p, err
	}
	s.notify(clientID)
	return p, nil
}

// Proxy returns one service.
func (s *Service) Proxy(ctx context.Context, id int64) (store.Proxy, error) {
	return s.db.Repos().Proxies.Get(ctx, id)
}

// Proxies returns a client's services.
func (s *Service) Proxies(ctx context.Context, clientID int64) ([]store.Proxy, error) {
	return s.db.Repos().Proxies.ListByClient(ctx, clientID)
}

// RemoveProxy deletes a service and updates its owner and visitors.
func (s *Service) RemoveProxy(ctx context.Context, proxyID int64) error {
	var changed []int64
	err := s.db.InTx(ctx, func(r *store.Repos) error {
		p, err := r.Proxies.Get(ctx, proxyID)
		if err != nil {
			return err
		}
		vs, err := r.Proxies.VisitorsOfProxy(ctx, proxyID)
		if err != nil {
			return err
		}
		changed = append(changed, p.ClientID)
		for _, v := range vs {
			changed = append(changed, v.VisitorClientID)
		}
		if err := r.Proxies.Delete(ctx, proxyID); err != nil {
			return err
		}
		return bumpAll(ctx, r, changed)
	})
	if err == nil {
		s.notify(changed...)
	}
	return err
}

// VisitorsOfProxy lists who may reach a private service.
func (s *Service) VisitorsOfProxy(ctx context.Context, proxyID int64) ([]store.VisitorView, error) {
	return s.db.Repos().Proxies.VisitorsOfProxy(ctx, proxyID)
}

// GrantVisitor lets visitorClientID reach a private service on bindPort
// (127.0.0.1 on the visiting machine).
func (s *Service) GrantVisitor(ctx context.Context, proxyID, visitorClientID int64, bindPort int) error {
	if bindPort < 1 || bindPort > 65535 {
		return invalidf("local port must be 1-65535")
	}
	var owner int64
	err := s.db.InTx(ctx, func(r *store.Repos) error {
		p, err := r.Proxies.Get(ctx, proxyID)
		if err != nil {
			return err
		}
		if p.Mode != store.ModePrivate {
			return invalidf("only private services take visitors")
		}
		if p.ClientID == visitorClientID {
			return invalidf("a client cannot visit its own service")
		}
		if err := r.Proxies.AddVisitor(ctx, store.Visitor{ProxyID: proxyID, VisitorClientID: visitorClientID,
			BindPort: bindPort}); err != nil {
			return err
		}
		owner = p.ClientID
		return bumpAll(ctx, r, []int64{owner, visitorClientID})
	})
	switch {
	case errors.Is(err, store.ErrPortTaken):
		return invalidf("that client already uses local port %d for another service", bindPort)
	case errors.Is(err, store.ErrNameTaken):
		return invalidf("that client already has access")
	case err != nil:
		return err
	}
	s.notify(owner, visitorClientID)
	return nil
}

// RevokeVisitor removes a grant.
func (s *Service) RevokeVisitor(ctx context.Context, proxyID, visitorClientID int64) error {
	var owner int64
	err := s.db.InTx(ctx, func(r *store.Repos) error {
		p, err := r.Proxies.Get(ctx, proxyID)
		if err != nil {
			return err
		}
		if err := r.Proxies.RemoveVisitor(ctx, proxyID, visitorClientID); err != nil {
			return err
		}
		owner = p.ClientID
		return bumpAll(ctx, r, []int64{owner, visitorClientID})
	})
	if err == nil {
		s.notify(owner, visitorClientID)
	}
	return err
}

// DesiredConfig returns the revision and frpc config a client should run.
func (s *Service) DesiredConfig(ctx context.Context, clientID int64) (int64, frpconf.ClientConfig, error) {
	g, err := s.Global(ctx)
	if err != nil {
		return 0, frpconf.ClientConfig{}, err
	}
	var (
		rev int64
		cfg frpconf.ClientConfig
	)
	err = s.db.InTx(ctx, func(r *store.Repos) error {
		c, err := r.Clients.Get(ctx, clientID)
		if err != nil {
			return err
		}
		clientLayer, err := frpconf.ParseOverrides(c.Overrides)
		if err != nil {
			return err
		}
		opts := frpconf.Resolve(g.Overrides, clientLayer)
		rev = c.DesiredRev
		cfg = frpconf.ClientConfig{
			ServerAddr: s.publicHost, ServerPort: g.BindPort, Token: g.Token,
			User: c.Name, Protocol: opts.Protocol,
			Proxies: []frpconf.Proxy{}, Visitors: []frpconf.Visitor{},
		}

		proxies, err := r.Proxies.ListByClient(ctx, clientID)
		if err != nil {
			return err
		}
		for _, p := range proxies {
			if !p.Enabled {
				continue
			}
			proxyLayer, err := frpconf.ParseOverrides(p.Overrides)
			if err != nil {
				return err
			}
			po := frpconf.Resolve(g.Overrides, clientLayer, proxyLayer)
			fp := frpconf.Proxy{
				Name: p.Name, Type: p.Type, LocalIP: p.LocalIP, LocalPort: p.LocalPort,
				RemotePort: p.RemotePort, SecretKey: p.SecretKey,
				UseEncryption: po.UseEncryption, UseCompression: po.UseCompression,
			}
			if p.Mode == store.ModePrivate {
				// frps only lets other users visit when they are listed here.
				vs, err := r.Proxies.VisitorsOfProxy(ctx, p.ID)
				if err != nil {
					return err
				}
				for _, v := range vs {
					fp.AllowUsers = append(fp.AllowUsers, v.ClientName)
				}
			}
			cfg.Proxies = append(cfg.Proxies, fp)
		}

		visitors, err := r.Proxies.VisitorsOfClient(ctx, clientID)
		if err != nil {
			return err
		}
		for _, v := range visitors {
			cfg.Visitors = append(cfg.Visitors, frpconf.Visitor{
				Name: v.OwnerName + "-" + v.ProxyName, ServerUser: v.OwnerName, ServerName: v.ProxyName,
				SecretKey: v.SecretKey, BindAddr: defaultLocalIP, BindPort: v.BindPort,
				UseEncryption: opts.UseEncryption, UseCompression: opts.UseCompression,
			})
		}
		return nil
	})
	return rev, cfg, err
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
