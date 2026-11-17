package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Proxy modes and types as stored.
const (
	ModePublic  = "public"
	ModePrivate = "private"
	TypeTCP     = "tcp"
	TypeUDP     = "udp"
	TypeSTCP    = "stcp"
)

// Proxy is a service exposed by a client.
type Proxy struct {
	ID         int64
	ClientID   int64
	Name       string
	Mode       string
	Type       string
	LocalIP    string
	LocalPort  int
	RemotePort int    // public only
	SecretKey  string // private only
	Overrides  string // JSON
	Enabled    bool
	CreatedAt  time.Time
}

// Visitor grants a client access to a private proxy on a local bind port.
type Visitor struct {
	ProxyID         int64
	VisitorClientID int64
	BindPort        int
}

// VisitorView is a visitor grant joined with what the visiting agent needs to
// build its config.
type VisitorView struct {
	Visitor
	ProxyName  string
	OwnerName  string // frp user of the proxy owner
	SecretKey  string
	ClientName string // the visiting client
}

// ProxyRepo persists proxies and visitor grants.
type ProxyRepo struct{ base }

const proxyCols = `id, client_id, name, mode, type, local_ip, local_port, COALESCE(remote_port, 0),
	COALESCE(sk, ''), overrides_json, enabled, created_at`

func scanProxy(row interface{ Scan(...any) error }) (Proxy, error) {
	var (
		p       Proxy
		created int64
	)
	err := row.Scan(&p.ID, &p.ClientID, &p.Name, &p.Mode, &p.Type, &p.LocalIP, &p.LocalPort,
		&p.RemotePort, &p.SecretKey, &p.Overrides, &p.Enabled, &created)
	p.CreatedAt = time.Unix(created, 0)
	return p, err
}

// Create inserts a proxy. A duplicate name for the client returns
// ErrNameTaken; a remote port already used by the same protocol returns
// ErrPortTaken.
func (r *ProxyRepo) Create(ctx context.Context, p Proxy) (Proxy, error) {
	if p.Overrides == "" {
		p.Overrides = "{}"
	}
	p.CreatedAt = time.Unix(r.unixNow(), 0)
	var remote, sk any
	if p.RemotePort != 0 {
		remote = p.RemotePort
	}
	if p.SecretKey != "" {
		sk = p.SecretKey
	}
	res, err := r.q.ExecContext(ctx, `INSERT INTO proxies
		(client_id, name, mode, type, local_ip, local_port, remote_port, sk, overrides_json, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ClientID, p.Name, p.Mode, p.Type, p.LocalIP, p.LocalPort, remote, sk, p.Overrides, p.Enabled, p.CreatedAt.Unix())
	if err != nil {
		return Proxy{}, mapUnique(err, map[string]error{
			"proxies.client_id, proxies.name":   ErrNameTaken,
			"proxies.type, proxies.remote_port": ErrPortTaken,
		})
	}
	p.ID, err = res.LastInsertId()
	return p, err
}

// Get returns a proxy or ErrNotFound.
func (r *ProxyRepo) Get(ctx context.Context, id int64) (Proxy, error) {
	p, err := scanProxy(r.q.QueryRowContext(ctx, `SELECT `+proxyCols+` FROM proxies WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Proxy{}, ErrNotFound
	}
	return p, err
}

// ListByClient returns a client's proxies ordered by name.
func (r *ProxyRepo) ListByClient(ctx context.Context, clientID int64) ([]Proxy, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT `+proxyCols+` FROM proxies WHERE client_id = ? ORDER BY name`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proxy
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a proxy and its visitor grants.
func (r *ProxyRepo) Delete(ctx context.Context, id int64) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id))
}

// UsedRemotePorts returns the remote ports taken by proxies of a protocol.
func (r *ProxyRepo) UsedRemotePorts(ctx context.Context, typ string) (map[int]bool, error) {
	rows, err := r.q.QueryContext(ctx,
		`SELECT remote_port FROM proxies WHERE type = ? AND remote_port IS NOT NULL`, typ)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		used[p] = true
	}
	return used, rows.Err()
}

// PublicPort is a VDS port a public proxy listens on.
type PublicPort struct {
	Port       int
	Proto      string // tcp or udp
	ClientName string
	ProxyName  string
}

// PublicPorts lists the remote ports of all enabled public proxies, by port.
func (r *ProxyRepo) PublicPorts(ctx context.Context) ([]PublicPort, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT p.remote_port, p.type, c.name, p.name
		FROM proxies p JOIN clients c ON c.id = p.client_id
		WHERE p.mode = 'public' AND p.enabled = 1 ORDER BY p.remote_port, p.type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PublicPort
	for rows.Next() {
		var pp PublicPort
		if err := rows.Scan(&pp.Port, &pp.Proto, &pp.ClientName, &pp.ProxyName); err != nil {
			return nil, err
		}
		out = append(out, pp)
	}
	return out, rows.Err()
}

// AddVisitor grants access. A bind port already used by the visiting client
// returns ErrPortTaken; an existing grant returns ErrNameTaken.
func (r *ProxyRepo) AddVisitor(ctx context.Context, v Visitor) error {
	_, err := r.q.ExecContext(ctx,
		`INSERT INTO proxy_visitors (proxy_id, visitor_client_id, bind_port) VALUES (?, ?, ?)`,
		v.ProxyID, v.VisitorClientID, v.BindPort)
	return mapUnique(err, map[string]error{
		"proxy_visitors.visitor_client_id, proxy_visitors.bind_port": ErrPortTaken,
		"proxy_visitors.proxy_id, proxy_visitors.visitor_client_id":  ErrNameTaken,
	})
}

// RemoveVisitor revokes a grant.
func (r *ProxyRepo) RemoveVisitor(ctx context.Context, proxyID, visitorClientID int64) error {
	return rowsAffectedOrNotFound(r.q.ExecContext(ctx,
		`DELETE FROM proxy_visitors WHERE proxy_id = ? AND visitor_client_id = ?`, proxyID, visitorClientID))
}

const visitorViewQuery = `SELECT v.proxy_id, v.visitor_client_id, v.bind_port, p.name, owner.name, p.sk, vc.name
	FROM proxy_visitors v
	JOIN proxies p      ON p.id = v.proxy_id
	JOIN clients owner  ON owner.id = p.client_id
	JOIN clients vc     ON vc.id = v.visitor_client_id
	WHERE `

// VisitorsOfClient returns the grants a client uses as a visitor.
func (r *ProxyRepo) VisitorsOfClient(ctx context.Context, clientID int64) ([]VisitorView, error) {
	return r.visitors(ctx, visitorViewQuery+`v.visitor_client_id = ? ORDER BY p.name`, clientID)
}

// VisitorsOfProxy returns the grants for one proxy.
func (r *ProxyRepo) VisitorsOfProxy(ctx context.Context, proxyID int64) ([]VisitorView, error) {
	return r.visitors(ctx, visitorViewQuery+`v.proxy_id = ? ORDER BY vc.name`, proxyID)
}

func (r *ProxyRepo) visitors(ctx context.Context, query string, arg any) ([]VisitorView, error) {
	rows, err := r.q.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VisitorView
	for rows.Next() {
		var v VisitorView
		if err := rows.Scan(&v.ProxyID, &v.VisitorClientID, &v.BindPort, &v.ProxyName, &v.OwnerName,
			&v.SecretKey, &v.ClientName); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
