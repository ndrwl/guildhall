package interp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

var (
	checksWait  = interp.Next{Wait: interp.WaitChecks, Step: "deliver"}
	ciPendingOn = []forge.Check{{Name: "ci", State: forge.CheckPending}}
	trainBranch = interp.TrainBranch("t7", 1)
)

// TestTrainBranch: a train branch is guildhall/train/<leader>-<stay>, a branch the forge accepts and a task branch,
// never the base.
func TestTrainBranch(t *testing.T) {
	for _, leader := range []string{"t7", "d229"} {
		b := interp.TrainBranch(leader, 12)
		if b != "guildhall/train/"+leader+"-12" || !forge.ValidBranch(b) || b == "main" {
			t.Errorf("train branch = %q", b)
		}
	}
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	f.AddCommit(repoPath, head)
	if _, err := f.Publish(context.Background(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: interp.TrainBranch("t7", 1), Head: head, Title: "Merge train", Body: "b"}); err != nil {
		t.Fatalf("publish to the train branch = %v", err)
	}
}

// prNumbered is pull request number of the remote, failing when there is none.
func prNumbered(t *testing.T, f *forgetest.Fake, number int) forge.PullRequest {
	t.Helper()
	prs := f.PullRequests(repoRemote)
	if len(prs) < number {
		t.Fatalf("no pull request %d among %+v", number, prs)
	}
	return prs[number-1]
}

// leadToTesting makes app/t7 the leader of a batch of 3 with peers t9 and t8 behind it: t7 is rebased onto baseN(1)
// as head2, its checks pass, and t9 and t8 link at commitN(3) and commitN(4); t7's next advance moves the batch to
// testing, opens the train pull request #2 at commitN(4) and waits for its checks, which are pending.
func leadToTesting(t *testing.T) (*env, *forgetest.Fake, *queueWorkspaces, *store.Store) {
	t.Helper()
	e, f, ws := queueEnv(t, backends[1])
	e.eng.Config.Repositories[0].Merge.MergeBatchSize = ptr(3)
	s := peer(t, e, "t9")
	peer(t, e, "t8")
	for _, id := range []string{"t9", "t8"} {
		if _, err := s.SetTaskPriority("later-"+id, "app", id, store.PrioritySomeday); err != nil {
			t.Fatal(err)
		}
	}
	clean(ws, baseN(1), head, head2)
	f.SetMergeability(forge.Mergeability{Head: head, Base: baseN(1), State: "behind", Behind: 1})
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), deliverWait)
	b := batchOf(t, s)
	if b.Leader != "t7" || len(b.Members) != 3 || b.Members[1].TaskID != "t9" || b.Members[2].TaskID != "t8" || b.Members[0].Link != head2 || b.State != store.BatchAssembling {
		t.Fatalf("batch = %+v", b)
	}
	f.SetChecks(repoRemote, head2, ciPassed...)
	f.SetMergeability(forge.Mergeability{})
	// One member still without its link keeps the batch assembling, and the leader waits in the queue.
	if _, err := s.SetMergeBatchLink("link-t9", "app", "t9", 1, commitN(3)); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if b := batchOf(t, s); b.State != store.BatchAssembling || b.Train != nil || len(f.PullRequests(repoRemote)) != 1 {
		t.Fatalf("batch with a link missing = %+v, pull requests %+v", b, f.PullRequests(repoRemote))
	}
	if _, err := s.SetMergeBatchLink("link-t8", "app", "t8", 1, commitN(4)); err != nil {
		t.Fatal(err)
	}
	f.SetChecks(repoRemote, commitN(4), ciPendingOn...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), checksWait)
	b = batchOf(t, s)
	if b.State != store.BatchTesting || b.Base != baseN(1) || b.Train == nil || b.Train.Number != 2 || b.Train.Head != commitN(4) {
		t.Fatalf("batch after every link = %+v, train %+v", b, b.Train)
	}
	return e, f, ws, s
}

// TestTrainLeaderLands: once every member is linked, the leader pushes the last link to guildhall/train/t7-1 and
// opens one train pull request, titled with each member, recorded as deliver-2-train-1-pr; with its own checks green
// the batch lands once every member is ready. The train is squash-merged once, the leader's -merge names it, its own
// pull request is closed with a comment linking it, the task is done, and the landed batch holds the queue until
// its other members finish.
func TestTrainLeaderLands(t *testing.T) {
	e, f, _, s := leadToTesting(t)
	train := prNumbered(t, f, 2)
	if train.Branch != trainBranch || train.Head != commitN(4) || train.State != forge.Open {
		t.Fatalf("train pull request = %+v", train)
	}
	title, body := f.Text(repoRemote, 2)
	if title != "Merge train: Straight path (#1), Peer t9, Peer t8" || !strings.Contains(body, "`app/t8` | none recorded | `"+commitN(4)+"`") {
		t.Fatalf("train text = %q\n%s", title, body)
	}
	if d := e.pullRequestDoc(t, "deliver-2-train-1-pr"); d["number"] != float64(2) || d["head"] != commitN(4) {
		t.Fatalf("train record = %v", d)
	}
	// A replay of the wait pushes nothing again.
	publishes, _ := f.Calls()
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), checksWait)
	if again, _ := f.Calls(); again != publishes {
		t.Fatalf("a replayed wait published %d more", again-publishes)
	}

	// Green: the batch lands, the leader is ready, and it waits for its members.
	f.SetChecks(repoRemote, commitN(4), ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	b := batchOf(t, s)
	if b.State != store.BatchLanding || !b.Members[0].Ready || b.Members[1].Ready || b.Members[2].Ready {
		t.Fatalf("batch after green train = %+v", b)
	}
	if _, merges := f.Calls(); merges != 0 {
		t.Fatalf("merged %d times before the members were ready", merges)
	}
	for _, m := range []struct{ id, link string }{{"t9", commitN(3)}, {"t8", commitN(4)}} {
		if _, err := s.SetMergeBatchReady("ready-"+m.id, "app", m.id, m.link); err != nil {
			t.Fatal(err)
		}
	}
	if next := e.advance(t, interp.Event{Kind: interp.TaskCreated}); next != done {
		t.Fatalf("next = %+v, messages %v", next, e.messages(t))
	}
	if _, merges := f.Calls(); merges != 1 {
		t.Fatalf("merges = %d, want the train merged once", merges)
	}
	train = prNumbered(t, f, 2)
	if train.State != forge.Merged || train.Head != commitN(4) || f.MergeSubject(repoRemote, 2) != title+" (#2)" {
		t.Fatalf("train = %+v, subject %q", train, f.MergeSubject(repoRemote, 2))
	}
	if own := prNumbered(t, f, 1); own.State != forge.Closed {
		t.Fatalf("own pull request = %+v, want closed", own)
	}
	if _, comments := f.Closes(); !slices.Equal(comments, []string{"Landed by Guildhall in merge train " + train.URL + " as " + train.MergeCommit + "."}) {
		t.Fatalf("close comments = %q", comments)
	}
	m := e.pullRequestDoc(t, "deliver-2-merge")
	tr, _ := m["train"].(map[string]any)
	if m["number"] != float64(2) || m["merge_commit"] != train.MergeCommit || tr["leader"] != "t7" || tr["link"] != head2 || tr["head"] != head2 || tr["authorized"] != "deliver-2-authorized" {
		t.Fatalf("merge record = %v", m)
	}
	if own, _ := tr["pull_request"].(map[string]any); own["number"] != float64(1) {
		t.Fatalf("own pull request in the record = %v", tr["pull_request"])
	}
	if text := e.messages(t)["app.t7.merged.deliver-2"]; !strings.Contains(text, "merge train #2") || !strings.Contains(text, "merged in train #2 led by t7, deliver-2-authorized") {
		t.Fatalf("merged message = %q", text)
	}
	wantNoOutside(t, e)

	// The landed batch stays for t9 and t8, and no one else claims the front meanwhile.
	q, err := s.MergeQueue("app")
	if err != nil || q.Batch == nil || q.Batch.State != store.BatchLanded || !q.Batch.Members[0].Finished || q.Front != nil {
		t.Fatalf("queue after landing = %+v, batch %+v, %v", q, q.Batch, err)
	}
	if _, err := s.ClaimMergeFront("claim-t9", "app", "t9"); !errors.Is(err, store.ErrMergeQueued) {
		t.Fatalf("claim during the landed batch = %v", err)
	}
	for _, id := range []string{"t9", "t8"} {
		if _, err := s.LeaveMergeQueue("merged-"+id, "app", id, false); err != nil {
			t.Fatal(err)
		}
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil {
		t.Fatalf("batch after every member finished = %+v", q.Batch)
	}
}

func wantNoOutside(t *testing.T, e *env) {
	t.Helper()
	for id := range e.messages(t) {
		if strings.Contains(id, "merged.outside") {
			t.Fatalf("an outside merge was announced: %v", e.messages(t))
		}
	}
}

// failingTrainRecord fails the train's merge record, as a crash between the forge's merge and its record would.
type failingTrainRecord struct {
	interp.Records
	queueRecords
}

func (r *failingTrainRecord) SetMergeBatchTrain(m, repo, leader string, train store.MergeTrain) (store.Receipt, error) {
	if train.MergeCommit != "" {
		return store.Receipt{}, errors.New("interrupted before the train's merge record")
	}
	return r.queueRecords.SetMergeBatchTrain(m, repo, leader, train)
}

// TestTrainLeaderCrashReplay: an advance interrupted after the train merged, before the merge commit reaches the
// batch record or before the leader's -merge is recorded, replays to the same records: the forge reports the train
// merged and nothing merges again, no outside merge is announced, and the task is done.
func TestTrainLeaderCrashReplay(t *testing.T) {
	for name, wrap := range map[string]func(interp.Records) interp.Records{
		"before the batch records the merge": func(r interp.Records) interp.Records {
			return &failingTrainRecord{Records: r, queueRecords: r.(interp.MergeQueue)}
		},
		"before the merge record": func(r interp.Records) interp.Records {
			return &interruptedQueueRecord{Records: r, queueRecords: r.(interp.MergeQueue), name: "deliver-2-merge"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, f, _, s := leadToTesting(t)
			f.SetChecks(repoRemote, commitN(4), ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
			for _, m := range []struct{ id, link string }{{"t9", commitN(3)}, {"t8", commitN(4)}} {
				if _, err := s.SetMergeBatchReady("ready-"+m.id, "app", m.id, m.link); err != nil {
					t.Fatal(err)
				}
			}
			records := e.eng.Records
			e.eng.Records = wrap(records)
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); err == nil || !strings.Contains(err.Error(), "interrupted") {
				t.Fatalf("interrupted advance = %v", err)
			}
			e.eng.Records = records
			if prNumbered(t, f, 2).State != forge.Merged {
				t.Fatal("the train did not merge before the interruption")
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
			if _, merges := f.Calls(); merges != 1 {
				t.Fatalf("merges = %d, want one", merges)
			}
			if m := e.pullRequestDoc(t, "deliver-2-merge"); m["number"] != float64(2) {
				t.Fatalf("merge record = %v", m)
			}
			wantNoOutside(t, e)
			if b := batchOf(t, s); b.State != store.BatchLanded || b.Train.MergeCommit != prNumbered(t, f, 2).MergeCommit {
				t.Fatalf("batch = %+v", b)
			}
		})
	}
}

// TestTrainRedEndsBatch: a train whose own checks fail ends the batch: its pull request is closed with why, the
// Director is told, and the leader merges its own pull request alone, its members keeping their places.
func TestTrainRedEndsBatch(t *testing.T) {
	e, f, _, s := leadToTesting(t)
	f.SetChecks(repoRemote, commitN(4), ciFailed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	if train := prNumbered(t, f, 2); train.State != forge.Closed {
		t.Fatalf("train = %+v, want closed", train)
	}
	if own := prNumbered(t, f, 1); own.State != forge.Merged || own.Head != head2 {
		t.Fatalf("own pull request = %+v, want merged at the leader's link", own)
	}
	if _, comments := f.Closes(); len(comments) != 1 || !strings.Contains(comments[0], "checks of merge train") {
		t.Fatalf("close comments = %q", comments)
	}
	found := slices.ContainsFunc(mapValues(e.messages(t)), func(text string) bool { return strings.Contains(text, "The merge batch task app/t7 leads ended") })
	if !found {
		t.Fatalf("no batch-ended message: %v", e.messages(t))
	}
	if q, _ := s.MergeQueue("app"); q.Batch != nil || !slices.Equal(queueOrder(t, s), []string{"t9", "t8"}) {
		t.Fatalf("queue after the red train = %+v", q)
	}
}

// landingBehind puts app/t7 at the end of a batch of 2 that t1 leads, linked at commitN(6) onto t1's link
// commitN(5), with the train pull request #2 at commitN(6) and the batch landing.
func landingBehind(t *testing.T, e *env, f *forgetest.Fake, ws *queueWorkspaces) *store.Store {
	t.Helper()
	s := batchBehind(t, e, 2, commitN(5))
	clean(ws, commitN(5), head, commitN(6))
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if _, err := s.SetMergeBatchState("t1-testing", "app", "t1", store.BatchTesting, baseN(1)); err != nil {
		t.Fatal(err)
	}
	train, err := f.Publish(context.Background(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: interp.TrainBranch("t1", 1), Head: commitN(6), Title: "Merge train: t1, t7", Body: "train"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchTrain("t1-train", "app", "t1", store.MergeTrain{Remote: repoRemote, Number: train.Number, URL: train.URL, Head: train.Head}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("t1-landing", "app", "t1", store.BatchLanding, ""); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTrainMemberLands: a member of a landing batch reads the train's own checks, carries its approval over to its
// link as deliver-1-link-1-authorized and marks itself ready; once the leader landed the train, the member records
// its deliver-1-merge naming the train, closes its own pull request with a comment linking it, and is done, which
// clears the batch it was the last to finish.
func TestTrainMemberLands(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	s := landingBehind(t, e, f, ws)
	f.SetChecks(repoRemote, commitN(6), ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	a := e.pullRequestDoc(t, "deliver-1-link-1-authorized")
	if a["schema"] != "guildhall.merge-authorization/2" || a["carried_from"] != "deliver-1" || a["head"] != commitN(6) || a["kind"] != "no_conflicts" {
		t.Fatalf("link authorization = %v", a)
	}
	if b := batchOf(t, s); !b.Members[1].Ready || b.Members[0].Ready {
		t.Fatalf("batch = %+v", b)
	}

	// The leader lands the train and finishes.
	if _, err := s.SetMergeBatchReady("t1-ready", "app", "t1", commitN(5)); err != nil {
		t.Fatal(err)
	}
	merged, err := f.Merge(context.Background(), forge.Merge{Repository: repoPath, Remote: repoRemote, Number: 2, Head: commitN(6), Title: "Merge train: t1, t7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchTrain("t1-merged", "app", "t1", store.MergeTrain{Remote: repoRemote, Number: 2, URL: merged.URL, Head: commitN(6), MergeCommit: merged.MergeCommit}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchState("t1-landed", "app", "t1", store.BatchLanded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaveMergeQueue("t1-done", "app", "t1", false); err != nil {
		t.Fatal(err)
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
	m := e.pullRequestDoc(t, "deliver-1-merge")
	tr, _ := m["train"].(map[string]any)
	if m["number"] != float64(2) || m["merge_commit"] != merged.MergeCommit || tr["leader"] != "t1" || tr["link"] != commitN(6) || tr["head"] != head || tr["authorized"] != "deliver-1-link-1-authorized" {
		t.Fatalf("merge record = %v", m)
	}
	if own := prNumbered(t, f, 1); own.State != forge.Closed {
		t.Fatalf("own pull request = %+v", own)
	}
	if _, comments := f.Closes(); !slices.Equal(comments, []string{"Landed by Guildhall in merge train " + merged.URL + " as " + merged.MergeCommit + "."}) {
		t.Fatalf("close comments = %q", comments)
	}
	if text := e.messages(t)["app.t7.merged.deliver-1"]; !strings.Contains(text, "Landed head "+head+" in merge train #2") || !strings.Contains(text, "led by t1, deliver-1-link-1-authorized") {
		t.Fatalf("merged message = %q", text)
	}
	wantNoOutside(t, e)
	if q, _ := s.MergeQueue("app"); q.Batch != nil || len(q.Entries) != 0 {
		t.Fatalf("queue after the last member finished = %+v", q)
	}
}

// sliceReplays is the slice tests' workspace double with gittest's scripted replays.
type sliceReplays struct {
	*sliceWorkspaces
	replays *gittest.Workspaces
}

func (w sliceReplays) Rebase(ctx context.Context, r git.Replay) (git.Rebased, error) {
	return w.replays.Rebase(ctx, r)
}

// TestTrainSlicedMemberClosesItsSlice: a member delivering slice 1 of 3 that lands in a merge train records the
// slice's merge naming the train, closes the slice's pull request, retires the slice at its own head and goes on to
// slice 2; the task stays open.
func TestTrainSlicedMemberClosesItsSlice(t *testing.T) {
	e, f, ws := newSliceEnv(t, backends[1], true, toBuildOf(t, 2)...)
	s := peer(t, e, "t1")
	replays := gittest.NewWorkspaces()
	e.eng.Workspaces = sliceReplays{sliceWorkspaces: ws, replays: replays}
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	e.answer(t, "deliver-1", "merge")
	want(t, e.advance(t, answeredMerge), batchWait)
	if _, err := s.ClaimMergeBatch("batch-t1", "app", "t1", 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchLink("link-t1", "app", "t1", 1, commitN(5)); err != nil {
		t.Fatal(err)
	}
	link := commitN(6)
	f.AddCommit(repoPath, link)
	f.AddCommit(repoPath, commitN(5))
	replays.SetRebase(commitN(5), sliceHeads[0], git.Rebased{Commit: link, MergeBase: baseHead, HeadPatchID: "p", PatchID: "p"})
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if _, err := s.SetMergeBatchState("t1-testing", "app", "t1", store.BatchTesting, ""); err != nil {
		t.Fatal(err)
	}
	train, err := f.Publish(context.Background(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: interp.TrainBranch("t1", 1), Head: link, Title: "Merge train", Body: "train"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error {
			_, err := s.SetMergeBatchTrain("t1-train", "app", "t1", store.MergeTrain{Remote: repoRemote, Number: train.Number, URL: train.URL, Head: link})
			return err
		},
		func() error {
			_, err := s.SetMergeBatchState("t1-landing", "app", "t1", store.BatchLanding, "")
			return err
		},
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	f.SetChecks(repoRemote, link, ciPassed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	if b := batchOf(t, s); !b.Members[1].Ready {
		t.Fatalf("batch = %+v, want t7 ready", b)
	}
	merged, err := f.Merge(context.Background(), forge.Merge{Repository: repoPath, Remote: repoRemote, Number: train.Number, Head: link, Title: "Merge train"})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { _, err := s.SetMergeBatchReady("t1-ready", "app", "t1", commitN(5)); return err },
		func() error {
			_, err := s.SetMergeBatchTrain("t1-merged", "app", "t1", store.MergeTrain{Remote: repoRemote, Number: train.Number, URL: train.URL, Head: link, MergeCommit: merged.MergeCommit})
			return err
		},
		func() error {
			_, err := s.SetMergeBatchState("t1-landed", "app", "t1", store.BatchLanded, "")
			return err
		},
		func() error { _, err := s.LeaveMergeQueue("t1-done", "app", "t1", false); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
	if got := e.task(t).State; got != store.TaskOpen {
		t.Fatalf("task state = %s, want open", got)
	}
	if m := e.pullRequestDoc(t, "deliver-1-merge"); m["number"] != float64(train.Number) || m["train"] == nil {
		t.Fatalf("slice 1's merge record = %v", m)
	}
	if own := prNumbered(t, f, 1); own.State != forge.Closed {
		t.Fatalf("slice 1's pull request = %+v", own)
	}
	if r := ws.Retired(); len(r) != 1 || r[0].Branch != "guildhall/t7" || r[0].Head != sliceHeads[0] {
		t.Fatalf("retired %+v, want slice 1's branch at its own head", r)
	}
	if text := e.messages(t)["app.t7.merged.deliver-1"]; !strings.Contains(text, "merge train #2") || !strings.Contains(text, "slice 1 of 3 (Parser) merged") {
		t.Fatalf("merged message = %q", text)
	}
}

// TestTrainMemberFailsAGate: a member whose gates do not hold for its link while the batch is landing, here the
// train's checks read red, leaves the batch, which goes back to assembling, and tells the Director why.
func TestTrainMemberFailsAGate(t *testing.T) {
	e, f, ws := queueEnv(t, backends[1])
	s := landingBehind(t, e, f, ws)
	f.SetChecks(repoRemote, commitN(6), ciFailed...)
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), batchWait)
	b := batchOf(t, s)
	if len(b.Members) != 1 || b.State != store.BatchAssembling {
		t.Fatalf("batch = %+v, want t7 gone and the batch assembling", b)
	}
	if text := e.messages(t)["app.t7.batch-left.t1.1"]; !strings.Contains(text, "checks of merge train") {
		t.Fatalf("batch-left message = %q", text)
	}
	if _, ok := e.documents(t)["deliver-1-link-1-authorized"]; ok {
		t.Fatal("the link was authorized")
	}
}
