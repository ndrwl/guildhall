package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
)

// appRemote is app's remote in the run configs.
const appRemote = "git@github.com:example/app.git"

// cancelDirector is a run Director for task cancel: app/t-1 with whatever a test records for it, the fake forge and
// a fake stopper that records the attempts it is asked to stop and answers err.
type cancelDirector struct {
	runDirector
	forge *forgetest.Fake
	mu    sync.Mutex
	stops []string
	err   error
}

func newCancelDirector(t *testing.T) *cancelDirector {
	t.Helper()
	c := &cancelDirector{runDirector: newRunDirector(t), forge: forgetest.NewFake()}
	c.runDirector.forge = c.forge
	c.stopAttempt = func(_ context.Context, a launch.Attempt) (launch.StopReport, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.stops = append(c.stops, a.Key.AttemptID())
		return launch.StopReport{Endpoint: launch.EndpointStopped}, c.err
	}
	c.forge.SetBranch(appRemote, "main", strings.Repeat("a", 40))
	return c
}

func (c *cancelDirector) write(t *testing.T, fn func(*store.Store) (store.Receipt, error)) store.Receipt {
	t.Helper()
	r, err := withStore(c.store, false, fn)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// publish opens t-1's pull request on the fake forge and records it as publish-1-pr, as publish does.
func (c *cancelDirector) publish(t *testing.T) forge.PullRequest {
	t.Helper()
	head := strings.Repeat("b", 40)
	c.forge.AddCommit(appRepo, head)
	pr, err := c.forge.Publish(context.Background(), forge.Change{Repository: appRepo, Remote: appRemote, Branch: "guildhall/t-1", Head: head, Title: "Sort the task list"})
	if err != nil {
		t.Fatal(err)
	}
	c.putDocument(t, "publish-1-pr", map[string]any{"schema": "guildhall.pull-request/1", "remote": appRemote, "number": pr.Number,
		"url": pr.URL, "branch": pr.Branch, "head": pr.Head, "state": pr.State})
	return pr
}

// putDocument writes v as t-1's document name under the state's document directory and records it.
func (c *cancelDirector) putDocument(t *testing.T, name string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	p := path.Join("tasks", "app", "t-1", name+".json")
	file := filepath.Join(c.state, "documents", filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.PutDocument("doc-"+name, store.DocumentRef{Repository: "app", TaskID: "t-1", Name: name, Path: p, SHA256: digest.Of(data).Hex()})
	})
}

func (c *cancelDirector) cancel(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return c.run(t, append([]string{"task", "cancel"}, c.flags(args...)...)...)
}

func (c *cancelDirector) task(t *testing.T, id string) store.Task {
	t.Helper()
	return readTask(t, c.store, "app", id)
}

func (c *cancelDirector) receipts(t *testing.T) int {
	t.Helper()
	h, err := withStore(c.store, true, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory("app", "t-1") })
	if err != nil {
		t.Fatal(err)
	}
	return len(h.Receipts)
}

// TestTaskCancelRefusals: a missing or blank --reason, no task, an unlisted repository and a done task are refused,
// and nothing changes.
func TestTaskCancelRefusals(t *testing.T) {
	c := newCancelDirector(t)
	before := c.receipts(t)
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"t-1"}, 2, "--reason is required"},
		{[]string{"--reason", " ", "t-1"}, 2, "--reason is required"},
		{[]string{"--reason", "Duplicate."}, 2, "takes one task id"},
		{[]string{"--reason", strings.Repeat("x", store.MaxTextBytes+1), "t-1"}, 2, "the limit is"},
	} {
		code, _, stderr := c.cancel(t, tc.args...)
		if code != tc.code || !strings.Contains(stderr, tc.want) {
			t.Errorf("task cancel %q: exit %d, %s; want %d and %q", tc.args, code, stderr, tc.code, tc.want)
		}
	}
	code, _, stderr := c.run(t, "task", "cancel", "--store", c.store, "--config", c.config, "--state", c.state, "--repo", "elsewhere", "--reason", "Duplicate.", "t-1")
	if code != 1 || !strings.Contains(stderr, "elsewhere") {
		t.Errorf("task cancel in an unlisted repository: exit %d, %s", code, stderr)
	}
	code, _, stderr = c.cancel(t, "--reason", "Duplicate.", "t-9")
	if code != 1 || !strings.Contains(stderr, "t-9") {
		t.Errorf("task cancel of an unknown task: exit %d, %s", code, stderr)
	}
	if got := c.receipts(t); got != before {
		t.Errorf("refused cancels wrote %d receipts", got-before)
	}
	if got := c.task(t, "t-1"); got.State != store.TaskOpen {
		t.Errorf("task after refusals = %+v, want it open", got)
	}

	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("done-t-1", "app", "t-1", store.TaskDone)
	})
	before = c.receipts(t)
	code, _, stderr = c.cancel(t, "--reason", "Duplicate.", "t-1")
	if code != 1 || !strings.Contains(stderr, "is done") {
		t.Errorf("task cancel of a done task: exit %d, %s", code, stderr)
	}
	if got := c.task(t, "t-1"); got.State != store.TaskDone || got.CancelReason != "" || c.receipts(t) != before {
		t.Errorf("a refused cancel of a done task changed it: %+v", got)
	}
	// A done task's decisions, such as its audit, are still answered.
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"})
	})
	if code, out, stderr := runDecide(t, "This one.\n", "answer", "--store", c.store, "--repo", "app", "--task", "t-1", "--decision", "q-1", "--answer-file", "-"); code != 0 {
		t.Errorf("decide answer on a done task: exit %d, %s %s", code, out, stderr)
	}
}

// TestTaskCancelEndsEverything: a task with a running Artisan attempt and a Director stage's, an open pull request,
// open decisions, a start-queue entry, a merge-queue entry and a merge hold is cancelled: the Artisan attempt is
// stopped through its owner and both are recorded stopped, the pull request is closed with the reason and its branch
// kept, the open decisions are void, both queues are empty and the hold cleared, an open dependent is told once and
// stays open, and the task is cancelled with its reason and time. Each effect is printed. Then it leaves task list
// --open and director status, task show prints the reason, decide answer refuses its decision, and a second cancel
// prints that it is already cancelled and changes nothing.
func TestTaskCancelEndsEverything(t *testing.T) {
	c := newCancelDirector(t)
	newTask(t, c.store, c.config, "app", "Depends on t-1", "t-2", "app/t-1")
	newTask(t, c.store, c.config, "app", "Unrelated", "t-3")
	pr := c.publish(t)
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("start-build", "app", "t-1", "build")
	})
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.StartDirectorAttempt("start-grill", "app", "t-1", "grill", false)
	})
	_, err := withStore(c.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartCappedAttempt("queue-review", "app", "t-1", "review", store.AttemptCaps{MaxRunning: 1, Queue: &store.StartRequest{Kind: store.StartByTask}})
	})
	if !errors.Is(err, store.ErrStartQueued) {
		t.Fatalf("queued start = %v, want ErrStartQueued", err)
	}
	for _, id := range []string{"q-1", "q-2"} {
		c.write(t, func(s *store.Store) (store.Receipt, error) {
			return s.OpenDecision("open-"+id, store.Decision{Repository: "app", TaskID: "t-1", ID: id, Kind: store.DecisionQuestion, Question: "Which?"})
		})
	}
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.AnswerDecision("answer-q-2", "app", "t-1", "q-2", "This one.")
	})
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.JoinMergeQueue("join", "app", "t-1", "deliver-1-authorized", "document/publish-1-pr", pr.URL)
	})
	c.write(t, func(s *store.Store) (store.Receipt, error) { return s.SetMergeHold("hold", "app", "t-1", true) })

	const reason = "Duplicate of t-9."
	code, stdout, stderr := c.cancel(t, "--reason", reason, "t-1")
	if code != 0 {
		t.Fatalf("task cancel: exit %d, %s %s", code, stdout, stderr)
	}
	t.Logf("task cancel printed:\n%s", stdout)
	for _, line := range []string{
		"attempt 1 of step build stopped", "attempt 1 of step grill stopped",
		"pull request " + pr.URL + " closed; branch guildhall/t-1 kept",
		"decision q-1 voided", "left the start queue: attempt 1 of step review", "left the merge queue", "merge hold cleared",
		"dependent task app/t-2 told", "task app/t-1 cancelled: " + reason,
	} {
		if !strings.Contains(stdout, line+"\n") {
			t.Errorf("task cancel did not print %q", line)
		}
	}
	if strings.Contains(stdout, "q-2") || strings.Contains(stdout, "t-3") {
		t.Errorf("task cancel touched the answered decision or the unrelated task:\n%s", stdout)
	}
	if !slices.Equal(c.stops, []string{"build-1"}) {
		t.Errorf("stopped through the owner: %q, want only the Artisan attempt build-1", c.stops)
	}

	got := c.task(t, "t-1")
	if got.State != store.TaskCancelled || got.CancelReason != reason || got.CancelledAt.IsZero() {
		t.Errorf("task = %+v, want cancelled with the reason and a time", got)
	}
	for _, a := range c.attempts(t) {
		if a.Outcome != store.AttemptStopped {
			t.Errorf("attempt %+v, want it stopped", a)
		}
	}
	prs := c.forge.PullRequests(appRemote)
	if len(prs) != 1 || prs[0].State != forge.Closed {
		t.Errorf("pull requests = %+v, want the one closed", prs)
	}
	if _, comments := c.forge.Closes(); !slices.Equal(comments, []string{"Cancelled by Guildhall: " + reason}) {
		t.Errorf("close comments = %q", comments)
	}
	if b := c.forge.Branch(appRemote, "guildhall/t-1"); b == "" {
		t.Error("the task branch was deleted")
	}
	decisions, err := withStore(c.store, true, func(s *store.Store) ([]store.Decision, error) { return s.Decisions("app", "t-1") })
	if err != nil || len(decisions) != 2 || !decisions[0].Void || decisions[0].VoidReason != "Task cancelled: "+reason || decisions[1].Void || !decisions[1].Answered {
		t.Errorf("decisions = %+v, %v; want q-1 void and q-2 answered", decisions, err)
	}
	queues, err := withStore(c.store, true, func(s *store.Store) (struct {
		starts []store.QueuedStart
		merge  store.MergeQueue
		hold   store.MergeHold
	}, error) {
		var q struct {
			starts []store.QueuedStart
			merge  store.MergeQueue
			hold   store.MergeHold
		}
		var err error
		if q.starts, err = s.StartQueue(); err != nil {
			return q, err
		}
		if q.merge, err = s.MergeQueue("app"); err != nil {
			return q, err
		}
		q.hold, err = s.MergeHold("app", "t-1")
		return q, err
	})
	if err != nil || len(queues.starts) != 0 || len(queues.merge.Entries) != 0 || len(queues.merge.Out) != 0 || queues.merge.Front != nil || queues.hold.Held {
		t.Errorf("queues = %+v, %v; want both empty and no hold", queues, err)
	}
	if got := c.task(t, "t-2"); got.State != store.TaskOpen {
		t.Errorf("dependent = %+v, want it open", got)
	}
	unread, err := withStore(c.store, true, func(s *store.Store) ([]store.Message, error) {
		v, err := s.DirectorView()
		return v.Unread, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var told []store.Message
	for _, m := range unread {
		if m.Type == store.MessageDependencyCancelled {
			told = append(told, m)
		}
	}
	if len(told) != 1 || told[0].TaskID != "t-2" || !strings.Contains(told[0].Text, "app/t-1") || !strings.Contains(told[0].Text, reason) {
		t.Errorf("dependency-cancelled messages = %+v, want one about t-2", told)
	}

	// The views.
	code, out, stderr := runTask(t, "list", "--store", c.store, "--open", "--json")
	if code != 0 || strings.Contains(out, `"id":"t-1","title"`) || !strings.Contains(out, `"id":"t-2","title"`) {
		t.Errorf("task list --open: exit %d, %s %s; want t-1 left out", code, out, stderr)
	}
	code, out, _ = runTask(t, "show", "--store", c.store, "--repo", "app", "--json", "t-1")
	if shown := decodeJSON[taskDetail](t, out); code != 0 || shown.Task.State != store.TaskCancelled || shown.Task.CancelReason != reason || shown.Task.CancelledAt.IsZero() {
		t.Errorf("task show = %+v, want it cancelled with the reason and time", shown.Task)
	}
	code, out, _ = runTask(t, "list", "--store", c.store, "--json")
	if code != 0 || !strings.Contains(out, `"cancel_reason":"Duplicate of t-9."`) || !strings.Contains(out, `"cancelled_at"`) {
		t.Errorf("task list: %s; want t-1 with its reason and time", out)
	}
	st, err := readDirectorStatus(context.Background(), c.store, c.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range st.NeedsMe {
		if it.TaskID == "t-1" || it.Kind == decisionKind {
			t.Errorf("director status lists the cancelled task or its void decision: %+v", it)
		}
	}
	code, _, stderr = runDecide(t, "Now.\n", "answer", "--store", c.store, "--repo", "app", "--task", "t-1", "--decision", "q-1", "--answer-file", "-")
	if code != 1 || !strings.Contains(stderr, "cancelled") {
		t.Errorf("decide answer on a cancelled task: exit %d, %s", code, stderr)
	}
	if code, out, _ := runDecide(t, "", "list", "--store", c.store); code != 0 || strings.Contains(out, `"q-1"`) {
		t.Errorf("decide list: exit %d, %s; want the void decision left out", code, out)
	}
	code, out, _ = runDecide(t, "", "show", "--store", c.store, "--repo", "app", "--task", "t-1", "--decision", "q-1")
	if d := decodeJSON[store.Decision](t, out); code != 0 || !d.Void || d.VoidReason != "Task cancelled: "+reason {
		t.Errorf("decide show = %+v, want it void with the reason", d)
	}

	// A second cancel changes nothing.
	before := c.receipts(t)
	code, stdout, stderr = c.cancel(t, "--reason", "Another reason.", "t-1")
	if code != 0 || stdout != "task app/t-1 is already cancelled: "+reason+"\n" {
		t.Errorf("a second cancel: exit %d, %q %s", code, stdout, stderr)
	}
	if closes, _ := c.forge.Closes(); closes != 1 || c.receipts(t) != before || len(c.stops) != 1 {
		t.Errorf("a second cancel closed %d times, wrote %d receipts, stopped %q", closes, c.receipts(t)-before, c.stops)
	}
}

// TestTaskCancelRefusesAnUnprovenStop: an Artisan attempt whose stop is not proven refuses the cancel, and nothing
// else changes: the task stays open, its pull request open and its decisions open.
func TestTaskCancelRefusesAnUnprovenStop(t *testing.T) {
	c := newCancelDirector(t)
	c.publish(t)
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("start-build", "app", "t-1", "build")
	})
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"})
	})
	c.err = launch.ErrNotStopped
	code, stdout, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1")
	if code != 1 || !strings.Contains(stderr, "not cancelled") || stdout != "" {
		t.Fatalf("task cancel with an unproven stop: exit %d, %q %s", code, stdout, stderr)
	}
	if got := c.task(t, "t-1"); got.State != store.TaskOpen {
		t.Errorf("task = %+v, want it open", got)
	}
	if a := c.attempts(t); len(a) != 1 || a[0].Outcome != "" {
		t.Errorf("attempts = %+v, want build-1 still running", a)
	}
	if prs := c.forge.PullRequests(appRemote); prs[0].State != forge.Open {
		t.Errorf("pull request = %+v, want it open", prs[0])
	}
	if d, err := withStore(c.store, true, func(s *store.Store) ([]store.Decision, error) { return s.Decisions("app", "t-1") }); err != nil || !d[0].Open() {
		t.Errorf("decisions = %+v, %v; want q-1 open", d, err)
	}
}

// TestTaskCancelRefusesMergedWork: a task whose deliver recorded its merge, or whose pull request merged on the
// forge, is refused, and its decisions, queues and state are unchanged.
func TestTaskCancelRefusesMergedWork(t *testing.T) {
	t.Run("merge document", func(t *testing.T) {
		c := newCancelDirector(t)
		pr := c.publish(t)
		c.putDocument(t, "deliver-1-merge", map[string]any{"schema": "guildhall.pull-request/1", "remote": appRemote, "number": pr.Number, "state": "merged"})
		before := c.receipts(t)
		code, _, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1")
		if code != 1 || !strings.Contains(stderr, "merging") {
			t.Errorf("task cancel of a merging task: exit %d, %s", code, stderr)
		}
		if closes, _ := c.forge.Closes(); closes != 0 || c.receipts(t) != before || c.task(t, "t-1").State != store.TaskOpen {
			t.Errorf("a refused cancel closed %d pull requests or wrote %d receipts", closes, c.receipts(t)-before)
		}
	})
	t.Run("merged on the forge", func(t *testing.T) {
		c := newCancelDirector(t)
		pr := c.publish(t)
		c.write(t, func(s *store.Store) (store.Receipt, error) {
			return s.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"})
		})
		if _, err := c.forge.Merge(context.Background(), forge.Merge{Repository: appRepo, Remote: appRemote, Number: pr.Number, Head: pr.Head}); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1")
		if code != 1 || !strings.Contains(stderr, "landed") {
			t.Errorf("task cancel of a merged task: exit %d, %s", code, stderr)
		}
		if got := c.task(t, "t-1"); got.State != store.TaskOpen {
			t.Errorf("task = %+v, want it open", got)
		}
		if d, err := withStore(c.store, true, func(s *store.Store) ([]store.Decision, error) { return s.Decisions("app", "t-1") }); err != nil || !d[0].Open() {
			t.Errorf("decisions = %+v, %v; want q-1 still open", d, err)
		}
	})
}

// lossyForge closes a pull request and then, once, reports a failure, as a close whose answer was lost does.
type lossyForge struct {
	*forgetest.Fake
	lost bool
}

func (f *lossyForge) Close(ctx context.Context, c forge.Close) (forge.PullRequest, error) {
	pr, err := f.Fake.Close(ctx, c)
	if err == nil && !f.lost {
		f.lost = true
		return forge.PullRequest{}, errors.New("gh pr close: connection reset")
	}
	return pr, err
}

// TestTaskCancelRerunsAfterAFailure: a cancel that fails after closing the pull request leaves the task open; the
// same command run again finishes it without closing the pull request twice or stopping the attempt again.
func TestTaskCancelRerunsAfterAFailure(t *testing.T) {
	c := newCancelDirector(t)
	lossy := &lossyForge{Fake: c.forge}
	c.runDirector.forge = lossy
	c.publish(t)
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("start-build", "app", "t-1", "build")
	})
	code, _, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1")
	if code != 1 || !strings.Contains(stderr, "connection reset") {
		t.Fatalf("first cancel: exit %d, %s; want the lost close", code, stderr)
	}
	if got := c.task(t, "t-1"); got.State != store.TaskOpen {
		t.Fatalf("task after the failure = %+v, want it open", got)
	}
	code, stdout, stderr := c.cancel(t, "--reason", "Duplicate.", "t-1")
	if code != 0 || !strings.Contains(stdout, "task app/t-1 cancelled: Duplicate.\n") || strings.Contains(stdout, "stopped") {
		t.Fatalf("second cancel: exit %d, %s %s", code, stdout, stderr)
	}
	if _, comments := c.forge.Closes(); len(comments) != 1 {
		t.Errorf("the pull request was closed %d times, want once", len(comments))
	}
	if len(c.stops) != 1 {
		t.Errorf("stops = %q, want one", c.stops)
	}
}

// TestTellDependentsSendsOnce: telling a cancelled task's dependents again, as a re-run cancel does, sends nothing
// new; a done or cancelled dependent, and a task that does not depend on it, is not told.
func TestTellDependentsSendsOnce(t *testing.T) {
	c := newCancelDirector(t)
	for _, id := range []string{"t-2", "t-3"} {
		newTask(t, c.store, c.config, "app", "Depends on t-1", id, "app/t-1")
	}
	newTask(t, c.store, c.config, "app", "Unrelated", "t-4")
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("done-t-3", "app", "t-3", store.TaskDone)
	})
	cfg, err := config.Load(c.config)
	if err != nil {
		t.Fatal(err)
	}
	var told []string
	say := func(format string, args ...any) { told = append(told, args[1].(string)) }
	for range 2 {
		if err := tellDependents(storeRecords{path: c.store}, cfg, store.TaskRef{Repository: "app", ID: "t-1"}, "Duplicate.", say); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(told, []string{"t-2", "t-2"}) {
		t.Errorf("told = %q, want t-2 each time", told)
	}
	unread, err := withStore(c.store, true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
	if err != nil || unread[store.MessageDependencyCancelled] != 1 {
		t.Errorf("unread = %v, %v; want one dependency-cancelled message", unread, err)
	}
}

// TestAdvanceOfACancelledTask: task start and step done of a cancelled task, as a stopped attempt's launcher runs
// them, print that it is cancelled and succeed; of a done task they still fail.
func TestAdvanceOfACancelledTask(t *testing.T) {
	for _, state := range []store.TaskState{store.TaskCancelled, store.TaskDone} {
		t.Run(string(state), func(t *testing.T) {
			c := newCancelDirector(t)
			c.write(t, func(s *store.Store) (store.Receipt, error) {
				return s.StartAttempt("start-build", "app", "t-1", "build")
			})
			c.write(t, func(s *store.Store) (store.Receipt, error) {
				if state == store.TaskDone {
					return s.SetTaskState("done", "app", "t-1", store.TaskDone)
				}
				return s.CancelTask("cancel", "app", "t-1", "Duplicate.")
			})
			result := c.result("build", 1)
			if err := os.MkdirAll(filepath.Dir(result), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(result, []byte("{}"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				append([]string{"task", "start"}, c.flags("t-1")...),
				append([]string{"step", "done"}, c.flags("--task", "t-1", "--step", "build", "--attempt", "1", "--result", result)...),
			} {
				code, stdout, stderr := c.run(t, args...)
				switch {
				case state == store.TaskCancelled && (code != 0 || stdout != "task app/t-1 is cancelled; nothing to advance\n"):
					t.Errorf("%s of a cancelled task: exit %d, %q %s", args[:2], code, stdout, stderr)
				case state == store.TaskDone && (code != 1 || !strings.Contains(stderr, "not open")):
					t.Errorf("%s of a done task: exit %d, %q %s", args[:2], code, stdout, stderr)
				}
			}
		})
	}
}
