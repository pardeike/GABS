package util

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// FrameWriter interface for JSON frame writers
type FrameWriter interface {
	WriteJSON(obj interface{}) error
}

// Message represents a JSON-RPC 2.0 message
type Message struct {
	Version string      `json:"jsonrpc,omitempty"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method,omitempty"`
	Params  interface{} `json:"params,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error
type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// GABP message envelope
type GABPMessage struct {
	V       string      `json:"v"`
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Method  string      `json:"method,omitempty"`
	Params  interface{} `json:"params,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	Channel string      `json:"channel,omitempty"`
	Seq     int         `json:"seq,omitempty"`
	Payload interface{} `json:"payload,omitempty"`
}

// NewGABPRequest creates a new GABP request message
func NewGABPRequest(method string, params interface{}) *GABPMessage {
	return &GABPMessage{
		V:      "gabp/1",
		ID:     uuid.New().String(),
		Type:   "request",
		Method: method,
		Params: params,
	}
}

// NewGABPResponse creates a GABP response message
func NewGABPResponse(requestID string, result interface{}) *GABPMessage {
	return &GABPMessage{
		V:      "gabp/1",
		ID:     requestID,
		Type:   "response",
		Result: result,
	}
}

// NewGABPError creates a GABP error response
func NewGABPError(requestID string, code int, message string, data interface{}) *GABPMessage {
	return &GABPMessage{
		V:    "gabp/1",
		ID:   requestID,
		Type: "response",
		Error: &RPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

// NewGABPEvent creates a GABP event message
func NewGABPEvent(channel string, seq int, payload interface{}) *GABPMessage {
	return &GABPMessage{
		V:       "gabp/1",
		ID:      uuid.New().String(),
		Type:    "event",
		Channel: channel,
		Seq:     seq,
		Payload: payload,
	}
}

// LSPFrameReader reads LSP-framed messages (Content-Length header)
type LSPFrameReader struct {
	reader *bufio.Reader
}

// NewLSPFrameReader creates a new LSP frame reader
func NewLSPFrameReader(r io.Reader) *LSPFrameReader {
	return &LSPFrameReader{
		reader: bufio.NewReader(r),
	}
}

// ReadMessage reads one LSP-framed message
func (r *LSPFrameReader) ReadMessage() ([]byte, error) {
	var contentLength int

	// Read headers until empty line
	for {
		line, err := r.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		// Remove \r\n or \n
		line = strings.TrimSuffix(line, "\r\n")
		line = strings.TrimSuffix(line, "\n")

		// Empty line indicates end of headers
		if line == "" {
			break
		}

		// Parse Content-Length header
		if strings.HasPrefix(line, "Content-Length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid Content-Length header: %s", line)
			}
			length, err := strconv.Atoi(strings.TrimSpace(parts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length value: %s", parts[1])
			}
			contentLength = length
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	// Read the message body
	body := make([]byte, contentLength)
	_, err := io.ReadFull(r.reader, body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// LSPFrameWriter writes LSP-framed messages
type LSPFrameWriter struct {
	writer io.Writer
}

// NewLSPFrameWriter creates a new LSP frame writer
func NewLSPFrameWriter(w io.Writer) *LSPFrameWriter {
	return &LSPFrameWriter{writer: w}
}

// WriteMessage writes a message with LSP framing
func (w *LSPFrameWriter) WriteMessage(data []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	_, err := w.writer.Write([]byte(header))
	if err != nil {
		return err
	}
	_, err = w.writer.Write(data)
	return err
}

// WriteJSON marshals object to JSON and writes with LSP framing
func (w *LSPFrameWriter) WriteJSON(obj interface{}) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	return w.WriteMessage(data)
}

// NewlineFrameWriter writes newline-delimited JSON messages (for MCP stdio)
type NewlineFrameWriter struct {
	writer io.Writer
}

// NewNewlineFrameWriter creates a newline frame writer
func NewNewlineFrameWriter(w io.Writer) *NewlineFrameWriter {
	return &NewlineFrameWriter{writer: w}
}

// WriteJSON marshals object and writes with newline delimiter
func (w *NewlineFrameWriter) WriteJSON(obj interface{}) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.writer.Write(data)
	return err
}

// NewlineFrameReader reads newline-delimited JSON messages
type NewlineFrameReader struct {
	scanner *bufio.Scanner
}

// NewNewlineFrameReader creates a newline frame reader
func NewNewlineFrameReader(r io.Reader) *NewlineFrameReader {
	scanner := bufio.NewScanner(r)
	// Increase buffer size to handle large messages (MCP spec mentions 10MB+)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	return &NewlineFrameReader{scanner: scanner}
}

// ReadJSON reads one newline-delimited JSON message
func (r *NewlineFrameReader) ReadJSON(obj interface{}) error {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return err
		}
		return io.EOF
	}
	return json.Unmarshal(r.scanner.Bytes(), obj)
}
