package mcp

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// ServeHTTP starts the MCP server on HTTP (Streamable HTTP transport)
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
	// For now, just a placeholder - Streamable HTTP is more complex
	// It requires SSE (Server-Sent Events) for bi-directional communication

	mux := http.NewServeMux()

	// Basic health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","server":"gabs","version":"0.1.0"}`)
	})

	// Placeholder for MCP endpoint
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(w, `{"error":"HTTP transport not fully implemented yet"}`)
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	s.log.Infow("starting HTTP server", "addr", addr)

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Errorw("HTTP server error", "error", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return server.Shutdown(shutdownCtx)
}
