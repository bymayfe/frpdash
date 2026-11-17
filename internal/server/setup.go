// This part (setup) is the server's first run: until an admin account and the
// public address exist, the panel shows a setup page instead of the panel,
// guarded by a one-time code that only root on the VDS can read.

package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bymayfe/frpdash/internal/frp"
	"github.com/bymayfe/frpdash/internal/store"
)

// KeyPublicHost is the settings key of the address agents and frpc dial.
const KeyPublicHost = "public_host"

// CodeFile is the setup code's file name in the data directory.
const CodeFile = "setup-code"

// ErrSetupInvalid wraps mistakes the operator can fix on the setup page.
var ErrSetupInvalid = errors.New("invalid setup input")

func setupInvalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSetupInvalid, fmt.Sprintf(format, args...))
}

// SetupService decides whether setup is needed and completes it.
type SetupService struct {
	db       *store.DB
	accounts *AuthService
	dataDir  string
}

// NewSetupService builds a Service.
func NewSetupService(db *store.DB, accounts *AuthService, dataDir string) *SetupService {
	return &SetupService{db: db, accounts: accounts, dataDir: dataDir}
}

// PublicHost returns the stored public address, or "" before setup.
func (s *SetupService) PublicHost(ctx context.Context) (string, error) {
	var host string
	err := s.db.Repos().Settings.Get(ctx, KeyPublicHost, &host)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	return host, err
}

// SetPublicHost validates and stores the public address.
func (s *SetupService) SetPublicHost(ctx context.Context, host string) error {
	host = strings.TrimSpace(host)
	if err := ValidateHost(host); err != nil {
		return err
	}
	return s.db.Repos().Settings.Set(ctx, KeyPublicHost, host)
}

// Needed reports whether the admin account or the public address is missing.
func (s *SetupService) Needed(ctx context.Context) (bool, error) {
	has, err := s.accounts.HasUsers(ctx)
	if err != nil || !has {
		return true, err
	}
	host, err := s.PublicHost(ctx)
	return host == "", err
}

// HasAdmin reports whether an account exists, so the page can skip that part.
func (s *SetupService) HasAdmin(ctx context.Context) (bool, error) {
	return s.accounts.HasUsers(ctx)
}

// Code returns the setup code, creating it on first use. The file is 0600 in
// the root-owned data directory: reading it proves access to the VDS.
func (s *SetupService) Code() (string, error) {
	path := filepath.Join(s.dataDir, CodeFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // fixed path in our data dir
		if code := normalizeCode(string(b)); len(code) == codeLen {
			return format(code), nil
		}
	}
	raw := make([]byte, codeLen/2)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := hex.EncodeToString(raw)
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil { //nolint:gosec // dataDir is the operator's --data-dir flag
		return "", err
	}
	if err := frp.WriteFileAtomic(path, []byte(format(code)+"\n"), 0o600); err != nil {
		return "", err
	}
	return format(code), nil
}

// Input is what the setup page submits.
type Input struct {
	Code       string
	Username   string // ignored when an admin already exists
	Password   string
	Password2  string
	PublicHost string
}

// Complete checks the code, creates the admin (if none) and stores the public
// address, then removes the code.
func (s *SetupService) Complete(ctx context.Context, in Input) error {
	want, err := s.Code()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(normalizeCode(in.Code)), []byte(normalizeCode(want))) != 1 {
		return setupInvalidf("the setup code is wrong; it is in %s on the server", filepath.Join(s.dataDir, CodeFile))
	}
	host := strings.TrimSpace(in.PublicHost)
	if err := ValidateHost(host); err != nil {
		return err
	}
	hasAdmin, err := s.accounts.HasUsers(ctx)
	if err != nil {
		return err
	}
	if !hasAdmin {
		if err := ValidateUsername(NormalizeUsername(in.Username)); err != nil {
			return setupInvalidf("%s", err.Error())
		}
		if err := ValidatePassword(in.Password); err != nil {
			return setupInvalidf("%s", err.Error())
		}
		if in.Password != in.Password2 {
			return setupInvalidf("the passwords do not match")
		}
		if _, err := s.accounts.CreateUser(ctx, in.Username, in.Password); err != nil {
			return err
		}
	}
	if err := s.db.Repos().Settings.Set(ctx, KeyPublicHost, host); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dataDir, CodeFile)); err != nil && !errors.Is(err, os.ErrNotExist) { //nolint:gosec // dataDir is the operator's --data-dir flag
		return err
	}
	return nil
}

const codeLen = 24 // hex characters

func normalizeCode(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "", "\n", "", "\t", "").Replace(s))
}

// format groups the code in fours so it is easy to read and type.
func format(code string) string {
	var parts []string
	for i := 0; i < len(code); i += 4 {
		parts = append(parts, code[i:min(i+4, len(code))])
	}
	return strings.Join(parts, "-")
}

var hostnameRe = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// ValidateHost accepts an IP address or a DNS name, nothing else: the value
// ends up in pairing codes, the install command and frpc configs.
func ValidateHost(host string) error {
	switch {
	case host == "":
		return setupInvalidf("enter the public address of this server")
	case net.ParseIP(host) != nil:
		if ip := net.ParseIP(host); ip.IsLoopback() || ip.IsUnspecified() {
			return setupInvalidf("%s is not reachable from other machines", host)
		}
		return nil
	case len(host) <= 253 && hostnameRe.MatchString(host):
		// Names are not resolved here. "localhost" passes on purpose: it is
		// what a single-machine test (e2e) uses.
		return nil
	default:
		return setupInvalidf("%q is not an IP address or host name", host)
	}
}

// Address is a candidate public address found on this machine.
type Address struct {
	IP      string
	Private bool // 10.x, 192.168.x, fc00::/7 ...: behind NAT, likely not what clients dial
}

// Addresses lists this machine's non-loopback unicast addresses, public ones
// and IPv4 first.
func Addresses() []Address {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []Address
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || !n.IP.IsGlobalUnicast() {
			continue
		}
		out = append(out, Address{IP: n.IP.String(), Private: n.IP.IsPrivate()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Private != out[j].Private {
			return !out[i].Private
		}
		return net.ParseIP(out[i].IP).To4() != nil && net.ParseIP(out[j].IP).To4() == nil
	})
	return out
}
