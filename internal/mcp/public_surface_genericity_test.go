package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestPublicSurfacesStayGeneric(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to locate test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))

	forbiddenTerms := []string{
		`\b` + "m" + `ods?\b`,
		`\b` + "m" + `odifications?\b`,
		"rim" + "world",
		"rim" + "bridge",
		"29" + "4100",
		"rim" + "worldwin64",
		"lude" + "on",
		"mine" + "craft",
	}
	forbidden := regexp.MustCompile(`(?i)` + strings.Join(forbiddenTerms, "|"))
	publicFiles := []string{
		"AGENTS.md",
		"README.md",
		"example-config.json",
		"skills/gabs-mcp/SKILL.md",
	}

	docEntries, err := os.ReadDir(filepath.Join(repoRoot, "docs"))
	if err != nil {
		t.Fatalf("failed to read docs directory: %v", err)
	}
	for _, entry := range docEntries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext == ".md" || ext == ".svg" {
			publicFiles = append(publicFiles, filepath.Join("docs", entry.Name()))
		}
	}

	for _, relativePath := range publicFiles {
		data, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
		if err != nil {
			t.Fatalf("failed to read %s: %v", relativePath, err)
		}
		if match := forbidden.Find(data); len(match) > 0 {
			t.Fatalf("%s contains setup-specific or legacy wording %q", relativePath, string(match))
		}
	}

	if match := forbidden.FindString(ServerInstructions); match != "" {
		t.Fatalf("ServerInstructions contain setup-specific or legacy wording %q", match)
	}
}
