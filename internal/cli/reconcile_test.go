package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
)

func (c *cancelDirector) reconcile(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return c.run(t, append([]string{"task", "reconcile"}, c.flags(args...)...)...)
}

// TestTaskReconcile: task reconcile changes nothing while no recorded pull request merged, even one closed; records
// the task done, with its running attempt stopped, once the pull request merged outside Guildhall at another head;
// and prints a closed task's state and changes nothing.
func TestTaskReconcile(t *testing.T) {
	c := newCancelDirector(t)
	pr := c.publish(t)
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"})
	})
	before := c.receipts(t)
	code, stdout, stderr := c.reconcile(t, "t-1")
	if code != 0 || stdout != "no change: no recorded pull request is merged\n" {
		t.Fatalf("task reconcile before the merge: exit %d, %q %s", code, stdout, stderr)
	}
	if c.receipts(t) != before || c.task(t, "t-1").State != store.TaskOpen {
		t.Fatalf("task reconcile with nothing merged changed the task")
	}

	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("start-build", "app", "t-1", "build")
	})
	c.forge.MergeOutside(appRemote, pr.Number, strings.Repeat("c", 40), strings.Repeat("9", 40))
	code, stdout, stderr = c.reconcile(t, "t-1")
	if code != 0 || stdout != "attempt 1 of step build stopped\ndone: pull request "+pr.URL+" merged outside Guildhall\n" {
		t.Fatalf("task reconcile after the merge: exit %d, %q %s", code, stdout, stderr)
	}
	c.wantMergedOutside(t, pr)
	if a := c.attempts(t); len(a) != 1 || a[0].Outcome != store.AttemptStopped {
		t.Errorf("attempts = %+v, want build-1 stopped", a)
	}
	if _, merges := c.forge.Calls(); merges != 0 {
		t.Errorf("merges = %d, want none", merges)
	}

	before = c.receipts(t)
	code, stdout, _ = c.reconcile(t, "t-1")
	if code != 0 || stdout != "task app/t-1 is already done\n" || c.receipts(t) != before {
		t.Errorf("task reconcile of a done task: exit %d, %q", code, stdout)
	}
}

// TestTaskReconcileClosedPullRequest: a pull request closed on the forge without merging is not landed work, and a
// cancelled task prints that it is cancelled.
func TestTaskReconcileClosedPullRequest(t *testing.T) {
	c := newCancelDirector(t)
	pr := c.publish(t)
	c.forge.CloseOutside(appRemote, pr.Number)
	code, stdout, stderr := c.reconcile(t, "t-1")
	if code != 0 || stdout != "no change: no recorded pull request is merged\n" || c.task(t, "t-1").State != store.TaskOpen {
		t.Fatalf("task reconcile of a closed pull request: exit %d, %q %s", code, stdout, stderr)
	}
	if code, _, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1"); code != 0 {
		t.Fatalf("task cancel: exit %d, %s", code, stderr)
	}
	before := c.receipts(t)
	code, stdout, _ = c.reconcile(t, "t-1")
	if code != 0 || stdout != "task app/t-1 is already cancelled: Duplicate.\n" || c.receipts(t) != before {
		t.Errorf("task reconcile of a cancelled task: exit %d, %q", code, stdout)
	}
	if prs := c.forge.PullRequests(appRemote); prs[0].State != forge.Closed {
		t.Errorf("pull request = %+v, want it closed", prs[0])
	}
}

// TestTaskReconcileRefusals: no task id and an unlisted repository are refused.
func TestTaskReconcileRefusals(t *testing.T) {
	c := newCancelDirector(t)
	if code, _, stderr := c.reconcile(t); code != 2 || !strings.Contains(stderr, "takes one task id") {
		t.Errorf("task reconcile without a task: exit %d, %s", code, stderr)
	}
	code, _, stderr := c.run(t, "task", "reconcile", "--store", c.store, "--config", c.config, "--state", c.state, "--repo", "elsewhere", "t-1")
	if code != 1 || !strings.Contains(stderr, "elsewhere") {
		t.Errorf("task reconcile in an unlisted repository: exit %d, %s", code, stderr)
	}
}
