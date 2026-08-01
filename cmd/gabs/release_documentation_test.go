package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func readRepositoryFile(t *testing.T, root, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(data)
}

func TestReleaseCriticalDocumentation(t *testing.T) {
	root := repositoryRoot(t)
	readme := readRepositoryFile(t, root, "README.md")
	for _, want := range []string{
		"https://github.com/pardeike/GABS/releases/latest",
		"https://github.com/pardeike/GABS/issues",
		"[v1.1.0 Release Notes](docs/releases/v1.1.0.md)",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README is missing release-critical route %q", want)
		}
	}
	for _, broken := range []string{
		"](releases/latest)",
		"](issues)",
	} {
		if strings.Contains(readme, broken) {
			t.Errorf("README still contains blob-relative GitHub route %q", broken)
		}
	}

	configuration := readRepositoryFile(t, root, "docs/CONFIGURATION.md")
	for _, want := range []string{
		"`gabs version`",
		"`gabs games doctor <id> --show-last-good`",
		"cross-ID collision rules run when the config is",
		"Verifies the pinned workload PID",
		"Only when that PID is absent, stopped, or cannot be used safely",
	} {
		if !strings.Contains(configuration, want) {
			t.Errorf("CONFIGURATION.md is missing parser-accepted command %s", want)
		}
	}
	for _, rejected := range []string{
		"`gabs --version`",
		"`gabs games doctor --show-last-good <id>`",
		"Finds and stops processes with that name",
	} {
		if strings.Contains(configuration, rejected) {
			t.Errorf("CONFIGURATION.md still recommends rejected syntax %s", rejected)
		}
	}

	releaseNotes := readRepositoryFile(t, root, "docs/releases/v1.1.0.md")
	for _, want := range []string{
		"unknown MCP arguments",
		"timeout",
		"duplicate JSON members",
		"Game IDs are validated when the config is loaded",
		"factory/",
		"canonical Unicode composition",
		"pre-1.1.0",
		"silently ignore",
		"profiles",
		"launchInputs",
		"lifecycle",
	} {
		if !strings.Contains(releaseNotes, want) {
			t.Errorf("v1.1.0 release notes omit %q", want)
		}
	}

	workflow := readRepositoryFile(t, root, ".github/workflows/release-binaries.yml")
	for _, want := range []string{
		`NOTES_FILE="docs/releases/${RELEASE_TAG}.md"`,
		`args+=(--notes "$RELEASE_NOTES")`,
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release workflow does not publish checked-in notes (%s)", want)
		}
	}
}
