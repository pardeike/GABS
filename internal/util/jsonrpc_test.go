package util

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLSPFrameWriterWritesOneCompleteFrame(t *testing.T) {
	writer := &countingWriter{}
	frameWriter := NewLSPFrameWriter(writer)

	if err := frameWriter.WriteJSON(Message{
		Version: "2.0",
		ID:      1,
		Result:  map[string]interface{}{"ok": true},
	}); err != nil {
		t.Fatalf("write json: %v", err)
	}

	if writer.Calls() != 1 {
		t.Fatalf("expected one write call, got %d", writer.Calls())
	}

	reader := NewLSPFrameReader(bytes.NewReader(writer.Bytes()))
	data, err := reader.ReadMessage()
	if err != nil {
		t.Fatalf("read message: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal message: %v", err)
	}
	if decoded.ID != float64(1) {
		t.Fatalf("unexpected id: %#v", decoded.ID)
	}
}

func TestAutoFrameWriterSerializesConcurrentWrites(t *testing.T) {
	writer := &overlapDetectingWriter{}
	frameWriter := NewAutoFrameWriter(writer)
	frameWriter.SetMode(FramingLSP)

	const count = 100
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup

	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- frameWriter.WriteJSON(Message{
				Version: "2.0",
				ID:      i,
				Result:  map[string]interface{}{"ok": true},
			})
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("write json: %v", err)
		}
	}
	if writer.Overlap() {
		t.Fatal("underlying writer saw overlapping writes")
	}

	reader := NewLSPFrameReader(bytes.NewReader(writer.Bytes()))
	seen := make(map[int]bool, count)
	for i := 0; i < count; i++ {
		data, err := reader.ReadMessage()
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}

		var decoded Message
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal frame %d: %v", i, err)
		}

		id, ok := decoded.ID.(float64)
		if !ok {
			t.Fatalf("frame %d has non-numeric id: %#v", i, decoded.ID)
		}
		seen[int(id)] = true
	}

	if _, err := reader.ReadMessage(); err != io.EOF {
		t.Fatalf("expected EOF after %d frames, got %v", count, err)
	}
	if len(seen) != count {
		t.Fatalf("expected %d unique ids, got %d", count, len(seen))
	}
}

type countingWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	calls int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.calls++
	return w.data.Write(p)
}

func (w *countingWriter) Calls() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.calls
}

func (w *countingWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]byte(nil), w.data.Bytes()...)
}

type overlapDetectingWriter struct {
	mu      sync.Mutex
	data    bytes.Buffer
	active  int32
	overlap atomic.Bool
}

func (w *overlapDetectingWriter) Write(p []byte) (int, error) {
	if atomic.AddInt32(&w.active, 1) > 1 {
		w.overlap.Store(true)
	}
	defer atomic.AddInt32(&w.active, -1)

	time.Sleep(250 * time.Microsecond)

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.data.Write(p)
}

func (w *overlapDetectingWriter) Overlap() bool {
	return w.overlap.Load()
}

func (w *overlapDetectingWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]byte(nil), w.data.Bytes()...)
}
