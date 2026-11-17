package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"

	"github.com/bymayfe/frpdash/internal/agent"
	"github.com/bymayfe/frpdash/internal/server"
	"github.com/bymayfe/frpdash/internal/server/auth"
	"github.com/bymayfe/frpdash/internal/server/setup"
	"github.com/bymayfe/frpdash/internal/server/store"
	"github.com/bymayfe/frpdash/internal/systemd"
	"github.com/bymayfe/frpdash/internal/version"
)

const (
	defaultDataDir      = "/var/lib/frpdash"
	defaultAgentConfig  = "/etc/frpdash/agent.json"
	defaultAgentDataDir = "/var/lib/frpdash-agent"
	dbFileName          = "frpdash.db"
	defaultAdminUser    = "admin"
	envAdminUser        = "FRPDASH_ADMIN_USER"
	envAdminPassword    = "FRPDASH_ADMIN_PASSWORD" //nolint:gosec // variable name, not a credential
)

// openAuth opens the database under dataDir and builds the account service.
// The caller closes the returned DB.
func openAuth(ctx context.Context, dataDir string) (*store.DB, *auth.Service, error) {
	db, err := store.Open(ctx, filepath.Join(dataDir, dbFileName))
	if err != nil {
		return nil, nil, err
	}
	svc, err := auth.NewService(db.Users(), db.Repos().Sessions, auth.BcryptHasher{Cost: auth.DefaultBcryptCost})
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, svc, nil
}

// ServerCommand runs the panel (VDS side).
type ServerCommand struct {
	io IO
	// serve runs the server; tests replace it.
	serve func(ctx context.Context, cfg server.Config, db *store.DB, accounts *auth.Service, log *slog.Logger) error
}

// Name implements Command.
func (c *ServerCommand) Name() string { return "server" }

// Summary implements Command.
func (c *ServerCommand) Summary() string {
	return "Server (VDS, frps side): run the panel and manage frps"
}

// Run implements Command.
func (c *ServerCommand) Run(ctx context.Context, args []string) error {
	var (
		cfg     server.Config
		install bool
	)
	fs := newFlagSet(c.Name(), c.io.Stderr)
	fs.StringVar(&cfg.DataDir, "data-dir", defaultDataDir, "directory for the database, certificates and frps binary")
	fs.StringVar(&cfg.UIAddr, "ui-addr", "127.0.0.1:8090", "panel listen address (loopback only)")
	fs.StringVar(&cfg.AgentAddr, "agent-addr", "0.0.0.0:7443", "agent WebSocket (TLS) listen address")
	fs.StringVar(&cfg.PublicHost, "public-host", "",
		"VDS IP or hostname that clients connect to (optional: asked in the browser on first run)")
	fs.BoolVar(&install, "install-service", false, "install and start the frpdash-server systemd service, then exit")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.io.Stderr, "frpdash server: unexpected arguments: %v\n", fs.Args())
		return errUsage
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	db, accounts, err := openAuth(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := c.adminFromEnv(ctx, accounts); err != nil {
		return err
	}
	if install {
		return c.install(ctx, cfg, setup.NewService(db, accounts, cfg.DataDir))
	}
	return c.serve(ctx, cfg, db, accounts, newLogger(c.io))
}

// install sets up the systemd service. The public host goes to the database,
// not the unit, so a later change in the panel is not overridden on restart.
func (c *ServerCommand) install(ctx context.Context, cfg server.Config, st *setup.Service) error {
	if cfg.PublicHost != "" {
		if err := st.SetPublicHost(ctx, cfg.PublicHost); err != nil {
			return err
		}
	}
	// Create the setup code before the service starts, so the service reads
	// this same code instead of racing to create its own.
	var code string
	needed, err := st.Needed(ctx)
	if err != nil {
		return err
	}
	if needed {
		if code, err = st.Code(); err != nil {
			return err
		}
	}
	unit := systemd.Unit{Name: "frpdash-server", Description: "frpdash panel and frps",
		Args: []string{"server", "--data-dir", cfg.DataDir, "--ui-addr", cfg.UIAddr, "--agent-addr", cfg.AgentAddr}}
	if err := unit.Install(ctx); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.UIAddr)
	if err != nil {
		return fmt.Errorf("ui-addr: %w", err)
	}
	fmt.Fprintln(c.io.Stdout, "Installed and started frpdash-server.")
	fmt.Fprintln(c.io.Stdout)
	fmt.Fprintln(c.io.Stdout, "Open the panel from your own computer:")
	fmt.Fprintf(c.io.Stdout, "  ssh -L %s:%s <vds>     then http://localhost:%s\n", port, cfg.UIAddr, port)
	if code == "" {
		return nil
	}
	fmt.Fprintln(c.io.Stdout)
	fmt.Fprintln(c.io.Stdout, "Finish setup in the browser with this one-time code:")
	fmt.Fprintf(c.io.Stdout, "  %s\n", code)
	fmt.Fprintf(c.io.Stdout, "(also in %s and in: journalctl -u frpdash-server)\n", filepath.Join(cfg.DataDir, setup.CodeFile))
	return nil
}

// adminFromEnv creates the first account from FRPDASH_ADMIN_USER /
// FRPDASH_ADMIN_PASSWORD, for unattended installs. Otherwise the setup page
// in the browser asks for it.
func (c *ServerCommand) adminFromEnv(ctx context.Context, accounts *auth.Service) error {
	pw := c.io.Getenv(envAdminPassword)
	if pw == "" {
		return nil
	}
	has, err := accounts.HasUsers(ctx)
	if err != nil || has {
		return err
	}
	user := c.io.Getenv(envAdminUser)
	if user == "" {
		user = defaultAdminUser
	}
	u, err := accounts.CreateUser(ctx, user, pw)
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	fmt.Fprintf(c.io.Stdout, "Created admin user %q.\n", u.Username)
	return nil
}

func runServer(ctx context.Context, cfg server.Config, db *store.DB, accounts *auth.Service, log *slog.Logger) error {
	srv, err := server.New(cfg, db, accounts, log)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

func newLogger(sys IO) *slog.Logger {
	return slog.New(slog.NewTextHandler(sys.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// PasswdCommand sets a panel user's password; it is the recovery path for a
// forgotten password, run over SSH on the VDS.
type PasswdCommand struct {
	io     IO
	prompt *Prompter

	DataDir string
}

// Name implements Command.
func (c *PasswdCommand) Name() string { return "passwd" }

// Summary implements Command.
func (c *PasswdCommand) Summary() string {
	return "Server (VDS, frps side): set a panel user's password"
}

// Run implements Command.
func (c *PasswdCommand) Run(ctx context.Context, args []string) error {
	fs := newFlagSet(c.Name(), c.io.Stderr)
	fs.StringVar(&c.DataDir, "data-dir", defaultDataDir, "directory containing the panel database")
	fs.Usage = func() {
		fmt.Fprintln(c.io.Stderr, "Usage: frpdash passwd [flags] <username>")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}

	db, accounts, err := openAuth(ctx, c.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	pw, err := c.prompt.NewPassword()
	if err != nil {
		return err
	}
	if err := accounts.SetPassword(ctx, fs.Arg(0), pw); err != nil {
		return err
	}
	fmt.Fprintf(c.io.Stdout, "Password updated for %q.\n", auth.NormalizeUsername(fs.Arg(0)))
	return nil
}

// AgentCommand runs the agent (client side), normally as the
// frpdash-agent systemd service.
type AgentCommand struct{ io IO }

// Name implements Command.
func (c *AgentCommand) Name() string { return "agent" }

// Summary implements Command.
func (c *AgentCommand) Summary() string { return "Client (frpc side): run the agent that manages frpc" }

// Run implements Command.
func (c *AgentCommand) Run(ctx context.Context, args []string) error {
	var credsPath, dataDir string
	fs := newFlagSet(c.Name(), c.io.Stderr)
	fs.StringVar(&credsPath, "config", defaultAgentConfig, "agent credentials written by `frpdash connect`")
	fs.StringVar(&dataDir, "data-dir", defaultAgentDataDir, "directory for the frpc binary and generated config")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.io.Stderr, "frpdash agent: unexpected arguments: %v\n", fs.Args())
		return errUsage
	}
	creds, err := agent.LoadCredentials(credsPath)
	if err != nil {
		return err
	}
	return agent.New(creds, dataDir, newLogger(c.io)).Run(ctx)
}

// ConnectCommand pairs this machine with a server and installs the agent
// service.
type ConnectCommand struct{ io IO }

// Name implements Command.
func (c *ConnectCommand) Name() string { return "connect" }

// Summary implements Command.
func (c *ConnectCommand) Summary() string {
	return "Client (frpc side): pair this machine with the server and install the agent"
}

// Run implements Command.
func (c *ConnectCommand) Run(ctx context.Context, args []string) error {
	var (
		credsPath, dataDir string
		noInstall          bool
	)
	fs := newFlagSet(c.Name(), c.io.Stderr)
	fs.StringVar(&credsPath, "config", defaultAgentConfig, "where to write the agent credentials")
	fs.StringVar(&dataDir, "data-dir", defaultAgentDataDir, "agent data directory (used by the installed service)")
	fs.BoolVar(&noInstall, "no-install", false, "only pair; do not install the systemd service")
	fs.Usage = func() {
		fmt.Fprintln(c.io.Stderr, "Usage: frpdash connect [flags] <pairing-code>")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errUsage
	}

	creds, err := agent.Pair(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	if err := creds.Save(credsPath); err != nil {
		return err
	}
	fmt.Fprintf(c.io.Stdout, "Paired as %q with %s.\n", creds.ClientName, creds.Server)
	if noInstall {
		fmt.Fprintf(c.io.Stdout, "Start the agent with: frpdash agent --config %s\n", credsPath)
		return nil
	}
	unit := systemd.Unit{Name: "frpdash-agent", Description: "frpdash agent (frpc)",
		Args: []string{"agent", "--config", credsPath, "--data-dir", dataDir}}
	if err := unit.Install(ctx); err != nil {
		return err
	}
	fmt.Fprintln(c.io.Stdout, "Installed and started frpdash-agent. It shows as online in the panel shortly.")
	return nil
}

// VersionCommand prints build information.
type VersionCommand struct{ io IO }

// Name implements Command.
func (c *VersionCommand) Name() string { return "version" }

// Summary implements Command.
func (c *VersionCommand) Summary() string { return "Print version information" }

// Run implements Command.
func (c *VersionCommand) Run(_ context.Context, args []string) error {
	if err := parseFlags(newFlagSet(c.Name(), c.io.Stderr), args); err != nil {
		return err
	}
	fmt.Fprintln(c.io.Stdout, version.String())
	return nil
}
