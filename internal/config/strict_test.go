package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDuplicateMembersRejected(t *testing.T) {
	cases := []struct {
		name, json, pathFrag string
	}{
		{"top-level", `{"version":"1.0","version":"1.0","games":{}}`, "/version"},
		{"game entry", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","target":"/y"}}}`, "/games/a/target"},
		{"env key", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","env":{"K":"1","K":"2"}}}}`, "/games/a/env/K"},
		{"profile name", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","defaultProfile":"p","profiles":{"p":{},"p":{}}}}}`, "/games/a/profiles/p"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, c.json)
			_, err := LoadGamesConfigFromPath(p)
			if err == nil {
				t.Fatalf("expected duplicate-member error")
			}
			if !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), c.pathFrag) {
				t.Fatalf("error should mention duplicate + path %q, got: %v", c.pathFrag, err)
			}
		})
	}
}

func TestUnknownKeysInNewSubtreesAreErrors(t *testing.T) {
	cases := []struct{ name, json, pathFrag string }{
		{"profile key", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","defaultProfile":"p","profiles":{"p":{"descriptoin":"typo"}}}}}`, "/games/a/profiles/p/descriptoin"},
		{"input key", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","launchInputs":{"q":{"description":"d","type":"boolean","args":["--q"],"defualt":true}}}}}`, "/games/a/launchInputs/q/defualt"},
		{"lifecycle key", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","lifecycle":{"stat":{"command":"c"}}}}}`, "/games/a/lifecycle/stat"},
		{"hook key", `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","lifecycle":{"status":{"command":"c","timeout":5}}}}}`, "/games/a/lifecycle/status/timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, c.json)
			_, err := LoadGamesConfigFromPath(p)
			if err == nil {
				t.Fatalf("expected unknown-key error")
			}
			if !strings.Contains(err.Error(), c.pathFrag) {
				t.Fatalf("error should mention path %q, got: %v", c.pathFrag, err)
			}
		})
	}
}

func TestUnknownKeysElsewhereAreWarnings(t *testing.T) {
	// top-level unknown key and unknown game-entry key warn but load fine
	p := writeTemp(t, `{"version":"1.0","timeoutz":{"x":1},"games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","profle":"typo"}}}`)
	cfg, err := LoadGamesConfigFromPath(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var paths []string
	for _, w := range cfg.Warnings {
		paths = append(paths, w.Path)
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "/timeoutz") {
		t.Fatalf("expected warning for /timeoutz, got %v", paths)
	}
	if !strings.Contains(joined, "/games/a/profle") {
		t.Fatalf("expected warning for /games/a/profle, got %v", paths)
	}
}

func TestOrdinaryLegacyConfigWarningFree(t *testing.T) {
	p := writeTemp(t, `{
	  "version": "1.0",
	  "apiKey": "k",
	  "toolNormalization": {"enableOpenAINormalization": true, "maxToolNameLength": 64, "preserveOriginalName": true},
	  "stripOutputSchema": false,
	  "portRanges": {"customRanges": [{"min": 8000, "max": 8999}]},
	  "timeouts": {"startup": {"processStartSeconds": 10, "gabpConnectSeconds": 60}, "session": {"ownerLeaseSeconds": 30}},
	  "games": {
	    "factory": {"id":"factory","name":"F","launchMode":"DirectPath","target":"/x","args":["-jar","s.jar"],"workingDir":"/w","description":"d"},
	    "adventure": {"id":"adventure","name":"A","launchMode":"SteamManaged","target":"123456"},
	    "arena": {"id":"arena","name":"Ar","launchMode":"CustomCommand","target":"srv.exe -x","stopProcessName":"srv.exe","gabpMode":"local"}
	  }
	}`)
	cfg, err := LoadGamesConfigFromPath(p)
	if err != nil {
		t.Fatalf("legacy config must load: %v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("legacy config must be warning-free, got %v", cfg.Warnings)
	}
}

func TestLoadValidatesNewFields(t *testing.T) {
	// profiles without defaultProfile -> load error with path
	p := writeTemp(t, `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","profiles":{"p":{"description":"x"}}}}}`)
	_, err := LoadGamesConfigFromPath(p)
	if err == nil || !strings.Contains(err.Error(), "defaultProfile") {
		t.Fatalf("expected defaultProfile error, got %v", err)
	}
}

// The M1 lifecycle feature gate is removed (M2.14): a config declaring
// lifecycle hooks now LOADS instead of being rejected as "not yet supported".
func TestLoadAcceptsLifecycle(t *testing.T) {
	p := writeTemp(t, `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","lifecycle":{"status":{"command":"c"},"stop":{"command":"s"}}}}}`)
	if _, err := LoadGamesConfigFromPath(p); err != nil {
		t.Fatalf("a lifecycle config must load once the M1 gate is removed, got: %v", err)
	}
}

func TestValidProfiledConfigLoads(t *testing.T) {
	p := writeTemp(t, `{
	  "version": "1.0",
	  "games": {
	    "adventure": {
	      "id": "adventure", "name": "Adventure", "launchMode": "DirectPath", "target": "/opt/adventure",
	      "env": {"LOG_FORMAT": "json"},
	      "unsetEnv": ["HOST_OVERRIDE"],
	      "defaultProfile": "vanilla",
	      "profiles": {
	        "vanilla": {"description": "base", "args": ["--data-root", "/srv/v"]},
	        "combat": {"description": "combat", "env": {"CONTENT_SET": "combat"}, "workingDir": "/srv/c"}
	      },
	      "launchInputs": {
	        "quickStart": {"description": "d", "type": "boolean", "args": ["--quick-start"]},
	        "scenario": {"description": "d", "type": "string", "enum": ["arena","tutorial"], "profiles": ["combat"], "args": ["--scenario", "${value}"]}
	      }
	    }
	  }
	}`)
	cfg, err := LoadGamesConfigFromPath(p)
	if err != nil {
		t.Fatalf("valid profiled config must load: %v", err)
	}
	g := cfg.Games["adventure"]
	if g.DefaultProfile != "vanilla" || len(g.Profiles) != 2 || len(g.LaunchInputs) != 2 {
		t.Fatalf("profiled fields not decoded: %+v", g)
	}
	if g.Profiles["combat"].WorkingDir != "/srv/c" {
		t.Fatalf("profile workingDir not decoded")
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", cfg.Warnings)
	}
}

func TestJSONPointerEscaping(t *testing.T) {
	// keys containing / and ~ must be escaped in issue paths (RFC 6901)
	p := writeTemp(t, `{"version":"1.0","we/ird~":1,"games":{}}`)
	cfg, err := LoadGamesConfigFromPath(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if w.Path == "/we~1ird~0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected escaped pointer /we~1ird~0, got %v", cfg.Warnings)
	}
}

// Finding 2 residual (round 6): two DISTINCT, individually-canonical game IDs
// that map to the same runtime directory on a case-insensitive filesystem must
// be rejected at load — uniformly, so a config is portable across filesystems.
func TestGameIDDirectoryCollisionRejected(t *testing.T) {
	cases := []struct{ name, json string }{
		{"case variants", `{"version":"1.0","games":{"Adventure":{"id":"Adventure","name":"A","launchMode":"DirectPath","target":"/x"},"adventure":{"id":"adventure","name":"a","launchMode":"DirectPath","target":"/y"}}}`},
		{"path-normalizing alias", `{"version":"1.0","games":{"factory/../adventure":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x"},"adventure":{"id":"b","name":"B","launchMode":"DirectPath","target":"/y"}}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := writeTemp(t, c.json)
			_, err := LoadGamesConfigFromPath(p)
			if err == nil {
				t.Fatal("game IDs that map to the same runtime directory must be rejected")
			}
			if !strings.Contains(err.Error(), "same runtime directory") {
				t.Fatalf("error should explain the directory collision, got: %v", err)
			}
		})
	}
}

// Distinct IDs — including legitimate nested-slash IDs — still load unchanged.
func TestDistinctAndNestedGameIDsLoad(t *testing.T) {
	p := writeTemp(t, `{"version":"1.0","games":{"adventure":{"id":"adventure","name":"A","launchMode":"DirectPath","target":"/x"},"factory":{"id":"factory","name":"F","launchMode":"DirectPath","target":"/y"},"factory/old":{"id":"factory/old","name":"O","launchMode":"DirectPath","target":"/z"}}}`)
	if _, err := LoadGamesConfigFromPath(p); err != nil {
		t.Fatalf("distinct (including nested-slash) game IDs must load: %v", err)
	}
}
