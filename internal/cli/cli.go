// Package cli wires frpdash subcommands. App dispatches to Command values;
// each subcommand is its own type in this package.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// errUsage signals bad arguments; the message has already been written.
var errUsage = errors.New("usage error")

// IO is the process environment a command runs in. main fills it from the
// real process; tests substitute fakes.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// IsTerminal reports whether Stdin is an interactive terminal.
	IsTerminal bool
	// ReadPassword reads one line from the terminal without echo.
	ReadPassword func() ([]byte, error)
}

// Command is one frpdash subcommand.
type Command interface {
	Name() string
	Summary() string
	Run(ctx context.Context, args []string) error
}

// App routes arguments to commands.
type App struct {
	io       IO
	commands []Command
}

// NewApp builds the frpdash CLI.
func NewApp(sys IO) *App {
	p := &Prompter{io: sys}
	return &App{
		io: sys,
		commands: []Command{
			&ServerCommand{io: sys, serve: runServer},
			&PasswdCommand{io: sys, prompt: p},
			&ConnectCommand{io: sys},
			&AgentCommand{io: sys},
			&VersionCommand{io: sys},
		},
	}
}

// Run executes the subcommand in args (program name excluded) and returns a
// process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage(a.io.Stderr)
		return ExitUsage
	}
	switch args[0] {
	case "-h", "--help", "help":
		a.usage(a.io.Stdout)
		return ExitOK
	}

	for _, c := range a.commands {
		if c.Name() != args[0] {
			continue
		}
		err := c.Run(ctx, args[1:])
		switch {
		case err == nil, errors.Is(err, flag.ErrHelp):
			return ExitOK
		case errors.Is(err, errUsage):
			return ExitUsage
		default:
			fmt.Fprintf(a.io.Stderr, "frpdash %s: %v\n", c.Name(), err)
			return ExitError
		}
	}

	fmt.Fprintf(a.io.Stderr, "frpdash: unknown command %q\n\n", args[0])
	a.usage(a.io.Stderr)
	return ExitUsage
}

func (a *App) usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: frpdash <command> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, c := range a.commands {
		fmt.Fprintf(w, "  %-9s %s\n", c.Name(), c.Summary())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, `Run "frpdash <command> -h" for command flags.`)
}

// parseFlags parses args into fs, mapping non-help errors to errUsage.
func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errUsage
	}
	return nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("frpdash "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
