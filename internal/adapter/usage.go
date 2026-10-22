package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// UsageSchema is the schema of the usage file (docs/adapters.md#usage-file), with its version.
const (
	UsageSchema  = "guildhall.attempt-usage/"
	UsageVersion = 1
)

// UsageFileName is the usage file's name, beside the result file.
const UsageFileName = "usage.json"

// maxUsageBytes bounds a usage file.
const maxUsageBytes = 4 << 10

// ErrMalformedUsage reports a usage file that breaks the contract.
var ErrMalformedUsage = errors.New("malformed attempt usage")

// Measure says where a usage number came from.
type Measure string

// Measures. A number a runtime cannot report is unknown, never a guess.
const (
	// Measured numbers are the runtime's own count.
	Measured Measure = "measured"
	// Estimated numbers are computed, such as a cost priced from token counts.
	Estimated Measure = "estimated"
	// Unknown numbers were not reported; they are zero.
	Unknown Measure = "unknown"
)

// Usage is what one attempt's runtime reports it spent: its tokens and its cost in US dollars, each marked
// with where it came from. Unknown numbers are zero.
type Usage struct {
	Tokens                   Measure `json:"tokens"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	Cost                     Measure `json:"cost"`
	CostUSD                  float64 `json:"cost_usd"`
}

// UnknownUsage is the usage of an attempt whose runtime reported none.
var UnknownUsage = Usage{Tokens: Unknown, Cost: Unknown}

// Validate checks that both measures are known values, every number is finite and not negative, and an
// unknown measure's numbers are zero.
func (u Usage) Validate() error {
	for _, m := range []Measure{u.Tokens, u.Cost} {
		if m != Measured && m != Estimated && m != Unknown {
			return fmt.Errorf("measure %q must be %q, %q or %q", m, Measured, Estimated, Unknown)
		}
	}
	tokens := []int64{u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens}
	for _, n := range tokens {
		if n < 0 {
			return fmt.Errorf("token count %d is negative", n)
		}
		if u.Tokens == Unknown && n != 0 {
			return errors.New("unknown tokens must have zero counts")
		}
	}
	switch {
	case math.IsNaN(u.CostUSD) || math.IsInf(u.CostUSD, 0) || u.CostUSD < 0:
		return fmt.Errorf("cost %v must be a finite amount, not negative", u.CostUSD)
	case u.Cost == Unknown && u.CostUSD != 0:
		return errors.New("an unknown cost must be zero")
	}
	return nil
}

// usageFile is the usage file's shape: Usage's fields after the schema. strictjson decodes no embedded fields.
type usageFile struct {
	Schema                   string  `json:"schema"`
	Tokens                   Measure `json:"tokens"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	Cost                     Measure `json:"cost"`
	CostUSD                  float64 `json:"cost_usd"`
}

// UsageFile is the usage file for the result file resultFile: UsageFileName beside it.
func UsageFile(resultFile string) string {
	return filepath.Join(filepath.Dir(resultFile), UsageFileName)
}

// WriteUsage validates u and writes it as the usage file of resultFile, replacing any earlier one atomically.
func WriteUsage(resultFile string, u Usage) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedUsage, err)
	}
	data, err := json.MarshalIndent(usageFile{fmt.Sprintf("%s%d", UsageSchema, UsageVersion),
		u.Tokens, u.InputTokens, u.OutputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, u.Cost, u.CostUSD}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(UsageFile(resultFile), append(data, '\n'))
}

// ReadUsage reads the usage file of resultFile. A missing file is UnknownUsage with no error, since a runtime
// that cannot report usage writes none; a file that breaks the contract is an error matching
// ErrMalformedUsage.
func ReadUsage(resultFile string) (Usage, error) {
	f, err := atomicfile.Open(UsageFile(resultFile))
	if errors.Is(err, fs.ErrNotExist) {
		return UnknownUsage, nil
	}
	if err != nil {
		return Usage{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxUsageBytes+1))
	// The file was opened read-only, so a close error loses nothing.
	_ = f.Close()
	if err != nil {
		return Usage{}, err
	}
	var file usageFile
	if err := strictjson.Decode(data, &file, strictjson.Options{Schema: UsageSchema, Version: UsageVersion, MaxBytes: maxUsageBytes}); err != nil {
		return Usage{}, fmt.Errorf("%w: %v", ErrMalformedUsage, err)
	}
	u := Usage{file.Tokens, file.InputTokens, file.OutputTokens, file.CacheCreationInputTokens, file.CacheReadInputTokens, file.Cost, file.CostUSD}
	if err := u.Validate(); err != nil {
		return Usage{}, fmt.Errorf("%w: %v", ErrMalformedUsage, err)
	}
	return u, nil
}
