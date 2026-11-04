package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
)

// startEnv is newValidateEnv with a one-minute checks refresh and a clock at missingStart, at validate waiting on
// head 1's absent checks.
func startEnv(t *testing.T, b backend) (*env, *forgetest.Fake, *time.Time) {
	t.Helper()
	e, f := newValidateEnv(t, b)
	now := missingStart
	e.eng.Now = func() time.Time { return now }
	e.eng.ChecksRefresh = time.Minute
	reopen(e)
	want(t, e.toValidate(t), waitChecks)
	return e, f, &now
}

// reopen opts the env's repository in to ci_retrigger = reopen.
func reopen(e *env) {
	e.eng.Config.Repositories[0].CIRetrigger = config.CIRetriggerReopen
}

var (
	missingID  = "app.t7.checks-missing.validate." + head[:12]
	afterStart = missingID + ".after-start"
	startName  = "validate-ci-start-" + head[:12]
)

// startCall is the one StartCI call wanted for head h of pull request 1, whose comment names the head.
func startCall(t *testing.T, calls []forgetest.StartCall, h string) {
	t.Helper()
	if len(calls) != 1 || calls[0].Remote != repoRemote || calls[0].Number != 1 || calls[0].Head != h ||
		!strings.Contains(calls[0].Comment, "closes and reopens this pull request to start its CI") || !strings.Contains(calls[0].Comment, h) {
		t.Fatalf("StartCI calls = %+v, want one closing and reopening pull request 1 at %s", calls, h)
	}
}

// TestValidateStartsCI: a head that reports no check for ci_start_grace has its pull request's CI re-triggered once,
// by closing and reopening the pull request, its record written before the forge is asked and its outcome after; the
// checks-missing message says so. A run that never appears is reported once more ci_start_grace after the start, and
// never again, and nothing is started again.
func TestValidateStartsCI(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, now := startEnv(t, b)
			if sent := e.minutes(t, now, 9, waitChecks); len(sent) != 0 || len(f.StartCalls()) != 0 {
				t.Fatalf("within the grace: sent %q, started %v; want nothing", sent, f.StartCalls())
			}
			if sent := e.minutes(t, now, 1, waitChecks); !slices.Equal(sent, []string{missingID}) {
				t.Fatalf("at the grace: sent %q, want only %s", sent, missingID)
			}
			startCall(t, f.StartCalls(), head)
			if text := e.messages(t)[missingID]; !strings.Contains(text, "Guildhall closed and reopened the pull request") {
				t.Errorf("checks-missing = %q, want the re-triggered run", text)
			}
			rec := e.pullRequestDoc(t, startName)
			if rec["schema"] != "guildhall.ci-start/1" || rec["head"] != head || rec["pull_request"] != float64(1) || rec["url"] != "https://forge.invalid/1" ||
				rec["step"] != "validate" || rec["at"] != missingStart.Add(10*time.Minute).Format(time.RFC3339) {
				t.Errorf("%s = %v", startName, rec)
			}
			if out := e.pullRequestDoc(t, startName+"-outcome"); out["outcome"] != "started" || out["record"] != startName {
				t.Errorf("%s-outcome = %v", startName, out)
			}
			if d := e.documents(t)[startName]; d.Path != "tasks/app/t7/"+startName+"/start.json" {
				t.Errorf("%s = %+v", startName, d)
			}
			if sent := e.minutes(t, now, 9, waitChecks); len(sent) != 0 {
				t.Fatalf("after the start, within the grace: sent %q", sent)
			}
			if sent := e.minutes(t, now, 1, waitChecks); !slices.Equal(sent, []string{afterStart}) {
				t.Fatalf("a grace after the start: sent %q, want only %s", sent, afterStart)
			}
			if text := e.messages(t)[afterStart]; !strings.Contains(text, "the run did not appear") || !strings.Contains(text, head) {
				t.Errorf("after-start message = %q", text)
			}
			if sent := e.minutes(t, now, 200, waitChecks); len(sent) != 0 {
				t.Fatalf("200 more minutes sent %q", sent)
			}
			if n := len(f.StartCalls()); n != 1 {
				t.Fatalf("StartCI called %d times, want once", n)
			}
		})
	}
}

// TestValidateDispatchRunIsNotThePullRequestsCI is d176's regression for the run the Director dispatched on PR #303's
// head: checks a workflow_dispatch run left on the head, which GitHub does not link to the pull request, neither pass
// validate nor stop the start. The step waits on the pull request's own checks, re-triggers them by closing and
// reopening the pull request once ci_start_grace has passed, and passes on the reopened run's checks.
func TestValidateDispatchRunIsNotThePullRequestsCI(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			now := missingStart
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			reopen(e)
			f.SetUnlinkedChecks(repoRemote, head, ciPassed...)
			want(t, e.toValidate(t), waitChecks)
			if sent := e.minutes(t, &now, 9, waitChecks); len(sent) != 0 || len(f.StartCalls()) != 0 {
				t.Fatalf("within the grace: sent %q, started %v; want nothing", sent, f.StartCalls())
			}
			f.SetStartChecks(ciPassed...)
			if sent := e.minutes(t, &now, 1, waitChecks); !slices.Equal(sent, []string{missingID}) {
				t.Fatalf("at the grace: sent %q, want only %s", sent, missingID)
			}
			startCall(t, f.StartCalls(), head)
			now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := e.validationDoc(t, "validate-1-result"); !equalResult(got, result(head, "passed", ciPassed)) {
				t.Errorf("validate-1-result = %+v", got)
			}
		})
	}
}

// TestValidateStartedRunPasses: the reopened pull request's run attaches its checks to the head, and the step goes
// on as for any checks: they pass and the merge decision opens, with no after-start message.
func TestValidateStartedRunPasses(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, now := startEnv(t, b)
			f.SetStartChecks(ciPassed...)
			e.minutes(t, now, 10, waitChecks)
			*now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := e.validationDoc(t, "validate-1-result"); !equalResult(got, result(head, "passed", ciPassed)) {
				t.Errorf("validate-1-result = %+v", got)
			}
			*now = now.Add(time.Hour)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if _, ok := e.messages(t)[afterStart]; ok {
				t.Error("a run whose checks appeared sent the after-start message")
			}
		})
	}
}

// TestValidateStartOutcomes: each outcome StartCI answers and a refusal give the checks-missing text; only a start
// that may have run is reported again after the grace.
func TestValidateStartOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome forge.StartOutcome
		err     error
		text    string
		again   bool
	}{
		{"started", forge.Started, nil, "whose reopened event runs its CI on this same head", true},
		{"head moved", forge.HeadMoved, nil, "The pull request is no longer at the head, so nothing was started", false},
		{"refused", "", errors.New("pull request https://forge.invalid/1 is left closed, reopen it: reopening it: X could not be reopened"),
			"Closing and reopening the pull request to start its CI failed: pull request https://forge.invalid/1 is left closed, reopen it", false},
	} {
		for _, b := range backends {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				e, f, now := startEnv(t, b)
				f.ScriptStartCI(tc.outcome, tc.err)
				e.minutes(t, now, 10, waitChecks)
				if text := e.messages(t)[missingID]; !strings.Contains(text, tc.text) {
					t.Errorf("checks-missing = %q, want %q", text, tc.text)
				}
				out := e.pullRequestDoc(t, startName+"-outcome")
				if tc.err != nil && out["error"] != tc.err.Error() || tc.err == nil && out["outcome"] != string(tc.outcome) {
					t.Errorf("outcome = %v", out)
				}
				e.minutes(t, now, 30, waitChecks)
				if _, ok := e.messages(t)[afterStart]; ok != tc.again {
					t.Errorf("after-start message sent = %v, want %v", ok, tc.again)
				}
				if n := len(f.StartCalls()); n != 1 {
					t.Errorf("StartCI called %d times, want once", n)
				}
			})
		}
	}
}

// crashStarter is a forge whose StartCI stops the advance, as a crash between the start record and the call would,
// without calling through.
type crashStarter struct {
	*forgetest.Fake
	stop  context.CancelFunc
	calls int
}

func (c *crashStarter) StartCI(ctx context.Context, _ forge.Retrigger) (forge.StartOutcome, error) {
	c.calls++
	c.stop()
	return "", ctx.Err()
}

// TestValidateStartCrash: an advance stopped after the start record and before the forge answered never asks the
// forge again; the checks-missing message says the outcome is unknown, and the after-start bound still applies.
func TestValidateStartCrash(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, now := startEnv(t, b)
			e.minutes(t, now, 9, waitChecks)
			ctx, stop := context.WithCancel(context.Background())
			cs := &crashStarter{Fake: f, stop: stop}
			e.eng.Forge = cs
			*now = now.Add(time.Minute)
			if _, err := e.eng.Advance(ctx, interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("crashed advance = %v, want it cancelled", err)
			}
			if _, ok := e.documents(t)[startName]; !ok {
				t.Fatal("the start was not recorded before the forge was asked")
			}
			if _, ok := e.documents(t)[startName+"-outcome"]; ok {
				t.Fatal("a crashed start recorded an outcome")
			}
			e.minutes(t, now, 1, waitChecks)
			if text := e.messages(t)[missingID]; !strings.Contains(text, "whether it did is not recorded") || !strings.Contains(text, "Check that the pull request is open") {
				t.Errorf("checks-missing = %q, want the unknown outcome", text)
			}
			e.minutes(t, now, 200, waitChecks)
			if cs.calls != 1 || len(f.StartCalls()) != 0 {
				t.Errorf("StartCI calls after the crash = %d, %v; want the one crashed call", cs.calls, f.StartCalls())
			}
			if _, ok := e.messages(t)[afterStart]; !ok {
				t.Error("an unknown start that never showed checks sent no after-start message")
			}
		})
	}
}

// noStarter is the Fake without its CIStarter capability.
type noStarter struct{ readingForge }

// TestValidateNoStart: on a forge that cannot start CI, nothing is started or recorded and the checks-missing message
// says why.
func TestValidateNoStart(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, now := startEnv(t, b)
			e.eng.Forge = noStarter{f}
			e.minutes(t, now, 40, waitChecks)
			if n := len(f.StartCalls()); n != 0 {
				t.Errorf("StartCI called %d times", n)
			}
			if _, ok := e.documents(t)[startName]; ok {
				t.Error("a start was recorded")
			}
			if text := e.messages(t)[missingID]; !strings.Contains(text, "this forge cannot start a CI run") || !strings.Contains(text, "Close and reopen the pull request") {
				t.Errorf("checks-missing = %q", text)
			}
			if _, ok := e.messages(t)[afterStart]; ok {
				t.Error("no start sent an after-start message")
			}
		})
	}
}

// TestValidateZeroGraceAfterStart: with ci_start_grace = "0s", the advance that closes and reopens the pull request
// does not also report that the run did not appear; a later advance with still no check does, once.
func TestValidateZeroGraceAfterStart(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			now := missingStart
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			e.eng.CIStartGrace = -1
			reopen(e)
			want(t, e.toValidate(t), waitChecks)
			startCall(t, f.StartCalls(), head)
			if _, ok := e.messages(t)[afterStart]; ok {
				t.Fatal("the after-start message was sent in the advance that started the run")
			}
			if sent := e.minutes(t, &now, 1, waitChecks); !slices.Equal(sent, []string{afterStart}) {
				t.Fatalf("the next minute sent %q, want only %s", sent, afterStart)
			}
			if sent := e.minutes(t, &now, 20, waitChecks); len(sent) != 0 || len(f.StartCalls()) != 1 {
				t.Fatalf("20 more minutes sent %q, started %d times", sent, len(f.StartCalls()))
			}
		})
	}
}

// TestValidateNoRetriggerWithoutOptInis review 2's f1 regression: closing and reopening a pull request notifies
// its subscribers and fires its closed workflows, so a repository that does not set ci_retrigger = "reopen", or sets
// it off, has nothing started or recorded on a forge that can start CI; the checks-missing message names the key.
func TestValidateNoRetriggerWithoutOptIn(t *testing.T) {
	for _, policy := range []config.CIRetrigger{"", config.CIRetriggerOff} {
		for _, b := range backends {
			t.Run(string(policy)+"/"+b.name, func(t *testing.T) {
				e, f := newValidateEnv(t, b)
				now := missingStart
				e.eng.Now = func() time.Time { return now }
				e.eng.ChecksRefresh = time.Minute
				e.eng.Config.Repositories[0].CIRetrigger = policy
				want(t, e.toValidate(t), waitChecks)
				if sent := e.minutes(t, &now, 40, waitChecks); !slices.Equal(sent, []string{missingID}) {
					t.Fatalf("sent %q, want only %s", sent, missingID)
				}
				if n := len(f.StartCalls()); n != 0 {
					t.Errorf("StartCI called %d times without the opt-in", n)
				}
				if _, ok := e.documents(t)[startName]; ok {
					t.Error("a start was recorded without the opt-in")
				}
				if text := e.messages(t)[missingID]; !strings.Contains(text, "No run was started") || !strings.Contains(text, `ci_retrigger = "reopen"`) {
					t.Errorf("checks-missing = %q, want the missing opt-in named", text)
				}
			})
		}
	}
}

// TestValidateAbsentSurvivesRestart:the first read with no checks is kept on disk, so an engine started afresh part
// way through the grace reports the head missing when the grace runs out, not a grace after the restart; a negative
// grace reports it at once.
func TestValidateAbsentSurvivesRestart(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, now := startEnv(t, b)
			e.minutes(t, now, 5, waitChecks)
			restarted := *e.eng
			e.eng = &restarted
			if sent := e.minutes(t, now, 5, waitChecks); !slices.Equal(sent, []string{missingID}) {
				t.Fatalf("after a restart: sent %q, want %s at the original grace", sent, missingID)
			}
		})
	}
	t.Run("negative grace", func(t *testing.T) {
		e, f := newValidateEnv(t, backends[0])
		e.eng.CIStartGrace = -1
		want(t, e.toValidate(t), waitChecks)
		if _, ok := e.messages(t)[missingID]; !ok {
			t.Errorf("messages = %v, want checks-missing at the first read", e.inbox(t))
		}
		f.SetChecks(repoRemote, head, ciPassed...)
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	})
}

// TestValidateNoCIWithoutChecksPassesAtOnce: absent checks under no_ci pass at the first read and start nothing,
// even with no grace.
func TestValidateNoCIWithoutChecksPassesAtOnce(t *testing.T) {
	e, f := newValidateEnv(t, backends[1])
	e.eng.CIStartGrace = -1
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote, NoCI: true}}
	want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	if n := len(f.StartCalls()); n != 0 {
		t.Errorf("StartCI called %d times under no_ci", n)
	}
	if _, ok := e.messages(t)[missingID]; ok {
		t.Error("no_ci sent checks-missing")
	}
}

// TestQueueFrontStartsCI: the rebased merge-queue front re-triggers its pull request's CI for its pushed head once,
// recorded under deliver's round, and merges once the reopened run's checks pass; a dispatch run's checks on the head,
// which the pull request does not report, do not merge it first.
func TestQueueFrontStartsCI(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			now := missingStart
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			reopen(e)
			clean(ws, baseN(1), head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
			f.SetUnlinkedChecks(repoRemote, head2, ciPassed...)
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), deliverWait)
			f.SetMergeability(forge.Mergeability{})
			f.SetStartChecks(ciPassed...)
			e.minutes(t, &now, 10, deliverWait)
			startCall(t, f.StartCalls(), head2)
			if _, ok := e.documents(t)["deliver-ci-start-"+head2[:12]]; !ok {
				t.Error("the front's start was not recorded under deliver")
			}
			now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if got := mergedHead(f); got != head2 {
				t.Fatalf("merged head = %q, want the rebased head", got)
			}
		})
	}
}
