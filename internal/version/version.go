// Package version holds build information injected at link time via -ldflags.
package version

// These are overridden by the Makefile:
//
//	-X github.com/bymayfe/frpdash/internal/version.Version=...
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a one-line, human-readable version description.
func String() string {
	return "frpdash " + Version + " (commit " + Commit + ", built " + Date + ")"
}
