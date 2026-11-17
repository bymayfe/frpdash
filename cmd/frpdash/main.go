// Command frpdash is a single binary with server, agent and connect modes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/bymayfe/frpdash/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	code := cli.NewApp(cli.IO{
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		Getenv:       os.Getenv,
		IsTerminal:   term.IsTerminal(fd),
		ReadPassword: func() ([]byte, error) { return term.ReadPassword(fd) },
	}).Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
