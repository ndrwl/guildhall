package store

import (
	"encoding/json"
	"fmt"
	"math"

	bolt "go.etcd.io/bbolt"
)

// Measure says where a spend number came from (docs/store.md#spend).
type Measure string

// Measures. A number the runtime did not report is unknown and zero, never a guess.
const (
	SpendMeasured  Measure = "measured"
	SpendEstimated Measure = "estimated"
	SpendUnknown   Measure = "unknown"
)

// Spend is what one attempt's runtime reported it spent: tokens and a cost in US dollars, each with its Measure.
// Unknown numbers are zero.
type Spend struct {
	Tokens                   Measure `json:"tokens"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	Cost                     Measure `json:"cost"`
	CostUSD                  float64 `json:"cost_usd"`
}

// UnknownSpend is the spend of an attempt whose runtime reported none.
var UnknownSpend = Spend{Tokens: SpendUnknown, Cost: SpendUnknown}

// SpendTotal sums the spend of several attempts. Attempts counts the attempts added; TokensUnknown and
// CostUnknown count those of them whose tokens or cost are unknown, which add nothing to the sums. Tokens and
// Cost are the sums' measures: unknown when no attempt's number is known, estimated when any known one is
// estimated, otherwise measured.
type SpendTotal struct {
	Attempts                 int     `json:"attempts"`
	TokensUnknown            int     `json:"tokens_unknown"`
	CostUnknown              int     `json:"cost_unknown"`
	Tokens                   Measure `json:"tokens"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	Cost                     Measure `json:"cost"`
	CostUSD                  float64 `json:"cost_usd"`
}

// NoSpend is the total of no attempts, which every total starts from: its measures are unknown.
var NoSpend = SpendTotal{Tokens: SpendUnknown, Cost: SpendUnknown}

// AddAttempt adds a's spend to t when a has finished or has a recorded spend; a finished attempt without a
// recorded spend counts as unknown, and a running attempt without one is not counted.
func (t *SpendTotal) AddAttempt(a StageAttempt) {
	sp := a.Spend
	switch {
	case sp == nil && a.Outcome == "":
		return
	case sp == nil:
		sp = &UnknownSpend
	}
	t.Attempts++
	if sp.Tokens == SpendUnknown {
		t.TokensUnknown++
	} else {
		t.Tokens = sumMeasure(t.Tokens, sp.Tokens)
		t.InputTokens += sp.InputTokens
		t.OutputTokens += sp.OutputTokens
		t.CacheCreationInputTokens += sp.CacheCreationInputTokens
		t.CacheReadInputTokens += sp.CacheReadInputTokens
	}
	if sp.Cost == SpendUnknown {
		t.CostUnknown++
	} else {
		t.Cost = sumMeasure(t.Cost, sp.Cost)
		t.CostUSD += sp.CostUSD
	}
}

// sumMeasure is the measure of a sum measured as total so far with a known number measured as m added.
func sumMeasure(total, m Measure) Measure {
	if total == SpendEstimated || m == SpendEstimated {
		return SpendEstimated
	}
	return SpendMeasured
}

func validateSpend(sp Spend) error {
	for _, m := range []Measure{sp.Tokens, sp.Cost} {
		if m != SpendMeasured && m != SpendEstimated && m != SpendUnknown {
			return invalid("spend measure %q must be %q, %q or %q", m, SpendMeasured, SpendEstimated, SpendUnknown)
		}
	}
	for _, n := range []int64{sp.InputTokens, sp.OutputTokens, sp.CacheCreationInputTokens, sp.CacheReadInputTokens} {
		if n < 0 || sp.Tokens == SpendUnknown && n != 0 {
			return invalid("token count %d must not be negative, and must be zero when tokens are unknown", n)
		}
	}
	if math.IsNaN(sp.CostUSD) || math.IsInf(sp.CostUSD, 0) || sp.CostUSD < 0 || sp.Cost == SpendUnknown && sp.CostUSD != 0 {
		return invalid("cost %v must be finite and not negative, and zero when the cost is unknown", sp.CostUSD)
	}
	return nil
}

// RecordAttemptSpend records the spend of attempt number of step, running or finished. The first record
// stands: recording an equal spend again under another mutation id returns the first record's receipt and
// writes nothing, and a different spend is refused with ErrInvalid. An invalid spend is refused with ErrInvalid.
func (s *Store) RecordAttemptSpend(mutationID, repository, taskID, step string, number int, sp Spend) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	if number < 1 {
		return Receipt{}, invalid("attempt number %d must be positive", number)
	}
	if err := validateSpend(sp); err != nil {
		return Receipt{}, err
	}
	req := StageAttempt{Repository: repository, TaskID: taskID, Step: step, Number: number, Spend: &sp}
	return s.mutate(mutationID, "record-attempt-spend", req, func(tx *bolt.Tx, r *Receipt) error {
		r.Repository, r.TaskID, r.Attempt, r.Record = repository, taskID, number, attemptRecord(step, number)
		attempts := repoBucket(tx, repository, bucketAttempts)
		if attempts == nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, ErrNotFound)
		}
		key := attemptKey(taskID, step, number)
		var a StageAttempt
		if err := getJSON(attempts, key, &a); err != nil {
			return fmt.Errorf("attempt %d of %s/%s step %q: %w", number, repository, taskID, step, err)
		}
		if a.Spend != nil {
			if *a.Spend == sp {
				if first, ok := earlierReceipt(tx, "record-attempt-spend", r.RequestDigest); ok {
					return standing{first}
				}
			}
			return invalid("attempt %d of step %q already has its spend recorded", number, step)
		}
		a.Spend = &sp
		return putJSON(attempts, key, a)
	})
}

// earlierReceipt finds the receipt of the op mutation with requestDigest.
func earlierReceipt(tx *bolt.Tx, op, requestDigest string) (Receipt, bool) {
	c := tx.Bucket(bucketReceipts).Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var r Receipt
		if json.Unmarshal(v, &r) == nil && r.Operation == op && r.RequestDigest == requestDigest {
			return r, true
		}
	}
	return Receipt{}, false
}
