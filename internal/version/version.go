// Package version holds the version information stamped into the binary at build time.
package version

// Version is the bare release version, such as "1.2.3". The Makefile sets it with
// -ldflags "-X github.com/stuffam/drawbridge/internal/version.Version=1.2.3".
var Version = "0.0.0-dev"

// Commit is the short git commit the binary was built from.
var Commit = "unknown"

// String returns the version as people see it, such as "v1.2.3 (commit abc1234)".
func String() string {
	return "v" + Version + " (commit " + Commit + ")"
}
