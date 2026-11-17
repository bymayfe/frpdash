package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/bymayfe/frpdash/internal/protocol"
)

const certValidity = 10 * 365 * 24 * time.Hour

// AgentCert is the agent listener's self-signed certificate. Agents trust it
// by fingerprint, so it never needs a CA or a domain.
type AgentCert struct {
	TLS         tls.Certificate
	Fingerprint string
	// PublicKeyPin is the base64 SHA-256 of the certificate's public key, the
	// form curl's --pinnedpubkey takes (after "sha256//").
	PublicKeyPin string
}

// LoadOrCreateAgentCert reads cert.pem/key.pem from dir, creating them for
// host on first run. The key file is 0600.
func LoadOrCreateAgentCert(dir, host string) (AgentCert, error) {
	certPath, keyPath := filepath.Join(dir, "agent-cert.pem"), filepath.Join(dir, "agent-key.pem")
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := createCert(certPath, keyPath, host); err != nil {
			return AgentCert{}, fmt.Errorf("create agent certificate: %w", err)
		}
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return AgentCert{}, fmt.Errorf("load agent certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return AgentCert{}, fmt.Errorf("parse agent certificate: %w", err)
	}
	return AgentCert{TLS: c, Fingerprint: protocol.Fingerprint(c.Certificate[0]), PublicKeyPin: PublicKeyPin(leaf)}, nil
}

// PublicKeyPin returns the base64 SHA-256 of a certificate's
// SubjectPublicKeyInfo.
func PublicKeyPin(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func createCert(certPath, keyPath, host string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "frpdash " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644) //nolint:gosec // public cert
}
