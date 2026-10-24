package claude

import (
	"strings"
	"testing"
)

func TestTranscriptContextUsesLatestMainUsageNotTotals(t *testing.T) {
	transcript := `{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":10,"cache_creation_input_tokens":20000,"cache_read_input_tokens":123990,"output_tokens":10000}}}
{"type":"assistant","isSidechain":true,"message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":190000}}}
{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":1000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":138000,"output_tokens":5000}}}
`
	n, ok, err := TranscriptContext(strings.NewReader(transcript))
	if err != nil || !ok || n != 70 {
		t.Fatalf("context=%d,%v,%v; want 70%% (input+cache, not output or cumulative)", n, ok, err)
	}
}

func TestTranscriptContextWindowsAndCompaction(t *testing.T) {
	for _, tt := range []struct {
		line    string
		percent int
		known   bool
	}{
		{`{"type":"assistant","message":{"model":"claude-opus-4-6","usage":{"input_tokens":720000}}}`, 72, true},
		{`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","usage":{"input_tokens":144000}}}`, 72, true},
		{`{"type":"assistant","message":{"model":"unknown","usage":{"input_tokens":144000}}}`, 0, false},
		{`{"type":"assistant","message":{"model":"claude-opus-4-6","usage":{"input_tokens":-1}}}`, 0, false},
		{`{"type":"system","subtype":"compact_boundary"}`, 0, false},
		{`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":0}}}`, 0, false},
		{`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":10000001}}}`, 0, false},
	} {
		n, ok, err := TranscriptContext(strings.NewReader(`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":144000}}}` + "\n" + tt.line + "\n"))
		if err != nil || n != tt.percent || ok != tt.known {
			t.Errorf("%s: %d %v %v", tt.line, n, ok, err)
		}
	}
}
