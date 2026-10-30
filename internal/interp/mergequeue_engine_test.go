package interp_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var thresholds = []config.RebaseKind{"", config.RebaseNoConflicts, config.RebaseMinorConflicts, config.RebaseMajorConflicts, config.RebaseNever}

var building2 = interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2}

// toMajor answers deliver-1 with the front's replay onto baseN(1) a major_conflicts one, a conflict without markers
// in logo.png, so the build is sent back as attempt 2, which will land head 3.
func toMajor(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces) {
	t.Helper()
	base := baseN(1)
	ws.SetRebase(base, head, git.Rebased{Marked: commitN(1), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "logo.png"}}, HeadPatchID: "p"})
	f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"logo.png"}})
	exp := buildExp(t, 2, head3, "")
	exp.Inputs["feedback"] = interptest.AnyDigest
	e.script.Expect(exp)
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), building2)
	if esc := e.pullRequestDoc(t, "deliver-1-escalated-1"); esc["carry"] != true || esc["spec_digest"] != specDigest(t) {
		t.Fatalf("escalation = %v, want it to carry and to record the spec deliver-1 was approved under", esc)
	}
}

// rebuild runs build attempt 2's head 3 through review attempt 2, answering o, publish and a green validate, and
// returns deliver's wait.
func rebuild(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces, o workflow.ReviewOutcome) interp.Next {
	t.Helper()
	e.script.Expect(reviewExp(t, "review", "reviewer", 2, head3, ptr(bound(t, workflow.VerdictApproved, head, nil)), o))
	f.SetMergeability(forge.Mergeability{})
	f.SetChecks(repoRemote, head3, ciPassed...)
	ws.setTip(head3)
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 2})
	return e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2})
}

// TestQueueMajorConflictsUnderEachThreshold: a major_conflicts rebase sends the build back under every
// reapprove_after_rebase, and the rebuilt head needs a new merge decision, except under never, where the approval of
// the head it replaced carries over to it once every condition holds and the accepted spec is unchanged.
func TestQueueMajorConflictsUnderEachThreshold(t *testing.T) {
	for _, b := range backends {
		for _, threshold := range thresholds {
			t.Run(b.name+"/"+string(threshold), func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = threshold
				toMajor(t, e, f, ws)
				next := rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, nil))
				if threshold != config.RebaseNever {
					want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
					if d := e.decision(t, "deliver-2"); d.Subject == nil || d.Subject.Digest != head3 {
						t.Fatalf("decision = %+v", d)
					}
					if got := e.authorizations(t); len(got) != 0 {
						t.Fatalf("authorizations = %q, want none carried", got)
					}
					return
				}
				want(t, next, done)
				a := e.pullRequestDoc(t, "deliver-2-authorized")
				if a["schema"] != "guildhall.merge-authorization/2" || a["carried_from"] != "deliver-1" || a["kind"] != "major_conflicts" || a["head"] != head3 || a["spec_digest"] != specDigest(t) {
					t.Fatalf("carried authorization = %v", a)
				}
				if mergedHead(f) != head3 {
					t.Fatalf("merged head = %q, want the rebuilt head", mergedHead(f))
				}
			})
		}
	}
}

// respecRecords rewrites the spec digest of every escalation recorded, as if the spec the approval was given under
// were another one, and keeps the merge queue of the records it wraps.
type respecRecords struct {
	interp.Records
	interp.MergeQueue
	dir string
}

func (r *respecRecords) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	if strings.Contains(d.Name, "-escalated-") {
		file := filepath.Join(r.dir, filepath.FromSlash(d.Path))
		data, err := os.ReadFile(file)
		if err != nil {
			return store.Receipt{}, err
		}
		data = bytes.Replace(data, []byte(`"spec_digest":"sha256:`), []byte(`"spec_digest":"sha256:0`), 1)
		if err := os.Remove(file); err != nil {
			return store.Receipt{}, err
		}
		if err := os.WriteFile(file, data, 0o644); err != nil {
			return store.Receipt{}, err
		}
		d.SHA256 = digest.Of(data).Hex()
	}
	return r.Records.PutDocument(id, d)
}

// TestQueueNeverCarryChecksEveryCondition: under reapprove_after_rebase = never, the approval of a head sent back for
// major conflicts does not carry over to the rebuilt head when nothing_open fails, even where it is not required, or
// when the accepted spec is not the one the approval was given under; a new merge decision says why.
func TestQueueNeverCarryChecksEveryCondition(t *testing.T) {
	t.Run("a finding with nothing_open not required", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = config.RebaseNever
		e.requires(config.MergeRequires{NothingOpen: ptr(false)})
		toMajor(t, e, f, ws)
		next := rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "No red test shown."}}))
		want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
		if d := e.decision(t, "deliver-2"); !strings.Contains(d.Question, "but it does not") || !strings.Contains(d.Question, "nothing_open does not hold") {
			t.Fatalf("question = %q", d.Question)
		}
		if got := e.authorizations(t); len(got) != 0 {
			t.Fatalf("authorizations = %q, want none carried", got)
		}
	})
	t.Run("another spec", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = config.RebaseNever
		records := e.eng.Records
		e.eng.Records = &respecRecords{Records: records, MergeQueue: records.(interp.MergeQueue), dir: e.eng.DocumentDir}
		base := baseN(1)
		ws.SetRebase(base, head, git.Rebased{Marked: commitN(1), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "logo.png"}}, HeadPatchID: "p"})
		f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"logo.png"}})
		exp := buildExp(t, 2, head3, "")
		exp.Inputs["feedback"] = interptest.AnyDigest
		e.script.Expect(exp)
		e.answer(t, "deliver-1", "merge")
		want(t, e.advance(t, answeredMerge), building2)
		e.eng.Records = records
		next := rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, nil))
		want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
		if d := e.decision(t, "deliver-2"); !strings.Contains(d.Question, "the one deliver-1 was approved under") {
			t.Fatalf("question = %q", d.Question)
		}
		if got := e.authorizations(t); len(got) != 0 {
			t.Fatalf("authorizations = %q, want none carried", got)
		}
	})
}

// drive advances the task from ev, interrupting, once, the first advance that would record document name, until the
// task reaches stop; each attempt wait is answered by its result. It fails when no advance was interrupted.
func drive(t *testing.T, e *env, name string, ev interp.Event, stop interp.Next) {
	t.Helper()
	records := e.eng.Records
	interrupted := false
	for i := 0; i < 20; i++ {
		if _, ok := e.documents(t)[name]; !ok && !interrupted {
			e.eng.Records = &interruptedDocumentRecord{Records: records, name: name}
			err := advanceErr(t, e, ev)
			e.eng.Records = records
			interrupted = err != nil
			if interrupted && !strings.Contains(err.Error(), "interrupted before document record") {
				t.Fatalf("interrupted advance = %v", err)
			}
		}
		next := e.advance(t, ev)
		if next == stop {
			if !interrupted {
				t.Fatalf("no advance was interrupted before %s", name)
			}
			return
		}
		ev = interp.Event{Kind: interp.TaskCreated}
		if next.Wait == interp.WaitAttempt {
			ev = interp.Event{Kind: interp.AttemptResult, Step: next.Step, Attempt: next.Attempt}
		}
	}
	t.Fatalf("the task never reached %+v", stop)
}

// attemptsOf counts the task's attempts of step.
func attemptsOf(t *testing.T, e *env, step string) int {
	t.Helper()
	n := 0
	for _, a := range e.attempts(t) {
		if a.Step == step {
			n++
		}
	}
	return n
}

// TestQueueTier2CrashReplay: an advance interrupted before each record of a tier 2 rebase at the front (the rebase
// document, the rebaser's conflicts input, the resolved head, the narrowed review's inputs and coverage, the push, the
// checks read, the carried authorization and the merge) replays to the same records: one rebaser attempt, one
// narrowed review, one authorization and one merge.
func TestQueueTier2CrashReplay(t *testing.T) {
	base := baseN(1)
	resolved := squashed(base, commitN(2))
	names := []string{"deliver-1-checks-" + resolved, "deliver-2-authorized", "deliver-2-merge"}
	for _, part := range []string{"", "-conflicts", "-resolved", "-resolution", "-head", "-covered", "-pr"} {
		names = append(names, "deliver-1-rebase-1"+part)
	}
	for _, b := range backends {
		for _, name := range names {
			t.Run(b.name+"/"+name, func(t *testing.T) {
				e, f, ws := queueEnv(t, b)
				p := &perHead{Fake: f, by: map[string]forge.Mergeability{}}
				p.set(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
				e.eng.Forge = p
				minor(ws, base, head, commitN(1))
				f.AddCommit(repoPath, resolved)
				f.SetChecks(repoRemote, resolved, ciPassed...)
				withRebaser(e)
				ws.setTip(commitN(2))
				e.script.Expect(rebaserExp("deliver", 1, commitN(2), false), narrowExp(t, "deliver", 1, resolved, bound(t, workflow.VerdictApproved, resolved, nil)))
				e.answer(t, "deliver-1", "merge")
				drive(t, e, name, answeredMerge, done)
				if mergedHead(f) != resolved {
					t.Fatalf("merged head = %q, want the resolution", mergedHead(f))
				}
				if r, n := attemptsOf(t, e, "deliver-rebase"), attemptsOf(t, e, "deliver-rebase-review"); r != 1 || n != 1 {
					t.Fatalf("rebaser attempts = %d, narrowed reviews = %d, want one each", r, n)
				}
				if a := e.pullRequestDoc(t, "deliver-2-authorized"); a["kind"] != "minor_conflicts" || a["head"] != resolved {
					t.Fatalf("authorization = %v", a)
				}
				if _, ok := e.documents(t)["deliver-1-rebase-2"]; ok {
					t.Fatal("the replay rebased again")
				}
				onceEach(t, e)
			})
		}
	}
}

// TestValidateRebaseCrashReplay: an advance interrupted before each record of a rebase at validate, for a clean
// replay and for a resolved one, replays to the same records: one rebase, one push of the rebased head and a result
// bound to it.
func TestValidateRebaseCrashReplay(t *testing.T) {
	resolved := squashed(baseN(1), commitN(2))
	cases := map[string]struct {
		names []string
		setup func(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces) string
	}{
		"clean": {[]string{"validate-1-rebase-1", "validate-1-rebase-1-pr", "validate-1-result"}, func(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces) string {
			clean(ws, baseN(1), head, head2)
			return head2
		}},
		"resolved": {[]string{"validate-1-rebase-1", "validate-1-rebase-1-conflicts", "validate-1-rebase-1-resolved", "validate-1-rebase-1-covered", "validate-1-rebase-1-pr", "validate-1-result"}, func(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces) string {
			minor(ws, baseN(1), head, commitN(1))
			withRebaser(e)
			f.AddCommit(repoPath, resolved)
			ws.setTip(commitN(2))
			e.script.Expect(rebaserExp("validate", 1, commitN(2), false), narrowExp(t, "validate", 1, resolved, bound(t, workflow.VerdictApproved, resolved, nil)))
			return resolved
		}},
	}
	for _, b := range backends {
		for tier, tc := range cases {
			for _, name := range tc.names {
				t.Run(b.name+"/"+tier+"/"+name, func(t *testing.T) {
					e, f, ws := validateRebaseEnv(t, b)
					h := tc.setup(t, e, f, ws)
					f.SetChecks(repoRemote, h, ciPassed...)
					drive(t, e, name, reviewed, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
					if v := e.pullRequestDoc(t, "validate-1-result"); v["head"] != h || v["verdict"] != "passed" || len(v["rebase"].([]any)) != 1 {
						t.Fatalf("result = %v", v)
					}
					if _, ok := e.documents(t)["validate-1-rebase-2"]; ok {
						t.Fatal("the replay rebased again")
					}
					if prs := f.PullRequests(repoRemote); len(prs) != 1 || prs[0].Head != h {
						t.Fatalf("pull requests = %+v", prs)
					}
					// A crash after the push and before its record pushes the same head again; nothing else is pushed.
					if publishes, _ := f.Calls(); publishes < 2 || publishes > 3 {
						t.Fatalf("publishes = %d", publishes)
					}
					if tier == "resolved" && (attemptsOf(t, e, "validate-rebase") != 1 || attemptsOf(t, e, "validate-rebase-review") != 1) {
						t.Fatalf("attempts = %+v, want one rebaser and one narrowed review", e.attempts(t))
					}
				})
			}
		}
	}
}

// TestValidateRebaseWithoutRebaser: a minor_conflicts replay at validate in a copy that declares no rebaser profile
// fails the round, naming the missing profile, and the failed loop sends the build back.
func TestValidateRebaseWithoutRebaser(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, ws := validateRebaseEnv(t, b)
			minor(ws, baseN(1), head, commitN(1))
			exp := buildExp(t, 2, head3, "")
			exp.Inputs["feedback"] = interptest.AnyDigest
			e.script.Expect(exp)
			want(t, e.advance(t, reviewed), building2)
			v := e.pullRequestDoc(t, "validate-1-result")
			esc, _ := v["rebase_escalation"].(map[string]any)
			if v["verdict"] != "failed" || esc == nil || esc["kind"] != "major_conflicts" || !strings.Contains(esc["reason"].(string), `no "rebaser" profile`) {
				t.Fatalf("result = %v", v)
			}
			if attemptsOf(t, e, "validate-rebase") != 0 {
				t.Fatal("a rebaser attempt started without a rebaser profile")
			}
		})
	}
}

// peer creates task id beside t7, authorized and queued under deliver-1, as another task's deliver would, and
// returns its store.
func peer(t *testing.T, e *env, id string) *store.Store {
	t.Helper()
	s := e.h.Records.(*store.Store)
	other, err := s.Task("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	other.ID, other.Title = id, "Peer "+id
	if _, err := s.CreateTask("create-"+id, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("auth-"+id, store.DocumentRef{Repository: "app", TaskID: id, Name: "deliver-1-authorized", Path: "tasks/app/" + id + "/a.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	rc, err := s.JoinMergeQueue("join-"+id, "app", id, "deliver-1", "document/deliver-1-authorized", "")
	if err != nil {
		t.Fatal(err)
	}
	// Ready times are receipt commit times: wait until the clock has passed this one, so the next receipt is later.
	for !time.Now().After(rc.CommittedAt) {
		runtime.Gosched()
	}
	return s
}

// order is the queue's task ids, front first.
func queueOrder(t *testing.T, s *store.Store) []string {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range q.Entries {
		ids = append(ids, e.TaskID)
	}
	return ids
}

var queuedWait = interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"}

// TestQueuePriorityReordersWaiting: a priority change moves a task waiting in the queue ahead of an earlier one of
// lower priority, but never takes the front from the task holding it; once the front leaves, the reordered task
// claims it and merges, and the task it passed cannot claim it first.
func TestQueuePriorityReordersWaiting(t *testing.T) {
	e, f, _ := queueEnv(t, backends[1])
	s := peer(t, e, "t1")
	if _, err := s.ClaimMergeFront("front-t1", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	peer(t, e, "t2")
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), queuedWait)
	if got := queueOrder(t, s); !slices.Equal(got, []string{"t1", "t2", "t7"}) {
		t.Fatalf("order = %v", got)
	}
	if _, err := s.SetTaskPriority("p-t7", "app", "t7", store.PriorityUrgent); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), queuedWait)
	if got := queueOrder(t, s); !slices.Equal(got, []string{"t1", "t7", "t2"}) {
		t.Fatalf("order after the priority change = %v, want t7 ahead of t2 and t1 keeping the front", got)
	}
	if _, err := s.LeaveMergeQueue("leave-t1", "app", "t1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimMergeFront("front-t2", "app", "t2"); !errors.Is(err, store.ErrMergeQueued) {
		t.Fatalf("claim by t2, now behind t7 = %v", err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if mergedHead(f) != head {
		t.Fatal("not merged")
	}
}

// TestQueueRejoinsAtFirstReadyTime: a task sent back for major conflicts rejoins the queue, once its rebuilt head is
// authorized, at its first ready time, ahead of a task of the same priority that became ready while it was rebuilt.
func TestQueueRejoinsAtFirstReadyTime(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	toMajor(t, e, f, ws)
	s := e.h.Records.(*store.Store)
	q, err := s.MergeQueue("app")
	if err != nil || len(q.Out) != 1 || q.Out[0].TaskID != "t7" {
		t.Fatalf("queue after the send-back = %+v, %v", q, err)
	}
	ready := q.Out[0].ReadyAt
	peer(t, e, "t1")
	if _, err := s.ClaimMergeFront("front-t1", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	peer(t, e, "t0")
	want(t, rebuild(t, e, f, ws, bound(t, workflow.VerdictApproved, head3, nil)), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-2"})
	e.answer(t, "deliver-2", "merge")
	want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-2"}), queuedWait)
	q, err = s.MergeQueue("app")
	if err != nil {
		t.Fatal(err)
	}
	if got := queueOrder(t, s); !slices.Equal(got, []string{"t1", "t7", "t0"}) {
		t.Fatalf("order = %v, want t7 back at its first ready time, ahead of t0", got)
	}
	if !q.Entries[1].ReadyAt.Equal(ready) || q.Entries[1].Authorization != "deliver-2" {
		t.Fatalf("rejoined entry = %+v, want ready time %v", q.Entries[1], ready)
	}
	if _, err := s.LeaveMergeQueue("leave-t1", "app", "t1", false); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if mergedHead(f) != head3 {
		t.Fatalf("merged head = %q", mergedHead(f))
	}
}

// TestQueueCancelledFront: a front whose task is cancelled no longer stands, and the next task claims it and merges.
func TestQueueCancelledFront(t *testing.T) {
	e, f, _ := queueEnv(t, backends[1])
	s := peer(t, e, "t1")
	if _, err := s.ClaimMergeFront("front-t1", "app", "t1"); err != nil {
		t.Fatal(err)
	}
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), queuedWait)
	if _, err := s.SetTaskState("cancel-t1", "app", "t1", store.TaskCancelled); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if mergedHead(f) != head {
		t.Fatal("not merged")
	}
}

// TestQueueTwoTasksRaceForTheFront: once the front is free, the task's advance and another queued task's claim, run
// at once, never both act as the front: the task first in queue order holds it, and the other waits, rebasing and merging nothing.
func TestQueueTwoTasksRaceForTheFront(t *testing.T) {
	for _, first := range []string{"t7", "t1"} {
		t.Run(first+" first", func(t *testing.T) {
			e, f, ws := queueEnv(t, backends[1])
			clean(ws, baseN(1), head, head2)
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
			// Both tasks wait behind t0 first, so each is queued when the front frees up.
			s := peer(t, e, "t0")
			if _, err := s.ClaimMergeFront("front-t0", "app", "t0"); err != nil {
				t.Fatal(err)
			}
			if first == "t1" {
				peer(t, e, "t1")
			}
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), queuedWait)
			if first == "t7" {
				// t7 joined in the advance, not through peer: wait until the clock has passed its ready time, so on a
				// coarse clock t1 cannot tie it and win on task id.
				q, err := s.MergeQueue("app")
				if err != nil {
					t.Fatal(err)
				}
				i := q.Position("t7") - 1
				if i < 0 {
					t.Fatalf("queue = %+v, want t7 queued", q.Entries)
				}
				for !time.Now().After(q.Entries[i].ReadyAt) {
					runtime.Gosched()
				}
				peer(t, e, "t1")
			}
			if _, err := s.LeaveMergeQueue("leave-t0", "app", "t0", false); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var claim error
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, claim = s.ClaimMergeFront("front-t1", "app", "t1")
			}()
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			wg.Wait()
			q, err := s.MergeQueue("app")
			if err != nil || q.Front == nil || q.Front.TaskID != first {
				t.Fatalf("front = %+v, %v; want %s", q.Front, err, first)
			}
			if first == "t1" {
				want(t, next, queuedWait)
				if claim != nil || len(ws.Replays()) != 0 {
					t.Fatalf("claim = %v, replays = %d; want t1's claim to stand and t7 to rebase nothing", claim, len(ws.Replays()))
				}
				return
			}
			want(t, next, deliverWait)
			if !errors.Is(claim, store.ErrMergeQueued) {
				t.Fatalf("claim by t1 = %v, want it refused", claim)
			}
		})
	}
}

// judgeAfter starts an attempt of a step the walk does not own, as a judge's would, once document name is recorded,
// so the rest of the same advance meets an attempt that was not running when it began.
type judgeAfter struct {
	interp.Records
	interp.MergeQueue
	name    string
	started bool
}

func (r *judgeAfter) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	rc, err := r.Records.PutDocument(id, d)
	if err == nil && d.Name == r.name && !r.started {
		r.started = true
		_, err = r.StartCappedAttempt("judge-"+d.Name, "app", "t7", "judge", store.AttemptCaps{})
	}
	return rc, err
}

// TestQueueSquashAndAdoptRefusedWhileAnAttemptRuns: an attempt that starts during the advance, after the replay,
// refuses the worktree move before the push, the move to the marked replay before the rebaser, the squash of the
// resolution and the move to the squashed head, each from a fresh read of the task's attempts.
func TestQueueSquashAndAdoptRefusedWhileAnAttemptRuns(t *testing.T) {
	refused := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "cannot rebase while an attempt runs") {
			t.Fatalf("advance = %v, want it refused while the judge runs", err)
		}
	}
	wrap := func(e *env, name string) {
		records := e.eng.Records
		e.eng.Records = &judgeAfter{Records: records, MergeQueue: records.(interp.MergeQueue), name: name}
	}
	t.Run("move before the push", func(t *testing.T) {
		e, f, ws := queueEnv(t, backends[1])
		clean(ws, baseN(1), head, head2)
		f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
		e.answer(t, "deliver-1", "merge")
		wrap(e, "deliver-1-rebase-1")
		refused(t, advanceErr(t, e, answeredMerge))
		if publishes, _ := f.Calls(); publishes != 1 || len(ws.adopted) != 0 {
			t.Fatalf("publishes = %d, adopted = %v; want nothing moved or pushed", publishes, ws.adopted)
		}
	})
	// tier2Start reaches the rebaser's result with the conflicts of tier 2 at the front.
	tier2Start := func(t *testing.T) (*env, *queueWorkspaces, string) {
		e, f, ws := queueEnv(t, backends[1])
		base := baseN(1)
		minor(ws, base, head, commitN(1))
		resolved := squashed(base, commitN(2))
		f.AddCommit(repoPath, resolved)
		f.SetMergeability(forge.Mergeability{Head: head, Base: base, State: "conflicting", Behind: 1, Files: []string{"a.go"}})
		withRebaser(e)
		ws.setTip(commitN(2))
		e.answer(t, "deliver-1", "merge")
		return e, ws, resolved
	}
	t.Run("move before the rebaser", func(t *testing.T) {
		e, ws, _ := tier2Start(t)
		wrap(e, "deliver-1-rebase-1-conflicts")
		refused(t, advanceErr(t, e, answeredMerge))
		if len(ws.adopted) != 0 || attemptsOf(t, e, "deliver-rebase") != 0 {
			t.Fatalf("adopted = %v, rebaser attempts = %d; want neither", ws.adopted, attemptsOf(t, e, "deliver-rebase"))
		}
	})
	t.Run("squash", func(t *testing.T) {
		e, ws, _ := tier2Start(t)
		e.script.Expect(rebaserExp("deliver", 1, commitN(2), false))
		want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase", Attempt: 1})
		wrap(e, "deliver-rebase-1-rebase")
		refused(t, advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase", Attempt: 1}))
		if ws.squashes != 0 {
			t.Fatalf("squashes = %d, want none", ws.squashes)
		}
	})
	t.Run("move to the squashed head", func(t *testing.T) {
		e, ws, resolved := tier2Start(t)
		e.script.Expect(rebaserExp("deliver", 1, commitN(2), false))
		want(t, e.advance(t, answeredMerge), interp.Next{Wait: interp.WaitAttempt, Step: "deliver-rebase", Attempt: 1})
		ws.afterSquash = func() {
			ws.afterSquash = nil
			if _, err := e.h.Records.StartCappedAttempt("judge-squash", "app", "t7", "judge", store.AttemptCaps{}); err != nil {
				t.Error(err)
			}
		}
		refused(t, advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "deliver-rebase", Attempt: 1}))
		if last := ws.adopted[len(ws.adopted)-1]; last[1] == resolved {
			t.Fatalf("adopted = %v, want no move to the squashed head", ws.adopted)
		}
		if _, ok := e.documents(t)["deliver-1-rebase-1-resolved"]; ok {
			t.Fatal("the resolution was recorded")
		}
	})
}
