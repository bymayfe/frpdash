// This part (fwsync) keeps the host firewall in step with frpdash: the agent and
// frps ports are always open, and each public service's VDS port is opened
// when the service is added and closed when it is removed. Nothing is
// changed until the operator lets frpdash manage the firewall once (setup
// page or Settings → Firewall); from then on changes follow automatically.
//
// Only rules frpdash opened itself (remembered in the settings table) are
// ever closed, so rules the operator added by hand and SSH stay untouched.

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/bymayfe/frpdash/internal/store"
)

// settingsKey stores State.
const settingsKey = "firewall_state"

// State is what frpdash remembers about the firewall.
type State struct {
	Managed bool   `json:"managed"` // the operator let frpdash manage it
	Applied []Rule `json:"applied"` // rules frpdash opened and may close again
}

// FirewallBackend is what FirewallSync needs from FirewallManager.
type FirewallBackend interface {
	Detect(ctx context.Context) Status
	Apply(ctx context.Context, st Status, rules []Rule) ([]Step, error)
	Remove(ctx context.Context, st Status, rules []Rule) ([]Step, error)
}

// FirewallSync computes the required rules and applies them.
type FirewallSync struct {
	db        *store.DB
	fw        FirewallBackend
	agentPort int
	log       *slog.Logger

	mu      sync.Mutex // one sync at a time
	trigger chan struct{}
}

// NewFirewallSync builds a Service.
func NewFirewallSync(db *store.DB, fw FirewallBackend, agentPort int, log *slog.Logger) *FirewallSync {
	return &FirewallSync{db: db, fw: fw, agentPort: agentPort, log: log.With("component", "firewall"),
		trigger: make(chan struct{}, 1)}
}

// Detect reports the host firewall.
func (s *FirewallSync) Detect(ctx context.Context) Status { return s.fw.Detect(ctx) }

// Rules returns the ports frpdash needs now: the agent listener, frps, and
// the VDS port of every enabled public service.
func (s *FirewallSync) Rules(ctx context.Context) ([]Rule, error) {
	g, err := NewFleetService(s.db, "", s.agentPort).Global(ctx)
	if err != nil {
		return nil, err
	}
	rules := []Rule{
		{Port: s.agentPort, Proto: "tcp", Purpose: "client connections and install script"},
		{Port: g.BindPort, Proto: "tcp", Purpose: "frps"},
		{Port: g.BindPort, Proto: "udp", Purpose: "frps (kcp/quic)"},
	}
	public, err := s.db.Repos().Proxies.PublicPorts(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range public {
		rules = append(rules, Rule{Port: p.Port, Proto: p.Proto,
			Purpose: fmt.Sprintf("public service %s.%s", p.ClientName, p.ProxyName)})
	}
	return dedupe(rules), nil
}

// Managed reports whether the operator has let frpdash manage the firewall.
func (s *FirewallSync) Managed(ctx context.Context) (bool, error) {
	st, err := s.state(ctx)
	return st.Managed, err
}

// Sync is the operator's "open the ports" action: it opens every required
// rule (installing and enabling ufw if needed, SSH first), closes rules
// frpdash opened that are no longer needed, and from then on keeps the
// firewall in step automatically.
func (s *FirewallSync) Sync(ctx context.Context) ([]Step, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.fw.Detect(ctx)
	if !st.Supported() {
		return nil, errors.New("no supported firewall: open the ports in your firewall or provider panel")
	}
	want, err := s.Rules(ctx)
	if err != nil {
		return nil, err
	}
	steps, err := s.fw.Apply(ctx, st, want)
	if err != nil {
		return steps, err
	}
	return s.closeStale(ctx, steps, want)
}

// ClientChanged implements Notifier: a service may have been added or
// removed, so the firewall is brought in step (in Run, without blocking).
func (s *FirewallSync) ClientChanged(int64) {
	select {
	case s.trigger <- struct{}{}:
	default: // a sync is already pending
	}
}

// Run applies changes signalled by ClientChanged until ctx ends.
func (s *FirewallSync) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
			if err := s.follow(ctx); err != nil {
				s.log.Error("firewall sync", "err", err)
			}
		}
	}
}

// follow opens what is missing and closes what is no longer needed, but only
// if frpdash manages an active firewall.
func (s *FirewallSync) follow(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(ctx)
	if err != nil || !state.Managed {
		return err
	}
	st := s.fw.Detect(ctx)
	if !st.Active {
		s.log.Warn("firewall is not active; ports were not changed")
		return nil
	}
	want, err := s.Rules(ctx)
	if err != nil {
		return err
	}
	if add := missing(want, state.Applied); len(add) > 0 {
		steps, err := s.fw.Apply(ctx, st, add)
		s.logSteps(steps)
		if err != nil {
			return err
		}
	}
	steps, err := s.closeStale(ctx, nil, want)
	s.logSteps(steps)
	return err
}

// closeStale closes rules frpdash opened that want no longer has, and
// records want (plus anything that failed to close) as applied.
func (s *FirewallSync) closeStale(ctx context.Context, steps []Step, want []Rule) ([]Step, error) {
	state, err := s.state(ctx)
	if err != nil {
		return steps, err
	}
	stale := missing(state.Applied, want)
	removed, rmErr := s.fw.Remove(ctx, s.fw.Detect(ctx), stale)
	steps = append(steps, removed...)
	applied := want
	if rmErr != nil {
		applied = dedupe(append(slices.Clone(want), stale...)) // try again next time
	}
	if err := s.db.Repos().Settings.Set(ctx, settingsKey, State{Managed: true, Applied: applied}); err != nil {
		return steps, err
	}
	return steps, rmErr
}

func (s *FirewallSync) state(ctx context.Context) (State, error) {
	var st State
	err := s.db.Repos().Settings.Get(ctx, settingsKey, &st)
	if errors.Is(err, store.ErrNotFound) {
		return State{}, nil
	}
	return st, err
}

func (s *FirewallSync) logSteps(steps []Step) {
	for _, st := range steps {
		if st.Err != "" {
			s.log.Warn("firewall command failed", "cmd", st.Command, "err", st.Err, "output", st.Output)
		} else {
			s.log.Info("firewall", "cmd", st.Command)
		}
	}
}

// missing returns the rules in a that b lacks (compared by port/proto).
func missing(a, b []Rule) []Rule {
	have := map[string]bool{}
	for _, r := range b {
		have[r.String()] = true
	}
	var out []Rule
	for _, r := range a {
		if !have[r.String()] {
			out = append(out, r)
		}
	}
	return out
}

// dedupe drops repeated port/proto pairs, keeping the first.
func dedupe(rules []Rule) []Rule {
	seen := map[string]bool{}
	out := rules[:0:0]
	for _, r := range rules {
		if !seen[r.String()] {
			seen[r.String()] = true
			out = append(out, r)
		}
	}
	return out
}
