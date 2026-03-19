package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestParseOptionalTimeoutSecondsArg(t *testing.T) {
	t.Parallel()

	defaultTimeout := 30 * time.Second
	tests := []struct {
		name        string
		args        map[string]interface{}
		want        time.Duration
		wantErrText string
	}{
		{
			name: "missing uses default",
			args: map[string]interface{}{},
			want: defaultTimeout,
		},
		{
			name: "int preserves long timeout",
			args: map[string]interface{}{"timeout": 150},
			want: 150 * time.Second,
		},
		{
			name: "float64 preserves long timeout",
			args: map[string]interface{}{"timeout": 150.0},
			want: 150 * time.Second,
		},
		{
			name: "string preserves long timeout",
			args: map[string]interface{}{"timeout": "150"},
			want: 150 * time.Second,
		},
		{
			name: "zero falls back to default",
			args: map[string]interface{}{"timeout": 0},
			want: defaultTimeout,
		},
		{
			name:        "fractional values are rejected",
			args:        map[string]interface{}{"timeout": 1.5},
			want:        defaultTimeout,
			wantErrText: "Argument 'timeout' must be an integer",
		},
		{
			name:        "non-numeric strings are rejected",
			args:        map[string]interface{}{"timeout": "slow"},
			want:        defaultTimeout,
			wantErrText: "Argument 'timeout' must be an integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, invalid := parseOptionalTimeoutSecondsArg(tt.args, "timeout", defaultTimeout)
			if got != tt.want {
				t.Fatalf("expected timeout %v, got %v", tt.want, got)
			}

			if tt.wantErrText == "" {
				if invalid != nil {
					t.Fatalf("expected no error, got %+v", invalid)
				}
				return
			}

			if invalid == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErrText)
			}

			if len(invalid.Content) == 0 || strings.Contains(invalid.Content[0].Text, tt.wantErrText) == false {
				t.Fatalf("expected error text %q, got %+v", tt.wantErrText, invalid)
			}
		})
	}
}

func TestDeriveMirroredToolCallTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        map[string]interface{}
		want        time.Duration
		wantErrText string
	}{
		{
			name: "default stays at 30 seconds",
			args: map[string]interface{}{},
			want: 30 * time.Second,
		},
		{
			name: "timeoutMs extends proxy timeout with headroom",
			args: map[string]interface{}{"timeoutMs": 120000},
			want: 125 * time.Second,
		},
		{
			name: "timeout seconds can also extend proxy timeout",
			args: map[string]interface{}{"timeout": 45},
			want: 45 * time.Second,
		},
		{
			name: "longer timeoutMs wins over timeout seconds",
			args: map[string]interface{}{"timeout": 45, "timeoutMs": 120000},
			want: 125 * time.Second,
		},
		{
			name:        "invalid timeoutMs is rejected",
			args:        map[string]interface{}{"timeoutMs": 1.5},
			want:        30 * time.Second,
			wantErrText: "Argument 'timeoutMs' must be an integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, invalid := deriveMirroredToolCallTimeout(tt.args, 30*time.Second)
			if got != tt.want {
				t.Fatalf("expected timeout %v, got %v", tt.want, got)
			}

			if tt.wantErrText == "" {
				if invalid != nil {
					t.Fatalf("expected no error, got %+v", invalid)
				}
				return
			}

			if invalid == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErrText)
			}

			if len(invalid.Content) == 0 || strings.Contains(invalid.Content[0].Text, tt.wantErrText) == false {
				t.Fatalf("expected error text %q, got %+v", tt.wantErrText, invalid)
			}
		})
	}
}
