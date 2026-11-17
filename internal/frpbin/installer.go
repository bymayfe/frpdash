// Package frpbin installs and runs the official frp binaries. The version is
// pinned and downloads are checked against SHA-256 sums compiled into
// frpdash, so a tampered release or mirror cannot slip a different binary in.
package frpbin

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Version is the pinned frp release.
const Version = "0.72.0"

// releaseSums are the SHA-256 sums of the v0.72.0 release archives
// (frp_sha256_checksums.txt), keyed by GOOS/GOARCH.
var releaseSums = map[string]string{
	"linux/amd64": "daf719b32ca2778770bb92955e231a07414d754378b9af328aa2642862479804",
	"linux/arm64": "02180b857480302fd934e9ddbd50fe2f73229f99062f88756abc6de9df5e95cf",
}

const defaultBaseURL = "https://github.com/fatedier/frp/releases/download"

// maxArchiveSize bounds the download; the real archive is ~15 MB.
const maxArchiveSize = 100 << 20

// Installer places frpc/frps under Dir/frp-<version>/.
type Installer struct {
	Dir     string
	baseURL string
	sums    map[string]string
	client  *http.Client
}

// NewInstaller builds an Installer for dir.
func NewInstaller(dir string) *Installer {
	return &Installer{Dir: dir, baseURL: defaultBaseURL, sums: releaseSums,
		client: &http.Client{Timeout: 5 * time.Minute}}
}

func platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Path returns where binary name ("frpc" or "frps") is installed.
func (i *Installer) Path(name string) string {
	return filepath.Join(i.Dir, "frp-"+Version, name)
}

// Ensure installs binary name if it is missing and returns its path.
func (i *Installer) Ensure(ctx context.Context, name string) (string, error) {
	if name != "frpc" && name != "frps" {
		return "", fmt.Errorf("unknown frp binary %q", name)
	}
	path := i.Path(name)
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		return path, nil
	}
	sum, ok := i.sums[platform()]
	if !ok {
		return "", fmt.Errorf("frp %s: no pinned checksum for %s", Version, platform())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // binaries dir must be traversable
		return "", err
	}

	archive, err := i.download(ctx, sum)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	member := fmt.Sprintf("frp_%s_%s_%s/%s", Version, runtime.GOOS, runtime.GOARCH, name)
	if err := extract(archive, member, path); err != nil {
		return "", err
	}
	return path, nil
}

// download fetches the release archive into a temp file and verifies it.
func (i *Installer) download(ctx context.Context, wantSum string) (string, error) {
	url := fmt.Sprintf("%s/v%s/frp_%s_%s_%s.tar.gz", i.baseURL, Version, Version, runtime.GOOS, runtime.GOARCH)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download frp: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download frp: %s", resp.Status)
	}

	f, err := os.CreateTemp(i.Dir, "frp-*.tar.gz")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchiveSize))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != wantSum {
		err = errors.New("frp archive checksum mismatch; refusing to install")
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// extract writes one archive member to dest atomically with mode 0755.
func extract(archive, member, dest string) error {
	f, err := os.Open(archive) //nolint:gosec // our own temp file
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s not found in frp archive", member)
		}
		if err != nil {
			return err
		}
		if hdr.Name != member || hdr.Typeflag != tar.TypeReg {
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(dest), ".frp-bin-*")
		if err != nil {
			return err
		}
		_, err = io.Copy(tmp, io.LimitReader(tr, maxArchiveSize))
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(tmp.Name(), 0o755) //nolint:gosec // executable
		}
		if err == nil {
			err = os.Rename(tmp.Name(), dest)
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
		return err
	}
}
