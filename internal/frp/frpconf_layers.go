package frp

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Transport protocols an agent may use to reach frps. "kcp" needs UDP on the
// frps bind port.
var protocols = map[string]bool{"tcp": true, "kcp": true, "websocket": true}

func validateProtocol(p string) error {
	if !protocols[p] {
		return fmt.Errorf("unsupported protocol %q (tcp, kcp, websocket)", p)
	}
	return nil
}

// Overrides is one settings layer (global, client or proxy). A nil field
// inherits from the layer above.
type Overrides struct {
	Protocol       *string `json:"protocol,omitempty"`
	UseEncryption  *bool   `json:"useEncryption,omitempty"`
	UseCompression *bool   `json:"useCompression,omitempty"`
}

// Options are fully resolved settings.
type Options struct {
	Protocol       string
	UseEncryption  bool
	UseCompression bool
}

// DefaultOptions apply below the global layer.
var DefaultOptions = Options{Protocol: "tcp"}

// ParseOverrides decodes a stored layer, rejecting unknown keys.
func ParseOverrides(raw string) (Overrides, error) {
	var o Overrides
	if raw == "" {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, fmt.Errorf("decode overrides: %w", err)
	}
	return o, o.Validate()
}

// Validate checks the values that are set.
func (o Overrides) Validate() error {
	if o.Protocol != nil {
		return validateProtocol(*o.Protocol)
	}
	return nil
}

// Resolve applies layers in order (global, client, proxy) over
// DefaultOptions; later layers win.
func Resolve(layers ...Overrides) Options {
	opt := DefaultOptions
	for _, l := range layers {
		if l.Protocol != nil {
			opt.Protocol = *l.Protocol
		}
		if l.UseEncryption != nil {
			opt.UseEncryption = *l.UseEncryption
		}
		if l.UseCompression != nil {
			opt.UseCompression = *l.UseCompression
		}
	}
	return opt
}

// PortRange is an inclusive port range.
type PortRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Validate checks bounds and order.
func (r PortRange) Validate() error {
	if !validPort(r.Min) || !validPort(r.Max) || r.Min > r.Max {
		return fmt.Errorf("invalid port range %d-%d", r.Min, r.Max)
	}
	return nil
}

// Contains reports whether p is in the range.
func (r PortRange) Contains(p int) bool { return p >= r.Min && p <= r.Max }

// Intersect returns the overlap of two ranges and whether it is non-empty.
func (r PortRange) Intersect(o PortRange) (PortRange, bool) {
	out := PortRange{Min: max(r.Min, o.Min), Max: min(r.Max, o.Max)}
	return out, out.Min <= out.Max
}

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Min, r.Max) }
