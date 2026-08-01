package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// docs/TROUBLESHOOTING.md claims its bad-case table is the design/05 table
// verbatim. Enforce exact equality of the two "Bad-case map" tables so a future
// edit (or a genericity-motivated wording change) cannot silently create a
// divergence. If the wording must change, both rows change together.
func TestBadCaseTableMatchesDesign(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))

	design := extractBadCaseTable(t, filepath.Join(repoRoot, "design", "05-start-pipeline.md"))
	public := extractBadCaseTable(t, filepath.Join(repoRoot, "docs", "TROUBLESHOOTING.md"))
	if len(design) == 0 {
		t.Fatal("no rows extracted from the design bad-case table")
	}
	if !reflect.DeepEqual(design, public) {
		t.Fatalf("bad-case table drift between design/05 and docs/TROUBLESHOOTING:\n--- design ---\n%s\n--- public ---\n%s",
			strings.Join(design, "\n"), strings.Join(public, "\n"))
	}
}

// extractBadCaseTable returns the pipe-table rows under the "## Bad-case map"
// heading, up to the next heading.
func extractBadCaseTable(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows []string
	inSection := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "## Bad-case map") {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(line, "## ") {
			break
		}
		if inSection && strings.HasPrefix(line, "| ") {
			rows = append(rows, strings.TrimRight(line, " \t"))
		}
	}
	return rows
}
