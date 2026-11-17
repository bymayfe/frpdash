package web

import (
	"context"
	"net/http"

	"github.com/bymayfe/frpdash/internal/firewall"
)

// Firewall is what the panel needs from fwsync.Service.
type Firewall interface {
	Detect(ctx context.Context) firewall.Status
	Rules(ctx context.Context) ([]firewall.Rule, error)
	Managed(ctx context.Context) (bool, error)
	Sync(ctx context.Context) ([]firewall.Step, error)
}

// firewallView is what the templates show about the firewall.
type firewallView struct {
	Status  firewall.Status
	Rules   []firewall.Rule
	Managed bool            // frpdash opens and closes service ports automatically
	Steps   []firewall.Step // after Sync
	Err     string          // Sync's error, if any
}

// firewallStatus detects the firewall and lists the required rules; nil
// when the panel has no firewall support.
func firewallStatus(ctx context.Context, fw Firewall) (*firewallView, error) {
	if fw == nil {
		return nil, nil
	}
	rules, err := fw.Rules(ctx)
	if err != nil {
		return nil, err
	}
	managed, err := fw.Managed(ctx)
	if err != nil {
		return nil, err
	}
	return &firewallView{Status: fw.Detect(ctx), Rules: rules, Managed: managed}, nil
}

// syncFirewall opens the ports and records the outcome in v.
func syncFirewall(ctx context.Context, fw Firewall, v *firewallView) {
	steps, err := fw.Sync(ctx)
	v.Steps = steps
	if err != nil {
		v.Err = err.Error()
	}
	v.Status = fw.Detect(ctx)
	v.Managed, _ = fw.Managed(ctx)
}

// openFirewall is the Settings page's "Open the required ports" button.
func (u *UI) openFirewall(w http.ResponseWriter, r *http.Request, d *pageData) {
	if u.cfg.Firewall == nil {
		http.NotFound(w, r)
		return
	}
	v, err := firewallStatus(r.Context(), u.cfg.Firewall)
	if err != nil {
		u.fail(w, r, err, nil)
		return
	}
	syncFirewall(r.Context(), u.cfg.Firewall, v)
	if v.Err != "" {
		u.log.Warn("firewall", "err", v.Err)
	} else {
		u.log.Info("firewall ports opened", "kind", v.Status.Kind)
	}
	d.Firewall = v
	u.renderSettings(w, r, d)
}
