package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// HTTPClient represents an HTTP client connection for SSE
type HTTPClient struct {
	ID       string
	Writer   http.ResponseWriter
	Flusher  http.Flusher
	Done     chan struct{}
	Request  *http.Request
}

// ServeHTTP starts the MCP server on HTTP (Streamable HTTP transport)
func (s *Server) ServeHTTP(ctx context.Context, addr string) error {
	// HTTP clients for Server-Sent Events
	httpClients := make(map[string]*HTTPClient)
	httpClientsMu := sync.RWMutex{}

	mux := http.NewServeMux()

	// Basic health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","server":"gabs","version":"0.1.0"}`)
	})

	// MCP JSON-RPC endpoint - handles all MCP method calls
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		s.handleMCPHTTPRequest(w, r)
	})

	// Server-Sent Events endpoint for notifications
	mux.HandleFunc("/mcp/events", func(w http.ResponseWriter, r *http.Request) {
		s.handleSSEConnection(w, r, httpClients, &httpClientsMu)
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	s.log.Infow("starting HTTP server with full MCP support", "addr", addr)

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Errorw("HTTP server error", "error", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Close all SSE connections
	httpClientsMu.Lock()
	for _, client := range httpClients {
		close(client.Done)
	}
	httpClientsMu.Unlock()

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return server.Shutdown(shutdownCtx)
}

// handleMCPHTTPRequest handles JSON-RPC requests over HTTP
func (s *Server) handleMCPHTTPRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, `{"error":"Method not allowed. Use POST for MCP requests."}`)
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"Failed to read request body"}`)
		return
	}
	defer r.Body.Close()

	// Parse JSON-RPC message
	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"Invalid JSON-RPC message"}`)
		return
	}

	s.log.Debugw("received HTTP MCP request", "method", msg.Method, "id", msg.ID)

	// Handle the message using existing handler
	response := s.handleMessage(&msg)

	// Send response
	w.Header().Set("Content-Type", "application/json")
	if response != nil {
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			s.log.Errorw("failed to encode HTTP response", "error", err)
		}
	} else {
		// No response for notifications
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSSEConnection handles Server-Sent Events connections for notifications
func (s *Server) handleSSEConnection(w http.ResponseWriter, r *http.Request, clients map[string]*HTTPClient, clientsMu *sync.RWMutex) {
	// Check if client supports SSE
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Server-Sent Events not supported", http.StatusNotImplemented)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")

	// Generate client ID
	clientID := fmt.Sprintf("client-%d", time.Now().UnixNano())
	
	// Create client
	client := &HTTPClient{
		ID:      clientID,
		Writer:  w,
		Flusher: flusher,
		Done:    make(chan struct{}),
		Request: r,
	}

	// Register client
	clientsMu.Lock()
	clients[clientID] = client
	clientsMu.Unlock()

	// Clean up on disconnect
	defer func() {
		clientsMu.Lock()
		delete(clients, clientID)
		clientsMu.Unlock()
		s.log.Debugw("SSE client disconnected", "clientId", clientID)
	}()

	s.log.Debugw("SSE client connected", "clientId", clientID)

	// Send initial connection event
	fmt.Fprintf(w, "event: connected\n")
	fmt.Fprintf(w, "data: {\"clientId\":\"%s\",\"server\":\"gabs\",\"version\":\"0.1.0\"}\n\n", clientID)
	flusher.Flush()

	// Keep connection alive and wait for disconnect
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-client.Done:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Send keepalive ping
			fmt.Fprintf(w, "event: ping\n")
			fmt.Fprintf(w, "data: {\"timestamp\":%d}\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}

// SendHTTPNotification sends notifications to all connected HTTP SSE clients
func (s *Server) SendHTTPNotification(method string, params interface{}, clients map[string]*HTTPClient, clientsMu *sync.RWMutex) {
	clientsMu.RLock()
	defer clientsMu.RUnlock()

	notification := NewNotification(method, params)
	data, err := json.Marshal(notification)
	if err != nil {
		s.log.Errorw("failed to marshal notification for HTTP", "error", err)
		return
	}

	for clientID, client := range clients {
		select {
		case <-client.Done:
			continue // Client already disconnected
		default:
			fmt.Fprintf(client.Writer, "event: notification\n")
			fmt.Fprintf(client.Writer, "data: %s\n\n", string(data))
			client.Flusher.Flush()
			s.log.Debugw("sent HTTP notification", "clientId", clientID, "method", method)
		}
	}
}
