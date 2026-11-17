package agentapi

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"strings"
	"text/template"

	"github.com/bymayfe/frpdash/internal/protocol"
	"github.com/bymayfe/frpdash/internal/server/fleet"
)

// InstallConfig enables the one-line client installer: a shell script and the
// frpdash binary, served only for a still valid pairing token.
type InstallConfig struct {
	Addr         string // host:port agents dial
	Fingerprint  string // hex SHA-256 of the certificate, for the pairing code
	PublicKeyPin string // base64 SHA-256 of the public key, for curl
	// Binary returns the path of the frpdash build for linux/<arch>, or
	// os.ErrNotExist when this server has none.
	Binary func(arch string) (string, error)
}

// EnableInstall turns on the install endpoints. Call it before Handler.
func (h *Hub) EnableInstall(cfg InstallConfig) { h.install = &cfg }

// Architectures the install script can fetch.
var installArches = map[string]bool{"amd64": true, "arm64": true}

func (h *Hub) serveInstallScript(w http.ResponseWriter, r *http.Request) {
	token, ok := h.installToken(w, r)
	if !ok {
		return
	}
	cfg := h.install
	var buf bytes.Buffer
	err := installScript.Execute(&buf, map[string]string{
		"Base": "https://" + cfg.Addr + protocol.PathInstall + token,
		"Pin":  cfg.PublicKeyPin,
		"Code": protocol.PairingCode{Addr: cfg.Addr, Fingerprint: cfg.Fingerprint, Token: token}.Encode(),
	})
	if err != nil {
		h.log.Error("install script", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.log.Info("install script fetched", "remote", r.RemoteAddr)
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

func (h *Hub) serveInstallBinary(w http.ResponseWriter, r *http.Request) {
	arch, ok := strings.CutPrefix(r.PathValue("file"), "frpdash-linux-")
	if !ok || !installArches[arch] {
		http.NotFound(w, r)
		return
	}
	if _, ok := h.installToken(w, r); !ok {
		return
	}
	path, err := h.install.Binary(arch)
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "this server has no frpdash build for linux/"+arch, http.StatusNotFound)
		return
	}
	if err != nil {
		h.log.Error("install binary", "arch", arch, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	f, err := os.Open(path) //nolint:gosec // path comes from the server, not the request
	if err != nil {
		h.log.Error("install binary", "arch", arch, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.log.Info("install binary fetched", "arch", arch, "remote", r.RemoteAddr)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// installToken checks the token in the path; it writes the error response
// and returns false when the installer is off or the token is not valid.
func (h *Hub) installToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := r.PathValue("token")
	if h.install == nil || !isHexToken(token) {
		http.NotFound(w, r)
		return "", false
	}
	_, err := h.fleet.CheckPairingToken(r.Context(), token)
	if errors.Is(err, fleet.ErrPairingFailed) {
		h.log.Warn("install rejected", "remote", r.RemoteAddr)
		http.Error(w, "pairing code is invalid, expired or already used; create a new one in the panel", http.StatusForbidden)
		return "", false
	}
	if err != nil {
		h.log.Error("install token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return "", false
	}
	return token, true
}

// isHexToken accepts exactly what CreatePairingToken issues, so nothing else
// can reach the script.
func isHexToken(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// shellQuote wraps s in single quotes for POSIX sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// installScript runs as `curl ... | sudo sh`. Everything sits in main, called
// on the last line, so a truncated download runs nothing. Arguments after
// `sh -s --` go to `frpdash connect` (e.g. --no-install).
var installScript = template.Must(template.New("install").Funcs(template.FuncMap{"q": shellQuote}).Parse(`#!/bin/sh
# frpdash agent installer. Downloads frpdash from this server, pairs it and
# installs the frpdash-agent systemd service.
set -eu

main() {
	case "$(uname -s)" in
	Linux) ;;
	*) echo "frpdash: only Linux is supported" >&2; exit 1 ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) echo "frpdash: unsupported architecture $(uname -m)" >&2; exit 1 ;;
	esac
	command -v curl >/dev/null || { echo "frpdash: curl is required" >&2; exit 1; }

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	echo "frpdash: downloading frpdash for linux/$arch"
	# -k: the certificate is self-signed; --pinnedpubkey is what verifies it.
	curl -fsSL -k --pinnedpubkey {{q (print "sha256//" .Pin)}} -o "$tmp/frpdash" {{q .Base}}"/frpdash-linux-$arch"
	chmod 0755 "$tmp/frpdash"
	"$tmp/frpdash" connect "$@" {{q .Code}}
}

main "$@"
`))
