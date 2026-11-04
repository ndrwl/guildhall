package interp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// recordsSnapshot is everything of the task a result check must leave as it found it.
type recordsSnapshot struct {
	attempts  []store.StageAttempt
	documents map[string]store.DocumentRef
	messages  map[string]string
}

func recordsOf(t *testing.T, e *env) recordsSnapshot {
	t.Helper()
	return recordsSnapshot{e.attempts(t), e.documents(t), e.messages(t)}
}

// checkBefore runs Engine.CheckResult on the published result of attempt n of step, while it is still running, and
// returns what it said, nil when the attempt has no result file or already has an outcome. It fails t if the check
// changed the task's records or its result file.
func checkBefore(t *testing.T, e *env, step string, n int) *checkSaid {
	t.Helper()
	file := filepath.Join(e.eng.StateDir, "app", "t7", step+"-"+strconv.Itoa(n), artisan.ResultFile)
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	for _, a := range e.attempts(t) {
		if a.Step == step && a.Number == n && a.Outcome != "" {
			return nil
		}
	}
	before := recordsOf(t, e)
	err = e.eng.CheckResult(context.Background(), artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: step, Attempt: n}, data)
	if after := recordsOf(t, e); !reflect.DeepEqual(before, after) {
		t.Fatalf("CheckResult changed the records:\nbefore %+v\nafter  %+v", before, after)
	}
	if again, _ := os.ReadFile(file); string(again) != string(data) {
		t.Fatal("CheckResult changed the result file")
	}
	said := &checkSaid{}
	var rejection *interp.Rejection
	switch {
	case errors.As(err, &rejection):
		said.reason = rejection.Reason
	case err != nil:
		t.Fatalf("CheckResult = %v, want a rejection or nil", err)
	}
	t.Logf("CheckResult of %s-%d said %q", step, n, said.reason)
	return said
}

// checkSaid is what a result check said: reason is the rejection, "" for an accepted result.
type checkSaid struct{ reason string }

// agrees fails t unless intake gave attempt n of step the outcome the check said it would: the same rejection
// reason, or none.
func (c *checkSaid) agrees(t *testing.T, e *env, step string, n int) {
	t.Helper()
	if c == nil {
		return
	}
	for _, a := range e.attempts(t) {
		if a.Step == step && a.Number == n {
			if a.IntakeRejection != c.reason {
				t.Fatalf("intake rejection %q, but CheckResult said %q", a.IntakeRejection, c.reason)
			}
			return
		}
	}
	t.Fatalf("attempt %d of %s absent", n, step)
}

// TestCheckResultMatchesIntake: for a spec intake accepts and for each it refuses, CheckResult on the running
// attempt's result says what intake then records, and leaves the records unchanged.
func TestCheckResultMatchesIntake(t *testing.T) {
	for _, c := range []struct{ name, spec string }{
		{"accepted", summarisedSpec},
		{"preamble before Q1", strings.Replace(summarisedSpec, "**Q1.", "These are the questions.\n\n**Q1.", 1)},
		{"no summary", noSummary},
		{"a gap", strings.Replace(summarisedSpec, "**Q3.", "**Q4.", 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, backends[1], true, sliceScript(t, c.spec)...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			said := checkBefore(t, e, "plan", 1)
			if said == nil {
				t.Fatal("the plan attempt was not checked")
			}
			if (said.reason == "") != (c.name == "accepted") {
				t.Fatalf("CheckResult said %q", said.reason)
			}
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			said.agrees(t, e, "plan", 1)
		})
	}
}

// TestCheckResultRefusesUnacknowledgedMessages: while a steering message to the running attempt is unacknowledged,
// CheckResult refuses a succeeded result naming it, and intake records the same reason; once it is acknowledged the
// same result is accepted. A message to another attempt does not count.
func TestCheckResultRefusesUnacknowledgedMessages(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			for _, ack := range []bool{false, true} {
				t.Run(map[bool]string{false: "unacknowledged", true: "acknowledged"}[ack], func(t *testing.T) {
					e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					to := store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}
					for _, id := range []string{"steer-1", "steer-2"} {
						if _, err := e.h.SendMessage(store.Message{ID: id, Recipient: store.RecipientArtisan, Repository: "app", TaskID: "t7",
							Step: "plan", Attempt: 1, Type: "note", Text: "The base moved; rebase first.", Sender: store.SenderDirector}); err != nil {
							t.Fatal(err)
						}
					}
					want := "unacknowledged steering messages to this attempt: steer-1, steer-2; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again"
					if ack {
						for _, id := range []string{"steer-1", "steer-2"} {
							if _, err := e.h.AckMessage(to, id); err != nil {
								t.Fatal(err)
							}
						}
						want = ""
					}
					key := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}
					data, err := os.ReadFile(filepath.Join(e.eng.StateDir, "app", "t7", "plan-1", artisan.ResultFile))
					if err != nil {
						t.Fatal(err)
					}
					var rejection *interp.Rejection
					if err := e.eng.CheckResult(context.Background(), key, data); errors.As(err, &rejection) {
						if rejection.Reason != want || !rejection.Unacknowledged {
							t.Fatalf("CheckResult = %+v, want reason %q, unacknowledged", rejection, want)
						}
					} else if err != nil || want != "" {
						t.Fatalf("CheckResult = %v, want reason %q", err, want)
					}
					said := checkBefore(t, e, "plan", 1)
					e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
					said.agrees(t, e, "plan", 1)
				})
			}
		})
	}
}

// TestCheckResultRefusesAnAttemptNotRunning: a finished attempt, an unknown one and one on an unknown step are
// ErrAttemptNotRunning, never a rejection.
func TestCheckResultRefusesAnAttemptNotRunning(t *testing.T) {
	e := newEnv(t, backends[1], planExpectation(t, interptest.Succeed("missing document")))
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	data := []byte(`{}`)
	for _, key := range []artisan.AttemptKey{
		{Repository: "app", TaskID: "t7", Step: "plan", Attempt: 2},
		{Repository: "app", TaskID: "t7", Step: "nowhere", Attempt: 1},
	} {
		if err := e.eng.CheckResult(context.Background(), key, data); !errors.Is(err, interp.ErrAttemptNotRunning) {
			t.Errorf("CheckResult(%s) = %v, want ErrAttemptNotRunning", key, err)
		}
	}
	if _, err := e.h.Records.FinishAttempt("launcher", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	key := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}
	if err := e.eng.CheckResult(context.Background(), key, data); !errors.Is(err, interp.ErrAttemptNotRunning) {
		t.Errorf("CheckResult of a finished attempt = %v, want ErrAttemptNotRunning", err)
	}
}
