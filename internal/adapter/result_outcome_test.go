package adapter

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// outcomeResult is a failed result with no outputs carrying outcome, a JSON object.
func outcomeResult(outcome string) string {
	return `{"schema": "guildhall.artisan-result", "version": 1, "task_id": "task-1", "attempt_id": "attempt-1",
  "status": "failed", "summary": "Already delivered.", "outputs": [], "outcome": ` + outcome + `}`
}

// TestParseResultAcceptsEachOutcome: each outcome shape parses into Outcome, and its label names it.
func TestParseResultAcceptsEachOutcome(t *testing.T) {
	for raw, want := range map[string]struct {
		outcome Outcome
		label   string
	}{
		`{"kind": "already-delivered", "by_pr": "https://github.com/o/r/pull/351"}`: {Outcome{Kind: OutcomeAlreadyDelivered, ByPR: "https://github.com/o/r/pull/351"}, "already delivered by https://github.com/o/r/pull/351"},
		`{"kind": "already-delivered", "by_slice": 2}`:                              {Outcome{Kind: OutcomeAlreadyDelivered, BySlice: 2}, "already delivered by slice 2"},
		`{"kind": "nothing-to-do", "reason": "The flag is already the default."}`:   {Outcome{Kind: OutcomeNothingToDo, Reason: "The flag is already the default."}, "nothing to do: The flag is already the default."},
	} {
		r, err := ParseResult([]byte(outcomeResult(raw)), testID)
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if r.Outcome == nil || *r.Outcome != want.outcome || r.Outcome.Label() != want.label {
			t.Errorf("%s: outcome = %+v", raw, r.Outcome)
		}
	}
	if r, err := ParseResult([]byte(strings.Replace(validResult, `"succeeded"`, `"failed"`, 1)), testID); err != nil || r.Outcome != nil {
		t.Errorf("a result without an outcome = %+v, %v", r, err)
	}
}

// TestParseResultRefusesBadOutcomes: an unknown kind, a missing or extra field, and an outcome on a succeeded
// result or one with outputs are malformed.
func TestParseResultRefusesBadOutcomes(t *testing.T) {
	nothing := outcomeResult(`{"kind": "nothing-to-do", "reason": "none"}`)
	succeeded := strings.Replace(nothing, `"failed"`, `"succeeded"`, 1)
	withOutputs := strings.Replace(nothing, `"outputs": []`, `"outputs": [{"path": "spec.md"}]`, 1)
	for name, c := range map[string]struct{ data, reason string }{
		"unknown kind":     {outcomeResult(`{"kind": "obsolete", "reason": "x"}`), `outcome.kind "obsolete"`},
		"no kind":          {outcomeResult(`{"by_pr": "https://github.com/o/r/pull/1"}`), "outcome.kind is required"},
		"no by_pr":         {outcomeResult(`{"kind": "already-delivered"}`), "names the merged pull request in by_pr"},
		"both":             {outcomeResult(`{"kind": "already-delivered", "by_pr": "https://github.com/o/r/pull/1", "by_slice": 1}`), "not both"},
		"by_slice zero":    {outcomeResult(`{"kind": "already-delivered", "by_slice": 0}`), "must be a slice number"},
		"by_pr not a url":  {outcomeResult(`{"kind": "already-delivered", "by_pr": "#351"}`), "http or https URL"},
		"delivered reason": {outcomeResult(`{"kind": "already-delivered", "by_pr": "https://github.com/o/r/pull/1", "reason": "x"}`), "not reason"},
		"no reason":        {outcomeResult(`{"kind": "nothing-to-do"}`), "says why in reason"},
		"blank reason":     {outcomeResult(`{"kind": "nothing-to-do", "reason": "  "}`), "says why in reason"},
		"nothing-to-do pr": {outcomeResult(`{"kind": "nothing-to-do", "reason": "x", "by_pr": "https://github.com/o/r/pull/1"}`), "not by_pr or by_slice"},
		"unknown field":    {outcomeResult(`{"kind": "nothing-to-do", "reason": "x", "why": "y"}`), ""},
		"not an object":    {outcomeResult(`"nothing-to-do"`), ""},
		"succeeded":        {succeeded, `pairs with status "failed"`},
		"with outputs":     {withOutputs, "lists no outputs"},
	} {
		_, err := ParseResult([]byte(c.data), testID)
		if !errors.Is(err, ErrMalformedResult) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: err = %v; want malformed naming %q", name, err, c.reason)
		}
	}
}

// TestWriteResultKeepsTheOutcome: a written outcome reads back unchanged, and WriteResult refuses one ParseResult
// would.
func TestWriteResultKeepsTheOutcome(t *testing.T) {
	dir := t.TempDir()
	o := &Outcome{Kind: OutcomeAlreadyDelivered, ByPR: "https://github.com/o/r/pull/351"}
	path := filepath.Join(dir, "result.json")
	if err := WriteResult(path, Result{Identity: testID, Status: StatusFailed, Summary: "Already delivered.", Outcome: o}); err != nil {
		t.Fatal(err)
	}
	r, err := ReadResult(path, testID)
	if err != nil || r.Outcome == nil || *r.Outcome != *o {
		t.Fatalf("read back = %+v, %v", r, err)
	}
	err = WriteResult(filepath.Join(dir, "succeeded.json"), Result{Identity: testID, Status: StatusSucceeded, Summary: "done", Outcome: o})
	if !errors.Is(err, ErrMalformedResult) {
		t.Errorf("succeeded with an outcome: %v", err)
	}
}
