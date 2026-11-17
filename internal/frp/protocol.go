// This part (protocol) defines what the server and agent exchange: the pairing
// code, the pairing request, the WebSocket messages of the desired-state
// loop, and TLS certificate pinning.

package frp

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// HTTP paths served by the agent listener.
const (
	PathPair    = "/agent/v1/pair"
	PathConnect = "/agent/v1/connect"
	PathInstall = "/install/" // + token: the install script; + token/frpdash-linux-<arch>: the binary
)

// InstallCommand is the one line "Add Client" shows for a client machine. curl
// trusts the self-signed certificate only by its public key pin.
func InstallCommand(addr, publicKeyPin, token string) string {
	return "curl -fsSL -k --pinnedpubkey 'sha256//" + publicKeyPin + "' 'https://" + addr + PathInstall + token + "' | sudo sh"
}

// PairingCode is what "Add Client" shows; `frpdash connect <code>` decodes
// it. It carries where to connect, which certificate to trust and the
// one-time token.
type PairingCode struct {
	Addr        string `json:"a"` // host:port of the agent listener
	Fingerprint string `json:"f"` // hex SHA-256 of the server certificate
	Token       string `json:"t"`
}

// Encode returns the code as URL-safe base64.
func (c PairingCode) Encode() string {
	b, _ := json.Marshal(c) // plain struct of strings: cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodePairingCode parses and sanity-checks a code.
func DecodePairingCode(s string) (PairingCode, error) {
	var c PairingCode
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return c, errors.New("pairing code is not valid base64")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, errors.New("pairing code is malformed")
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil || len(c.Fingerprint) != 64 || c.Token == "" {
		return c, errors.New("pairing code is incomplete")
	}
	return c, nil
}

// PairRequest is POSTed to PathPair.
type PairRequest struct {
	Token string `json:"token"`
}

// PairResponse returns the agent's permanent credentials.
type PairResponse struct {
	ClientID   int64  `json:"clientId"`
	ClientName string `json:"clientName"`
	Secret     string `json:"secret"`
}

// AuthHeader builds the Authorization header an agent sends on connect.
func AuthHeader(clientID int64, secret string) string {
	return "Bearer " + strconv.FormatInt(clientID, 10) + "." + secret
}

// ParseAuthHeader is the inverse of AuthHeader.
func ParseAuthHeader(h string) (int64, string, error) {
	tok, ok := strings.CutPrefix(h, "Bearer ")
	id, secret, ok2 := strings.Cut(tok, ".")
	n, err := strconv.ParseInt(id, 10, 64)
	if !ok || !ok2 || err != nil || secret == "" {
		return 0, "", errors.New("missing or malformed agent credentials")
	}
	return n, secret, nil
}

// Message types on the WebSocket.
const (
	TypeHello   = "hello"   // agent -> server, once after connect
	TypeDesired = "desired" // server -> agent
	TypeAck     = "ack"     // agent -> server, after applying a desired state
)

// Message is the WebSocket envelope; exactly one payload is set.
type Message struct {
	Type    string   `json:"type"`
	Hello   *Hello   `json:"hello,omitempty"`
	Desired *Desired `json:"desired,omitempty"`
	Ack     *Ack     `json:"ack,omitempty"`
}

// Hello reports what the agent currently runs.
type Hello struct {
	AppliedRev int64  `json:"appliedRev"`
	Version    string `json:"version"`
}

// Desired is the config the agent should run.
type Desired struct {
	Rev    int64        `json:"rev"`
	Config ClientConfig `json:"config"`
}

// Ack reports the result of applying a revision; Error is empty on success.
type Ack struct {
	Rev   int64  `json:"rev"`
	Error string `json:"error,omitempty"`
}

// Fingerprint returns the hex SHA-256 of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// HashSecret returns the hex SHA-256 of a high-entropy secret (tokens,
// agent secrets). It is not for passwords.
func HashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// EqualHashes compares two hex hashes in constant time.
func EqualHashes(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// PinnedTLSConfig trusts exactly one self-signed server certificate,
// identified by its fingerprint, instead of a CA chain.
func PinnedTLSConfig(fingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // replaced by the pin check below
		// VerifyConnection (unlike VerifyPeerCertificate) also runs on resumed
		// sessions, so the pin cannot be skipped.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			if got := Fingerprint(cs.PeerCertificates[0].Raw); !EqualHashes(got, strings.ToLower(fingerprint)) {
				return fmt.Errorf("server certificate fingerprint %s does not match pinned %s", got, fingerprint)
			}
			return nil
		},
	}
}
