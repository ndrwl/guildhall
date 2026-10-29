package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const (
	repoPath   = "/srv/app"
	repoRemote = "https://github.com/acme/app.git"
	baseHead   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func publishDefinition(t *testing.T) *workflow.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(interptest.Testdata(), "workflows", "publish.json"))
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	return def
}

// newPublishEnv is the straight path with a publish step over a fake forge holding the build's head.
func newPublishEnv(t *testing.T, b backend) (*env, *forgetest.Fake) {
	t.Helper()
	return newPublishEnvTask(t, b, nil)
}

// newPublishEnvTask is newPublishEnv with app/t7 changed by edit, after an earlier task app/t3, when edit is not nil.
func newPublishEnvTask(t *testing.T, b backend, edit func(*store.Task)) (*env, *forgetest.Fake) {
	t.Helper()
	e := newEnvTasks(t, b, publishDefinition(t), edit, straightScript(t)...)
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	f.AddCommit(repoPath, head)
	e.eng.Forge = f
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	return e, f
}

// toSignOff runs the task from creation through the Guildmaster's sign-off and returns that advance's wait.
func (e *env) toSignOff(t *testing.T) interp.Next {
	t.Helper()
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
	e.answer(t, "sign-off-1", "approved")
	return e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"})
}

// pullRequestDoc reads a recorded pull-request document.
func (e *env) pullRequestDoc(t *testing.T, name string) map[string]any {
	t.Helper()
	d, ok := e.documents(t)[name]
	if !ok {
		t.Fatalf("document %s not recorded", name)
	}
	data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil || sum(data) != d.SHA256 {
		t.Fatalf("document %s: %v", name, err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("document %s: %v: %s", name, err, data)
	}
	t.Logf("%s: %s", name, data)
	return v
}

func (e *env) task(t *testing.T) store.Task {
	t.Helper()
	got, err := e.h.Records.Task("app", "t7")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// bodies is a forge that records the title and body of each change published.
type bodies struct {
	*forgetest.Fake
	mu     sync.Mutex
	titles []string
	seen   []string
}

func (b *bodies) Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error) {
	b.mu.Lock()
	b.titles = append(b.titles, c.Title)
	b.seen = append(b.seen, c.Body)
	b.mu.Unlock()
	return b.Fake.Publish(ctx, c)
}

// TestPublishNamesTheFixedTask: a task created to fix an earlier task's defect has the fix line in its pull request
// body, the line rework and change failure count from; a task that fixes nothing has none.
func TestPublishNamesTheFixedTask(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			const line = "\nFixes a defect introduced by task `app/t3`.\n"
			for _, fixes := range []bool{true, false} {
				e, f := newPublishEnvTask(t, b, func(task *store.Task) {
					if fixes {
						task.Fixes = &store.TaskRef{Repository: "app", ID: "t3"}
					}
				})
				rec := &bodies{Fake: f}
				e.eng.Forge = rec
				want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				if len(rec.seen) != 1 || strings.HasSuffix(rec.seen[0], line) != fixes || strings.Count(rec.seen[0], "Fixes a defect introduced by task") != map[bool]int{true: 1, false: 0}[fixes] {
					t.Errorf("fixes %v: published bodies %q, want one with the fix line only when the task fixes one", fixes, rec.seen)
				}
			}
		})
	}
}

// newPublishTextEnv is validate.json, with publish given the accepted spec when spec is true, over a recording
// forge holding heads 1 and 2; build 1 summarises "summary A", and with the spec build 2, after a send-back, "summary B".
func newPublishTextEnv(t *testing.T, b backend, spec bool) (*env, *bodies) {
	t.Helper()
	def := loadWorkflow(t, "validate")
	if spec {
		for i := range def.Steps {
			if def.Steps[i].ID == "publish" {
				def.Steps[i].Inputs["spec"] = workflow.Refs{"accept.spec"}
			}
		}
		if err := workflow.Validate(def); err != nil {
			t.Fatal(err)
		}
	}
	build := func(n int, h, feedback, summary string) interptest.Expectation {
		x := buildExp(t, n, h, feedback)
		x.Respond = interptest.Succeed(summary, interptest.File{Path: "head", Data: headFile(h)})
		return x
	}
	e := newEnvDefinition(t, b, def,
		planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
		build(1, head, "", "summary A"),
		reviewExp(t, "review", "reviewer", 1, head, nil, bound(t, workflow.VerdictApproved, head, nil)))
	if spec {
		e.script.Expect(build(2, head2, "sha256:"+sum([]byte(sendBackReason)), "summary B"),
			reviewExp(t, "review", "reviewer", 2, head2, ptr(bound(t, workflow.VerdictApproved, head, nil)), bound(t, workflow.VerdictApproved, head2, nil)))
	}
	f := forgetest.NewFake()
	f.SetBranch(repoRemote, "main", baseHead)
	f.AddCommit(repoPath, head)
	f.AddCommit(repoPath, head2)
	rec := &bodies{Fake: f}
	e.eng.Forge = rec
	e.eng.Config.Repositories = []config.Repository{{ID: "app", Path: repoPath, Remote: repoRemote}}
	return e, rec
}

const sendBackReason = "Name the head in the pull request."

// TestPublishTextFollowsTheHead: publish titles the pull request from the accepted spec's heading and describes
// the head it carries; a send-back's new head republishes the same pull request with text naming the new head,
// its build summary and its review, and none of the old head's.
func TestPublishTextFollowsTheHead(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, rec := newPublishTextEnv(t, b, true)
			want(t, e.toValidate(t), waitChecks)
			if len(rec.seen) != 1 || rec.titles[0] != "Spec" {
				t.Fatalf("published titles %q, want one from the spec's heading", rec.titles)
			}
			first := rec.seen[0]
			for _, s := range []string{"## Summary\n\nThe command prints the task list as JSON.", "## What changed\n\nsummary A", "- Head: `" + head + "`",
				"Reviewer: automated. Verdict: approved.", "- Accepted spec: `accept-1-spec`", "sha256:" + sum(specV1(t)), "<details><summary>Accepted spec</summary>"} {
				if !strings.Contains(first, s) {
					t.Errorf("first body lacks %q:\n%s", s, first)
				}
			}

			want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: sendBackReason}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 2}), waitChecks)
			if len(rec.seen) != 2 || rec.titles[1] != "Spec" {
				t.Fatalf("published titles %q, want a second from the spec's heading", rec.titles)
			}
			second := rec.seen[1]
			if !strings.Contains(second, "summary B") || !strings.Contains(second, "- Head: `"+head2+"`") || strings.Contains(second, head) || strings.Contains(second, "summary A") ||
				!strings.Contains(second, "Reviewer: automated. Verdict: approved.") {
				t.Errorf("second body does not describe head 2 alone:\n%s", second)
			}
			prs := rec.PullRequests(repoRemote)
			if len(prs) != 1 {
				t.Fatalf("pull requests = %+v, want one", prs)
			}
			if title, body := rec.Text(repoRemote, prs[0].Number); title != "Spec" || body != second {
				t.Errorf("pull request reads %q, %q, want the second text", title, body)
			}
			// A replay publishes nothing new.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), waitChecks)
			if len(rec.seen) != 2 {
				t.Errorf("a replay published again: %d publishes", len(rec.seen))
			}
		})
	}
}

// TestPublishTextWithoutSpec: a publish step not given the accepted spec titles the pull request with the task's
// title and says it was given no spec.
func TestPublishTextWithoutSpec(t *testing.T) {
	e, rec := newPublishTextEnv(t, backends[0], false)
	want(t, e.toValidate(t), waitChecks)
	if len(rec.seen) != 1 || rec.titles[0] != "Straight path" {
		t.Fatalf("published titles %q, want the task's title", rec.titles)
	}
	for _, s := range []string{"No accepted spec was given to this step.", "## What changed\n\nsummary A", "- Accepted spec: none was given to this step"} {
		if !strings.Contains(rec.seen[0], s) {
			t.Errorf("body lacks %q:\n%s", s, rec.seen[0])
		}
	}
	if strings.Contains(rec.seen[0], "<details>") {
		t.Errorf("body inlines a spec it was not given:\n%s", rec.seen[0])
	}
}

// TestPublishAndMerge: publish pushes the reviewed head to guildhall/<task> and records its pull request;
// deliver opens the merge decision for the published head, and a merge answer squash-merges it at that head,
// records the merge and closes the task.
func TestPublishAndMerge(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if got := f.Branch(repoRemote, "guildhall/t7"); got != head {
				t.Errorf("task branch = %q, want %s", got, head)
			}
			if got := f.Branch(repoRemote, "main"); got != baseHead {
				t.Errorf("base moved to %q before the merge", got)
			}
			prs := f.PullRequests(repoRemote)
			if len(prs) != 1 || prs[0].State != forge.Open || prs[0].Head != head {
				t.Fatalf("pull requests = %+v", prs)
			}
			pr := e.pullRequestDoc(t, "publish-1-pr")
			if pr["schema"] != "guildhall.pull-request/1" || pr["url"] != prs[0].URL || pr["number"] != float64(1) || pr["branch"] != "guildhall/t7" ||
				pr["head"] != head || pr["state"] != "open" || pr["remote"] != repoRemote {
				t.Errorf("publish-1-pr = %v", pr)
			}
			if d := e.documents(t)["publish-1-head"]; d.SHA256 != sum([]byte(head+"\n")) {
				t.Errorf("publish-1-head = %+v", d)
			}
			merge := e.decision(t, "deliver-1")
			if merge.Subject == nil || merge.Subject.Digest != head || !strings.Contains(merge.Question, prs[0].URL) {
				t.Errorf("merge decision = %+v, want it bound to %s and naming %s", merge, head, prs[0].URL)
			}
			if got, want := e.inbox(t), []string{"app.t7.decision.accept-1", "app.t7.decision.sign-off-1", "app.t7.published.publish-1", "app.t7.decision.deliver-1"}; !slices.Equal(got, want) {
				t.Errorf("inbox = %q, want %q", got, want)
			}
			// Replays publish nothing new.
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			if publishes, merges := f.Calls(); publishes != 1 || merges != 0 {
				t.Errorf("forge calls = %d publishes, %d merges", publishes, merges)
			}

			e.answer(t, "deliver-1", "merge")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			prs = f.PullRequests(repoRemote)
			if prs[0].State != forge.Merged || f.Branch(repoRemote, "main") != prs[0].MergeCommit {
				t.Fatalf("after the merge: pull requests = %+v, main = %s", prs, f.Branch(repoRemote, "main"))
			}
			merged := e.pullRequestDoc(t, "deliver-1-merge")
			if merged["state"] != "merged" || merged["head"] != head || merged["merge_commit"] != prs[0].MergeCommit || merged["url"] != prs[0].URL {
				t.Errorf("deliver-1-merge = %v", merged)
			}
			if got := e.task(t).State; got != store.TaskDone {
				t.Errorf("task state = %q, want done", got)
			}
			if got := e.inbox(t); got[len(got)-1] != "app.t7.merged.deliver-1" {
				t.Errorf("inbox = %q, want the merge last", got)
			}
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}); !errors.Is(err, interp.ErrTaskClosed) {
				t.Errorf("advance after delivery = %v, want ErrTaskClosed", err)
			}
			if _, merges := f.Calls(); merges != 1 {
				t.Errorf("merges = %d, want 1", merges)
			}
		})
	}
}

// TestMergeRefusedWaitsForTheDirector: a merge the forge refuses records the refusal, tells the Director the
// forge's reason and waits for the Director; a later advance tries the merge again, recording a refusal for a
// new reason and nothing for one already recorded.
func TestMergeRefusedWaitsForTheDirector(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			e.toSignOff(t)
			f.Refuse(`Required status check "ci" is failing`)
			e.answer(t, "deliver-1", "merge")
			wait := interp.Next{Wait: interp.WaitDirector, Step: "deliver", Decision: "deliver-1"}
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), wait)
			if _, ok := e.documents(t)["deliver-1-merge"]; ok || e.task(t).State != store.TaskOpen {
				t.Fatalf("a refused merge recorded a merge or closed the task")
			}
			refused := e.pullRequestDoc(t, "deliver-1-refused")
			if refused["schema"] != "guildhall.merge-refusal/1" || refused["head"] != head || refused["number"] != float64(1) || refused["remote"] != repoRemote ||
				refused["url"] != f.PullRequests(repoRemote)[0].URL || !strings.Contains(refused["reason"].(string), `"ci" is failing`) {
				t.Errorf("deliver-1-refused = %v", refused)
			}
			page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			last := page.Messages[len(page.Messages)-1]
			if !strings.HasPrefix(last.ID, "app.t7.merge-refused.deliver-1") || last.Type != "merge-refused" || !strings.Contains(last.Text, `"ci" is failing`) {
				t.Errorf("last message = %+v, want the forge's refusal", last)
			}
			n := len(page.Messages)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
			if got := len(e.inbox(t)); got != n {
				t.Errorf("a repeated refusal sent %d new messages", got-n)
			}
			if _, ok := e.documents(t)["deliver-1-refused-2"]; ok {
				t.Errorf("a repeated refusal was recorded again")
			}

			f.Refuse("The head branch is behind the base branch")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
			if r := e.pullRequestDoc(t, "deliver-1-refused-2"); !strings.Contains(r["reason"].(string), "behind the base branch") {
				t.Errorf("deliver-1-refused-2 = %v", r)
			}
			f.Refuse(`Required status check "ci" is failing`)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), wait)
			if _, ok := e.documents(t)["deliver-1-refused-3"]; ok {
				t.Errorf("a refusal already recorded was recorded again")
			}

			f.Refuse("")
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
			if _, merges := f.Calls(); merges != 5 {
				t.Errorf("merges = %d, want 5", merges)
			}
		})
	}
}

// unreachable is a forge whose merges fail without the forge's answer, as when the connection drops.
type unreachable struct{ interp.Forge }

var errUnreachable = errors.New("error connecting to api.github.com")

func (unreachable) Merge(context.Context, forge.Merge) (forge.PullRequest, error) {
	return forge.PullRequest{}, errUnreachable
}

// TestMergeUnreachable: a merge that fails without the forge refusing it fails the advance, records no refusal
// and sends no merge-refused message; a later advance merges.
func TestMergeUnreachable(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			e.toSignOff(t)
			e.answer(t, "deliver-1", "merge")
			e.eng.Forge = unreachable{f}
			n := len(e.inbox(t))
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}); !errors.Is(err, errUnreachable) {
				t.Fatalf("advance = %v, want the forge's error", err)
			}
			if _, ok := e.documents(t)["deliver-1-refused"]; ok {
				t.Errorf("an unreachable forge was recorded as a refusal")
			}
			if got := len(e.inbox(t)); got != n {
				t.Errorf("an unreachable forge sent %d messages", got-n)
			}
			e.eng.Forge = f
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDone, Step: "deliver"})
		})
	}
}

// TestMergeHeld: a hold answer merges nothing and waits for the Director.
func TestMergeHeld(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newPublishEnv(t, b)
			e.toSignOff(t)
			e.answer(t, "deliver-1", "hold")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), interp.Next{Wait: interp.WaitDirector, Step: "deliver", Decision: "deliver-1"})
			if _, merges := f.Calls(); merges != 0 {
				t.Errorf("merges = %d after a hold", merges)
			}
			if got := e.inbox(t); got[len(got)-1] != "app.t7.merge-held.deliver-1" {
				t.Errorf("inbox = %q, want the hold last", got)
			}
		})
	}
}

// TestPublishRefusals: publish without a forge is unsupported, and a head the repository does not hold
// records nothing.
func TestPublishRefusals(t *testing.T) {
	t.Run("no forge", func(t *testing.T) {
		e, _ := newPublishEnv(t, backends[0])
		e.eng.Forge = nil
		if err := e.toSignOffErr(t); !errors.Is(err, interp.ErrUnsupported) {
			t.Errorf("publish without a forge = %v, want ErrUnsupported", err)
		}
	})
	t.Run("unknown head", func(t *testing.T) {
		e, _ := newPublishEnv(t, backends[0])
		f := forgetest.NewFake()
		f.SetBranch(repoRemote, "main", baseHead)
		e.eng.Forge = f
		if err := e.toSignOffErr(t); !errors.Is(err, forge.ErrUnknownHead) {
			t.Errorf("publish of an unknown head = %v, want ErrUnknownHead", err)
		}
		for name := range e.documents(t) {
			if strings.HasPrefix(name, "publish-") {
				t.Errorf("refused publish recorded %s", name)
			}
		}
	})
}

// toSignOffErr is toSignOff whose last advance is expected to fail.
func (e *env) toSignOffErr(t *testing.T) error {
	t.Helper()
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
	e.answer(t, "sign-off-1", "approved")
	return advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"})
}

// TestMergeNeedsAPublishedHead: deliver of a head no publish step pushed opens the merge decision but cannot
// merge it.
func TestMergeNeedsAPublishedHead(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)...)
	e.eng.Forge = forgetest.NewFake()
	want(t, e.toSignOff(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
	e.answer(t, "deliver-1", "merge")
	if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}); !errors.Is(err, interp.ErrUnsupported) {
		t.Errorf("merge of an unpublished head = %v, want ErrUnsupported", err)
	}
}

// failPut fails the first PutDocument of one document name, after or before it commits.
type failPut struct {
	interp.Records
	name   string
	after  bool
	failed bool
}

func (f *failPut) PutDocument(m string, d store.DocumentRef) (store.Receipt, error) {
	if d.Name != f.name || f.failed {
		return f.Records.PutDocument(m, d)
	}
	f.failed = true
	if f.after {
		if _, err := f.Records.PutDocument(m, d); err != nil {
			return store.Receipt{}, err
		}
	}
	return store.Receipt{}, errInjected
}

// TestPublishAndMergeReplay: a failure around each publish and merge record replays to one pull request,
// one merge and the same records.
func TestPublishAndMergeReplay(t *testing.T) {
	for _, b := range backends {
		for _, name := range []string{"publish-1-pr", "publish-1-head", "deliver-1-merge"} {
			for _, after := range []bool{false, true} {
				t.Run(b.name+"/"+name+map[bool]string{false: "/before", true: "/after"}[after], func(t *testing.T) {
					e, f := newPublishEnv(t, b)
					fp := &failPut{Records: e.eng.Records, name: name, after: after}
					e.eng.Records = fp
					ev := interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}
					if name == "deliver-1-merge" {
						e.toSignOff(t)
						e.answer(t, "deliver-1", "merge")
						ev = interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}
					} else if err := e.toSignOffErr(t); !errors.Is(err, errInjected) {
						t.Fatalf("advance = %v, want the injected failure", err)
					}
					if name == "deliver-1-merge" {
						if err := advanceErr(t, e, ev); !errors.Is(err, errInjected) {
							t.Fatalf("advance = %v, want the injected failure", err)
						}
					}
					next, err := e.eng.Advance(context.Background(), interp.Event{Kind: ev.Kind, Repository: "app", Task: "t7", Decision: ev.Decision})
					if err != nil {
						t.Fatal(err)
					}
					if name == "deliver-1-merge" {
						want(t, next, interp.Next{Wait: interp.WaitDone, Step: "deliver"})
					} else {
						want(t, next, interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
					}
					if prs := f.PullRequests(repoRemote); len(prs) != 1 {
						t.Errorf("pull requests = %+v, want one", prs)
					}
					// A merge whose record was lost is asked for again, and the forge returns the merge it made.
					wantMerges := map[bool]int{false: 2, true: 1}[after]
					if name != "deliver-1-merge" {
						wantMerges = 0
					}
					if _, merges := f.Calls(); merges != wantMerges {
						t.Errorf("merges = %d, want %d", merges, wantMerges)
					}
					for _, n := range []string{"publish-1-pr", "publish-1-head"} {
						if _, ok := e.documents(t)[n]; !ok {
							t.Errorf("%s not recorded", n)
						}
					}
				})
			}
		}
	}
}
