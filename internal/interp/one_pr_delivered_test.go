package interp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

// The tests of a one-PR task closed as already delivered by another merged pull request
// (docs/tasks.md#a-slice-already-delivered): the same slice-delivered route as a multi-PR task's last slice, as slice
// 1 of 1, over the fixtures of slice_test.go, with and without a slice range in the workflow.

// onePR is a spec with no `## Slices` list, so its task delivers one pull request.
const onePR = "# One change\n\n## Summary\n\n- **Goal:** one pull request.\n\n## Goal\n\nOne pull request.\n\n## Open questions\n\nNone.\n"

// failedOneBuild is the one-PR task's build attempt 1 failing as a builder with nothing left to commit reports it.
func failedOneBuild(by string) interptest.Expectation {
	return interptest.Expectation{Step: "build", Attempt: 1, Profile: "builder",
		Inputs: map[string]string{"spec": "sha256:" + sum([]byte(onePR))}, Standards: []string{"standards/go.md"},
		Respond: interptest.Fail("Already delivered by " + by + ": every item is in its merge.")}
}

// keepingWorkspaces is sliceWorkspaces whose retirements keep the branch, as a branch with commits past its base is
// kept.
type keepingWorkspaces struct{ *sliceWorkspaces }

func (w keepingWorkspaces) Retire(ctx context.Context, r git.Retirement) ([]string, error) {
	if _, err := w.sliceWorkspaces.Retire(ctx, r); err != nil {
		return nil, err
	}
	return []string{"it has commits past its base"}, nil
}

// toFailedOnePR is app/t7 on the sliced workflow, or its copy with no range, accepted with a spec of one pull request,
// its build failed with nothing to build, and pull request 1 of the repository, which shipped its change, merged on
// the forge from another branch.
func toFailedOnePR(t *testing.T, ranged bool) (*env, *forgetest.Fake, *sliceWorkspaces, forge.PullRequest) {
	t.Helper()
	e, f, ws := newSliceEnv(t, backends[1], ranged, append(sliceScript(t, onePR), failedOneBuild("https://forge.invalid/1"))...)
	want(t, e.toAccepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "build", Attempt: 1})
	other, err := f.Publish(t.Context(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: "elsewhere", Head: sliceHeads[1], Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := f.Merge(t.Context(), forge.Merge{Repository: repoPath, Remote: repoRemote, Number: other.Number, Head: other.Head, Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	return e, f, ws, merged
}

// dependant creates app/t8, bound to app/t7's workflow and depending on it, and advances it held, so it waits on its
// dependency or, once released, paused before its first attempt.
func (e *env) dependant(t *testing.T) func() interp.Next {
	t.Helper()
	task := e.task(t)
	dep := store.Task{Repository: "app", ID: "t8", Title: "Depends on t7", Workflow: task.Workflow, ConfigDigest: task.ConfigDigest,
		DependsOn: []store.TaskRef{{Repository: "app", ID: "t7"}}}
	if _, err := e.h.CreateTask("task:app:t8", dep); err != nil {
		t.Fatal(err)
	}
	return func() interp.Next {
		t.Helper()
		hold := e.eng.Hold
		e.eng.Hold = func() (bool, error) { return true, nil }
		defer func() { e.eng.Hold = hold }()
		next, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t8"})
		if err != nil {
			t.Fatalf("advance app/t8: %v", err)
		}
		return next
	}
}

// TestOnePRDeliveredClosesTheTaskDone: d187's case. A one-PR task whose build found its change already merged by
// another pull request of the repository is closed with slice-delivered as slice 1 by that pull request: the task is
// done, not cancelled, with the delivery record and no merge record, so no hallmark count; its open decisions are
// voided, the Director is sent a slice-delivered message in one-PR words, its branch is retired, and a task depending
// on it is released. A replay writes nothing, and a delivery after the close is ErrTaskClosed.
func TestOnePRDeliveredClosesTheTaskDone(t *testing.T) {
	for _, ranged := range []bool{true, false} {
		name := map[bool]string{true: "ranged", false: "unranged"}[ranged]
		t.Run(name, func(t *testing.T) {
			e, _, ws, merged := toFailedOnePR(t, ranged)
			advanceDependant := e.dependant(t)
			want(t, advanceDependant(), interp.Next{Wait: interp.WaitDependencies, Step: "plan"})
			if _, err := e.h.Records.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t7", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"}); err != nil {
				t.Fatal(err)
			}
			mergedBefore := e.mergedMessages(t)
			ev := interp.Event{Kind: interp.SliceDelivered, Slice: 1, PullRequest: merged.URL, Reason: "#1 shipped the change."}
			want(t, e.advance(t, ev), done)
			if got := e.task(t); got.State != store.TaskDone || got.CancelReason != "" {
				t.Fatalf("task = %+v, want it done", got)
			}
			docs := e.documents(t)
			if _, ok := docs["delivered-1-delivery"]; !ok {
				t.Error("the delivery is not recorded as delivered-1-delivery")
			}
			for name := range docs {
				if strings.HasSuffix(name, "-merge") {
					t.Errorf("document %s: nothing merged for the task", name)
				}
			}
			doc := e.pullRequestDoc(t, "delivered-1-delivery")
			by, _ := doc["by_pull_request"].(map[string]any)
			if doc["slice"] != float64(1) || by["url"] != merged.URL || by["merge_commit"] != merged.MergeCommit || doc["by_slice"] != nil {
				t.Errorf("delivery record = %+v", doc)
			}
			if d := e.decision(t, "q-1"); !d.Void || d.VoidReason != "Already delivered by "+merged.URL+": #1 shipped the change." {
				t.Errorf("q-1 = %+v, want it voided with the delivery", d)
			}
			text := e.messages(t)["app.t7.slice-delivered.delivered-1"]
			if want := "The Director closed the task as already delivered by " + merged.URL + ", with nothing merged for it: #1 shipped the change. The task is done."; text != want {
				t.Errorf("slice-delivered message = %q, want %q", text, want)
			}
			if n := e.mergedMessages(t); n != mergedBefore {
				t.Errorf("merged messages = %d, want %d: a delivery is no merge", n, mergedBefore)
			}
			r := ws.Retired()
			if len(r) != 1 || r[0].Branch != "guildhall/t7" || r[0].Head == "" {
				t.Errorf("retired %+v, want the task's branch at its base", r)
			}
			for id, text := range e.messages(t) {
				if strings.Contains(id, "workspace-kept") {
					t.Errorf("message %s = %q: a branch with no commits is removed", id, text)
				}
			}
			if p := e.slices(t); p.Total != 1 || len(p.Items) != 0 {
				t.Errorf("slice progress = %+v, want a one-slice task", p)
			}
			if got, ok, err := interp.TaskDeliveredBy(e.docList(t), e.eng.DocumentDir); err != nil || !ok || got != merged.URL {
				t.Errorf("TaskDeliveredBy = %q, %v, %v; want %s", got, ok, err, merged.URL)
			}
			want(t, advanceDependant(), interp.Next{Wait: interp.WaitPaused, Step: "plan"})

			ev.Repository, ev.Task = "app", "t7"
			before := len(e.documents(t))
			if _, err := e.eng.Advance(t.Context(), ev); !errors.Is(err, interp.ErrTaskClosed) {
				t.Errorf("a delivery of the done task = %v, want ErrTaskClosed", err)
			}
			if len(e.documents(t)) != before {
				t.Error("a delivery after the close wrote records")
			}
		})
	}
}

// docList is app/t7's documents in record order.
func (e *env) docList(t *testing.T) []store.DocumentRef {
	t.Helper()
	docs, err := e.h.Records.Documents("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

// TestOnePRDeliveredKeepsABranchWithCommits: a one-PR task whose build committed something keeps its branch and
// worktree at the close, with a workspace-kept message, and is still done.
func TestOnePRDeliveredKeepsABranchWithCommits(t *testing.T) {
	for _, ranged := range []bool{true, false} {
		e, _, ws, merged := toFailedOnePR(t, ranged)
		e.eng.Workspaces = keepingWorkspaces{ws}
		want(t, e.advance(t, interp.Event{Kind: interp.SliceDelivered, Slice: 1, PullRequest: merged.URL, Reason: "#1 shipped it"}), done)
		if got := e.task(t).State; got != store.TaskDone {
			t.Errorf("ranged %v: task state = %s, want done", ranged, got)
		}
		kept := 0
		for id, text := range e.messages(t) {
			if strings.Contains(id, "workspace-kept") {
				kept++
				if !strings.Contains(text, "branch guildhall/t7 after closing the slice as already delivered (delivered-1)") || !strings.Contains(text, "commits past its base") {
					t.Errorf("ranged %v: workspace-kept message = %q", ranged, text)
				}
			}
		}
		if kept != 1 {
			t.Errorf("ranged %v: workspace-kept messages = %d, want 1", ranged, kept)
		}
	}
}

// TestOnePRDeliveredRefusals: a one-PR task's delivery is refused, with nothing written, when it names a slice, a pull
// request the forge reports open, another repository's pull request, or a pull request on a forge that cannot read
// them, where only cancel remains.
func TestOnePRDeliveredRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   interp.Event
		set  func(*testing.T, *env, *forgetest.Fake)
		says string
	}{
		{"by a slice", interp.Event{Slice: 1, BySlice: 1, Reason: "r"}, nil, "app/t7 delivers one pull request; name the PR that shipped it with --by-pr"},
		{"not slice 1", interp.Event{Slice: 2, PullRequest: "https://forge.invalid/1", Reason: "r"}, nil, "has slices 1 to 1, not 2"},
		{"a pull request not merged", interp.Event{Slice: 1, PullRequest: "https://forge.invalid/2", Reason: "r"}, func(t *testing.T, _ *env, f *forgetest.Fake) {
			if _, err := f.Publish(t.Context(), forge.Change{Repository: repoPath, Remote: repoRemote, Branch: "other", Head: sliceHeads[2], Title: "Other"}); err != nil {
				t.Fatal(err)
			}
		}, "pull request https://forge.invalid/2 is open, not merged"},
		{"another repository's pull request", interp.Event{Slice: 1, PullRequest: "https://elsewhere.invalid/1", Reason: "r"}, nil, "is https://forge.invalid/1, not https://elsewhere.invalid/1"},
		{"a forge that reads no pull requests", interp.Event{Slice: 1, PullRequest: "https://forge.invalid/1", Reason: "r"}, func(_ *testing.T, e *env, _ *forgetest.Fake) {
			e.eng.Forge = readerless{e.eng.Forge}
		}, "a one-PR task cannot be closed as delivered; only cancel remains"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, f, _, _ := toFailedOnePR(t, false)
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
			if got := e.task(t).State; got != store.TaskOpen {
				t.Errorf("task state = %s after the refusal, want open", got)
			}
			if c.name == "a forge that reads no pull requests" && !errors.Is(err, interp.ErrUnsupported) {
				t.Errorf("refusal = %v, want ErrUnsupported", err)
			}
		})
	}
}

// TestOnePRDeliveredCrashResumes: a crash after a one-PR task's delivery was recorded, before its message and close,
// is finished by the next advance, which closes the task done; settleSlice has no earlier slice to settle, so
// closeDelivered alone resumes it.
func TestOnePRDeliveredCrashResumes(t *testing.T) {
	for _, ranged := range []bool{true, false} {
		e, _, _, merged := toFailedOnePR(t, ranged)
		real := e.eng.Outbox
		e.eng.Outbox = failingOutbox{}
		if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.SliceDelivered, Repository: "app", Task: "t7", Slice: 1, PullRequest: merged.URL, Reason: "#1 shipped it"}); err == nil {
			t.Fatal("advance with a failing outbox succeeded")
		}
		if _, ok := e.documents(t)["delivered-1-delivery"]; !ok {
			t.Fatal("the delivery was not recorded before the crash")
		}
		if got := e.task(t).State; got != store.TaskOpen {
			t.Fatalf("ranged %v: task state after the crash = %s, want open", ranged, got)
		}
		e.eng.Outbox = real
		want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), done)
		if got := e.task(t).State; got != store.TaskDone {
			t.Errorf("ranged %v: task state = %s, want done", ranged, got)
		}
		if _, ok := e.messages(t)["app.t7.slice-delivered.delivered-1"]; !ok {
			t.Errorf("ranged %v: the slice-delivered message was not sent when the advance resumed", ranged)
		}
	}
}
