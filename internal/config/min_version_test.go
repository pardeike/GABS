package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/version"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

const minVersionGame = `"games":{"g":{"id":"g","name":"g","launchMode":"DirectPath","target":"/bin/echo"}}`

// TestMinGabsVersionRejectsTooOldBinary is the forward-protection half of the
// version-skew hazard: a config author can declare the minimum GABS that
// understands their config, and a binary below it refuses to load rather than
// silently ignoring fields it does not know.
func TestMinGabsVersionRejectsTooOldBinary(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "1.0.8"

	dir := writeConfig(t, `{"version":"1.0","minGabsVersion":"1.1.0",`+minVersionGame+`}`)

	_, err := LoadGamesConfigFromDir(dir)
	if err == nil {
		t.Fatal("a binary older than minGabsVersion must refuse the config")
	}
	msg := err.Error()
	for _, want := range []string{"1.1.0", "1.0.8", "minGabsVersion"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name %q so the fix is obvious, got: %s", want, msg)
		}
	}
}

// TestMinGabsVersionAcceptsNewEnoughBinary keeps the satisfied case silent.
func TestMinGabsVersionAcceptsNewEnoughBinary(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })

	for _, running := range []string{"1.1.0", "1.1.5", "2.0.0"} {
		t.Run(running, func(t *testing.T) {
			version.Version = running
			dir := writeConfig(t, `{"version":"1.0","minGabsVersion":"1.1.0",`+minVersionGame+`}`)
			cfg, err := LoadGamesConfigFromDir(dir)
			if err != nil {
				t.Fatalf("running %s must satisfy minGabsVersion 1.1.0: %v", running, err)
			}
			for _, w := range cfg.Warnings {
				if strings.Contains(w.String(), "minGabsVersion") {
					t.Errorf("a satisfied requirement must not warn, got %q", w.Message)
				}
			}
		})
	}
}

// TestMinGabsVersionOnDevBuildWarnsButLoads pins the dev-build escape: refusing
// to load a config because the binary is stamped "dev" would be worse than the
// skew the check exists to catch.
func TestMinGabsVersionOnDevBuildWarnsButLoads(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "dev"

	dir := writeConfig(t, `{"version":"1.0","minGabsVersion":"1.1.0",`+minVersionGame+`}`)

	cfg, err := LoadGamesConfigFromDir(dir)
	if err != nil {
		t.Fatalf("a dev build must still load the config: %v", err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w.String(), "minGabsVersion") {
			found = true
		}
	}
	if !found {
		t.Error("a dev build must warn that the requirement could not be checked")
	}
}

// TestMinGabsVersionMalformedIsAConfigError keeps an unusable declaration
// deterministic rather than silently unenforced.
func TestMinGabsVersionMalformedIsAConfigError(t *testing.T) {
	dir := writeConfig(t, `{"version":"1.0","minGabsVersion":"not-a-version",`+minVersionGame+`}`)

	if _, err := LoadGamesConfigFromDir(dir); err == nil {
		t.Fatal("a malformed minGabsVersion must be a config error")
	}
}

// TestMinGabsVersionAbsentIsUnchanged protects the compatibility promise: a
// config that never declares the field behaves exactly as before.
func TestMinGabsVersionAbsentIsUnchanged(t *testing.T) {
	dir := writeConfig(t, `{"version":"1.0",`+minVersionGame+`}`)

	cfg, err := LoadGamesConfigFromDir(dir)
	if err != nil {
		t.Fatalf("a config without minGabsVersion must load: %v", err)
	}
	if cfg.MinGabsVersion != "" {
		t.Errorf("MinGabsVersion = %q, want empty", cfg.MinGabsVersion)
	}
	for _, w := range cfg.Warnings {
		if strings.Contains(w.String(), "minGabsVersion") {
			t.Errorf("absent field must not warn, got %q", w.Message)
		}
	}
}
