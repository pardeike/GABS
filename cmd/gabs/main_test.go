package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pardeike/gabs/internal/util"
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

type shutdownProbeServer struct {
	serveStarted chan struct{}
	serveRelease chan struct{}
	serveDone    chan struct{}
	shutdown     chan struct{}
	serveErr     error
	waitForCtx   bool
	startOnce    sync.Once
	doneOnce     sync.Once
	shutdownOnce sync.Once
}

func newShutdownProbeServer() *shutdownProbeServer {
	return &shutdownProbeServer{
		serveStarted: make(chan struct{}),
		serveRelease: make(chan struct{}),
		serveDone:    make(chan struct{}),
		shutdown:     make(chan struct{}),
	}
}

func (s *shutdownProbeServer) serve(ctx context.Context) error {
	s.startOnce.Do(func() { close(s.serveStarted) })
	if s.waitForCtx {
		<-ctx.Done()
	}
	<-s.serveRelease
	s.doneOnce.Do(func() { close(s.serveDone) })
	return s.serveErr
}

func (s *shutdownProbeServer) ServeStdio(ctx context.Context) error { return s.serve(ctx) }
func (s *shutdownProbeServer) ServeHTTP(ctx context.Context, _ string) error {
	return s.serve(ctx)
}
func (s *shutdownProbeServer) Shutdown() {
	s.shutdownOnce.Do(func() { close(s.shutdown) })
}

func TestServeConfiguredMCPShutsDownAfterTransportReturn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		serveErr error
		wantCode int
	}{
		{name: "clean", wantCode: 0},
		{name: "error", serveErr: errors.New("transport failed"), wantCode: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newShutdownProbeServer()
			server.serveErr = tc.serveErr
			close(server.serveRelease)
			got := serveConfiguredMCP(context.Background(), util.NewLogger("error"), options{transport: "stdio"}, server)
			if got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", got, tc.wantCode)
			}
			select {
			case <-server.shutdown:
			default:
				t.Fatal("production server return did not invoke Shutdown")
			}
		})
	}
}

func TestServeConfiguredMCPShutsDownWithoutWaitingForStdioRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := newShutdownProbeServer() // ServeStdio deliberately ignores ctx.
	result := make(chan int, 1)
	go func() {
		result <- serveConfiguredMCP(ctx, util.NewLogger("error"), options{transport: "stdio"}, server)
	}()
	<-server.serveStarted
	cancel()

	select {
	case <-server.shutdown:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not invoke Shutdown")
	}
	select {
	case got := <-result:
		if got != 0 {
			t.Fatalf("signal-style cancellation exit code = %d, want 0", got)
		}
	case <-time.After(time.Second):
		t.Fatal("stdio cancellation waited forever for the blocked stdin reader")
	}
	close(server.serveRelease)
	<-server.serveDone
}

func TestServeConfiguredMCPWaitsForHTTPGracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	server := newShutdownProbeServer()
	server.waitForCtx = true
	result := make(chan int, 1)
	go func() {
		result <- serveConfiguredMCP(ctx, util.NewLogger("error"), options{transport: "http"}, server)
	}()
	<-server.serveStarted
	cancel()

	select {
	case <-server.shutdown:
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation did not invoke Shutdown")
	}
	select {
	case got := <-result:
		t.Fatalf("returned with code %d before HTTP graceful shutdown completed", got)
	case <-time.After(25 * time.Millisecond):
	}

	close(server.serveRelease)
	select {
	case got := <-result:
		if got != 0 {
			t.Fatalf("HTTP cancellation exit code = %d, want 0", got)
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after HTTP graceful shutdown completed")
	}
}
