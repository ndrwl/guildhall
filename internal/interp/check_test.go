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
	"time"

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
					// The messages are sent before the result is published, so they count against it.
					e.h.Clock.Set(steerTime)
					for _, id := range []string{"steer-1", "steer-2"} {
						steer(t, e, id)
					}
					publishedAt(t, e, steerTime.Add(time.Minute))
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

// steerTime is when the late-steering tests send their messages; they set the result file's publication time around it.
var steerTime = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// steer sends the Director's steering message id to the plan step's attempt 1, at the records' clock.
func steer(t *testing.T, e *env, id string) {
	t.Helper()
	if _, err := e.h.SendMessage(store.Message{ID: id, Recipient: store.RecipientArtisan, Repository: "app", TaskID: "t7",
		Step: "plan", Attempt: 1, Type: "note", Text: "The base moved; rebase first.", Sender: store.SenderDirector}); err != nil {
		t.Fatal(err)
	}
}

// planResult is the path of the plan step's attempt 1 result file.
func planResult(e *env) string {
	return filepath.Join(e.eng.StateDir, "app", "t7", "plan-1", artisan.ResultFile)
}

// publishedAt sets the publication time, the modification time, of the plan step's attempt 1 result file.
func publishedAt(t *testing.T, e *env, at time.Time) {
	t.Helper()
	if err := os.Chtimes(planResult(e), at, at); err != nil {
		t.Fatal(err)
	}
}

// lateSteeringReports returns the texts of the Director's late-steering messages.
func lateSteeringReports(t *testing.T, e *env) []string {
	t.Helper()
	var out []string
	for id, text := range e.messages(t) {
		if strings.Contains(id, ".late-steering.") {
			out = append(out, text)
		}
	}
	return out
}

// TestLateSteeringDoesNotFailAGoodResult: a steering message sent well after the result was published does not count
// against it. CheckResult accepts it, intake succeeds, the message stays unacknowledged, and the Director gets one
// "arrived after the result" message naming it, which a replay does not send again.
func TestLateSteeringDoesNotFailAGoodResult(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			publishedAt(t, e, steerTime)
			// Well past the slack, so the message is clearly sent after publication.
			e.h.Clock.Set(steerTime.Add(time.Minute))
			steer(t, e, "steer-late")
			if said := checkBefore(t, e, "plan", 1); said == nil || said.reason != "" {
				t.Fatalf("CheckResult said %+v, want the result accepted", said)
			}
			ev := interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}
			e.advance(t, ev)
			for _, a := range e.attempts(t) {
				if a.Step == "plan" && a.Number == 1 && (a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "") {
					t.Fatalf("plan attempt 1 = %s, rejection %q; want it succeeded", a.Outcome, a.IntakeRejection)
				}
			}
			to := store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}
			if msgs, _, err := e.h.Records.Inbox(to, 0, 10); err != nil || len(msgs) != 1 || msgs[0].ID != "steer-late" {
				t.Fatalf("attempt inbox = %+v, %v; want steer-late still unacknowledged", msgs, err)
			}
			reports := lateSteeringReports(t, e)
			if len(reports) != 1 || !strings.Contains(reports[0], "steer-late") || !strings.Contains(reports[0], "arrived after the result") {
				t.Fatalf("late-steering reports = %q, want one naming steer-late", reports)
			}
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: ev.Kind, Repository: "app", Task: "t7", Step: "plan", Attempt: 1}); err != nil {
				t.Logf("replayed advance = %v", err)
			}
			if again := lateSteeringReports(t, e); len(again) != 1 {
				t.Fatalf("late-steering reports after a replay = %q, want the one", again)
			}
		})
	}
}

// TestCheckResultSkipsMessagesOfAnUnsteerableAttempt: an attempt whose adapter Engine.Steerable reports cannot be
// steered has its succeeded result accepted, by CheckResult and by intake, while a steering message to it is
// unacknowledged; an attempt whose adapter can be steered is still refused. The hook is asked the attempt's adapter.
func TestCheckResultSkipsMessagesOfAnUnsteerableAttempt(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			for _, steerable := range []bool{false, true} {
				t.Run(map[bool]string{false: "unsteerable", true: "steerable"}[steerable], func(t *testing.T) {
					e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
					var asked []string
					e.eng.Steerable = func(name string) bool {
						asked = append(asked, name)
						return steerable
					}
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					// The message is sent before the result is published, so it would count against a steerable attempt.
					e.h.Clock.Set(steerTime)
					steer(t, e, "steer-1")
					publishedAt(t, e, steerTime.Add(time.Minute))
					want := ""
					if steerable {
						want = "unacknowledged steering message to this attempt: steer-1; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again"
					}
					key := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}
					data, err := os.ReadFile(planResult(e))
					if err != nil {
						t.Fatal(err)
					}
					var rejection *interp.Rejection
					if err := e.eng.CheckResult(context.Background(), key, data); errors.As(err, &rejection) {
						if rejection.Reason != want {
							t.Fatalf("CheckResult = %+v, want reason %q", rejection, want)
						}
					} else if err != nil || want != "" {
						t.Fatalf("CheckResult = %v, want reason %q", err, want)
					}
					if len(asked) == 0 || asked[0] != interp.MockAdapter {
						t.Fatalf("Steerable was asked %q, want the attempt's adapter %q", asked, interp.MockAdapter)
					}
					said := checkBefore(t, e, "plan", 1)
					e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
					said.agrees(t, e, "plan", 1)
				})
			}
		})
	}
}

// TestSteeringAtOrBeforeTheResultRefuses: a message sent at the result's publication time counts against it, as does
// one whose send time is a little after a coarse publication time, and any message while no result is published, as
// when `guildhall result submit` checks a draft.
func TestSteeringAtOrBeforeTheResultRefuses(t *testing.T) {
	const reason = "unacknowledged steering message to this attempt: steer-1; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again"
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Run("sent at the cutoff", func(t *testing.T) {
				e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				e.h.Clock.Set(steerTime)
				steer(t, e, "steer-1")
				publishedAt(t, e, steerTime)
				said := checkBefore(t, e, "plan", 1)
				if said == nil || said.reason != reason {
					t.Fatalf("CheckResult said %+v, want %q", said, reason)
				}
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
				said.agrees(t, e, "plan", 1)
				if reports := lateSteeringReports(t, e); len(reports) != 0 {
					t.Fatalf("late-steering reports = %q, want none", reports)
				}
			})
			// The kernel stamps the result's mtime from its coarse tick clock, which can trail the fine-grained clock a
			// message's SentAt comes from, so a message sent just before publication can carry a later time.
			t.Run("sent just before a coarse publication time", func(t *testing.T) {
				e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				e.h.Clock.Set(steerTime)
				steer(t, e, "steer-1")
				publishedAt(t, e, steerTime.Add(-3*time.Millisecond))
				said := checkBefore(t, e, "plan", 1)
				if said == nil || said.reason != reason {
					t.Fatalf("CheckResult said %+v, want %q", said, reason)
				}
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
				said.agrees(t, e, "plan", 1)
				if reports := lateSteeringReports(t, e); len(reports) != 0 {
					t.Fatalf("late-steering reports = %q, want none", reports)
				}
			})
			t.Run("no result published", func(t *testing.T) {
				e, _, _ := newSliceEnv(t, b, true, sliceScript(t, summarisedSpec)...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				publishedAt(t, e, steerTime)
				data, err := os.ReadFile(planResult(e))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(planResult(e)); err != nil {
					t.Fatal(err)
				}
				e.h.Clock.Set(steerTime.Add(time.Hour))
				steer(t, e, "steer-1")
				var rejection *interp.Rejection
				err = e.eng.CheckResult(context.Background(), artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "plan", Attempt: 1}, data)
				if !errors.As(err, &rejection) || rejection.Reason != reason || !rejection.Unacknowledged {
					t.Fatalf("CheckResult = %v, want %q", err, reason)
				}
			})
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
