// Package frpconf turns structured frpdash data into frp (v0.52+) TOML.
// Configs are validated first and encoded with a TOML library, so nothing a
// user typed ever reaches frp as raw config text.
package frpconf

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// Proxy types.
const (
	TypeTCP  = "tcp"
	TypeUDP  = "udp"
	TypeSTCP = "stcp"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// ValidateName checks an frp user, proxy or visitor name.
func ValidateName(kind, s string) error {
	if !namePattern.MatchString(s) {
		return fmt.Errorf("invalid %s name %q: use a-z, 0-9, '-' or '_', starting with a letter or digit", kind, s)
	}
	return nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// validSecret rejects empty values and control characters.
func validSecret(s string) bool {
	return s != "" && !strings.ContainsFunc(s, unicode.IsControl)
}

// ClientConfig is everything an agent needs to run frpc. The server sends it
// as JSON; the agent validates it again and renders TOML locally.
type ClientConfig struct {
	ServerAddr string    `json:"serverAddr"`
	ServerPort int       `json:"serverPort"`
	Token      string    `json:"token"`
	User       string    `json:"user"`
	Protocol   string    `json:"protocol"`
	Proxies    []Proxy   `json:"proxies"`
	Visitors   []Visitor `json:"visitors"`
}

// Proxy is one service frpc exposes.
type Proxy struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	LocalIP        string   `json:"localIP"`
	LocalPort      int      `json:"localPort"`
	RemotePort     int      `json:"remotePort,omitempty"` // tcp/udp
	SecretKey      string   `json:"secretKey,omitempty"`  // stcp
	AllowUsers     []string `json:"allowUsers,omitempty"` // stcp: frp users (clients) allowed to visit
	UseEncryption  bool     `json:"useEncryption"`
	UseCompression bool     `json:"useCompression"`
}

// Visitor lets this client reach another client's stcp proxy on a local port.
type Visitor struct {
	Name           string `json:"name"`
	ServerUser     string `json:"serverUser"`
	ServerName     string `json:"serverName"`
	SecretKey      string `json:"secretKey"`
	BindAddr       string `json:"bindAddr"`
	BindPort       int    `json:"bindPort"`
	UseEncryption  bool   `json:"useEncryption"`
	UseCompression bool   `json:"useCompression"`
}

// Validate checks every field frpc will see.
func (c ClientConfig) Validate() error {
	if err := validateHost(c.ServerAddr); err != nil {
		return err
	}
	if !validPort(c.ServerPort) {
		return fmt.Errorf("invalid server port %d", c.ServerPort)
	}
	if !validSecret(c.Token) {
		return errors.New("invalid token")
	}
	if err := ValidateName("user", c.User); err != nil {
		return err
	}
	if err := validateProtocol(c.Protocol); err != nil {
		return err
	}

	names := map[string]bool{}
	unique := func(kind, name string) error {
		if err := ValidateName(kind, name); err != nil {
			return err
		}
		if names[name] {
			return fmt.Errorf("duplicate name %q", name)
		}
		names[name] = true
		return nil
	}
	for _, p := range c.Proxies {
		if err := unique("proxy", p.Name); err != nil {
			return err
		}
		if err := p.validate(); err != nil {
			return fmt.Errorf("proxy %q: %w", p.Name, err)
		}
	}
	for _, v := range c.Visitors {
		if err := unique("visitor", v.Name); err != nil {
			return err
		}
		if err := v.validate(); err != nil {
			return fmt.Errorf("visitor %q: %w", v.Name, err)
		}
	}
	return nil
}

func (p Proxy) validate() error {
	if net.ParseIP(p.LocalIP) == nil {
		return fmt.Errorf("invalid local IP %q", p.LocalIP)
	}
	if !validPort(p.LocalPort) {
		return fmt.Errorf("invalid local port %d", p.LocalPort)
	}
	switch p.Type {
	case TypeTCP, TypeUDP:
		if !validPort(p.RemotePort) || p.SecretKey != "" {
			return errors.New("tcp/udp needs a remote port and no secret key")
		}
	case TypeSTCP:
		if p.RemotePort != 0 || !validSecret(p.SecretKey) {
			return errors.New("stcp needs a secret key and no remote port")
		}
		for _, u := range p.AllowUsers {
			if err := ValidateName("allowed user", u); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported type %q", p.Type)
	}
	return nil
}

func (v Visitor) validate() error {
	if err := ValidateName("server user", v.ServerUser); err != nil {
		return err
	}
	if err := ValidateName("server", v.ServerName); err != nil {
		return err
	}
	if !validSecret(v.SecretKey) {
		return errors.New("invalid secret key")
	}
	if net.ParseIP(v.BindAddr) == nil {
		return fmt.Errorf("invalid bind address %q", v.BindAddr)
	}
	if !validPort(v.BindPort) {
		return fmt.Errorf("invalid bind port %d", v.BindPort)
	}
	return nil
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})$`)

func validateHost(h string) error {
	if net.ParseIP(h) != nil || hostPattern.MatchString(h) {
		return nil
	}
	return fmt.Errorf("invalid server address %q", h)
}

// TOML renders the frpc config. It validates first.
func (c ClientConfig) TOML() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	f := frpcFile{
		ServerAddr:    c.ServerAddr,
		ServerPort:    c.ServerPort,
		User:          c.User,
		LoginFailExit: false, // keep retrying while the server is unreachable
		Auth:          authSection{Method: "token", Token: c.Token},
		Transport:     clientTransport{Protocol: c.Protocol},
		Log:           logSection{To: "console", Level: "info", DisablePrintColor: true},
	}
	for _, p := range c.Proxies {
		f.Proxies = append(f.Proxies, frpcProxy{
			Name: p.Name, Type: p.Type, LocalIP: p.LocalIP, LocalPort: p.LocalPort,
			RemotePort: p.RemotePort, SecretKey: p.SecretKey, AllowUsers: p.AllowUsers,
			Transport: proxyTransport{UseEncryption: p.UseEncryption, UseCompression: p.UseCompression},
		})
	}
	for _, v := range c.Visitors {
		f.Visitors = append(f.Visitors, frpcVisitor{
			Name: v.Name, Type: TypeSTCP, ServerUser: v.ServerUser, ServerName: v.ServerName,
			SecretKey: v.SecretKey, BindAddr: v.BindAddr, BindPort: v.BindPort,
			Transport: proxyTransport{UseEncryption: v.UseEncryption, UseCompression: v.UseCompression},
		})
	}
	return toml.Marshal(f)
}

// ServerConfig is the frps configuration.
type ServerConfig struct {
	BindAddr   string
	BindPort   int
	Token      string
	AllowPorts []PortRange
}

// Validate checks every field frps will see.
func (s ServerConfig) Validate() error {
	if net.ParseIP(s.BindAddr) == nil {
		return fmt.Errorf("invalid bind address %q", s.BindAddr)
	}
	if !validPort(s.BindPort) {
		return fmt.Errorf("invalid bind port %d", s.BindPort)
	}
	if !validSecret(s.Token) {
		return errors.New("invalid token")
	}
	for _, r := range s.AllowPorts {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// TOML renders the frps config. It validates first.
func (s ServerConfig) TOML() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	f := frpsFile{
		BindAddr:    s.BindAddr,
		BindPort:    s.BindPort,
		KCPBindPort: s.BindPort, // UDP, so it can share the number; used by protocol "kcp"
		Auth:        authSection{Method: "token", Token: s.Token},
		Log:         logSection{To: "console", Level: "info", DisablePrintColor: true},
	}
	for _, r := range s.AllowPorts {
		f.AllowPorts = append(f.AllowPorts, portsRange{Start: r.Min, End: r.Max})
	}
	return toml.Marshal(f)
}

// TOML document shapes, keyed as frp v0.52+ expects.
type (
	frpcFile struct {
		ServerAddr    string          `toml:"serverAddr"`
		ServerPort    int             `toml:"serverPort"`
		User          string          `toml:"user"`
		LoginFailExit bool            `toml:"loginFailExit"`
		Auth          authSection     `toml:"auth"`
		Transport     clientTransport `toml:"transport"`
		Log           logSection      `toml:"log"`
		Proxies       []frpcProxy     `toml:"proxies,omitempty"`
		Visitors      []frpcVisitor   `toml:"visitors,omitempty"`
	}
	frpsFile struct {
		BindAddr    string       `toml:"bindAddr"`
		BindPort    int          `toml:"bindPort"`
		KCPBindPort int          `toml:"kcpBindPort"`
		Auth        authSection  `toml:"auth"`
		AllowPorts  []portsRange `toml:"allowPorts,omitempty"`
		Log         logSection   `toml:"log"`
	}
	authSection struct {
		Method string `toml:"method"`
		Token  string `toml:"token"`
	}
	clientTransport struct {
		Protocol string `toml:"protocol"`
	}
	logSection struct {
		To                string `toml:"to"`
		Level             string `toml:"level"`
		DisablePrintColor bool   `toml:"disablePrintColor"`
	}
	proxyTransport struct {
		UseEncryption  bool `toml:"useEncryption"`
		UseCompression bool `toml:"useCompression"`
	}
	frpcProxy struct {
		Name       string         `toml:"name"`
		Type       string         `toml:"type"`
		LocalIP    string         `toml:"localIP"`
		LocalPort  int            `toml:"localPort"`
		RemotePort int            `toml:"remotePort,omitempty"`
		SecretKey  string         `toml:"secretKey,omitempty"`
		AllowUsers []string       `toml:"allowUsers,omitempty"`
		Transport  proxyTransport `toml:"transport"`
	}
	frpcVisitor struct {
		Name       string         `toml:"name"`
		Type       string         `toml:"type"`
		ServerUser string         `toml:"serverUser"`
		ServerName string         `toml:"serverName"`
		SecretKey  string         `toml:"secretKey"`
		BindAddr   string         `toml:"bindAddr"`
		BindPort   int            `toml:"bindPort"`
		Transport  proxyTransport `toml:"transport"`
	}
	portsRange struct {
		Start int `toml:"start"`
		End   int `toml:"end"`
	}
)
