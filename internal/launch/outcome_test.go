package launch

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestAttemptOutcome is the translation table over every outcome state and
// result: none, succeeded, failed and a status the store does not record.
func TestAttemptOutcome(t *testing.T) {
	result := func(s adapter.Status) *adapter.Result { return &adapter.Result{Status: s} }
	results := map[string]*adapter.Result{
		"none":      nil,
		"succeeded": result(adapter.StatusSucceeded),
		"failed":    result(adapter.StatusFailed),
		"unknown":   result("blocked"),
	}
	want := map[artisan.OutcomeState]map[string]store.AttemptOutcome{
		artisan.Cancelled: {"none": store.AttemptStopped, "succeeded": store.AttemptStopped, "failed": store.AttemptStopped, "unknown": store.AttemptStopped},
		artisan.Failed:    {"none": store.AttemptFailed, "succeeded": store.AttemptFailed, "failed": store.AttemptFailed, "unknown": store.AttemptFailed},
		artisan.Finished:  {"succeeded": store.AttemptSucceeded, "failed": store.AttemptFailed},
		"":                {},
		"stuck":           {},
	}
	for state, row := range want {
		for name, r := range results {
			got, err := attemptOutcome(artisan.Outcome{State: state}, r)
			w, ok := row[name]
			switch {
			case ok && (err != nil || got != w):
				t.Errorf("%q with result %s = %q, %v; want %q", state, name, got, err, w)
			case !ok && err == nil:
				t.Errorf("%q with result %s = %q; want refused", state, name, got)
			}
		}
	}
}
