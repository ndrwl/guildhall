package adapter

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var claudeLikeUsage = Usage{Tokens: Measured, InputTokens: 120, OutputTokens: 45, CacheCreationInputTokens: 300, CacheReadInputTokens: 2000, Cost: Estimated, CostUSD: 0.0125}

func TestUsageFileIsBesideTheResult(t *testing.T) {
	result := filepath.Join(t.TempDir(), "plan-1", "result.json")
	if got, want := UsageFile(result), filepath.Join(filepath.Dir(result), "usage.json"); got != want {
		t.Fatalf("UsageFile = %q, want %q", got, want)
	}
}

func TestUsageRoundTrips(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result.json")
	for _, u := range []Usage{claudeLikeUsage, UnknownUsage, {Tokens: Measured, InputTokens: 1, Cost: Unknown}} {
		if err := WriteUsage(result, u); err != nil {
			t.Fatalf("WriteUsage(%+v) = %v", u, err)
		}
		got, err := ReadUsage(result)
		if err != nil || got != u {
			t.Fatalf("ReadUsage = %+v, %v; want %+v", got, err, u)
		}
	}
	data, err := os.ReadFile(UsageFile(result))
	if err != nil || !strings.Contains(string(data), `"schema": "guildhall.attempt-usage/1"`) {
		t.Fatalf("usage file = %s, %v; want the versioned schema", data, err)
	}
}

func TestMissingUsageIsUnknown(t *testing.T) {
	got, err := ReadUsage(filepath.Join(t.TempDir(), "result.json"))
	if err != nil || got != UnknownUsage {
		t.Fatalf("ReadUsage without a file = %+v, %v; want UnknownUsage", got, err)
	}
}

func TestUsageValidateRefusesGuesses(t *testing.T) {
	cases := map[string]Usage{
		"no measures":             {},
		"unknown measure value":   {Tokens: "guessed", Cost: Unknown},
		"unknown tokens counted":  {Tokens: Unknown, InputTokens: 5, Cost: Unknown},
		"unknown cost priced":     {Tokens: Unknown, Cost: Unknown, CostUSD: 0.5},
		"negative tokens":         {Tokens: Measured, OutputTokens: -1, Cost: Unknown},
		"negative cost":           {Tokens: Unknown, Cost: Estimated, CostUSD: -0.01},
		"not a number cost":       {Tokens: Unknown, Cost: Estimated, CostUSD: math.NaN()},
		"infinite cost":           {Tokens: Unknown, Cost: Measured, CostUSD: math.Inf(1)},
		"cache tokens negative":   {Tokens: Measured, CacheReadInputTokens: -3, Cost: Unknown},
		"cache creation negative": {Tokens: Estimated, CacheCreationInputTokens: -3, Cost: Unknown},
	}
	for name, u := range cases {
		if err := u.Validate(); err == nil {
			t.Errorf("%s: Validate(%+v) = nil, want a refusal", name, u)
		}
		if err := WriteUsage(filepath.Join(t.TempDir(), "result.json"), u); err == nil {
			t.Errorf("%s: WriteUsage wrote an invalid usage", name)
		}
	}
	for _, u := range []Usage{claudeLikeUsage, UnknownUsage} {
		if err := u.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v", u, err)
		}
	}
}

func TestReadUsageRefusesMalformed(t *testing.T) {
	cases := map[string]string{
		"not json":       `{"schema":`,
		"no schema":      `{"tokens":"unknown","input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0}`,
		"newer version":  `{"schema":"guildhall.attempt-usage/2","tokens":"unknown","input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0}`,
		"unknown field":  `{"schema":"guildhall.attempt-usage/1","tokens":"unknown","input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0,"model":"x"}`,
		"guessed tokens": `{"schema":"guildhall.attempt-usage/1","tokens":"unknown","input_tokens":9,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cost":"unknown","cost_usd":0}`,
	}
	for name, body := range cases {
		result := filepath.Join(t.TempDir(), "result.json")
		if err := os.WriteFile(UsageFile(result), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if u, err := ReadUsage(result); !errors.Is(err, ErrMalformedUsage) {
			t.Errorf("%s: ReadUsage = %+v, %v; want ErrMalformedUsage", name, u, err)
		}
	}
}
