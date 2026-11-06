package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

var batchWait = interp.Next{Wait: interp.WaitMergeQueue, Step: "deliver"}

// batchBehind puts app/t7 behind leader t1 in a merge batch of size, with t1's link recorded as leaderLink: t1
// queues first, t7 is authorized and waits behind it, and t1 claims the batch. It returns the store.
func batchBehind(t *testing.T, e *env, size int, leaderLink string) *store.Store {
	t.Helper()
	s := peer(t, e, "t1")
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), batchWait)
	if _, err := s.ClaimMergeBatch("batch-t1", "app", "t1", 1, size); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchLink("link-t1", "app", "t1", 1, leaderLink); err != nil {
		t.Fatal(err)
	}
	return s
}

// batchOf is the standing batch of app, failing when there is none.
func batchOf(t *testing.T, s *store.Store) store.MergeBatch {
	t.Helper()
	q, err := s.MergeQueue("app")
	if err != nil || q.Batch == nil {
		t.Fatalf("batch = %+v, %v", q.Batch, err)
	}
	return *q.Batch
}

// prHead is the head of app/t7's pull request, the remote's first.
func prHead(t *testing.T, f *forgetest.Fake) string {
	t.Helper()
	prs := f.PullRequests(repoRemote)
	if len(prs) == 0 {
		t.Fatal("no pull request")
	}
	return prs[0].Head
}

// TestBatchMemberReplaysOntoItsPredecessor: a member behind the leader replays its own head onto the leader's link,
// records it as deliver-1-link-1, pushes it to its pull request without moving its worktree and records it as its
// link; a replay of the same advance changes nothing, and a new leader link is replayed onto again as
// deliver-1-link-2.
func TestBatchMemberReplaysOntoItsPredecessor(t *testing.T) {
	// The batch's other members are made in the store, so these tests run on the store backend.
	for _, b := range backends[1:] {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			s := batchBehind(t, e, 3, commitN(5))
			clean(ws, commitN(5), head, commitN(6))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			l := e.pullRequestDoc(t, "deliver-1-link-1")
			if l["schema"] != "guildhall.batch-link/1" || l["kind"] != "no_conflicts" || l["from"] != head || l["onto"] != commitN(5) || l["rebased"] != commitN(6) || l["leader"] != "t1" {
				t.Fatalf("link = %v", l)
			}
			if got := prHead(t, f); got != commitN(6) {
				t.Fatalf("pull request head = %s, want the link", got)
			}
			if m := batchOf(t, s).Members; len(m) != 2 || m[1].TaskID != "t7" || m[1].Link != commitN(6) || m[1].Slice != 1 {
				t.Fatalf("members = %+v", m)
			}
			if len(ws.adopted) != 0 {
				t.Fatalf("worktree moved: %v", ws.adopted)
			}
			if _, ok := e.messages(t)["app.t7.batch-link.deliver-1-link-1"]; !ok {
				t.Fatalf("no batch-link message: %v", e.messages(t))
			}
			// Joining the batch moves the task's place but is not an event of its own: the batch-link message tells it, and
			// the only merge-queued message is the one sent when the task entered the queue.
			if slices.ContainsFunc(mapValues(e.messages(t)), func(text string) bool { return strings.Contains(text, "in batch 2/2 that task t1 leads") }) {
				t.Fatalf("a merge-queued message for joining the batch: %v", e.messages(t))
			}
			if _, ok := e.messages(t)["app.t7.merge-queued.deliver.deliver-1"]; !ok {
				t.Fatalf("no enter message: %v", e.messages(t))
			}
			publishes, _ := f.Calls()
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if again, _ := f.Calls(); again != publishes || len(ws.Replays()) != 1 {
				t.Fatalf("a replayed advance published %d more and replayed %d times", again-publishes, len(ws.Replays()))
			}

			// The leader rebased again: its new link clears t7's, which replays onto it.
			if _, err := s.SetMergeBatchLink("link-t1-2", "app", "t1", 1, commitN(7)); err != nil {
				t.Fatal(err)
			}
			clean(ws, commitN(7), head, commitN(8))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if l := e.pullRequestDoc(t, "deliver-1-link-2"); l["onto"] != commitN(7) || l["rebased"] != commitN(8) {
				t.Fatalf("second link = %v", l)
			}
			if got := prHead(t, f); got != commitN(8) || batchOf(t, s).Members[1].Link != commitN(8) {
				t.Fatalf("pull request head = %s, batch = %+v", got, batchOf(t, s))
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// TestBatchMemberWaitsForItsPredecessor: a member whose predecessor has no link yet replays nothing.
func TestBatchMemberWaitsForItsPredecessor(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	s := peer(t, e, "t1")
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), batchWait)
	if _, err := s.ClaimMergeBatch("batch-t1", "app", "t1", 1, 2); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if len(ws.Replays()) != 0 || prHead(t, f) != head {
		t.Fatalf("replays = %d, pull request head = %s", len(ws.Replays()), prHead(t, f))
	}
}

// TestBatchMemberLeaves: a member leaves the batch, keeping its place in the queue, on a minor or major conflicts
// replay, on a hold, while an attempt of the task runs, and when its approval would not carry over a no_conflicts
// replay; nothing is pushed, and the Director is told why.
func TestBatchMemberLeaves(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(t *testing.T, e *env, s *store.Store, ws *queueWorkspaces)
		why   string
	}{
		"minor conflicts": {func(t *testing.T, e *env, s *store.Store, ws *queueWorkspaces) {
			ws.SetRebase(commitN(5), head, git.Rebased{Marked: commitN(6), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "a.go", Lines: 2, Markers: true}}, HeadPatchID: "p"})
		}, "is minor_conflicts"},
		"major conflicts": {func(t *testing.T, e *env, s *store.Store, ws *queueWorkspaces) {
			ws.SetRebase(commitN(5), head, git.Rebased{Marked: commitN(6), MergeBase: baseHead, Conflicts: []git.Conflict{{Path: "logo.png"}}, HeadPatchID: "p"})
		}, "is major_conflicts"},
		"a running attempt": {func(t *testing.T, e *env, s *store.Store, ws *queueWorkspaces) {
			if _, err := s.StartCappedAttempt("running", "app", "t7", "judge", store.AttemptCaps{}); err != nil {
				t.Fatal(err)
			}
		}, "an attempt of the task is running"},
		"no_conflicts needs reapproval": {func(t *testing.T, e *env, s *store.Store, ws *queueWorkspaces) {
			clean(ws, commitN(5), head, commitN(6))
			e.eng.Config.Repositories[0].Merge.ReapproveAfterRebase = "no_conflicts"
		}, "does not carry over a no_conflicts replay"},
	} {
		t.Run(name, func(t *testing.T) {
			e, f, ws := queueEnv(t, backends[1])
			s := batchBehind(t, e, 3, commitN(5))
			c.setup(t, e, s, ws)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if m := batchOf(t, s).Members; len(m) != 1 || m[0].TaskID != "t1" {
				t.Fatalf("members = %+v, want t7 gone", m)
			}
			if got := queueOrder(t, s); !slices.Equal(got, []string{"t1", "t7"}) {
				t.Fatalf("order = %v, want t7 still queued", got)
			}
			if got := prHead(t, f); got != head {
				t.Fatalf("pull request head = %s, want nothing pushed", got)
			}
			if text := e.messages(t)["app.t7.batch-left.t1.1"]; !strings.Contains(text, c.why) {
				t.Fatalf("batch-left message = %q, want %q", text, c.why)
			}
		})
	}
	t.Run("a hold", func(t *testing.T) {
		e, _, ws := queueEnv(t, backends[1])
		s := batchBehind(t, e, 3, commitN(5))
		clean(ws, commitN(5), head, commitN(6))
		if _, err := s.SetMergeHold("hold", "app", "t7", true); err != nil {
			t.Fatal(err)
		}
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
		if m := batchOf(t, s).Members; len(m) != 1 || len(ws.Replays()) != 0 {
			t.Fatalf("members = %+v, replays = %d", m, len(ws.Replays()))
		}
	})
}

// TestBatchMiddleMemberLeaves: in a batch of 3 (t1 leading, t2, t7 last), t7 replays onto t2's link; when t2 leaves the
// batch, as its own advance does on a minor_conflicts replay, or is cancelled, which drops it out through the queue,
// t7's link is cleared and its next advance replays its head onto the leader's link as deliver-1-link-2, pushes it to
// its pull request and records it as its link.
func TestBatchMiddleMemberLeaves(t *testing.T) {
	for name, leave := range map[string]func(t *testing.T, s *store.Store){
		"minor conflicts": func(t *testing.T, s *store.Store) {
			if _, err := s.LeaveMergeBatch("t2-left", "app", "t2"); err != nil {
				t.Fatal(err)
			}
		},
		"a cancel": func(t *testing.T, s *store.Store) {
			if _, err := s.CancelTask("t2-cancelled", "app", "t2", "not wanted"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, f, ws := queueEnv(t, backends[1])
			s := peer(t, e, "t1")
			peer(t, e, "t2")
			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, answeredMerge), batchWait)
			if _, err := s.ClaimMergeBatch("batch-t1", "app", "t1", 1, 3); err != nil {
				t.Fatal(err)
			}
			// In batch order: a link clears those after it.
			for _, l := range []struct{ id, link string }{{"t1", commitN(5)}, {"t2", commitN(6)}} {
				if _, err := s.SetMergeBatchLink("link-"+l.id, "app", l.id, 1, l.link); err != nil {
					t.Fatal(err)
				}
			}
			clean(ws, commitN(6), head, commitN(7))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if l := e.pullRequestDoc(t, "deliver-1-link-1"); l["onto"] != commitN(6) || l["rebased"] != commitN(7) {
				t.Fatalf("first link = %v", l)
			}
			if m := batchOf(t, s).Members; len(m) != 3 || m[1].TaskID != "t2" || m[2].TaskID != "t7" || m[2].Link != commitN(7) {
				t.Fatalf("members = %+v", m)
			}

			leave(t, s)
			if m := batchOf(t, s).Members; len(m) != 2 || m[1].TaskID != "t7" || m[1].Link != "" {
				t.Fatalf("members after t2 left = %+v, want t7's link cleared", m)
			}
			clean(ws, commitN(5), head, commitN(8))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if l := e.pullRequestDoc(t, "deliver-1-link-2"); l["from"] != head || l["onto"] != commitN(5) || l["rebased"] != commitN(8) || l["kind"] != "no_conflicts" {
				t.Fatalf("second link = %v", l)
			}
			if got := prHead(t, f); got != commitN(8) {
				t.Fatalf("pull request head = %s, want the new link", got)
			}
			if m := batchOf(t, s).Members; len(m) != 2 || m[0].Link != commitN(5) || m[1].Link != commitN(8) {
				t.Fatalf("members = %+v", m)
			}
		})
	}
}

// TestBatchMemberRestoresItsHead: a member that pushed a link and then reached the front alone, once the leader
// merged, puts its own head back on its pull request before it merges, and records it once.
func TestBatchMemberRestoresItsHead(t *testing.T) {
	for _, b := range backends[1:] {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			s := batchBehind(t, e, 2, commitN(5))
			clean(ws, commitN(5), head, commitN(6))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if prHead(t, f) != commitN(6) {
				t.Fatal("the link was not pushed")
			}
			if _, err := s.LeaveMergeQueue("t1-merged", "app", "t1", false); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if got := mergedHead(f); got != head {
				t.Fatalf("merged head = %q, want the task's own head", got)
			}
			if r := e.pullRequestDoc(t, "deliver-1-link-1-restored"); r["head"] != head {
				t.Fatalf("restored = %v", r)
			}
			title, _ := f.Text(repoRemote, 1)
			if strings.Contains(title, "batch") || title == "" {
				t.Fatalf("restored title = %q", title)
			}
		})
	}
}

// The leader's part, from claiming the batch to landing its train, is TestTrainLeaderLands (mergetrain_test.go).

// queueRecords is interp.MergeQueue under a name of its own: embedded as itself, its field would be named MergeQueue
// and hide the method of that name, so the wrapper would keep no queue.
type queueRecords = interp.MergeQueue

var _ interp.MergeQueue = (*interruptedQueueRecord)(nil)

// interruptedQueueRecord is interruptedDocumentRecord over records that keep a merge queue.
type interruptedQueueRecord struct {
	interp.Records
	queueRecords
	name string
}

func (r *interruptedQueueRecord) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	if d.Name == r.name {
		return store.Receipt{}, errors.New("interrupted before document record")
	}
	return r.Records.PutDocument(id, d)
}

// TestBatchMemberCrashReplay: an advance interrupted before the link's record or its push record replays to the same
// link, recorded once.
func TestBatchMemberCrashReplay(t *testing.T) {
	for _, name := range []string{"deliver-1-link-1", "deliver-1-link-1-pr"} {
		t.Run(name, func(t *testing.T) {
			e, f, ws := queueEnv(t, backends[1])
			s := batchBehind(t, e, 2, commitN(5))
			clean(ws, commitN(5), head, commitN(6))
			records := e.eng.Records
			e.eng.Records = &interruptedQueueRecord{Records: records, queueRecords: records.(interp.MergeQueue), name: name}
			if next, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); err == nil || !strings.Contains(err.Error(), "interrupted before document record") {
				t.Fatalf("interrupted advance = %+v, %v; documents %v", next, err, e.documents(t))
			}
			e.eng.Records = records
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			if _, ok := e.documents(t)[name]; !ok {
				t.Fatalf("%s not recorded on the replay", name)
			}
			if _, ok := e.documents(t)["deliver-1-link-2"]; ok {
				t.Fatal("a second link was recorded")
			}
			if prHead(t, f) != commitN(6) || batchOf(t, s).Members[1].Link != commitN(6) {
				t.Fatalf("pull request head = %s, batch = %+v", prHead(t, f), batchOf(t, s))
			}
		})
	}
}
