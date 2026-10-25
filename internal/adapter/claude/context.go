package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
)

// TranscriptContext reads Claude's JSONL transcript without model calls. It uses
// the latest main-session assistant usage: input plus both cache input counters,
// not cumulative usage or output tokens. A compaction boundary clears prior use.
// Unknown models have no report rather than a guessed context capacity. Lines
// are bounded to 8 MiB and total input to 128 MiB. Malformed rows are ignored.
func TranscriptContext(r io.Reader) (int, bool, error) {
	const maxBytes = 128 << 20
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	scan := bufio.NewScanner(limited)
	scan.Buffer(make([]byte, 64<<10), 8<<20)
	percent, known := 0, false
	for scan.Scan() {
		var row struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Sidechain bool   `json:"isSidechain"`
			Message   struct {
				Model string `json:"model"`
				Usage *struct {
					Input    int64 `json:"input_tokens"`
					Creation int64 `json:"cache_creation_input_tokens"`
					Read     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil || row.Sidechain {
			continue
		}
		if row.Type == "system" && row.Subtype == "compact_boundary" {
			percent, known = 0, false
			continue
		}
		u := row.Message.Usage
		if row.Type != "assistant" || u == nil {
			continue
		}
		percent, known = 0, false
		size := modelContextWindow(row.Message.Model)
		if size == 0 || u.Input < 0 || u.Creation < 0 || u.Read < 0 || u.Input > 10000000 || u.Creation > 10000000 || u.Read > 10000000 {
			continue
		}
		used := u.Input + u.Creation + u.Read
		if used == 0 {
			continue
		}
		percent = int(min(100.0, math.Round(float64(used)*100/float64(size))))
		known = true
	}
	if err := scan.Err(); err != nil {
		return 0, false, fmt.Errorf("claude transcript: %w", err)
	}
	if limited.N == 0 {
		return 0, false, fmt.Errorf("claude transcript exceeds 128 MiB")
	}
	return percent, known, nil
}

// Capacities follow the Anthropic direct API context-window table. Explicit
// [1m] aliases opt into 1M; unknown/provider-specific model IDs are not guessed.
func modelContextWindow(model string) int64 {
	if !strings.HasPrefix(model, "claude-") {
		return 0
	}
	oneMillion := strings.HasSuffix(model, "[1m]")
	twoHundred := strings.HasSuffix(model, "[200k]")
	name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(model, "claude-"), "[1m]"), "[200k]")
	// Dated IDs append exactly eight decimal digits; do not accept an unknown
	// version merely because it shares a prefix with an older supported family.
	if at := strings.LastIndexByte(name, '-'); at >= 0 {
		suffix := name[at+1:]
		if len(suffix) == 8 && strings.Trim(suffix, "0123456789") == "" {
			name = name[:at]
		}
	}
	var size int64
	switch name {
	case "opus-4-6", "opus-4-7", "opus-4-8", "opus-5", "opus-5-5", "sonnet-4-6", "sonnet-5", "sonnet-5-5", "haiku-5-5", "fable-5", "fable-5-1", "mythos-5", "mythos-5-1", "mythos-preview":
		size = 1000000
	case "opus-4", "opus-4-0", "opus-4-1", "opus-4-5", "sonnet-4", "sonnet-4-0", "sonnet-4-5", "haiku-4-5", "3-5-sonnet", "3-5-haiku", "3-7-sonnet", "3-opus", "3-sonnet", "3-haiku":
		size = 200000
	}
	if size != 0 {
		if oneMillion {
			return 1000000
		}
		if twoHundred {
			return 200000
		}
	}
	return size
}
