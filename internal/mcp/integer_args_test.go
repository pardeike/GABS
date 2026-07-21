package mcp

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/pardeike/gabs/internal/util"
)

// Integer launch inputs require exact decoding end-to-end (design/03):
// values must arrive at tool handlers as json.Number, never float64, through
// both the stdio framing and the tools/call params re-decode.
func TestIntegerArgumentsSurviveStdioTransport(t *testing.T) {
	s := NewServerForTesting(util.NewLogger("error"))

	captured := make(chan map[string]interface{}, 1)
	s.RegisterTool(Tool{
		Name:        "capture_args",
		Description: "test helper",
		InputSchema: map[string]interface{}{"type": "object"},
	}, func(args map[string]interface{}) (*ToolResult, error) {
		captured <- args
		return &ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
	})

	// 2^53+1: rounds silently if any float64 intermediary is involved.
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"capture_args","arguments":{"seed":9007199254740993,"small":1}}}` + "\n"

	in := strings.NewReader(request)
	out := &strings.Builder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve(in, io.Writer(out))
	}()
	<-done

	select {
	case args := <-captured:
		seed, ok := args["seed"].(json.Number)
		if !ok {
			t.Fatalf("seed must arrive as json.Number, got %T", args["seed"])
		}
		if seed.String() != "9007199254740993" {
			t.Fatalf("seed lost exactness: %s", seed.String())
		}
		small, ok := args["small"].(json.Number)
		if !ok || small.String() != "1" {
			t.Fatalf("small integer must arrive as json.Number(1), got %T %v", args["small"], args["small"])
		}
	default:
		t.Fatalf("tool handler was not invoked; output: %s", out.String())
	}
}

// The existing timeout parsers must accept json.Number now that transports
// preserve numeric tokens.
func TestParseOptionalPositiveIntValueJSONNumber(t *testing.T) {
	v, ok, res := parseOptionalPositiveIntValue(json.Number("42"), "timeout")
	if res != nil || !ok || v != 42 {
		t.Fatalf("json.Number must parse: v=%d ok=%v res=%v", v, ok, res)
	}
	_, _, res = parseOptionalPositiveIntValue(json.Number("1.5"), "timeout")
	if res == nil {
		t.Fatalf("non-integral json.Number must be rejected")
	}
}
