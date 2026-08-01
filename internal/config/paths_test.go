package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfigPaths(t *testing.T) {
	t.Run("with custom base directory", func(t *testing.T) {
		customDir := "/tmp/custom-gabs"
		cp, err := NewConfigPaths(customDir)
		if err != nil {
			t.Fatalf("Failed to create ConfigPaths: %v", err)
		}

		if cp.GetBaseDir() != customDir {
			t.Errorf("Expected base dir %s, got %s", customDir, cp.GetBaseDir())
		}
	})

	t.Run("with empty base directory (uses default)", func(t *testing.T) {
		cp, err := NewConfigPaths("")
		if err != nil {
			t.Fatalf("Failed to create ConfigPaths: %v", err)
		}

		homeDir, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("Failed to get home directory: %v", err)
		}
		expectedDir := filepath.Join(homeDir, ".gabs")

		if cp.GetBaseDir() != expectedDir {
			t.Errorf("Expected base dir %s, got %s", expectedDir, cp.GetBaseDir())
		}
	})
}

func TestConfigPathsMethods(t *testing.T) {
	testBaseDir := "/tmp/test-gabs"
	cp, err := NewConfigPaths(testBaseDir)
	if err != nil {
		t.Fatalf("Failed to create ConfigPaths: %v", err)
	}

	t.Run("GetMainConfigPath", func(t *testing.T) {
		expected := filepath.Join(testBaseDir, "config.json")
		actual := cp.GetMainConfigPath()
		if actual != expected {
			t.Errorf("Expected main config path %s, got %s", expected, actual)
		}
	})

	t.Run("GetGameDir", func(t *testing.T) {
		gameID := "factory"
		expected := filepath.Join(testBaseDir, gameID)
		actual := cp.GetGameDir(gameID)
		if actual != expected {
			t.Errorf("Expected game dir %s, got %s", expected, actual)
		}
	})

	t.Run("GetBridgeConfigPath", func(t *testing.T) {
		gameID := "factory"
		expected := filepath.Join(testBaseDir, gameID, "bridge.json")
		actual := cp.GetBridgeConfigPath(gameID)
		if actual != expected {
			t.Errorf("Expected bridge config path %s, got %s", expected, actual)
		}
	})
}

func TestConfigPathsDirectoryOperations(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	testBaseDir := filepath.Join(tempDir, "test-gabs")

	cp, err := NewConfigPaths(testBaseDir)
	if err != nil {
		t.Fatalf("Failed to create ConfigPaths: %v", err)
	}

	t.Run("EnsureBaseDir", func(t *testing.T) {
		if err := cp.EnsureBaseDir(); err != nil {
			t.Fatalf("Failed to ensure base directory: %v", err)
		}

		// Verify directory was created
		if _, err := os.Stat(testBaseDir); os.IsNotExist(err) {
			t.Errorf("Base directory was not created: %s", testBaseDir)
		}
	})

	t.Run("EnsureGameDir", func(t *testing.T) {
		gameID := "factory"
		if err := cp.EnsureGameDir(gameID); err != nil {
			t.Fatalf("Failed to ensure game directory: %v", err)
		}

		// Verify directory was created
		expectedGameDir := cp.GetGameDir(gameID)
		if _, err := os.Stat(expectedGameDir); os.IsNotExist(err) {
			t.Errorf("Game directory was not created: %s", expectedGameDir)
		}
	})
}

// Finding 2 (round 6): the ID→runtime-directory mapping must be INJECTIVE.
// Non-canonical spellings that clean to a directory another ID owns must be
// rejected, so status/history/stop for one ID cannot reach another ID's claim.
// Legitimate nested-slash and RFC-6901 (`~`) IDs stay valid.
func TestValidateGameIDCanonicalInjective(t *testing.T) {
	valid := []string{"adventure", "factory/old", "a/b/c", "~", "a/~/b", "My-Game_1", "123456"}
	for _, id := range valid {
		if err := ValidateGameID(id); err != nil {
			t.Errorf("valid canonical ID %q was rejected: %v", id, err)
		}
	}
	// Every entry aliases a *different* spelling's directory (or escapes), so all
	// must be rejected.
	invalid := []string{
		"factory/../adventure", // cleans to "adventure"
		"adventure/",           // trailing slash → "adventure"
		"a//b",                 // empty segment → "a/b"
		"./a",                  // "." segment → "a"
		"a/.",                  // → "a"
		"a/../b",               // → "b"
		"..",                   // escapes
		".",                    // → "" (base itself)
		`a\b`,                  // backslash: a Windows separator (per-OS alias)
	}
	for _, id := range invalid {
		if err := ValidateGameID(id); err == nil {
			t.Errorf("non-canonical/aliasing ID %q was accepted (breaks injectivity)", id)
		}
	}
}

// The concrete cross-ID reproduction: "factory/../adventure", "adventure/" and
// "adventure" must NOT all resolve to the same runtime directory. Only the
// canonical "adventure" resolves; the aliases are rejected before any path is
// handed back.
func TestSafeGameDirRejectsAliasesOfAnotherID(t *testing.T) {
	cp, err := NewConfigPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := cp.SafeGameDir("adventure")
	if err != nil {
		t.Fatalf("the canonical ID must resolve: %v", err)
	}
	for _, alias := range []string{"factory/../adventure", "adventure/", "./adventure"} {
		if dir, err := cp.SafeGameDir(alias); err == nil {
			t.Fatalf("alias %q resolved to %q (canonical was %q) — mapping is not injective", alias, dir, canonical)
		}
	}
}
