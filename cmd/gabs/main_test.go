package main

import (
	"testing"
	"time"
)

func TestParseBackoffDefault(t *testing.T) {
	min, max, err := parseBackoff(defaultBackoff)
	if err != nil {
		t.Fatalf("parseBackoff returned error: %v", err)
	}
	if min != 100*time.Millisecond {
		t.Fatalf("expected min 100ms, got %v", min)
	}
	if max != time.Second {
		t.Fatalf("expected max 1s, got %v", max)
	}
}
