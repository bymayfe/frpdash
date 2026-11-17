// Package ports decides which VDS ports public proxies may use. It is pure:
// callers pass in the ports already taken and, optionally, a check for ports
// other programs on the host listen on.
//
// A port the operator picks may be any port (within the client's own range,
// if it has one); an automatically picked port comes from the server range.
package ports

import (
	"errors"
	"fmt"

	"github.com/bymayfe/frpdash/internal/frpconf"
)

// Errors returned by Allocator.
var (
	ErrNoOverlap = errors.New("client port range does not overlap the server's allowed range")
	ErrExhausted = errors.New("no free port left in the allowed range")
	ErrReserved  = errors.New("port is reserved by frpdash/frps")
	ErrInUse     = errors.New("port is already used by another service")
	ErrBusy      = errors.New("port is already used by another program on the VDS")
)

// Allocator hands out remote ports from the global range, minus reserved
// ports (frps bind port, agent listener).
type Allocator struct {
	global   frpconf.PortRange
	reserved map[int]bool
	busy     func(port int) bool // nil: no host check
}

// WithBusy adds a check for ports other programs on the host listen on.
func (a *Allocator) WithBusy(busy func(port int) bool) *Allocator {
	a.busy = busy
	return a
}

func (a *Allocator) isBusy(port int) bool { return a.busy != nil && a.busy(port) }

// NewAllocator builds an Allocator.
func NewAllocator(global frpconf.PortRange, reserved ...int) *Allocator {
	r := make(map[int]bool, len(reserved))
	for _, p := range reserved {
		r[p] = true
	}
	return &Allocator{global: global, reserved: r}
}

// allowed returns the range a client may use: the global range, narrowed by
// the client's own range when it has one.
func (a *Allocator) allowed(client *frpconf.PortRange) (frpconf.PortRange, error) {
	if client == nil {
		return a.global, nil
	}
	r, ok := a.global.Intersect(*client)
	if !ok {
		return frpconf.PortRange{}, ErrNoOverlap
	}
	return r, nil
}

// Check validates a port the user picked: any port, or one inside the
// client's own range when it has one.
func (a *Allocator) Check(port int, client *frpconf.PortRange, used map[int]bool) error {
	switch {
	case port < 1 || port > 65535:
		return fmt.Errorf("port %d is not 1-65535", port)
	case client != nil && !client.Contains(port):
		return fmt.Errorf("port %d is outside this client's allowed range %s", port, *client)
	case a.reserved[port]:
		return ErrReserved
	case used[port]:
		return ErrInUse
	case a.isBusy(port):
		return ErrBusy
	}
	return nil
}

// Pick returns the lowest free port the client may use.
func (a *Allocator) Pick(client *frpconf.PortRange, used map[int]bool) (int, error) {
	r, err := a.allowed(client)
	if err != nil {
		return 0, err
	}
	for p := r.Min; p <= r.Max; p++ {
		if !a.reserved[p] && !used[p] && !a.isBusy(p) {
			return p, nil
		}
	}
	return 0, ErrExhausted
}
