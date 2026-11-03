package interp_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// The tests of a slice closed as already delivered (docs/tasks.md#a-slice-already-delivered), over the fixtures of
// slice_test.go.

// failedSliceBuild is slice k's build attempt 1 failing as a builder with nothing left to commit reports it.
func failedSliceBuild(k int, by string) interptest.Expectation {
	return interptest.Expectation{Step: workflow.SliceStep("build", k), Attempt: 1, Profile: "builder",
		Inputs: map[string]string{"spec": "sha256:" + sum([]byte(threeSlices))}, Standards: []string{"standards/go.md"},
		Respond: interptest.Fail("Already delivered by " + by + ": every item is in its merge.")}
}

// hungSliceBuild is slice k's build attempt 1 launched and never finishing.
func hungSliceBuild(k int) interptest.Expectation {
	return interptest.Expectation{Step: workflow.SliceStep("build", k), Attempt: 1, Profile: "builder",
		Inputs: map[string]string{"spec": "sha256:" + sum([]byte(threeSlices))}, Standards: []string{"standards/go.md"}, Respond: interptest.Hang()}
}

// mergedMessages counts the Director's merged messages, the messages hallmark_after_merges counts.
func (e *env) mergedMessages(t *testing.T) int {
	t.Helper()
	page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range page.Messages {
		if m.Type == store.MessageMerged {
			n++
		}
	}
	return n
}

// slices reads app/t7's slice progress as director status and task cancel do.
func (e *env) slices(t *testing.T) interp.SliceProgress {
	t.Helper()
	docs, err := e.h.Records.Documents("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := e.h.Records.Decisions("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	def, err := e.eng.Workflow(e.task(t).Workflow)
	if err != nil {
		t.Fatal(err)
	}
	p, err := interp.TaskSlices(def, "app", "t7", docs, decisions, e.eng.DocumentDir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// toFailedSlice2 is a three-slice task with slice 1 merged and slice 2's build failed with nothing to build, then exps.
func toFailedSlice2(t *testing.T, b backend, exps ...interptest.Expectation) (*env, *forgetest.Fake, *sliceWorkspaces) {
	t.Helper()
	e, f, ws := newSliceEnv(t, b, true, append(append(sliceScript(t, threeSlices, 1), failedSliceBuild(2, "slice 1")), exps...)...)
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build--s2", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build--s2", Attempt: 1})
	return e, f, ws
}

// TestDeliveredMiddleSliceStartsTheNext: d160's failure. Slice 1 merged, and slice 2's build failed because slice 1
// had already shipped its work. The Director's slice-delivered event naming slice 1 records slice 2's delivery, voids
// its open decisions, tells the Director once, clears the merge hold, and the same advance starts slice 3's build.
func TestDeliveredMiddleSliceStartsTheNext(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, ws := toFailedSlice2(t, b, hungSliceBuild(3))
			if _, err := e.h.Records.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t7", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"}); err != nil {
				t.Fatal(err)
			}
			s, isStore := e.h.Records.(*store.Store)
			if isStore {
				if _, err := s.SetMergeHold("hold-t7", "app", "t7", true); err != nil {
					t.Fatal(err)
				}
			}
			merged := e.mergedMessages(t)
			ev := interp.Event{Kind: interp.SliceDelivered, Slice: 2, BySlice: 1, Reason: "slice 1 (#1) shipped every slice 2 item"}
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitAttempt, Step: "build--s3", Attempt: 1})
			if got := e.task(t).State; got != store.TaskOpen {
				t.Fatalf("task state = %s, want open", got)
			}
			if _, ok := e.documents(t)["delivered--s2-1-delivery"]; !ok {
				t.Error("slice 2's delivery is not recorded as delivered--s2-1-delivery")
			}
			if d := e.decision(t, "q-1"); !d.Void || d.VoidReason != "Slice 2 already delivered by slice 1: slice 1 (#1) shipped every slice 2 item" {
				t.Errorf("q-1 = %+v, want it voided with slice 2's delivery", d)
			}
			text := e.messages(t)["app.t7.slice-delivered.delivered--s2-1"]
			if !strings.Contains(text, "slice 2 of 3 (Engine) as already delivered by slice 1") || !strings.Contains(text, "Slice 3 of 3 starts from main on branch guildhall/t7.s3") {
				t.Errorf("slice-delivered message = %q", text)
			}
			if n := e.mergedMessages(t); n != merged {
				t.Errorf("merged messages = %d after the delivery, want %d: nothing merged", n, merged)
			}
			if isStore {
				if q, err := s.MergeQueue("app"); err != nil || q.Holds["t7"] {
					t.Errorf("merge holds after the delivery = %v, %v, want t7's cleared", q.Holds, err)
				}
			}
			var branches []string
			for _, r := range ws.Retired() {
				branches = append(branches, r.Branch)
			}
			if !slices.Contains(branches, "guildhall/t7.s2") {
				t.Errorf("retired %q, want slice 2's branch", branches)
			}
			progress := e.slices(t)
			if progress.Current != 3 || progress.Merging || progress.Items[1].State != interp.SliceAlreadyDelivered || progress.Items[1].DeliveredBy != "slice 1" {
				t.Errorf("slice progress = %+v, want slice 2 delivered by slice 1 and slice 3 current", progress)
			}
			// A replay of the same event records and sends nothing new and leaves the task where it waits.
			docs, msgs := len(e.documents(t)), len(e.messages(t))
			want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitAttempt, Step: "build--s3", Attempt: 1})
			if len(e.documents(t)) != docs || len(e.messages(t)) != msgs {
				t.Error("a replay of the delivery wrote new records")
			}
		})
	}
}

// TestDeliveredLastSliceClosesTheTaskDone: the research reproduction, TestD162Repro. Slices 1 and 2 merged and slice
// 3's build failed with nothing to build; delivering slice 3 by a merged pull request closes the task done, as the
// last slice's merge would, with no merged message, so no hallmark count. A done task releases its dependents.
func TestDeliveredLastSliceClosesTheTaskDone(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, _ := newSliceEnv(t, b, true, append(sliceScript(t, threeSlices, 1, 2), failedSliceBuild(3, "https://forge.invalid/2"))...)
			e.toAccepted(t)
			for k := 1; k <= 2; k++ {
				e.toMergeDecision(t, k)
				e.mergeSlice(t, k)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build--s3", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build--s3", Attempt: 1})
			if prs := f.PullRequests(repoRemote); len(prs) != 2 || prs[1].State != forge.Merged {
				t.Fatalf("pull requests = %+v, want slice 2's merged", prs)
			}
			merged := e.mergedMessages(t)
			next := e.advance(t, interp.Event{Kind: interp.SliceDelivered, Slice: 3, PullRequest: "https://forge.invalid/2", Reason: "slice 2's pull request shipped the views too"})
			want(t, next, interp.Next{Wait: interp.WaitDone, Step: "deliver--s3"})
			if got := e.task(t).State; got != store.TaskDone {
				t.Fatalf("task state = %s, want done", got)
			}
			if n := e.mergedMessages(t); n != merged {
				t.Errorf("merged messages = %d, want %d: a delivery is no merge", n, merged)
			}
			if text := e.messages(t)["app.t7.slice-delivered.delivered--s3-1"]; !strings.Contains(text, "already delivered by https://forge.invalid/2") || !strings.Contains(text, "It was the last, so the task is done.") {
				t.Errorf("slice-delivered message = %q", text)
			}
			doc := e.pullRequestDoc(t, "delivered--s3-1-delivery")
			by, _ := doc["by_pull_request"].(map[string]any)
			if doc["slice"] != float64(3) || by["url"] != "https://forge.invalid/2" || by["merge_commit"] == "" || doc["reason"] != "slice 2's pull request shipped the views too" {
				t.Errorf("delivery record = %+v", doc)
			}
			if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 3, PullRequest: "https://forge.invalid/2", Reason: "again"}); !errors.Is(err, interp.ErrTaskClosed) {
				t.Errorf("a delivery of a done task = %v, want ErrTaskClosed", err)
			}
		})
	}
}

// readerless is a forge that cannot read pull requests.
type readerless struct{ interp.Forge }

// TestSliceDeliveredRefusals: a slice-delivered event is refused, with nothing written, for a slice that is not current
// or does not exist, a slice named that is not earlier or not merged, a pull request not merged or not the
// repository's, no reason, both or neither piece of evidence, a running attempt, a forge that cannot read pull
// requests given a pull request, a one-slice task and a closed task.
func TestSliceDeliveredRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   interp.Event
		set  func(*testing.T, *env, *forgetest.Fake)
		says string
	}{
		{"not the current slice", interp.Event{Slice: 3, BySlice: 1, Reason: "r"}, nil, "slice 3 is not app/t7's current slice, 2"},
		{"no such slice", interp.Event{Slice: 4, BySlice: 1, Reason: "r"}, nil, "has slices 1 to 3, not 4"},
		{"by a later slice", interp.Event{Slice: 2, BySlice: 2, Reason: "r"}, nil, "slice 2 is not earlier than slice 2"},
		{"no reason", interp.Event{Slice: 2, BySlice: 1, Reason: " "}, nil, "needs a reason"},
		{"both pieces of evidence", interp.Event{Slice: 2, BySlice: 1, PullRequest: "https://forge.invalid/1", Reason: "r"}, nil, "names one earlier slice or one pull request"},
		{"no evidence", interp.Event{Slice: 2, Reason: "r"}, nil, "names one earlier slice or one pull request"},
		{"a pull request not merged", interp.Event{Slice: 2, PullRequest: "https://forge.invalid/2", Reason: "r"}, func(t *testing.T, _ *env, f *forgetest.Fake) {
			if _, err := f.Publish(t.Context(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: "other", Head: sliceHeads[1], Title: "Other"}); err != nil {
				t.Fatal(err)
			}
		}, "pull request https://forge.invalid/2 is open, not merged"},
		{"a pull request the forge does not have", interp.Event{Slice: 2, PullRequest: "https://forge.invalid/9", Reason: "r"}, nil, "no pull request 9"},
		{"another repository's pull request", interp.Event{Slice: 2, PullRequest: "https://elsewhere.invalid/1", Reason: "r"}, nil, "is https://forge.invalid/1, not https://elsewhere.invalid/1"},
		{"not a pull request URL", interp.Event{Slice: 2, PullRequest: "https://forge.invalid/pulls", Reason: "r"}, nil, "is not a pull request URL"},
		{"a forge that reads no pull requests", interp.Event{Slice: 2, PullRequest: "https://forge.invalid/1", Reason: "r"}, func(_ *testing.T, e *env, _ *forgetest.Fake) {
			e.eng.Forge = readerless{e.eng.Forge}
		}, "the forge does not read pull requests"},
		{"a running attempt", interp.Event{Slice: 2, BySlice: 1, Reason: "r"}, func(t *testing.T, e *env, _ *forgetest.Fake) {
			if _, err := e.h.Records.StartAttempt("start-build--s2-2", "app", "t7", "build--s2"); err != nil {
				t.Fatal(err)
			}
		}, "attempt 2 of step build--s2 is running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, f, _ := toFailedSlice2(t, backends[1])
			if c.set != nil {
				c.set(t, e, f)
			}
			before := len(e.documents(t))
			ev := c.ev
			ev.Kind, ev.Repository, ev.Task = interp.SliceDelivered, "app", "t7"
			_, err := e.eng.Advance(t.Context(), ev)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("Advance(%+v) = %v, want it refused saying %q", ev, err, c.says)
			}
			if after := len(e.documents(t)); after != before {
				t.Errorf("documents = %d after the refusal, want %d", after, before)
			}
			if c.name == "a forge that reads no pull requests" && !errors.Is(err, interp.ErrUnsupported) {
				t.Errorf("refusal = %v, want ErrUnsupported", err)
			}
		})
	}
	t.Run("by a slice not merged", func(t *testing.T) {
		// Slice 2 closed as delivered, with nothing merged, so it delivered nothing slice 3 could stand on.
		e, _, _ := toFailedSlice2(t, backends[1], failedSliceBuild(3, "slice 2"))
		e.advance(t, interp.Event{Kind: interp.SliceDelivered, Slice: 2, BySlice: 1, Reason: "r"})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build--s3", Attempt: 1})
		before := len(e.documents(t))
		if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 3, BySlice: 2, Reason: "r"}); err == nil || !strings.Contains(err.Error(), "slice 2 has no merge record") {
			t.Errorf("a delivery of slice 3 by delivered slice 2 = %v", err)
		}
		if len(e.documents(t)) != before {
			t.Error("the refusal wrote a record")
		}
	})
	t.Run("a one-slice task", func(t *testing.T) {
		const one = "# One change\n\n## Summary\n\n- **Goal:** one pull request.\n\n## Goal\n\nOne pull request.\n\n## Open questions\n\nNone.\n"
		e, _, _ := newSliceEnv(t, backends[1], true, sliceScript(t, one)...)
		e.advance(t, interp.Event{Kind: interp.TaskCreated})
		e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
		if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 1, PullRequest: "https://forge.invalid/1", Reason: "r"}); err == nil || !strings.Contains(err.Error(), "delivers one pull request") {
			t.Errorf("a delivery of a one-slice task = %v", err)
		}
	})
	t.Run("a closed task", func(t *testing.T) {
		e, _, _ := toFailedSlice2(t, backends[1])
		if _, err := e.h.Records.(*store.Store).CancelTask("cancel-t7", "app", "t7", "gone"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 2, BySlice: 1, Reason: "r"}); !errors.Is(err, interp.ErrTaskClosed) {
			t.Errorf("a delivery of a cancelled task = %v, want ErrTaskClosed", err)
		}
	})
}

// TestSliceDeliveredCrashResumes: a crash after slice 2's delivery was recorded, before its message and retirement,
// is finished by the next advance, which closes slice 2 and starts slice 3; a replay after that writes nothing new.
func TestSliceDeliveredCrashResumes(t *testing.T) {
	e, _, ws := toFailedSlice2(t, backends[1], hungSliceBuild(3))
	real := e.eng.Outbox
	e.eng.Outbox = failingOutbox{}
	ev := interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 2, BySlice: 1, Reason: "slice 1 shipped it"}
	if _, err := e.eng.Advance(t.Context(), ev); err == nil {
		t.Fatal("advance with a failing outbox succeeded")
	}
	if _, ok := e.documents(t)["delivered--s2-1-delivery"]; !ok {
		t.Fatal("the delivery was not recorded before the crash")
	}
	retired := len(ws.Retired())
	e.eng.Outbox = real
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s3", Attempt: 1})
	if _, ok := e.messages(t)["app.t7.slice-delivered.delivered--s2-1"]; !ok {
		t.Error("slice 2's slice-delivered message was not sent when the advance resumed")
	}
	if r := ws.Retired(); len(r) <= retired || r[len(r)-1].Branch != "guildhall/t7.s2" {
		t.Errorf("retired %+v, want slice 2's branch once the advance resumed", r)
	}
	docs, msgs := len(e.documents(t)), len(e.messages(t))
	want(t, e.advance(t, ev), interp.Next{Wait: interp.WaitAttempt, Step: "build--s3", Attempt: 1})
	if len(e.documents(t)) != docs || len(e.messages(t)) != msgs {
		t.Error("a replay of the delivery wrote new records")
	}
}

// TestSliceDeliveredCrashResumesTheLast: a crash after the last slice's delivery was recorded, before the task closed,
// reads as the slice closing, and the next advance finishes it, closing the task done.
func TestSliceDeliveredCrashResumesTheLast(t *testing.T) {
	e, _, _ := newSliceEnv(t, backends[1], true, append(sliceScript(t, threeSlices, 1, 2), failedSliceBuild(3, "slice 2"))...)
	e.toAccepted(t)
	for k := 1; k <= 2; k++ {
		e.toMergeDecision(t, k)
		e.mergeSlice(t, k)
	}
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build--s3", Attempt: 1})
	real := e.eng.Outbox
	e.eng.Outbox = failingOutbox{}
	if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 3, BySlice: 2, Reason: "slice 2 shipped it"}); err == nil {
		t.Fatal("advance with a failing outbox succeeded")
	}
	if p := e.slices(t); !p.Merging || p.Current != 3 {
		t.Errorf("slice progress after the crash = %+v, want slice 3 closing", p)
	}
	e.eng.Outbox = real
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDone, Step: "deliver--s3"})
	if got := e.task(t).State; got != store.TaskDone {
		t.Errorf("task state = %s, want done", got)
	}
}

// TestSliceDeliveredWithNoBranch: a slice whose build never launched has no base recorded and no worktree, so its
// delivery retires nothing and keeps nothing, and the next slice starts.
func TestSliceDeliveredWithNoBranch(t *testing.T) {
	e, _, ws := newSliceEnv(t, backends[1], true, append(sliceScript(t, threeSlices, 1), hungSliceBuild(3))...)
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	// Slice 2's build is held before it is recorded, so the slice has no attempt, base or worktree.
	e.eng.Hold = func() (bool, error) { return true, nil }
	want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitPaused, Step: "build--s2"})
	e.eng.Hold = nil
	want(t, e.advance(t, interp.Event{Kind: interp.SliceDelivered, Slice: 2, BySlice: 1, Reason: "slice 1 shipped it"}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s3", Attempt: 1})
	for _, r := range ws.Retired() {
		if r.Branch == "guildhall/t7.s2" {
			t.Errorf("retired %+v: slice 2 never had a branch", r)
		}
	}
	for id, text := range e.messages(t) {
		if strings.Contains(id, "workspace-kept") {
			t.Errorf("message %s = %q: nothing was kept", id, text)
		}
	}
}
