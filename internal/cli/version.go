package cli

// Build information injected at link time via -ldflags.

// These are overridden by the Makefile:
//
//	-X github.com/bymayfe/frpdash/internal/cli.Version=...
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// versionString returns a one-line, human-readable version description.
func versionString() string {
	return "frpdash " + Version + " (commit " + Commit + ", built " + Date + ")"
}
