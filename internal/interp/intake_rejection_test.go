package interp_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/adapter"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

func assertRejected(t *testing.T, e *env, ev interp.Event, reason string) {
	t.Helper()
	next := e.advance(t, ev)
	if next != (interp.Next{Wait: interp.WaitDirector, Step: ev.Step, Attempt: ev.Attempt}) {
		t.Fatalf("rejection wait=%+v", next)
	}
	for _, a := range e.attempts(t) {
		if a.Step == ev.Step && a.Number == ev.Attempt {
			if a.Outcome != store.AttemptFailed || !strings.Contains(a.IntakeRejection, reason) {
				t.Fatalf("rejected attempt=%+v; want reason %q", a, reason)
			}
			return
		}
	}
	t.Fatal("rejected attempt absent")
}

type interruptedRejection struct {
	interp.Records
	after bool
}

func (r *interruptedRejection) RejectAttempt(id, repo, task, step string, n int, reason string) (store.Receipt, error) {
	if r.after {
		if _, err := r.Records.RejectAttempt(id, repo, task, step, n, reason); err != nil {
			return store.Receipt{}, err
		}
	}
	return store.Receipt{}, errors.New("interrupted rejection")
}

func TestRejectionRecovery(t *testing.T) {
	for _, b := range backends {
		for _, fault := range []string{"before commit", "after commit", "notification", "interrupt"} {
			t.Run(b.name+"/"+fault, func(t *testing.T) {
				e := newEnv(t, b, planExpectation(t, interptest.Succeed("missing document")))
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				if _, err := e.h.Records.FinishAttempt("launcher", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
					t.Fatal(err)
				}
				switch fault {
				case "before commit", "after commit":
					e.eng.Records = &interruptedRejection{Records: e.h.Records, after: fault == "after commit"}
				case "notification":
					e.eng.Outbox = &interruptedOutbox{Outbox: e.eng.Outbox, fail: true}
				}
				ctx := context.Background()
				if fault == "interrupt" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				ev := interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}
				if _, err := e.eng.Advance(ctx, ev); err == nil {
					t.Fatal("expected interrupted intake")
				}
				if fault == "before commit" || fault == "interrupt" {
					if a := e.attempts(t)[0]; a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "" {
						t.Fatalf("operational error classified as rejection: %+v", a)
					}
				}
				e.eng.Records = e.h.Records
				want := interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1}
				if got := e.advance(t, ev); got != want {
					t.Fatalf("recovery=%+v", got)
				}
				e.advance(t, ev)
				if m := e.messages(t); len(m) != 1 || !strings.Contains(m["app.t7.attempt-failed.plan-1"], "no output for port spec") {
					t.Fatalf("notifications=%+v", m)
				}
			})
		}
	}
}

func TestIntakeOperationalIOReplays(t *testing.T) {
	for _, b := range backends {
		for _, mode := range []string{"result file", "document directory"} {
			t.Run(b.name+"/"+mode, func(t *testing.T) {
				e := newEnv(t, b, straightScript(t)[:1]...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				if _, err := e.h.Records.FinishAttempt("launcher", "app", "t7", "plan", 1, store.AttemptSucceeded); err != nil {
					t.Fatal(err)
				}
				var restore func()
				if mode == "result file" {
					file := filepath.Join(e.eng.StateDir, "app", "t7", "plan-1", "result.json")
					if err := os.Rename(file, file+".saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(file, 0700); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(file+".saved", file); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					// A file blocks Guildhall's own document-directory creation.
					file := filepath.Join(e.eng.DocumentDir, "tasks", "app", "t7", "plan-1")
					if err := os.WriteFile(file, []byte("blocked"), 0600); err != nil {
						t.Fatal(err)
					}
					restore = func() {
						if err := os.Remove(file); err != nil {
							t.Fatal(err)
						}
					}
				}
				ev := interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}
				if _, err := e.eng.Advance(context.Background(), ev); err == nil {
					t.Fatal("expected operational I/O failure")
				}
				if a := e.attempts(t)[0]; a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "" {
					t.Fatalf("operational error rejected result: %+v", a)
				}
				if m := e.messages(t); len(m) != 0 {
					t.Fatalf("operational failure notified as rejected: %+v", m)
				}
				restore()
				if next := e.advance(t, ev); next.Wait != interp.WaitDecision {
					t.Fatalf("replay=%+v", next)
				}
			})
		}
	}
}

func TestInvalidResultOutputs(t *testing.T) {
	for _, b := range backends {
		for _, mode := range []string{"missing file", "directory", "malformed result", "invalid path"} {
			t.Run(b.name+"/"+mode, func(t *testing.T) {
				e := newEnv(t, b, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})))
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				file := filepath.Join(e.eng.StateDir, "app", "t7", "work", "spec.md")
				reason := "read output"
				switch mode {
				case "missing file", "directory":
					if err := os.Remove(file); err != nil {
						t.Fatal(err)
					}
					if mode == "directory" {
						if err := os.Mkdir(file, 0700); err != nil {
							t.Fatal(err)
						}
					}
				case "malformed result", "invalid path":
					result := filepath.Join(e.eng.StateDir, "app", "t7", "plan-1", "result.json")
					data := []byte(`{}`)
					if mode == "invalid path" {
						data = []byte(`{"schema":"guildhall.artisan-result","version":1,"task_id":"t7","attempt_id":"plan-1","status":"succeeded","summary":"planned","outputs":[{"path":"../private"}]}`)
					}
					if err := os.WriteFile(result, data, 0600); err != nil {
						t.Fatal(err)
					}
					reason = adapter.ErrMalformedResult.Error()
				}
				assertRejected(t, e, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}, reason)
				if _, ok := e.documents(t)["plan-1-spec"]; ok {
					t.Fatal("invalid output recorded")
				}
			})
		}
	}
}

// D3: a review result says succeeded with outputs [], and the launcher has
// already finished the attempt successfully before intake can validate it.
func TestRejectedLauncherSuccess(t *testing.T) {
	for _, b := range backends {
		for _, announced := range []bool{false, true} {
			name := b.name + "/task-start"
			if announced {
				name = b.name + "/step-done"
			}
			t.Run(name, func(t *testing.T) {
				script := straightScript(t)
				retry := script[2]
				retry.Attempt = 2
				script[2].Respond = interptest.Succeed("approved, but no verdict file")
				e := newEnv(t, b, append(script, retry)...)
				e.advance(t, interp.Event{Kind: interp.TaskCreated})
				next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
				e.answer(t, next.Decision, "approved")
				e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: next.Decision})
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				if _, err := e.h.Records.FinishAttempt("launcher-finish", "app", "t7", "review", 1, store.AttemptSucceeded); err != nil {
					t.Fatal(err)
				}
				ev := interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}
				if announced {
					ev.Kind = interp.AttemptResult
					ev.Step = "review"
					ev.Attempt = 1
				}
				next, err := e.eng.Advance(context.Background(), ev)
				if err != nil {
					t.Fatalf("rejected intake must wait for Director, not wedge at launcher success: %v", err)
				}
				want := interp.Next{Wait: interp.WaitDirector, Step: "review", Attempt: 1}
				if next != want {
					t.Fatalf("next=%+v, want %+v", next, want)
				}
				if a := e.attempts(t); a[len(a)-1].Outcome != store.AttemptFailed {
					t.Fatalf("attempts=%+v", a)
				}
				if got := e.advance(t, ev); got != want {
					t.Fatalf("replay=%+v", got)
				}
				messages := e.messages(t)
				count := 0
				for id, text := range messages {
					if strings.Contains(id, "attempt-failed.review-1") {
						count++
						if !strings.Contains(text, "no output for port outcome") || !strings.Contains(text, "Attempt 1 of step review") {
							t.Fatalf("message=%s", text)
						}
					}
				}
				if count != 1 {
					t.Fatalf("failure notifications=%d", count)
				}
				next = e.advance(t, interp.Event{Kind: interp.AttemptRetried, Step: "review", Attempt: 1})
				if next != (interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2}) {
					t.Fatalf("retry=%+v", next)
				}
				next = e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2})
				if next.Wait != interp.WaitDecision {
					t.Fatalf("valid retry=%+v", next)
				}
			})
		}
	}
}
