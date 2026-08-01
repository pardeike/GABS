package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The integer arguments (timeout, limit, cursor) are parsed with
// strconv.ParseInt(s, 10, 0) and converted to int. A value that overflows the
// platform int must be REJECTED as "must be an integer" — never silently
// truncated, never a panic. These tests exercise the extreme/overflow path
// (which the in-range TestTimeoutRangeEnforced does not) through BOTH transports.

const (
	overflowInt64   = "99999999999999999999999" // far beyond int64
	maxInt64Str     = "9223372036854775807"     // math.MaxInt64 (parses; then range-checked)
	maxInt64Plus1   = "9223372036854775808"     // MaxInt64 + 1: overflows int64
	mustBeInteger   = "must be an integer"
	timeoutTooLarge = "timeout_out_of_range"
)

// raw stdio path: HandleMessage over a JSON-number argument.
func TestIntegerArgOverflowRejectedStdio(t *testing.T) {
	s := newProfiledServer(t)

	cases := []struct {
		tool string
		args map[string]interface{}
		want string // substring the response must contain
	}{
		// timeout -> parseOptionalPositiveIntValue
		{"games.start", map[string]interface{}{"gameId": "adventure", "timeout": json.Number(overflowInt64)}, mustBeInteger},
		{"games.start", map[string]interface{}{"gameId": "adventure", "timeout": json.Number(maxInt64Plus1)}, mustBeInteger},
		// the in-range boundary still parses, then fails the 1..3600 range check
		{"games.start", map[string]interface{}{"gameId": "adventure", "timeout": json.Number(maxInt64Str)}, timeoutTooLarge},
		// limit -> getOptionalPositiveIntArg ; cursor -> getCursorOffset
		{"games.tool_names", map[string]interface{}{"gameId": "adventure", "limit": json.Number(overflowInt64)}, mustBeInteger},
		{"games.tool_names", map[string]interface{}{"gameId": "adventure", "cursor": json.Number(overflowInt64)}, mustBeInteger},
	}
	for _, c := range cases {
		raw, _ := callTool(t, s, c.tool, c.args)
		if !strings.Contains(raw, c.want) {
			t.Fatalf("%s %v: response must contain %q, got: %s", c.tool, c.args, c.want, raw)
		}
	}
}

// real HTTP round-trip: the huge integer arrives as a JSON literal in the POST
// body, is decoded by UnmarshalPreservingNumbers into a json.Number, and must be
// rejected cleanly (HTTP 200 + the error result), never a panic or a 500.
func TestIntegerArgOverflowRejectedHTTP(t *testing.T) {
	s := newProfiledServer(t)
	srv := httptest.NewServer(http.HandlerFunc(s.handleMCPHTTPRequest))
	defer srv.Close()

	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"games.start","arguments":{"gameId":"adventure","timeout":%s}}}`, overflowInt64)
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overflow integer must yield a clean 200 JSON-RPC error, got HTTP %d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), mustBeInteger) {
		t.Fatalf("HTTP response must reject the overflow integer as %q, got: %s", mustBeInteger, string(out))
	}
}

// real newline-framed stdio round-trip: the huge integer is a JSON literal in
// the framed request, decoded by the auto-frame reader (UnmarshalPreservingNumbers)
// exactly as a live stdio client sends it.
func TestIntegerArgOverflowFramedStdio(t *testing.T) {
	s := newProfiledServer(t)
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"games.start","arguments":{"gameId":"adventure","timeout":%s}}}`, overflowInt64) + "\n"
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(req), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(out.String(), mustBeInteger) {
		t.Fatalf("framed stdio must reject the overflow integer as %q, got: %s", mustBeInteger, out.String())
	}
}

// A native-width MaxInt limit with a non-zero cursor must NOT panic at
// entries[cursor:end] (cursor+limit overflows negative). Needs >= 2 listed tools.
func TestPaginationLimitOverflowDoesNotPanic(t *testing.T) {
	s := newProfiledServer(t)
	noop := func(args map[string]interface{}) (*ToolResult, error) { return &ToolResult{}, nil }
	s.RegisterGameTool("adventure", Tool{Name: "adventure.alpha", Description: "a"}, noop, nil)
	s.RegisterGameTool("adventure", Tool{Name: "adventure.beta", Description: "b"}, noop, nil)

	// cursor=1, limit=MaxInt64: without a checked sum this panics.
	raw, _ := callTool(t, s, "games.tool_names", map[string]interface{}{
		"gameId": "adventure",
		"cursor": "1",
		"limit":  json.Number(maxInt64Str),
	})
	if strings.Contains(raw, `"error"`) && strings.Contains(raw, "panic") {
		t.Fatalf("pagination panicked: %s", raw)
	}
	// A successful page (the remaining tool after the cursor) proves no panic.
	if !strings.Contains(raw, "adventure.beta") {
		t.Fatalf("expected the page after cursor=1 to contain adventure.beta, got: %s", raw)
	}
}

// A native-width integer inside int but beyond the largest representable
// Duration must clamp to a POSITIVE duration, never overflow into a negative
// timeout (internal/mcp/stdio_server.go:502/511/520).
func TestDurationConversionClampsOverflow(t *testing.T) {
	if d := clampedSecondsToDuration(math.MaxInt); d <= 0 {
		t.Fatalf("clampedSecondsToDuration(MaxInt) = %v, must stay positive", d)
	}
	if d := clampedMillisToDuration(math.MaxInt); d <= 0 {
		t.Fatalf("clampedMillisToDuration(MaxInt) = %v, must stay positive", d)
	}
	if d, res := parseOptionalTimeoutSecondsArg(map[string]interface{}{"t": json.Number(maxInt64Str)}, "t", time.Second); res != nil || d <= 0 {
		t.Fatalf("parseOptionalTimeoutSecondsArg overflow: d=%v res=%v", d, res)
	}
	if d, res := deriveMirroredToolCallTimeout(map[string]interface{}{"timeout": json.Number(maxInt64Str)}, time.Second); res != nil || d <= 0 {
		t.Fatalf("mirrored timeout overflow: d=%v res=%v", d, res)
	}
	if d, res := deriveMirroredToolCallTimeout(map[string]interface{}{"timeoutMs": json.Number(maxInt64Str)}, time.Second); res != nil || d <= 0 {
		t.Fatalf("mirrored timeoutMs overflow: d=%v res=%v", d, res)
	}
}
