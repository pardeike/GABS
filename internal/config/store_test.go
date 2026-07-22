package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

const validCfgA = `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x"}}}`
const validCfgB = `{"version":"1.0","games":{"b":{"id":"b","name":"B","launchMode":"DirectPath","target":"/y"}}}`
const invalidCfg = `{"version":"1.0","games":{"a":{"id":"a","name":"A","launchMode":"DirectPath","target":"/x","profiles":{"p":{"description":"x"}}}}}`

func storeAt(t *testing.T, content string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(p), p
}

func TestStoreNextCallPickup(t *testing.T) {
	s, p := storeAt(t, validCfgA)
	snap, cerr := s.Snapshot()
	if cerr != nil || snap == nil {
		t.Fatalf("initial snapshot failed: %v", cerr)
	}
	if _, ok := snap.Config.Games["a"]; !ok {
		t.Fatalf("expected game a")
	}
	revA := snap.Revision
	if !strings.HasPrefix(revA, "sha256:") || len(revA) != len("sha256:")+12 {
		t.Fatalf("bad revision format: %q", revA)
	}

	// in-place edit
	if err := os.WriteFile(p, []byte(validCfgB), 0600); err != nil {
		t.Fatal(err)
	}
	snap, cerr = s.Snapshot()
	if cerr != nil {
		t.Fatalf("unexpected error: %v", cerr)
	}
	if _, ok := snap.Config.Games["b"]; !ok {
		t.Fatalf("expected game b after in-place edit")
	}
	if snap.Revision == revA {
		t.Fatalf("revision must change with content")
	}

	// atomic rename edit
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(validCfgA), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	snap, cerr = s.Snapshot()
	if cerr != nil {
		t.Fatalf("unexpected error: %v", cerr)
	}
	if _, ok := snap.Config.Games["a"]; !ok {
		t.Fatalf("expected game a after rename edit")
	}
	if snap.Revision != revA {
		t.Fatalf("same content must produce same revision")
	}
}

func TestStoreLastKnownGood(t *testing.T) {
	s, p := storeAt(t, validCfgA)
	good, _ := s.Snapshot()

	if err := os.WriteFile(p, []byte(invalidCfg), 0600); err != nil {
		t.Fatal(err)
	}
	snap, cerr := s.Snapshot()
	if cerr == nil {
		t.Fatalf("expected config error for invalid content")
	}
	if !strings.Contains(cerr.Err.Error(), "defaultProfile") {
		t.Fatalf("error should carry the exact validation error, got %v", cerr.Err)
	}
	if snap == nil || snap.Revision != good.Revision {
		t.Fatalf("must keep last-known-good snapshot")
	}

	// fixing the file clears the condition on the next call
	if err := os.WriteFile(p, []byte(validCfgB), 0600); err != nil {
		t.Fatal(err)
	}
	snap, cerr = s.Snapshot()
	if cerr != nil {
		t.Fatalf("fix must clear error, got %v", cerr)
	}
	if _, ok := snap.Config.Games["b"]; !ok {
		t.Fatalf("expected game b after fix")
	}
}

func TestStoreInvalidParsedOnce(t *testing.T) {
	s, p := storeAt(t, validCfgA)
	if _, cerr := s.Snapshot(); cerr != nil {
		t.Fatal(cerr)
	}
	if err := os.WriteFile(p, []byte(invalidCfg), 0600); err != nil {
		t.Fatal(err)
	}
	before := s.parseCount
	for i := 0; i < 5; i++ {
		if _, cerr := s.Snapshot(); cerr == nil {
			t.Fatalf("expected error")
		}
	}
	if s.parseCount != before+1 {
		t.Fatalf("invalid content must be parsed exactly once, got %d extra parses", s.parseCount-before)
	}
}

func TestStoreStartupInvalid(t *testing.T) {
	s, _ := storeAt(t, invalidCfg)
	snap, cerr := s.Snapshot()
	if cerr == nil {
		t.Fatalf("expected error on startup-invalid config")
	}
	if snap != nil {
		t.Fatalf("no last-known-good exists at startup; snapshot must be nil")
	}
}

func TestStoreMissingFile(t *testing.T) {
	s, _ := storeAt(t, "")
	snap, cerr := s.Snapshot()
	if cerr != nil {
		t.Fatalf("missing file keeps existing empty-config behavior, got %v", cerr)
	}
	if snap == nil || snap.Config == nil || len(snap.Config.Games) != 0 {
		t.Fatalf("expected default empty config")
	}
	if snap.Config.ToolNormalization == nil {
		t.Fatalf("defaults must be applied to the empty config")
	}
}

func TestStoreConcurrent(t *testing.T) {
	s, p := storeAt(t, validCfgA)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		contents := []string{validCfgA, validCfgB, invalidCfg}
		for i := 0; i < 50; i++ {
			_ = os.WriteFile(p, []byte(contents[i%3]), 0600)
		}
		close(stop)
	}()
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					snap, cerr := s.Snapshot()
					if snap == nil && cerr == nil {
						t.Error("snapshot and error both nil")
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestStoreMissingThenEmptyFile(t *testing.T) {
	s, p := storeAt(t, "")
	if _, cerr := s.Snapshot(); cerr != nil {
		t.Fatalf("missing file must serve defaults: %v", cerr)
	}
	// A zero-byte file is invalid JSON — it must NOT be conflated with the
	// absent-file default (absence is not a content hash).
	if err := os.WriteFile(p, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	snap, cerr := s.Snapshot()
	if cerr == nil {
		t.Fatalf("empty file must surface a ConfigError, got clean snapshot %+v", snap)
	}
}

func TestSaveConfigAlwaysPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission-bit semantics")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// an interrupted old-style save left a loose fixed-name temp file
	if err := os.WriteFile(path+".tmp", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &GamesConfig{Version: "1.0", Games: map[string]GameConfig{
		"a": {ID: "a", Name: "A", LaunchMode: "DirectPath", Target: "/bin/echo",
			Env: map[string]string{"SECRET": "value"}},
	}}
	if err := SaveGamesConfigToPath(cfg, path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("published config must be 0600, got %v", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".config-") {
			t.Fatalf("temp file lingers: %s", e.Name())
		}
	}
}

func TestBridgeEndpointTokenRotatesPerLaunch(t *testing.T) {
	dir := t.TempDir()
	port1, token1, _, _, err := PrepareBridgeEndpointForStart("g", dir, nil, false, BridgeDiagnostics{})
	if err != nil {
		t.Fatal(err)
	}
	port2, token2, _, reused, err := PrepareBridgeEndpointForStart("g", dir, nil, false, BridgeDiagnostics{})
	if err != nil {
		t.Fatal(err)
	}
	if !reused || port2 != port1 {
		t.Fatalf("the port may be reused: %d vs %d (reused=%v)", port1, port2, reused)
	}
	if token2 == token1 {
		t.Fatalf("the token must rotate per launch — a superseded process's credentials must never remain valid")
	}
	// bridge.json reflects the rotated token
	_, _, diskToken, err := ReadBridgeJSON("g", dir)
	if err != nil || diskToken != token2 {
		t.Fatalf("bridge.json must carry the rotated token: %q vs %q (%v)", diskToken, token2, err)
	}
}
