package interp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// queueWorkspaces is the workspace double for the merge queue's tests: rebases, squashes and diffs are
// gittest.Workspaces', while every working directory is a plain one whose branch tip is tip, so the scripted
// constant heads pass the code-head check; Adopt records each move.
type queueWorkspaces struct {
	*gittest.Workspaces
	mu       sync.Mutex
	tip      string
	adopted  [][2]string
	squashes int
	// forwards counts FastForward calls.
	forwards int
	// afterSquash, when set, runs after each squash.
	afterSquash func()
}

func (w *queueWorkspaces) Squash(ctx context.Context, repository, base, head string) (string, error) {
	c, err := w.Workspaces.Squash(ctx, repository, base, head)
	w.mu.Lock()
	w.squashes++
	after := w.afterSquash
	w.mu.Unlock()
	if after != nil {
		after()
	}
	return c, err
}

func (w *queueWorkspaces) Ensure(_ context.Context, ws git.Workspace) error {
	return os.MkdirAll(ws.Dir, 0o755)
}
func (w *queueWorkspaces) FastForward(context.Context, git.Workspace) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.forwards++
	return "", nil
}

func (w *queueWorkspaces) fastForwards() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.forwards
}
func (w *queueWorkspaces) Retire(context.Context, git.Retirement) ([]string, error) { return nil, nil }
func (w *queueWorkspaces) Tip(context.Context, string, string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tip, nil
}
func (w *queueWorkspaces) IsAncestor(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (w *queueWorkspaces) Behind(context.Context, string, string, string) (int, []string, error) {
	return 0, nil, nil
}
func (w *queueWorkspaces) Base(context.Context, string, string) (string, error) { return baseHead, nil }
func (w *queueWorkspaces) Adopt(_ context.Context, _ git.Workspace, from, to string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.adopted = append(w.adopted, [2]string{from, to})
	return nil
}

func (w *queueWorkspaces) setTip(h string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tip = h
}

// commitN is a full commit id for test commit n, distinct from head, head2 and head3.
func commitN(n int) string { return fmt.Sprintf("%02x", n) + strings.Repeat("d", 38) }

// baseN is the base tip a test moves main to, n.
func baseN(n int) string { return fmt.Sprintf("%02x", n) + strings.Repeat("e", 38) }

// queueEnv is validate.json through validate with green checks on head, at the unanswered merge decision deliver-1,
// with the queue's workspace double installed and heads 1 to 3 and commitN(1..9) known to the forge.
func queueEnv(t *testing.T, b backend) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	return queueEnvDefinition(t, b, loadWorkflow(t, "validate"))
}

// queueEnvDefinition is queueEnv over def, a variant of validate.json.
func queueEnvDefinition(t *testing.T, b backend, def *workflow.Definition) (*env, *forgetest.Fake, *queueWorkspaces) {
	t.Helper()
	e, f := newValidateEnvDefinition(t, b, def)
	ws := &queueWorkspaces{Workspaces: gittest.NewWorkspaces(), tip: head}
	for n := 1; n < 10; n++ {
		f.AddCommit(repoPath, commitN(n))
	}
	f.SetChecks(repoRemote, head, ciPassed...)
	want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	e.eng.Workspaces = ws
	return e, f, ws
}

// clean scripts a no_conflicts replay of from onto base as commit to.
func clean(ws *queueWorkspaces, base, from, to string) {
	ws.SetRebase(base, from, git.Rebased{Commit: to, MergeBase: baseHead, HeadPatchID: "patch-" + from[:8], PatchID: "patch-" + from[:8]})
}

var (
	deliverWait   = interp.Next{Wait: interp.WaitChecks, Step: "deliver"}
	answeredMerge = interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}
)

// mergedHead is the head the task's pull request merged at, or "".
func mergedHead(f *forgetest.Fake) string {
	for _, pr := range f.PullRequests(repoRemote) {
		if pr.State == forge.Merged {
			return pr.Head
		}
	}
	return ""
}

// TestQueueTier1CarriesApproval: a behind pull request at the front is replayed onto the base tip mergeability
// measured; the patch-identical replay is pushed to the same pull request, its checks are read, and the Guildmaster's
// approval carries over to it as deliver-2-authorized (schema 2) under every reapprove_after_rebase above
// no_conflicts, while no_conflicts asks a new merge decision for the rebased head.
func TestQueueTier1CarriesApproval(t *testing.T) {
	for _, b := range backends {
		for _, threshold := range []config.RebaseKind{"", config.RebaseNoConflicts, config.RebaseMinorConflicts, config.RebaseMajorConflicts, config.RebaseNever} {
			t.Run(b.name+"/"+string(threshold), func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = threshold
				base := baseN(1)
				clean(ws, base, head, head2)
				f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
				e.answer(t, "deliver-1", "merge")
				want(t, e.advance(t, answeredMerge), deliverWait)
				// A replay of the wait pushes nothing again.
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), deliverWait)
				if publishes, merges := f.Calls(); publishes != 2 || merges != 0 {
					t.Fatalf("publishes = %d, merges = %d, want the rebased head pushed once", publishes, merges)
				}
				rb := e.pullRequestDoc(t, "deliver-1-rebase-1")
				if rb["kind"] != "no_conflicts" || rb["from"] != head || rb["onto"] != base || rb["rebased"] != head2 {
					t.Fatalf("rebase document = %v", rb)
				}
				if len(ws.adopted) != 1 || ws.adopted[0] != [2]string{head, head2} {
					t.Fatalf("adopted = %v, want the worktree moved to the rebased head", ws.adopted)
				}
				f.SetChecks(repoRemote, head2, ciPassed...)
				f.SetMergeability(forge.Mergeability{})
				next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
				if threshold == config.RebaseNoConflicts {
					want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
					if d := e.decision(t, "deliver-2"); d.Subject == nil || d.Subject.Digest != head2 || !strings.Contains(d.Question, "reapprove_after_rebase") {
						t.Fatalf("decision = %+v", d)
					}
					e.answer(t, "deliver-2", "merge")
					next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-2"})
				}
				want(t, next, done)
				if got := mergedHead(f); got != head2 {
					t.Fatalf("merged head = %q, want the rebased head", got)
				}
				if threshold != config.RebaseNoConflicts {
					a := e.pullRequestDoc(t, "deliver-2-authorized")
					if a["schema"] != "guildhall.merge-authorization/2" || a["carried_from"] != "deliver-1" || a["kind"] != "no_conflicts" || a["head"] != head2 {
						t.Fatalf("carried authorization = %v", a)
					}
					if text := e.messages(t)["app.t7.approval-carried.deliver-2"]; !strings.Contains(text, "deliver-1") {
						t.Fatalf("approval-carried = %q", text)
					}
					found := false
					for _, text := range e.messages(t) {
						found = found || strings.Contains(text, "carried-over approval of deliver-1 after no_conflicts rebase")
					}
					if !found {
						t.Fatalf("no merged message names the carried-over approval: %v", e.messages(t))
					}
				}
				if q, ok := e.h.Records.(*store.Store); ok {
					view, err := q.MergeQueue("app")
					if err != nil || len(view.Entries) != 0 || len(view.Out) != 0 {
						t.Fatalf("queue after merge = %+v, %v", view, err)
					}
				}
			})
		}
	}
}

// TestQueueRedChecksEscalate: red checks on the rebased head send the build that produced the head back, with
// feedback naming the base and the reason, take the task out of the queue keeping its ready time, and need a new
// merge decision for the rebuilt head.
func TestQueueRedChecksEscalate(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			base := baseN(1)
			clean(ws, base, head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), deliverWait)
			f.SetChecks(repoRemote, head2, ciFailed...)
			exp := buildExp(t, 2, head3, "")
			exp.Inputs["feedback"] = interptest.AnyDigest
			e.script.Expect(exp)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			esc := e.pullRequestDoc(t, "deliver-1-escalated-1")
			if esc["from"] != head2 || esc["carry"] == true || !strings.Contains(esc["reason"].(string), "ci") {
				t.Fatalf("escalation = %v", esc)
			}
			text := e.messages(t)["app.t7.rebase-escalated.deliver-1-escalated-1"]
			if !strings.Contains(text, base) || !strings.Contains(text, "build attempt 1") {
				t.Fatalf("rebase-escalated = %q", text)
			}
			if s, ok := e.h.Records.(*store.Store); ok {
				view, err := s.MergeQueue("app")
				if err != nil || len(view.Entries) != 0 || len(view.Out) != 1 {
					t.Fatalf("queue after escalation = %+v, %v", view, err)
				}
			}
			if _, merges := f.Calls(); merges != 0 {
				t.Fatalf("merges = %d", merges)
			}
		})
	}
}

// TestQueueMajorConflictsEscalate: a replay with more conflicting lines than minor_conflict_lines, or a conflict
// without markers, is major_conflicts: recorded, then the build is sent back with the conflicting files.
func TestQueueMajorConflictsEscalate(t *testing.T) {
	shapes := map[string][]git.Conflict{
		"lines":   {{Path: "a.go", Lines: 51, Markers: true}},
		"files":   {{Path: "a.go", Lines: 1, Markers: true}, {Path: "b.go", Lines: 1, Markers: true}, {Path: "c.go", Lines: 1, Markers: true}, {Path: "d.go", Lines: 1, Markers: true}},
		"markers": {{Path: "logo.png"}},
	}
	for _, b := range backends {
		for name, conflicts := range shapes {
			t.Run(b.name+"/"+name, func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				base := baseN(1)
				ws.SetRebase(base, head, git.Rebased{Marked: commitN(1), MergeBase: baseHead, Conflicts: conflicts, HeadPatchID: "p"})
				f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{conflicts[0].Path}})
				e.answer(t, "deliver-1", "merge")
				exp := buildExp(t, 2, head3, "")
				exp.Inputs["feedback"] = interptest.AnyDigest
				e.script.Expect(exp)
				want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
				rb := e.pullRequestDoc(t, "deliver-1-rebase-1")
				if rb["kind"] != "major_conflicts" {
					t.Fatalf("rebase = %v", rb)
				}
				esc := e.pullRequestDoc(t, "deliver-1-escalated-1")
				if esc["carry"] != true || !strings.Contains(fmt.Sprint(esc["files"]), conflicts[0].Path) {
					t.Fatalf("escalation = %v", esc)
				}
				if publishes, _ := f.Calls(); publishes != 1 {
					t.Fatalf("publishes = %d, want nothing pushed", publishes)
				}
			})
		}
	}
}

// TestQueueBaseMovesDuringChecks: when the base moves again while the front waits for the rebased head's checks,
// the front rebases again; after three rounds in one stay it waits for the Director with rebase-stuck.
func TestQueueBaseMovesDuringChecks(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			from := head
			e.answer(t, "deliver-1", "merge")
			ev := answeredMerge
			for n := 1; n <= 3; n++ {
				to := commitN(n)
				clean(ws, baseN(n), from, to)
				f.SetMergeability(forge.Mergeability{Head: from, Base: baseN(n), State: "behind", Behind: 1})
				want(t, e.advance(t, ev), deliverWait)
				f.SetChecks(repoRemote, to, ciPassed...)
				from, ev = to, interp.Event{Kind: interp.TaskCreated}
			}
			f.SetMergeability(forge.Mergeability{Head: from, Base: baseN(4), State: "behind", Behind: 1})
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
			if got := len(ws.Replays()); got != 3 {
				t.Fatalf("replays = %d, want 3", got)
			}
			if _, ok := e.messages(t)["app.t7.rebase-stuck.pr-blocked-"+from+"-behind"]; !ok {
				t.Fatalf("no rebase-stuck message: %v", e.messages(t))
			}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitDirector, Step: "deliver"})
			if got := len(ws.Replays()); got != 3 {
				t.Fatalf("replays after replay = %d, want 3", got)
			}
		})
	}
}

// TestQueueHoldFront: holding the front's merge releases the front at its next advance, not before, so another task
// cannot claim it while the held one may still be pushing or merging; the task keeps its authorization and ready
// time, and unholding it claims the front again in a new stay, and the merge completes.
func TestQueueHoldFront(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	s := e.h.Records.(*store.Store)
	base := baseN(1)
	clean(ws, base, head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), deliverWait)
	peer(t, e, "t9")
	if _, err := s.SetMergeHold("hold-1", "app", "t7", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("front-t9", "app", "t9"); !errors.Is(err, store.ErrMergeQueued) {
		t.Fatalf("claim by t9 before the held front's next advance = %v, want it refused", err)
	}
	if view, _ := s.MergeQueue("app"); view.Front == nil || view.Front.TaskID != "t7" {
		t.Fatalf("front before the held front's next advance = %+v", view.Front)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"})
	view, err := s.MergeQueue("app")
	if err != nil || view.Front != nil || len(view.Entries) != 2 || view.Entries[0].TaskID != "t7" || !view.Entries[0].Held {
		t.Fatalf("held queue = %+v, %v", view, err)
	}
	ready := view.Entries[0].ReadyAt
	if _, err := s.SetMergeHold("hold-2", "app", "t7", false); err != nil {
		t.Fatal(err)
	}
	f.SetChecks(repoRemote, head2, ciPassed...)
	f.SetMergeability(forge.Mergeability{})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if got := mergedHead(f); got != head2 {
		t.Fatalf("merged head = %q", got)
	}
	if ready.IsZero() {
		t.Fatal("no ready time")
	}
}

// TestQueueWaitsBehindTheFront: a task authorized while another holds its repository's front waits in the merge
// queue, told its place, and merges once the front leaves.
func TestQueueWaitsBehindTheFront(t *testing.T) {
	e, f, _ := queueEnv(t, backends[1])
	s := e.h.Records.(*store.Store)
	other, err := s.Task("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	other.ID, other.Title = "t1", "Ahead"
	if _, err := s.CreateTask("other", other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("other-auth", store.DocumentRef{Repository: "app", TaskID: "t1", Name: "deliver-1-authorized", Path: "tasks/app/t1/a.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinMergeQueue("other-join", "app", "t1", "deliver-1", "document/deliver-1-authorized", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("other-front", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"})
	found := false
	for _, text := range e.messages(t) {
		found = found || strings.Contains(text, "place 2 of 2; task t1 holds the front")
	}
	if !found {
		t.Fatalf("no merge-queued message: %v", e.messages(t))
	}
	if _, err := s.LeaveMergeQueue("other-leave", "app", "t1", false); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if mergedHead(f) != head {
		t.Fatal("not merged")
	}
}

// TestQueueTier1CrashReplay: an advance interrupted before each record of a tier 1 rebase at the front (the rebase
// document, the push, the checks read, the carried authorization and the merge) replays to the same records, pushing
// and merging once.
func TestQueueTier1CrashReplay(t *testing.T) {
	for _, b := range backends {
		for _, name := range []string{"deliver-1-rebase-1", "deliver-1-rebase-1-pr", "deliver-1-checks-" + head2, "deliver-2-authorized", "deliver-2-merge"} {
			t.Run(b.name+"/"+name, func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				base := baseN(1)
				clean(ws, base, head, head2)
				f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
				e.answer(t, "deliver-1", "merge")
				records := e.eng.Records
				ev := answeredMerge
				interrupted := false
				for i := 0; i < 6; i++ {
					if i == 1 {
						f.SetChecks(repoRemote, head2, ciPassed...)
						f.SetMergeability(forge.Mergeability{})
					}
					if _, ok := e.documents(t)[name]; !ok && !interrupted {
						e.eng.Records = &interruptedDocumentRecord{Records: records, name: name}
						err := advanceErr(t, e, ev)
						interrupted = err != nil
						if interrupted && !strings.Contains(err.Error(), "interrupted before document record") {
							t.Fatalf("interrupted advance = %v", err)
						}
						e.eng.Records = records
					}
					if next := e.advance(t, ev); next == done {
						break
					}
					ev = interp.Event{Kind: interp.TaskCreated}
				}
				if !interrupted {
					t.Fatalf("no advance was interrupted before %s", name)
				}
				if mergedHead(f) != head2 {
					t.Fatalf("merged head = %q", mergedHead(f))
				}
				// A crash after the forge call and before its record repeats the call on replay, with the same head,
				// as publish and merge do today; nothing else is pushed.
				if publishes, merges := f.Calls(); publishes < 2 || publishes > 3 || merges < 1 || merges > 2 {
					t.Fatalf("publishes = %d, merges = %d", publishes, merges)
				}
				if prs := f.PullRequests(repoRemote); len(prs) != 1 || prs[0].Head != head2 {
					t.Fatalf("pull requests = %+v", prs)
				}
				if a := e.pullRequestDoc(t, "deliver-2-authorized"); a["carried_from"] != "deliver-1" {
					t.Fatalf("authorization = %v", a)
				}
				onceEach(t, e)
			})
		}
	}
}

// onceEach checks that the carried merge was authorized and recorded once: deliver-2-authorized and deliver-2-merge,
// one approval-carried message, and no record of a later round.
func onceEach(t *testing.T, e *env) {
	t.Helper()
	if got := e.authorizations(t); !slices.Equal(got, []string{"deliver-2-authorized"}) {
		t.Fatalf("authorizations = %q, want only deliver-2-authorized", got)
	}
	var merges []string
	for name := range e.documents(t) {
		if strings.HasSuffix(name, "-merge") {
			merges = append(merges, name)
		}
		if strings.HasPrefix(name, "deliver-3-") {
			t.Fatalf("document %s records a third round", name)
		}
	}
	if !slices.Equal(merges, []string{"deliver-2-merge"}) {
		t.Fatalf("merge records = %q, want only deliver-2-merge", merges)
	}
	carried := 0
	for id := range e.messages(t) {
		if strings.Contains(id, "approval-carried.") {
			carried++
		}
	}
	if carried != 1 {
		t.Fatalf("approval-carried messages = %d, want 1", carried)
	}
}

// TestQueueCarriedMergeRefusedRetries: a merge the forge refuses after an approval carried over waits for the
// Director, and each retry merges under the same carried authorization instead of minting a new one.
func TestQueueCarriedMergeRefusedRetries(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			base := baseN(1)
			clean(ws, base, head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "behind", Behind: 1})
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), deliverWait)
			f.SetChecks(repoRemote, head2, ciPassed...)
			f.SetMergeability(forge.Mergeability{})
			f.Refuse("Required review is missing")
			director := interp.Next{Wait: interp.WaitDirector, Step: "deliver"}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			f.Refuse("")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if mergedHead(f) != head2 {
				t.Fatalf("merged head = %q", mergedHead(f))
			}
			onceEach(t, e)
		})
	}
}

// TestQueueRebaseRefusedWhileAnAttemptRuns: the front does not rebase while an attempt of the task runs.
func TestQueueRebaseRefusedWhileAnAttemptRuns(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	clean(ws, baseN(1), head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
	// An attempt the walk does not own, such as a judge's, still runs in the task's worktree.
	if _, err := e.h.Records.StartCappedAttempt("running", "app", "t7", "judge", store.AttemptCaps{}); err != nil {
		t.Fatal(err)
	}
	e.answer(t, "deliver-1", "merge")
	if err := advanceErr(t, e, answeredMerge); err == nil || !strings.Contains(err.Error(), "cannot rebase while an attempt runs") {
		t.Fatalf("advance = %v, want the rebase refused", err)
	}
	if len(ws.Replays()) != 0 {
		t.Fatal("replayed while an attempt runs")
	}
}
