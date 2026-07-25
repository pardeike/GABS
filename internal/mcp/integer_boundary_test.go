package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
