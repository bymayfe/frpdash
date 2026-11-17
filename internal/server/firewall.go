// This part (firewall) opens the ports frpdash needs in the host firewall: ufw or
// firewalld, or ufw installed fresh on Debian/Ubuntu. It always allows SSH
// before enabling a firewall, so the operator cannot lock themselves out.

package server

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Kind is the firewall in charge on this host.
type Kind string

// Supported firewalls.
const (
	None      Kind = "none"
	UFW       Kind = "ufw"
	Firewalld Kind = "firewalld"
)

// Rule allows a port or an inclusive port range for one protocol.
type Rule struct {
	Port    int    `json:"port"`
	EndPort int    `json:"endPort,omitempty"` // 0: a single port
	Proto   string `json:"proto"`             // "tcp" or "udp"
	Purpose string `json:"purpose,omitempty"` // shown to the operator
}

func (r Rule) String() string {
	if r.EndPort > r.Port {
		return fmt.Sprintf("%d-%d/%s", r.Port, r.EndPort, r.Proto)
	}
	return fmt.Sprintf("%d/%s", r.Port, r.Proto)
}

// Status is what Detect found.
type Status struct {
	Kind        Kind
	Active      bool
	Installable bool  // no firewall, but ufw can be installed with apt-get
	SSHPorts    []int // allowed before a firewall is enabled
}

// Action describes what Apply would do, for the operator to confirm.
func (s Status) Action() string {
	ssh := joinPorts(s.SSHPorts)
	switch {
	case s.Kind == UFW && s.Active:
		return "Open the required ports in ufw"
	case s.Kind == UFW:
		return "Enable ufw: allow SSH (" + ssh + ") first, then open the required ports"
	case s.Kind == Firewalld && s.Active:
		return "Open the required ports in firewalld"
	case s.Installable:
		return "Install ufw, allow SSH (" + ssh + "), open the required ports and enable it"
	default:
		return ""
	}
}

// Supported reports whether Apply can do anything here.
func (s Status) Supported() bool { return s.Action() != "" }

// Step is one command Apply ran.
type Step struct {
	Command string
	Output  string
	Err     string
}

// Runner executes a command; tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// FirewallManager detects the firewall and applies rules. It needs root.
type FirewallManager struct {
	run      Runner
	lookPath func(string) (string, error)
	sshdConf string
}

// NewFirewallManager returns a Manager that runs real commands.
func NewFirewallManager() *FirewallManager {
	// Only root, i.e. the frpdash-server service, escapes its sandbox this
	// way. INVOCATION_ID alone is not enough: desktop sessions run user
	// services that set it too, and a non-root systemd-run would make polkit
	// ask the person at the desk for a password.
	_, err := exec.LookPath("systemd-run")
	underSystemd := os.Geteuid() == 0 && os.Getenv("INVOCATION_ID") != "" && err == nil
	return &FirewallManager{
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			name, args = command(underSystemd, name, args)
			cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed command names, numeric arguments
			cmd.Env = append(os.Environ(), commandEnv...)
			return cmd.CombinedOutput()
		},
		lookPath: exec.LookPath,
		sshdConf: "/etc/ssh/sshd_config",
	}
}

var commandEnv = []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}

// command wraps name/args in systemd-run when frpdash runs as a systemd
// service. The frpdash-server unit has ProtectSystem=full, so /etc and /usr
// are read-only to it and to its children: ufw could not save its rules and
// apt-get could not install ufw. A transient unit started by systemd-run
// runs outside that sandbox; --wait and --pipe keep output and exit status.
func command(underSystemd bool, name string, args []string) (string, []string) {
	if !underSystemd {
		return name, args
	}
	wrapped := []string{"--quiet", "--wait", "--pipe", "--collect", "--service-type=exec"}
	for _, e := range commandEnv {
		wrapped = append(wrapped, "--setenv="+e)
	}
	wrapped = append(wrapped, "--", name)
	return "systemd-run", append(wrapped, args...)
}

// NewWith returns a Manager with a fake runner and command lookup, for tests.
func NewWith(run Runner, lookPath func(string) (string, error), sshdConf string) *FirewallManager {
	return &FirewallManager{run: run, lookPath: lookPath, sshdConf: sshdConf}
}

func (m *FirewallManager) has(name string) bool {
	_, err := m.lookPath(name)
	return err == nil
}

// Detect finds the firewall in use and the SSH ports to keep open.
func (m *FirewallManager) Detect(ctx context.Context) Status {
	st := Status{Kind: None, SSHPorts: m.sshPorts(ctx)}
	switch {
	case m.has("ufw"):
		st.Kind = UFW
		out, _ := m.run(ctx, "ufw", "status")
		st.Active = strings.Contains(string(out), "Status: active")
	case m.has("firewall-cmd"):
		st.Kind = Firewalld
		out, _ := m.run(ctx, "firewall-cmd", "--state")
		st.Active = strings.TrimSpace(string(out)) == "running"
	default:
		st.Installable = m.has("apt-get")
	}
	return st
}

// sshPorts reads the ports sshd listens on (sshd -T, then sshd_config),
// defaulting to 22.
func (m *FirewallManager) sshPorts(ctx context.Context) []int {
	var ports []int
	if out, err := m.run(ctx, "sshd", "-T"); err == nil {
		ports = parsePorts(string(out))
	}
	if len(ports) == 0 {
		if b, err := os.ReadFile(m.sshdConf); err == nil {
			ports = parsePorts(string(b))
		}
	}
	if len(ports) == 0 {
		ports = []int{22}
	}
	return ports
}

func parsePorts(text string) []int {
	seen := map[int]bool{}
	var ports []int
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || !strings.EqualFold(f[0], "port") {
			continue
		}
		if p, err := strconv.Atoi(f[1]); err == nil && p > 0 && p < 65536 && !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}

// Apply opens rules in the detected firewall, installing and enabling ufw
// when needed. It stops at the first failing command; the steps so far are
// returned either way.
func (m *FirewallManager) Apply(ctx context.Context, st Status, rules []Rule) ([]Step, error) {
	if !st.Supported() {
		return nil, fmt.Errorf("no supported firewall: open the ports in your firewall or provider panel")
	}
	var cmds [][]string
	switch {
	case st.Kind == Firewalld:
		for _, r := range rules {
			cmds = append(cmds, []string{"firewall-cmd", "--permanent", "--add-port=" + firewalldPort(r)})
		}
		cmds = append(cmds, []string{"firewall-cmd", "--reload"})
	default: // ufw, possibly installed and enabled here
		if st.Kind == None {
			cmds = append(cmds, []string{"apt-get", "install", "-y", "ufw"})
		}
		if !st.Active {
			// SSH first: enabling ufw without it would cut the operator off.
			for _, p := range st.SSHPorts {
				cmds = append(cmds, []string{"ufw", "allow", strconv.Itoa(p) + "/tcp"})
			}
		}
		for _, r := range rules {
			cmds = append(cmds, []string{"ufw", "allow", ufwPort(r)})
		}
		if !st.Active {
			cmds = append(cmds, []string{"ufw", "--force", "enable"})
		}
	}

	return m.runAll(ctx, cmds)
}

// Remove closes rules that Apply opened earlier. It never touches the SSH
// ports and does nothing when the firewall is not active.
func (m *FirewallManager) Remove(ctx context.Context, st Status, rules []Rule) ([]Step, error) {
	if !st.Active || len(rules) == 0 {
		return nil, nil
	}
	ssh := map[string]bool{}
	for _, p := range st.SSHPorts {
		ssh[Rule{Port: p, Proto: "tcp"}.String()] = true
	}
	var cmds [][]string
	for _, r := range rules {
		if ssh[r.String()] {
			continue
		}
		switch st.Kind {
		case UFW:
			cmds = append(cmds, []string{"ufw", "delete", "allow", ufwPort(r)})
		case Firewalld:
			cmds = append(cmds, []string{"firewall-cmd", "--permanent", "--remove-port=" + firewalldPort(r)})
		}
	}
	if st.Kind == Firewalld && len(cmds) > 0 {
		cmds = append(cmds, []string{"firewall-cmd", "--reload"})
	}
	return m.runAll(ctx, cmds)
}

func (m *FirewallManager) runAll(ctx context.Context, cmds [][]string) ([]Step, error) {
	steps := make([]Step, 0, len(cmds))
	for _, c := range cmds {
		out, err := m.run(ctx, c[0], c[1:]...)
		s := Step{Command: strings.Join(c, " "), Output: strings.TrimSpace(string(out))}
		if err != nil {
			s.Err = err.Error()
			steps = append(steps, s)
			return steps, fmt.Errorf("%s: %w", s.Command, err)
		}
		steps = append(steps, s)
	}
	return steps, nil
}

func ufwPort(r Rule) string {
	if r.EndPort > r.Port {
		return fmt.Sprintf("%d:%d/%s", r.Port, r.EndPort, r.Proto)
	}
	return fmt.Sprintf("%d/%s", r.Port, r.Proto)
}

func firewalldPort(r Rule) string { return r.String() }

func joinPorts(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ", ")
}
