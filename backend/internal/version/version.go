// Package version exposes build metadata injected at compile time.
package version

// These values are overridden with -ldflags "-X ..." during release builds.
var (
	// Version is the semantic version of the panel.
	Version = "0.1.0-dev"
	// Commit is the git revision the binary was built from.
	Commit = "unknown"
	// Date is the build timestamp (RFC 3339).
	Date = "unknown"
)