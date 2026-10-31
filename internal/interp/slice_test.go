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
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// slicedJSON is the publish fixture without its sign-off, declaring build through deliver as its slice range.
const slicedJSON = `{
  "schema": "guildhall.workflow/1",
  "name": "sliced",
  "slices": {"from": "build", "to": "deliver"},
  "steps": [
    {"id": "plan", "type": "plan", "version": 1, "settings": {"profile": "planner"}},
    {"id": "accept", "type": "guildmaster-approval", "version": 1, "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "settings": {"profile": "builder"}, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan", "profile": "reviewer"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]}, "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "publish", "type": "publish", "version": 1, "inputs": {"head": ["build.head"], "review": ["review.outcome"], "spec": ["accept.spec"]}},
    {"id": "deliver", "type": "deliver", "version": 1, "inputs": {"head": ["publish.head"], "review": ["review.outcome"]}}
  ]
}`

// threeSlices is a spec whose `## Slices` lists three.
const threeSlices = `# Large work

## Goal

Deliver the large work in three pull requests.

## Slices

1. **Parser**: the parser; about 100 lines in 2 files.
2. **Engine**: the engine; about 200 lines in 4 files.
3. **Views**: the views; about 100 lines in 3 files.
`

// sliceHeads are the heads the three slices' builds land.
var sliceHeads = []string{strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)}

func slicedDefinition(t *testing.T, ranged bool) *workflow.Definition {
	t.Helper()
	def, err := workflow.Parse([]byte(slicedJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !ranged {
		def.Slices, def.Name = nil, "unsliced"
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

// sliceScript is the mock agent's script for a plan writing spec and then each slice k in slices building
// sliceHeads[k-1] with an approving review.
func sliceScript(t *testing.T, spec string, ks ...int) []interptest.Expectation {
	digest := "sha256:" + sum([]byte(spec))
	exps := []interptest.Expectation{{Step: "plan", Attempt: 1, Profile: "planner", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
		Respond: interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: []byte(spec)})}}
	for _, k := range ks {
		exps = append(exps, sliceBuild(t, digest, k)...)
	}
	return exps
}

// toBuildOf is the three-slice script through slice k's build launch: every slice before k whole, then k's build.
func toBuildOf(t *testing.T, k int) []interptest.Expectation {
	var before []int
	for i := 1; i < k; i++ {
		before = append(before, i)
	}
	return append(sliceScript(t, threeSlices, before...), sliceBuild(t, "sha256:"+sum([]byte(threeSlices)), k)[0])
}

// sliceBuild is slice k's build and approving review under the accepted spec digest.
func sliceBuild(t *testing.T, digest string, k int) []interptest.Expectation {
	h := sliceHeads[k-1]
	file := []byte(h + "\n")
	o := bound(t, workflow.VerdictApproved, h, nil)
	o.Revisions.SpecDigest = digest
	return []interptest.Expectation{
		{Step: workflow.SliceStep("build", k), Attempt: 1, Profile: "builder", Inputs: map[string]string{"spec": digest}, Standards: []string{"standards/go.md"},
			Respond: interptest.Succeed("built slice", interptest.File{Path: "head", Data: file})},
		{Step: workflow.SliceStep("review", k), Attempt: 1, Profile: "reviewer", Inputs: map[string]string{"spec": digest, "head": "sha256:" + sum(file)},
			Standards: []string{"standards/go.md", "standards/review.md"}, Respond: interptest.Review(o)},
	}
}

// sliceWorkspaces is the workspace stub of the slice tests: each slice's branch is at its build's head, every
// worktree is a plain directory, and retirements are recorded.
type sliceWorkspaces struct {
	mu      sync.Mutex
	ensured []git.Workspace
	retired []git.Retirement
}

func (w *sliceWorkspaces) Ensure(_ context.Context, ws git.Workspace) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ensured = append(w.ensured, ws)
	return os.MkdirAll(ws.Dir, 0o755)
}

func (w *sliceWorkspaces) FastForward(context.Context, git.Workspace) (string, error) { return "", nil }

func (w *sliceWorkspaces) Retire(_ context.Context, r git.Retirement) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.retired = append(w.retired, r)
	return nil, os.RemoveAll(r.Dir)
}

func (w *sliceWorkspaces) Retired() []git.Retirement {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.retired)
}

func (w *sliceWorkspaces) Tip(_ context.Context, _, branch string) (string, error) {
	if strings.HasPrefix(branch, git.DefaultRef("")) {
		return baseHead, nil
	}
	for k := 1; k <= len(sliceHeads); k++ {
		if branch == interp.SliceBranch("t7", k) {
			return sliceHeads[k-1], nil
		}
	}
	return "", fmt.Errorf("no branch %s", branch)
}

func (w *sliceWorkspaces) IsAncestor(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (w *sliceWorkspaces) Behind(context.Context, string, string, string) (int, []string, error) {
	return 0, nil, nil
}
func (w *sliceWorkspaces) Base(context.Context, string, string) (string, error) { return baseHead, nil }
func (w *sliceWorkspaces) Rebase(context.Context, git.Replay) (git.Rebased, error) {
	return git.Rebased{}, errors.New("no rebase in the slice tests")
}
func (w *sliceWorkspaces) Squash(context.Context, string, string, string) (string, error) {
	return "", errors.New("no squash in the slice tests")
}
func (w *sliceWorkspaces) Adopt(context.Context, git.Workspace, string, string) error {
	return errors.New("no adopt in the slice tests")
}
func (w *sliceWorkspaces) Diff(context.Context, string, string, string) ([]byte, error) {
	return nil, errors.New("no diff in the slice tests")
}
func (w *sliceWorkspaces) Log(context.Context, string, string, string, ...string) ([]byte, error) {
	return nil, errors.New("no log in the slice tests")
}
func (w *sliceWorkspaces) Changes(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

// newSliceEnv is app/t7 bound to the sliced workflow, or its copy with no range, over a fake forge holding every
// slice's head and the workspace stub.
func newSliceEnv(t *testing.T, b backend, ranged bool, exps ...interptest.Expectation) (*env, *forgetest.Fake, *sliceWorkspaces) {
	t.Helper()
	e := newEnvDefinition(t, b, slicedDefinition(t, ranged), exps...)
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	for _, h := range sliceHeads {
		f.AddCommit(repoPath, h)
	}
	e.eng.Forge = f
	ws := &sliceWorkspaces{}
	e.eng.Workspaces = ws
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote, Merge: noCI}}
	return e, f, ws
}

// toAccepted runs the plan and its acceptance.
func (e *env) toAccepted(t *testing.T) interp.Next {
	t.Helper()
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	return e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
}

// toMergeDecision runs slice k from its build's result to its merge decision.
func (e *env) toMergeDecision(t *testing.T, k int) interp.Next {
	t.Helper()
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: workflow.SliceStep("build", k), Attempt: 1})
	return e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: workflow.SliceStep("review", k), Attempt: 1})
}

// mergeSlice answers slice k's merge decision and advances.
func (e *env) mergeSlice(t *testing.T, k int) interp.Next {
	t.Helper()
	id := workflow.SliceStep("deliver", k) + "-1"
	e.answer(t, id, "merge")
	return e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: id})
}

// TestThreeSlicesRunToDone: a spec listing three slices, accepted once, publishes and merges three pull requests in
// order, on guildhall/t7, guildhall/t7.s2 and guildhall/t7.s3, each slice starting a fresh worktree after the one
// before merged and retired, and the task is done only after the third merge; no new acceptance is asked between.
func TestThreeSlicesRunToDone(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := newSliceEnv(t, b, true, sliceScript(t, threeSlices, 1, 2, 3)...)
			want(t, e.toAccepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
			for k := 1; k <= 3; k++ {
				want(t, e.toMergeDecision(t, k), interp.Next{Wait: interp.WaitDecision, Step: workflow.SliceStep("deliver", k), Decision: workflow.SliceStep("deliver", k) + "-1"})
				next := e.mergeSlice(t, k)
				if k < 3 {
					// The merge of a slice before the last starts the next slice's build in the same advance.
					want(t, next, interp.Next{Wait: interp.WaitAttempt, Step: workflow.SliceStep("build", k+1), Attempt: 1})
					if got := e.task(t).State; got != store.TaskOpen {
						t.Fatalf("after slice %d merged the task is %s, want open", k, got)
					}
					continue
				}
				want(t, next, interp.Next{Wait: interp.WaitDone, Step: "deliver--s3"})
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %s, want done", got)
			}
			prs := f.PullRequests(repoRemote)
			wantBranches := []string{"guildhall/t7", "guildhall/t7.s2", "guildhall/t7.s3"}
			if len(prs) != 3 {
				t.Fatalf("pull requests = %+v, want 3", prs)
			}
			titles := []string{"Parser (slice 1 of 3)", "Engine (slice 2 of 3)", "Views (slice 3 of 3)"}
			for i, pr := range prs {
				if pr.Branch != wantBranches[i] || pr.State != forge.Merged || pr.Head != sliceHeads[i] {
					t.Errorf("pull request %d = %+v, want %s merged at %s", i+1, pr, wantBranches[i], sliceHeads[i])
				}
				title, body := f.Text(repoRemote, pr.Number)
				if title != titles[i] {
					t.Errorf("pull request %d title = %q, want %q", i+1, title, titles[i])
				}
				for _, part := range []string{"Deliver the large work in three pull requests.", "### Slice " + string(rune('1'+i)) + " of 3"} {
					if !strings.Contains(body, part) {
						t.Errorf("pull request %d body does not say %q:\n%s", i+1, part, body)
					}
				}
			}
			if _, body := f.Text(repoRemote, 3); !strings.Contains(body, "1. Parser: merged in https://forge.invalid/1") || !strings.Contains(body, "2. Engine: merged in https://forge.invalid/2") || !strings.Contains(body, "3. Views: this pull request") {
				t.Errorf("slice 3's body does not list the merged slices:\n%s", body)
			}
			docs := e.documents(t)
			for _, name := range []string{"deliver-1-merge", "deliver--s2-1-merge", "deliver--s3-1-merge", "accept-1-spec"} {
				if _, ok := docs[name]; !ok {
					t.Errorf("document %s not recorded", name)
				}
			}
			for name := range docs {
				if strings.HasPrefix(name, "accept-2") {
					t.Errorf("document %s: no slice asks for a new acceptance", name)
				}
			}
			retired := ws.Retired()
			if len(retired) != 3 {
				t.Fatalf("retired %+v, want each slice's workspace once", retired)
			}
			for i, r := range retired {
				if r.Branch != wantBranches[i] || !strings.HasSuffix(r.Dir, interp.SliceWorkDir(i+1)) || r.Head != sliceHeads[i] {
					t.Errorf("retirement %d = %+v, want %s in %s at %s", i+1, r, wantBranches[i], interp.SliceWorkDir(i+1), sliceHeads[i])
				}
			}
			for _, s := range e.script.Launched() {
				if s.Manifest.Identity.Step != "build--s2" {
					continue
				}
				prompt, err := os.ReadFile(s.PromptFile)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(prompt), "Commit on the task branch `guildhall/t7.s2`") {
					t.Error("slice 2's build is not told to commit on guildhall/t7.s2")
				}
				if !strings.HasSuffix(s.WorkDir, interp.SliceWorkDir(2)) {
					t.Errorf("slice 2's build works in %s, want its own %s", s.WorkDir, interp.SliceWorkDir(2))
				}
			}
			msgs := e.messages(t)
			if text := msgs["app.t7.merged.deliver-1"]; !strings.Contains(text, "slice 1 of 3 (Parser) merged, and slice 2 of 3 starts from main on branch guildhall/t7.s2. The task stays open.") {
				t.Errorf("slice 1 merged message = %q", text)
			}
			if text := msgs["app.t7.merged.deliver--s3-1"]; !strings.Contains(text, "slice 3 of 3 (Views) merged, the last, so the task is done.") {
				t.Errorf("slice 3 merged message = %q", text)
			}
		})
	}
}

// TestOneSliceKeepsTodaysNames: a spec with no `## Slices` under a ranged workflow runs as today: one branch, one
// pull request whose title is the spec's, and records under the names a workflow with no range gives.
func TestOneSliceKeepsTodaysNames(t *testing.T) {
	const one = "# One change\n\n## Goal\n\nOne pull request.\n"
	names := map[bool][]string{}
	titles := map[bool]string{}
	for _, ranged := range []bool{true, false} {
		e, f, _ := newSliceEnv(t, backends[1], ranged, sliceScript(t, one, 1)...)
		e.toAccepted(t)
		e.toMergeDecision(t, 1)
		want(t, e.mergeSlice(t, 1), done)
		for name := range e.documents(t) {
			names[ranged] = append(names[ranged], name)
		}
		slices.Sort(names[ranged])
		titles[ranged], _ = f.Text(repoRemote, 1)
		if prs := f.PullRequests(repoRemote); len(prs) != 1 || prs[0].Branch != "guildhall/t7" {
			t.Errorf("pull requests = %+v, want one on guildhall/t7", prs)
		}
	}
	if !slices.Equal(names[true], names[false]) {
		t.Errorf("record names with a range = %q, without = %q", names[true], names[false])
	}
	if titles[true] != "One change" || titles[false] != titles[true] {
		t.Errorf("titles = %v", titles)
	}
}

// TestSliceMergedOutsideKeepsTheTaskOpen: d69's failure through the outside-merge rule. Slice 1's pull request merged
// outside Guildhall at its merge decision is recorded as slice 1's merge, its decision voided and its workspace
// retired; the task stays open and slice 2's build starts.
func TestSliceMergedOutsideKeepsTheTaskOpen(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := newSliceEnv(t, b, true, toBuildOf(t, 3)...)
			e.toAccepted(t)
			e.toMergeDecision(t, 1)
			f.MergeOutside(repoRemote, 1, sliceHeads[0], outsideCommit)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
			if got := e.task(t).State; got != store.TaskOpen {
				t.Fatalf("task state = %s, want open", got)
			}
			if _, ok := e.documents(t)["outside-1-merge"]; !ok {
				t.Error("slice 1's outside merge is not recorded")
			}
			if d := e.decision(t, "deliver-1"); !d.Void {
				t.Errorf("deliver-1 = %+v, want it voided", d)
			}
			if r := ws.Retired(); len(r) != 1 || r[0].Branch != "guildhall/t7" {
				t.Errorf("retired %+v, want slice 1's branch", r)
			}
			if text := e.messages(t)["app.t7.merged.outside.1"]; !strings.Contains(text, "slice 1 of 3 (Parser) merged") || !strings.Contains(text, "The task stays open.") {
				t.Errorf("merged message = %q", text)
			}
			// Slice 2 publishes a new pull request on its own branch; the rule reads only slice 2's from now on.
			want(t, e.toMergeDecision(t, 2), interp.Next{Wait: interp.WaitDecision, Step: "deliver--s2", Decision: "deliver--s2-1"})
			if prs := f.PullRequests(repoRemote); len(prs) != 2 || prs[1].Branch != "guildhall/t7.s2" || prs[1].State != forge.Open {
				t.Errorf("pull requests = %+v, want slice 2's open on guildhall/t7.s2", prs)
			}
			// Slice 2 merged outside too is that slice's merge, and the last slice still stands open.
			f.MergeOutside(repoRemote, 2, sliceHeads[1], outsideCommit)
			next := e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if next.Wait != interp.WaitAttempt || next.Step != "build--s3" {
				t.Errorf("after slice 2 merged outside: %+v, want build--s3", next)
			}
			if _, ok := e.documents(t)["outside--s2-2-merge"]; !ok {
				t.Error("slice 2's outside merge is not recorded as outside--s2-2-merge")
			}
		})
	}
}

// TestReconcileAfterASliceKeepsTheTaskOpen: d69's failure through task reconcile: reconciling a three-slice task whose
// slice 1 merged outside Guildhall records the slice's merge and leaves the task open, and its next advance starts
// slice 2.
func TestReconcileAfterASliceKeepsTheTaskOpen(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, _ := newSliceEnv(t, b, true, toBuildOf(t, 2)...)
			e.toAccepted(t)
			e.toMergeDecision(t, 1)
			f.MergeOutside(repoRemote, 1, sliceHeads[0], outsideCommit)
			got, err := e.eng.Reconcile(t.Context(), "app", "t7")
			if err != nil {
				t.Fatal(err)
			}
			if got.Done || got.Slice != 1 || got.URL != "https://forge.invalid/1" {
				t.Errorf("Reconcile = %+v, want slice 1 merged and the task open", got)
			}
			if st := e.task(t).State; st != store.TaskOpen {
				t.Fatalf("task state = %s, want open", st)
			}
			again, err := e.eng.Reconcile(t.Context(), "app", "t7")
			if err != nil || again != (interp.Reconciled{}) {
				t.Errorf("Reconcile again = %+v, %v, want no change", again, err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
		})
	}
}

// TestSliceMergeCrashResumesAtTheNextSlice: a crash after slice 1's merge was recorded, before its message and
// retirement, resumes at slice 2 on the next advance, which first finishes slice 1's close.
func TestSliceMergeCrashResumesAtTheNextSlice(t *testing.T) {
	e, _, ws := newSliceEnv(t, backends[1], true, toBuildOf(t, 2)...)
	e.toAccepted(t)
	e.toMergeDecision(t, 1)
	e.answer(t, "deliver-1", "merge")
	real := e.eng.Outbox
	e.eng.Outbox = failingOutbox{}
	if _, err := e.eng.Advance(t.Context(), interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: "t7", Decision: "deliver-1"}); err == nil {
		t.Fatal("advance with a failing outbox succeeded")
	}
	if _, ok := e.documents(t)["deliver-1-merge"]; !ok {
		t.Fatal("the merge was not recorded before the crash")
	}
	if len(ws.Retired()) != 0 {
		t.Fatal("retired before the crash")
	}
	e.eng.Outbox = real
	want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
	if r := ws.Retired(); len(r) != 1 || r[0].Branch != "guildhall/t7" {
		t.Errorf("retired %+v, want slice 1's branch once the advance resumed", r)
	}
	if _, ok := e.messages(t)["app.t7.merged.deliver-1"]; !ok {
		t.Error("slice 1's merged message was not sent when the advance resumed")
	}
}

// TestSliceMergeVoidsItsOpenDecisions: a decision still open when a slice before the last merges, such as one an
// attempt of the slice asked, is voided with the slice's merge, so it never fails a later slice's nothing_open.
func TestSliceMergeVoidsItsOpenDecisions(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, b, true, sliceScript(t, threeSlices, 1, 2)...)
			e.toAccepted(t)
			e.toMergeDecision(t, 1)
			if _, err := e.h.Records.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t7", ID: "q-1", Kind: store.DecisionQuestion, Question: "Which?"}); err != nil {
				t.Fatal(err)
			}
			want(t, e.mergeSlice(t, 1), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 1})
			if d := e.decision(t, "q-1"); !d.Void || d.VoidReason != "Slice 1 merged: pull request https://forge.invalid/1" {
				t.Errorf("q-1 = %+v, want it voided with slice 1's merge", d)
			}
			// The voided decision no longer counts as unanswered: slice 2's merge decision reads nothing_open holds.
			want(t, e.toMergeDecision(t, 2), interp.Next{Wait: interp.WaitDecision, Step: "deliver--s2", Decision: "deliver--s2-1"})
			if q := e.decision(t, "deliver--s2-1").Question; !strings.Contains(q, "- nothing_open: holds") || strings.Contains(q, "q-1") {
				t.Errorf("slice 2's merge question = %q, want nothing_open to hold with q-1 void", q)
			}
		})
	}
}

// failingOutbox refuses every message, standing in for a crash after a merge was recorded.
type failingOutbox struct{}

func (failingOutbox) Send(store.Recipient, messaging.Envelope) (store.Receipt, error) {
	return store.Receipt{}, errors.New("outbox down")
}

// planAgain is plan's attempt 2 after a send-back, writing spec.
func planAgain(spec string) interptest.Expectation {
	return interptest.Expectation{Step: "plan", Attempt: 2, Profile: "planner", Inputs: map[string]string{interp.FeedbackPort: interptest.AnyDigest},
		Standards: []string{"standards/go.md"}, Respond: interptest.Succeed("planned again", interptest.File{Path: "spec.md", Data: []byte(spec)})}
}

// TestMultiSliceSpecRefusedWithoutARange: a task bound to a revision with no slice range, such as a retired default,
// refuses a plan whose spec lists more than one slice: the plan attempt fails with the reason and no spec is recorded.
// A malformed list is refused the same way under a ranged revision.
func TestMultiSliceSpecRefusedWithoutARange(t *testing.T) {
	for _, c := range []struct {
		name   string
		ranged bool
		spec   string
		says   string
	}{
		{"three slices, no range", false, threeSlices, "the spec lists 3 slices, but the task's workflow revision unsliced"},
		{"a gap", true, "# T\n\n## Slices\n\n1. **A**: a\n3. **C**: c\n", "entry 3 follows entry 1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, _ := newSliceEnv(t, backends[1], c.ranged, sliceScript(t, c.spec)...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 1})
			a := e.attempts(t)[0]
			if a.Outcome != store.AttemptFailed || !strings.Contains(a.IntakeRejection, c.says) {
				t.Errorf("plan attempt = %+v, want it failed saying %q", a, c.says)
			}
			if _, ok := e.documents(t)["plan-1-spec"]; ok {
				t.Error("a refused spec was recorded")
			}
		})
	}
}

// TestReplanFromASlice: sent back to plan from slice 2, a revised spec that changes slice 1's merged entry is refused,
// and one that rewrites slices 2 and 3 is accepted once more, after which slice 2's build runs again under it.
func TestReplanFromASlice(t *testing.T) {
	changedFirst := strings.Replace(threeSlices, "the parser; about 100", "a bigger parser; about 150", 1)
	rewritten := strings.Replace(strings.Replace(threeSlices, "the engine; about 200", "a smaller engine; about 150", 1), "the views; about 100", "the views and docs; about 150", 1)
	for _, c := range []struct {
		name, spec string
		refused    bool
	}{{"changes a merged slice", changedFirst, true}, {"rewrites the unmerged slices", rewritten, false}} {
		t.Run(c.name, func(t *testing.T) {
			exps := append(sliceScript(t, threeSlices, 1, 2), planAgain(c.spec))
			if !c.refused {
				d := "sha256:" + sum([]byte(c.spec))
				exps = append(exps, interptest.Expectation{Step: "build--s2", Attempt: 2, Profile: "builder", Inputs: map[string]string{"spec": d}, Standards: []string{"standards/go.md"},
					Respond: interptest.Hang()})
			}
			e, _, _ := newSliceEnv(t, backends[1], true, exps...)
			e.toAccepted(t)
			e.toMergeDecision(t, 1)
			e.mergeSlice(t, 1)
			want(t, e.toMergeDecision(t, 2), interp.Next{Wait: interp.WaitDecision, Step: "deliver--s2", Decision: "deliver--s2-1"})
			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "plan", Attempt: 1, Reason: "slice 2 is too big"}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 2})
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 2})
			if c.refused {
				want(t, next, interp.Next{Wait: interp.WaitDirector, Step: "plan", Attempt: 2})
				var a store.StageAttempt
				for _, x := range e.attempts(t) {
					if x.Step == "plan" && x.Number == 2 {
						a = x
					}
				}
				if !strings.Contains(a.IntakeRejection, `slice 1 "Parser" has merged, and the revised spec changes its entry`) {
					t.Errorf("plan attempt 2 rejection = %q", a.IntakeRejection)
				}
				return
			}
			want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-2"})
			e.answer(t, "accept-2", "approved")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-2"}), interp.Next{Wait: interp.WaitAttempt, Step: "build--s2", Attempt: 2})
			if got := e.task(t).State; got != store.TaskOpen {
				t.Errorf("task state = %s", got)
			}
		})
	}
}
