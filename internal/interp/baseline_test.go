package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// checksForge is a fake forge that counts Checks calls, fails the heads in fail with their error, and runs during,
// when set, inside each call.
type checksForge struct {
	*forgetest.Fake
	mu     sync.Mutex
	calls  int
	fail   map[string]error
	during func()
}

func (f *checksForge) Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error) {
	f.mu.Lock()
	f.calls++
	during, err := f.during, f.fail[c.Head]
	f.mu.Unlock()
	if during != nil {
		during()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return f.Fake.Checks(ctx, c)
}

func (f *checksForge) checksCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// baselineEnv is task app/t7 bound to the hallmark workflow over a real repository, not yet created, with a seed
// clone of its remote to land merges from and a forge that reports their checks.
type baselineEnv struct {
	*env
	remote, seed string
	forge        *checksForge
	clock        time.Time
}

func newBaselineEnv(t *testing.T, b backend, exps ...interptest.Expectation) *baselineEnv {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	def := hallmarkDef(t)
	e := newEnvDefinition(t, b, def, exps...)
	repository, remote, _ := gittest.Repository(t)
	e.eng.Workspaces = git.Program{}
	repo := &e.eng.Config.Repositories[0]
	repo.Path, repo.Remote = repository, remote
	f := &checksForge{Fake: forgetest.NewFake(), fail: map[string]error{}}
	e.eng.Forge = f
	h := &baselineEnv{env: e, remote: remote, seed: filepath.Join(t.TempDir(), "seed"), forge: f, clock: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	h.git(t, "", "clone", "-q", remote, h.seed)
	return h
}

// git runs git in dir, the seed clone when dir is "", and returns its trimmed output.
func (h *baselineEnv) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	if dir == "" && args[0] != "clone" {
		dir = h.seed
	}
	pre := []string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}
	if dir != "" {
		pre = append([]string{"-C", dir}, pre...)
	}
	out, err := exec.Command("git", append(pre, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit lands a new commit on the remote's default branch and returns it.
func (h *baselineEnv) commit(t *testing.T, name string) string {
	t.Helper()
	h.git(t, "", "commit", "-q", "--allow-empty", "-m", name)
	h.git(t, "", "push", "-q", "origin", "HEAD")
	return h.git(t, "", "rev-parse", "HEAD")
}

// putDoc records document name of task with data at p.
func (h *baselineEnv) putDoc(t *testing.T, task, name, p string, data []byte) {
	t.Helper()
	file := filepath.Join(h.eng.DocumentDir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.h.Records.PutDocument("doc:"+task+":"+name, store.DocumentRef{Repository: "app", TaskID: task, Name: name, Path: p, SHA256: sum(data)}); err != nil {
		t.Fatal(err)
	}
}

// close creates task with workflow rev and closes it done, a day after the task closed before it.
func (h *baselineEnv) close(t *testing.T, task string, rev workflow.Revision, docs func()) {
	t.Helper()
	if _, err := h.h.CreateTask("task:app:"+task, store.Task{Repository: "app", ID: task, Title: "Task " + task, Workflow: rev, ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	docs()
	h.clock = h.clock.Add(24 * time.Hour)
	h.h.Clock.Set(h.clock)
	if _, err := h.h.Records.SetTaskState("state:"+task, "app", task, store.TaskDone); err != nil {
		t.Fatal(err)
	}
}

// merged lands a commit for task, records it merged as pull request number at that merge commit, and makes the
// forge report checks for it; it returns the merge commit.
func (h *baselineEnv) merged(t *testing.T, task string, number int, checks ...forge.Check) string {
	t.Helper()
	c := h.commit(t, task)
	h.close(t, task, straight(t).Revision(), func() {
		data, err := json.Marshal(map[string]any{"schema": "guildhall.pull-request/1", "remote": h.remote, "number": number,
			"url": fmt.Sprintf("https://example.invalid/pull/%d", number), "branch": "guildhall/" + task, "head": c, "state": "merged", "merge_commit": c})
		if err != nil {
			t.Fatal(err)
		}
		h.putDoc(t, task, "deliver-1-merge", "tasks/app/"+task+"/deliver-1/merge.json", data)
	})
	h.forge.SetBranch(h.remote, "main-"+task, c)
	h.forge.SetChecks(h.remote, c, checks...)
	return c
}

// hallmark records task as a done hallmark task with report and, when head is set, a finished head.
func (h *baselineEnv) hallmark(t *testing.T, task string, report []byte, head string) {
	t.Helper()
	h.close(t, task, hallmarkDef(t).Revision(), func() {
		if report != nil {
			h.putDoc(t, task, "hallmark-1-report", "tasks/app/"+task+"/hallmark-1/report.md", report)
		}
		data, err := json.Marshal(map[string]string{"schema": "guildhall.finished/1", "head": head})
		if err != nil {
			t.Fatal(err)
		}
		h.putDoc(t, task, "finished", "tasks/app/"+task+"/finished.json", data)
	})
}

// baseline returns t7's recorded baseline document and its content.
func (h *baselineEnv) baseline(t *testing.T) (store.DocumentRef, string) {
	t.Helper()
	d, ok := h.documents(t)["baseline-hallmark"]
	if !ok {
		t.Fatal("no baseline-hallmark document recorded")
	}
	if d.Path != "tasks/app/t7/baseline-hallmark.md" {
		t.Fatalf("baseline path = %s", d.Path)
	}
	data, err := os.ReadFile(filepath.Join(h.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if sum(data) != d.SHA256 {
		t.Fatalf("baseline file does not match its digest")
	}
	t.Logf("baseline:\n%s", data)
	return d, string(data)
}

// bound returns the digest attempt n of the hallmark step was given on port.
func (h *baselineEnv) bound(t *testing.T, n int, port string) string {
	t.Helper()
	d, ok := h.documents(t)[fmt.Sprintf("hallmark-%d-inputs", n)]
	if !ok {
		t.Fatalf("attempt %d has no binding", n)
	}
	data, err := os.ReadFile(filepath.Join(h.eng.DocumentDir, filepath.FromSlash(d.Path)))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Inputs []struct{ Port, Path, SHA256 string } `json:"inputs"`
	}
	if err := json.Unmarshal(data, &b); err != nil {
		t.Fatal(err)
	}
	for _, in := range b.Inputs {
		if in.Port == port {
			return in.SHA256
		}
	}
	return ""
}

// containsInOrder fails t unless text contains each of want, in order.
func containsInOrder(t *testing.T, text string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		i := strings.Index(text[at:], w)
		if i < 0 {
			t.Fatalf("baseline lacks %q after offset %d", w, at)
		}
		at += i + len(w)
	}
}

// TestHallmarkBaseline: a hallmark after a done hallmark and two merges on main is given a baseline naming the
// previous hallmark's finished head, the current main and both merges, newest first, with their checks; a merge
// from before the previous head is not listed. A failed check with a job names its ci log command and is in the
// failed-jobs block; one without a job is listed with no command. The previous input and the baseline name the
// same previous hallmark, passing over a newer done hallmark with no report. The finished attempt is not stale.
func TestHallmarkBaseline(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			report := []byte("# Earlier hallmark\n")
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{"previous": "sha256:" + sum(report)}, interptest.Succeed("reported", interptest.File{Path: "report.md", Data: []byte("# Hallmark\n")})))
			h.eng.Copy = "/copy's dir"
			early := h.merged(t, "t1", 101, forge.Check{Name: "test", State: forge.CheckFailed, Job: "1111"})
			h.hallmark(t, "t2", report, early)
			older := h.merged(t, "t4", 104)
			newer := h.merged(t, "t5", 105,
				forge.Check{Name: "test (ubuntu)", State: forge.CheckPassed, URL: "https://example.invalid/runs/1"},
				forge.Check{Name: "lint", State: forge.CheckFailed, URL: "https://example.invalid/runs/2", Job: "7781"},
				forge.Check{Name: "status", State: forge.CheckFailed})
			h.hallmark(t, "t6", nil, newer)

			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			d, text := h.baseline(t)
			if got := h.bound(t, 1, "baseline"); got != d.SHA256 {
				t.Fatalf("attempt 1's baseline = %s, want %s", got, d.SHA256)
			}
			if prev := h.documents(t)["previous-hallmark"]; prev.Path != "tasks/app/t7/previous-hallmark-t2.md" {
				t.Fatalf("previous-hallmark = %+v, want t2's report", prev)
			}
			command := "`guildhall ci log --copy '/copy'\\''s dir' app/t7 7781`"
			containsInOrder(t, text,
				"# Hallmark baseline for app/t7\n\nRendered ",
				"when attempt 1 of step hallmark was bound.",
				"- Previous hallmark: t2, head `"+early+"`\n",
				"- Current: `"+newer+"`, the remote's default branch when this was rendered; the attempt's prepared head (`hallmark-1-prepared`)",
				"## Merged tasks (2, newest first)",
				"| t5 | [#105](https://example.invalid/pull/105) | `"+newer+"` | `"+newer+"` |",
				"| t4 | [#104](https://example.invalid/pull/104) | `"+older+"` | `"+older+"` |",
				"## Checks",
				"### t5 `"+newer+"`",
				"| lint | failed | https://example.invalid/runs/2 | "+command+" |",
				"| status | failed |  |  |",
				"| test (ubuntu) | passed | https://example.invalid/runs/1 |  |",
				"### t4 `"+older+"`\n\nNo checks reported.",
				"## Failed jobs\n\n```json\n",
			)
			// Look for t1's row, pull request and job by their markers: a bare "t1" or "1111" can also occur in
			// the remote's temporary path in the failed-jobs block or in a commit id.
			if strings.Contains(text, "| t1 |") || strings.Contains(text, "#101") || strings.Contains(text, "### t1 ") ||
				strings.Contains(text, `"task":"t1"`) || strings.Contains(text, `"job":"1111"`) {
				t.Fatal("the baseline lists a merge from before the previous hallmark's head")
			}
			jobs, err := interp.BaselineJobs([]byte(text))
			if err != nil || len(jobs) != 1 || jobs[0] != (interp.BaselineJob{Task: "t5", Remote: h.remote, Head: newer, Check: "lint", Job: "7781"}) {
				t.Fatalf("failed jobs = %+v, %v; want t5's lint job", jobs, err)
			}
			if got := h.forge.checksCalls(); got != 2 {
				t.Fatalf("Checks calls = %d, want one per listed merge", got)
			}

			want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), waitsOnFollowUps)
			if got := len(h.attempts(t)); got != 1 {
				t.Fatalf("attempts = %d, want the finished attempt alone, not stale", got)
			}
		})
	}
}

// TestHallmarkBaselineChecksError: a forge error for one merge's commit is recorded under that merge, and the
// attempt launches.
func TestHallmarkBaselineChecksError(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			older := h.merged(t, "t4", 104)
			newer := h.merged(t, "t5", 105, forge.Check{Name: "lint", State: forge.CheckPassed})
			h.forge.fail[older] = errors.New("gh: network\n| down")
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			containsInOrder(t, text, "- Previous hallmark: none\n", "### t5 `"+newer+"`", "| lint | passed |", "### t4 `"+older+"`\n\nchecks unavailable: gh: network \\| down\n",
				"## Failed jobs\n\n```json\n{\"schema\":\"guildhall.hallmark-baseline-jobs/1\",\"jobs\":[]}\n```\n")
		})
	}
}

// mergeRecord is a guildhall.pull-request/1 merge record of task's pull request number at head.
func (h *baselineEnv) mergeRecord(t *testing.T, task string, number int, head string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schema": "guildhall.pull-request/1", "remote": h.remote, "number": number,
		"url": fmt.Sprintf("https://example.invalid/pull/%d", number), "branch": "guildhall/" + task, "head": head, "state": "merged"})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestHallmarkBaselineTwoMergeRecords: a task with two merge records is listed at the newest one's head, the last in
// the store's name order (interp.MergeRecord), the head prune retires it at too.
func TestHallmarkBaselineTwoMergeRecords(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			a := h.commit(t, "t5-a")
			bh := h.commit(t, "t5-b")
			h.close(t, "t5", straight(t).Revision(), func() {
				h.putDoc(t, "t5", "deliver-1-merge", "tasks/app/t5/deliver-1/merge.json", h.mergeRecord(t, "t5", 105, a))
				h.putDoc(t, "t5", "outside-3-merge", "tasks/app/t5/outside-3/merge.json", h.mergeRecord(t, "t5", 3, bh))
			})
			h.eng.Forge = nil
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			containsInOrder(t, text, "## Merged tasks (1, newest first)", "| t5 | [#3](https://example.invalid/pull/3) | `` | `"+bh+"` |")
			if strings.Contains(text, a) {
				t.Fatal("the baseline names the older merge record's head")
			}
		})
	}
}

// TestHallmarkBaselineUnreadableMergeRecord: a task whose merge record names no valid head, and one whose record does
// not decode, are not dropped: the Not placed line lists each as unreadable, and the baseline is still recorded.
func TestHallmarkBaselineUnreadableMergeRecord(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			h.close(t, "t4", straight(t).Revision(), func() {
				h.putDoc(t, "t4", "deliver-1-merge", "tasks/app/t4/deliver-1/merge.json", []byte(`{"schema":"guildhall.pull-request/1","head":"nope"}`))
			})
			h.close(t, "t5", straight(t).Revision(), func() {
				h.putDoc(t, "t5", "deliver-1-merge", "tasks/app/t5/deliver-1/merge.json", []byte(`{"schema":`))
			})
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			containsInOrder(t, text, "No merged task is between the two heads.", "\nNot placed: ", "t5: merge record unreadable: merge record deliver-1-merge: ",
				"; t4: merge record unreadable: merge record deliver-1-merge names no valid head.\n")
		})
	}
}

// TestHallmarkBaselineCancelled: an advance whose context ends during Checks returns the context's error and
// records no baseline.
func TestHallmarkBaselineCancelled(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b)
			h.merged(t, "t5", 105)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h.forge.during = cancel
			if _, err := h.eng.Advance(ctx, interp.Event{Kind: interp.TaskCreated, Repository: "app", Task: "t7"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Advance = %v, want the context's error", err)
			}
			if _, ok := h.documents(t)["baseline-hallmark"]; ok {
				t.Fatal("a cancelled advance recorded a baseline")
			}
			if _, err := os.Stat(filepath.Join(h.eng.DocumentDir, "tasks", "app", "t7", "baseline-hallmark.md")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("baseline file = %v, want none", err)
			}
		})
	}
}

// TestHallmarkBaselineNoForge: without a forge the baseline has no checks and no failed-jobs block; with no previous
// hallmark it lists the newest in-range merges up to the limit and says how many it dropped.
func TestHallmarkBaselineNoForge(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			for i := 1; i <= 22; i++ {
				h.merged(t, fmt.Sprintf("m%02d", i), i)
			}
			h.eng.Forge = nil
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			containsInOrder(t, text, "- Previous hallmark: none\n", "## Merged tasks (20, newest first)", "| m22 |", "| m03 |", "2 older merges in the range are not listed (limit 20).")
			for _, absent := range []string{"| m02 |", "| m01 |", "## Checks", "## Failed jobs"} {
				if strings.Contains(text, absent) {
					t.Errorf("baseline has %q", absent)
				}
			}
		})
	}
}

// TestHallmarkBaselineNoWorkspaces: without Workspaces the current head is unknown and the merged tasks unavailable.
func TestHallmarkBaselineNoWorkspaces(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b, hallmarkExp(1, map[string]string{}, report("# Hallmark\n")))
			h.merged(t, "t5", 105)
			h.eng.Workspaces = nil
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			_, text := h.baseline(t)
			containsInOrder(t, text, "- Current: unknown\n", "merged tasks unavailable: no workspaces\n", "## Checks\n\nNo merged task to check.\n", "\"jobs\":[]")
		})
	}
}

// TestHallmarkBaselineRelaunch: a relaunch after a new merge is bound to the first attempt's baseline, and the
// forge is asked for no checks again.
func TestHallmarkBaselineRelaunch(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			h := newBaselineEnv(t, b,
				hallmarkExp(1, map[string]string{}, interptest.Fail("stopped short")),
				hallmarkExp(2, map[string]string{}, report("# Hallmark\n")))
			h.merged(t, "t5", 105)
			want(t, h.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 1})
			d, _ := h.baseline(t)
			calls := h.forge.checksCalls()
			want(t, h.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "hallmark", Attempt: 1})
			h.merged(t, "t8", 108)
			want(t, h.advance(t, interp.Event{Kind: interp.AttemptRetried, Step: "hallmark", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "hallmark", Attempt: 2})
			if got := h.bound(t, 2, "baseline"); got != d.SHA256 {
				t.Fatalf("attempt 2's baseline = %s, want the first %s", got, d.SHA256)
			}
			if got := h.forge.checksCalls(); got != calls {
				t.Fatalf("Checks calls = %d after the relaunch, want %d", got, calls)
			}
			if _, text := h.baseline(t); strings.Contains(text, "t8") {
				t.Fatal("the baseline moved with a later merge")
			}
		})
	}
}
