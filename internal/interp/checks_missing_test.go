package interp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
)

// missingStart is the clock the checks-missing tests start at.
var missingStart = time.Date(2026, 11, 3, 23, 15, 0, 0, time.UTC)

// minutes advances the task once a minute, n times, each a wait wanted, and reports the messages sent meanwhile.
func (e *env) minutes(t *testing.T, now *time.Time, n int, wait interp.Next) []string {
	t.Helper()
	before := len(e.inbox(t))
	for range n {
		*now = now.Add(time.Minute)
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
	}
	return e.inbox(t)[before:]
}

// TestValidateChecksMissing is the d167 regression at validate: a published head the forge reports no check for
// waits as pending, with one checks-pending message, until ci_start_grace has passed since the first read that saw
// none; then one checks-missing message names the head, the pull request and how long, and says no run was started
// on a forge that cannot start one; the step keeps waiting and later advances send nothing. Checks appearing
// continue as today.
func TestValidateChecksMissing(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.eng.Forge = noStarter{f}
			reopen(e)
			now := missingStart
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			want(t, e.toValidate(t), waitChecks)
			pending, missing := "app.t7.checks-pending.validate."+head[:12], "app.t7.checks-missing.validate."+head[:12]
			if got := e.inbox(t); got[len(got)-1] != pending {
				t.Fatalf("inbox = %q, want %s last", got, pending)
			}
			if sent := e.minutes(t, &now, 9, waitChecks); len(sent) != 0 {
				t.Fatalf("9 minutes with no checks sent %q, want nothing", sent)
			}
			if sent := e.minutes(t, &now, 1, waitChecks); len(sent) != 1 || sent[0] != missing {
				t.Fatalf("10 minutes with no checks sent %q, want only %s", sent, missing)
			}
			text := e.messages(t)[missing]
			for _, part := range []string{"Step validate", head, "https://forge.invalid/1", "10m0s", "No run was started", "this forge cannot start a CI run"} {
				if !strings.Contains(text, part) {
					t.Errorf("checks-missing = %q, want it to name %q", text, part)
				}
			}
			if sent := e.minutes(t, &now, 200, waitChecks); len(sent) != 0 {
				t.Fatalf("200 more minutes sent %q, want nothing", sent)
			}
			f.SetChecks(repoRemote, head, ciPassed...)
			now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		})
	}
}

// TestQueueFrontChecksMissing is the d167 regression at the merge-queue front: the front rebased and pushed as head2,
// which the forge reports no check for, waits with one checks-pending message, then one checks-missing message at
// ci_start_grace, keeps the front and, on a forge that cannot start a run, sends nothing more; once head2's checks
// pass it merges.
func TestQueueFrontChecksMissing(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			e.eng.Forge = noStarter{f}
			reopen(e)
			now := missingStart
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			clean(ws, baseN(1), head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), deliverWait)
			f.SetMergeability(forge.Mergeability{})
			missing := "app.t7.checks-missing.deliver." + head2[:12]
			if _, ok := e.messages(t)["app.t7.checks-pending.deliver."+head2[:12]]; !ok {
				t.Fatalf("messages = %v, want the rebased head's checks-pending", e.inbox(t))
			}
			if sent := e.minutes(t, &now, 9, deliverWait); len(sent) != 0 {
				t.Fatalf("9 minutes with no checks sent %q, want nothing", sent)
			}
			if sent := e.minutes(t, &now, 1, deliverWait); len(sent) != 1 || sent[0] != missing {
				t.Fatalf("10 minutes with no checks sent %q, want only %s", sent, missing)
			}
			if text := e.messages(t)[missing]; !strings.Contains(text, "Step deliver") || !strings.Contains(text, head2) {
				t.Errorf("checks-missing = %q, want the step and the rebased head", text)
			}
			if sent := e.minutes(t, &now, 200, deliverWait); len(sent) != 0 {
				t.Fatalf("200 more minutes sent %q, want nothing", sent)
			}
			if queued, known := inQueue(t, e); known && !queued {
				t.Error("the front left the queue")
			}
			f.SetChecks(repoRemote, head2, ciPassed...)
			now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if got := mergedHead(f); got != head2 {
				t.Fatalf("merged head = %q, want the rebased head", got)
			}
		})
	}
}
