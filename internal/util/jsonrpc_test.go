package util

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

// TestAutoFrameWriterConcurrentSafety verifies that concurrent WriteJSON calls
// on the same AutoFrameWriter do not produce interleaved or corrupted output.
// Before the mutex fix, a background goroutine (e.g. a GABP disconnect handler
// sending a tools/list_changed notification) could interleave its write with a
// response being written by the Serve loop, corrupting the LSP frame stream and
// causing the MCP client to disconnect.
func TestAutoFrameWriterConcurrentSafety(t *testing.T) {
	var buf bytes.Buffer
	w := NewAutoFrameWriter(&buf)
	w.SetMode(FramingLSP)

	const goroutines = 10
	const writesPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				msg := map[string]interface{}{
					"goroutine": id,
					"seq":       i,
				}
				if err := w.WriteJSON(msg); err != nil {
					t.Errorf("WriteJSON error from goroutine %d: %v", id, err)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify we can parse every frame back out without corruption.
	reader := NewLSPFrameReader(bytes.NewReader(buf.Bytes()))
	count := 0
	for {
		_, err := reader.ReadMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("corrupted frame at message %d: %v", count, err)
		}
		count++
	}

	expected := goroutines * writesPerGoroutine
	if count != expected {
		t.Errorf("expected %d messages, got %d", expected, count)
	}
}

// TestUnprotectedLSPWriterCorrupts demonstrates that concurrent writes to an
// unprotected LSPFrameWriter produce corrupted output. This is the bug that
// existed before adding the mutex to AutoFrameWriter — the GABP disconnect
// handler goroutine could write a notification while the Serve loop was writing
// a response, interleaving the two LSP frames on stdout.
func TestUnprotectedLSPWriterCorrupts(t *testing.T) {
	var buf bytes.Buffer
	// Use raw LSPFrameWriter directly — no mutex protection.
	rawWriter := &buf

	const goroutines = 10
	const writesPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			w := NewLSPFrameWriter(rawWriter)
			for i := 0; i < writesPerGoroutine; i++ {
				msg := map[string]interface{}{
					"goroutine": id,
					"seq":       i,
				}
				_ = w.WriteJSON(msg)
			}
		}(g)
	}

	wg.Wait()

	// Try to parse. With interleaved writes, frames will be corrupted.
	reader := NewLSPFrameReader(bytes.NewReader(buf.Bytes()))
	count := 0
	corrupted := false
	for {
		_, err := reader.ReadMessage()
		if err == io.EOF {
			break
		}
		if err != nil {
			corrupted = true
			break
		}
		count++
	}

	expected := goroutines * writesPerGoroutine
	if !corrupted && count == expected {
		// The race doesn't always manifest. Log but don't fail — the point is
		// that AutoFrameWriter's mutex *guarantees* safety while the raw writer
		// only happens to succeed when goroutine scheduling is favorable.
		t.Logf("no corruption detected in this run (race is timing-dependent), parsed %d/%d", count, expected)
	} else {
		t.Logf("corruption detected as expected: parsed %d/%d frames before error", count, expected)
	}
}
