// Package version provides centralized version management for GABS.
// The version information can be injected at build time using -ldflags.
package version

import "fmt"

var (
	// Version is the main version string for GABS.
	// Can be overridden at build time: go build -ldflags "-X github.com/pardeike/gabs/internal/version.Version=v1.0.0"
	Version = "0.1.0"
	
	// BuildDate is when the binary was built.
	// Can be overridden at build time: go build -ldflags "-X github.com/pardeike/gabs/internal/version.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	BuildDate = "unknown"
	
	// Commit is the git commit hash this binary was built from.
	// Can be overridden at build time: go build -ldflags "-X github.com/pardeike/gabs/internal/version.Commit=$(git rev-parse HEAD)"
	Commit = "unknown"
)

// Get returns the version string.
func Get() string {
	return Version
}

// GetBuildDate returns the build date.
func GetBuildDate() string {
	return BuildDate
}

// GetCommit returns the git commit hash.
func GetCommit() string {
	return Commit
}

// GetFullVersionInfo returns a formatted string with all version information.
func GetFullVersionInfo() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}

// GetDetailedVersionInfo returns a formatted string with detailed version information.
func GetDetailedVersionInfo() string {
	return fmt.Sprintf("gabs %s (commit: %s, built: %s)", Version, Commit, BuildDate)
}