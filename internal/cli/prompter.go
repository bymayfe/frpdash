package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bymayfe/frpdash/internal/server/auth"
)

const passwordAttempts = 3

// Prompter asks the operator for input on the terminal.
type Prompter struct {
	io IO
}

// NewPassword asks for a password twice without echo and checks the policy.
func (p *Prompter) NewPassword() (string, error) {
	if !p.io.IsTerminal || p.io.ReadPassword == nil {
		return "", errors.New("a terminal is required to enter a password")
	}
	for i := 0; i < passwordAttempts; i++ {
		pw, err := p.secret(fmt.Sprintf("Password (min %d characters): ", auth.MinPasswordLen))
		if err != nil {
			return "", err
		}
		if err := auth.ValidatePassword(pw); err != nil {
			fmt.Fprintln(p.io.Stdout, err)
			continue
		}
		again, err := p.secret("Repeat password: ")
		if err != nil {
			return "", err
		}
		if again != pw {
			fmt.Fprintln(p.io.Stdout, "Passwords do not match.")
			continue
		}
		return pw, nil
	}
	return "", errors.New("too many failed attempts")
}

func (p *Prompter) secret(label string) (string, error) {
	fmt.Fprint(p.io.Stdout, label)
	b, err := p.io.ReadPassword()
	fmt.Fprintln(p.io.Stdout)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
