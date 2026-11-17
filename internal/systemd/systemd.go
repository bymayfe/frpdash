// Package systemd installs frpdash as a systemd service.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstalledBinary is where frpdash copies itself so the unit does not depend
// on wherever the operator downloaded it.
const InstalledBinary = "/usr/local/bin/frpdash"

const unitDir = "/etc/systemd/system"

// Unit is a simple long-running service.
type Unit struct {
	Name        string // e.g. "frpdash-agent"
	Description string
	Args        []string // arguments after the binary
}

// Render returns the unit file text.
func (u Unit) Render(binary string) string {
	args := make([]string, 0, len(u.Args)+1)
	for _, a := range append([]string{binary}, u.Args...) {
		args = append(args, quote(a))
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
`, u.Description, strings.Join(args, " "))
}

// quote escapes an ExecStart argument (systemd uses C-style quoting).
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(s) + `"`
}

// Install copies the running binary to InstalledBinary, writes the unit and
// enables it. It needs root.
func (u Unit) Install(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("installing a systemd service needs root (use sudo), or pass --no-install")
	}
	if err := installSelf(); err != nil {
		return fmt.Errorf("copy binary: %w", err)
	}
	path := filepath.Join(unitDir, u.Name+".service")
	if err := os.WriteFile(path, []byte(u.Render(InstalledBinary)), 0o644); err != nil { //nolint:gosec // unit files are world-readable
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", u.Name}, {"restart", u.Name}} {
		if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed arguments
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}
	return nil
}

func installSelf() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if self == InstalledBinary {
		return nil
	}
	src, err := os.Open(self) //nolint:gosec // our own executable
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := InstalledBinary + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) //nolint:gosec // executable
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, InstalledBinary)
}
