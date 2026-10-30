package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/git/gittest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// pruneFixture is a Director copy whose config lists repository app, a real git repository, with a store whose
// clock is at, and a pruner whose clock is now.
type pruneFixture struct {
	t                  *testing.T
	copy, store, state string
	repository, remote string
	at, now            time.Time
	n                  int
	cfg                *config.Config
	app                *App
	stdout, stderr     *strings.Builder
	removeFails        func(string) bool
}

// pruneClosed is when the fixture's tasks close by default.
var pruneClosed = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newPruneFixture(t *testing.T) *pruneFixture {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repository, remote, _ := gittest.Repository(t)
	dir := t.TempDir()
	f := &pruneFixture{t: t, copy: dir, store: filepath.Join(dir, "director.store"), state: filepath.Join(dir, "state"),
		repository: repository, remote: remote, at: pruneClosed, now: pruneClosed.Add(15 * 24 * time.Hour)}
	data := strings.Replace(directorConfigWith(t, ""), `path = "/srv/app"`, "path = '"+repository+"'", 1)
	data = strings.Replace(data, `remote = "git@github.com:example/app.git"`, "remote = '"+remote+"'", 1)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	f.stdout, f.stderr = &strings.Builder{}, &strings.Builder{}
	f.app = &App{Stdout: f.stdout, Stderr: f.stderr, Now: func() time.Time { return f.now }, Getenv: func(string) string { return "" }}
	f.mutate(func(*store.Store) error { return nil })
	return f
}

func (f *pruneFixture) mutate(fn func(s *store.Store) error) {
	f.t.Helper()
	s, err := store.Open(f.store, store.Options{Now: func() time.Time { return f.at }})
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := fn(s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *pruneFixture) id() string {
	f.n++
	return fmt.Sprintf("m-%d", f.n)
}

// task creates repo/id, depending on deps.
func (f *pruneFixture) task(repo, id string, deps ...store.TaskRef) {
	f.t.Helper()
	d := "sha256:" + strings.Repeat("a", 64)
	f.mutate(func(s *store.Store) error {
		_, err := s.CreateTask(f.id(), store.Task{Repository: repo, ID: id, Title: "Task " + id, Workflow: workflow.Revision{Name: "default", Digest: d}, ConfigDigest: d, DependsOn: deps})
		return err
	})
}

// close moves repo/id to done at f.at.
func (f *pruneFixture) close(repo, id string) {
	f.t.Helper()
	f.mutate(func(s *store.Store) error {
		_, err := s.SetTaskState(f.id(), repo, id, store.TaskDone)
		return err
	})
}

// attemptFiles are the files attempt writes in an attempt directory, by slash path, besides its manifest and usage.
var attemptFiles = map[string]string{
	artisan.ResultFile:         `{"status":"succeeded"}` + "\n",
	artisan.OutcomeFile:        `{"outcome":"succeeded"}` + "\n",
	"prompt/prompt.md":         strings.Repeat("prompt ", 100),
	"prompt/skills/a/SKILL.md": strings.Repeat("skill ", 100),
	"model/draft.json":         strings.Repeat("draft ", 100),
	artisan.EventsFile:         strings.Repeat("event\n", 100),
	launch.LogFile:             strings.Repeat("log\n", 100),
	artisan.HeartbeatFile:      `{"beat":1}` + "\n",
	"containment.json":         "{}\n",
	"launched.json":            "{}\n",
	artisan.LockFile:           "",
}

// keptFiles are the files a prune leaves in a fixture attempt directory.
var keptFiles = []string{artisan.LockFile, artisan.OutcomeFile, "prompt/" + render.ManifestFile, artisan.ResultFile, adapter.UsageFileName}

// attemptManifest writes k's prompt manifest, naming profile builder, and its measured usage.
func attemptManifest(t *testing.T, state string, k artisan.AttemptKey) {
	t.Helper()
	m, err := json.Marshal(render.Manifest{Schema: render.ManifestSchema, Role: render.File{Path: "roles/artisan.md"},
		Identity: render.Identity{Repository: k.Repository, Task: k.TaskID, Step: k.Step, Kind: k.Step, KindVersion: 1, Attempt: k.Attempt, Profile: "builder"}})
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(k.Dir(state), "prompt", render.ManifestFile), string(m))
	u := adapter.Usage{Tokens: adapter.Measured, InputTokens: 100, OutputTokens: 20, Cost: adapter.Measured, CostUSD: 0.25}
	if err := adapter.WriteUsage(filepath.Join(k.Dir(state), artisan.ResultFile), u); err != nil {
		t.Fatal(err)
	}
}

// attempt records a finished attempt of step on repo/id and writes attemptFiles, its manifest and usage, and extra
// in its directory.
func (f *pruneFixture) attempt(repo, id, step string, outcome store.AttemptOutcome, extra map[string]string) artisan.AttemptKey {
	f.t.Helper()
	var k artisan.AttemptKey
	f.mutate(func(s *store.Store) error {
		r, err := s.StartAttempt(f.id(), repo, id, step)
		if err != nil {
			return err
		}
		k = artisan.AttemptKey{Repository: repo, TaskID: id, Step: step, Attempt: r.Attempt}
		if outcome != "" {
			_, err = s.FinishAttempt(f.id(), repo, id, step, r.Attempt, outcome)
		}
		return err
	})
	for name, data := range attemptFiles {
		writeTree(f.t, filepath.Join(k.Dir(f.state), filepath.FromSlash(name)), data)
	}
	attemptManifest(f.t, f.state, k)
	for name, data := range extra {
		writeTree(f.t, filepath.Join(k.Dir(f.state), filepath.FromSlash(name)), data)
	}
	return k
}

func (f *pruneFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *pruneFixture) workspace(id string) git.Workspace {
	return git.Workspace{Repository: f.repository, Remote: f.remote, Branch: "guildhall/" + id, Dir: filepath.Join(f.state, "app", id, "work")}
}

// worktree makes app/id's worktree with one commit and records the merge of that commit as deliver-1-merge, the
// document a merge records, and returns the head.
func (f *pruneFixture) worktree(id string) string {
	f.t.Helper()
	w := f.workspace(id)
	if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := (git.Program{}).Ensure(context.Background(), w); err != nil {
		f.t.Fatal(err)
	}
	writeTree(f.t, filepath.Join(w.Dir, "change.txt"), strings.Repeat("change\n", 200))
	f.git(w.Dir, "add", "change.txt")
	f.git(w.Dir, "commit", "-q", "-m", "change")
	head := f.git(w.Dir, "rev-parse", "HEAD")
	f.document(id, "deliver-1-merge", "deliver-1/merge.json", `{"schema":"guildhall.pull-request/1","remote":"origin","number":1,"head":"`+head+`","state":"merged"}`)
	return head
}

// document records data as app/id's document name at tasks/app/<id>/<rel>.
func (f *pruneFixture) document(id, name, rel, data string) {
	f.t.Helper()
	p := "tasks/app/" + id + "/" + rel
	writeTree(f.t, filepath.Join(f.state, "documents", filepath.FromSlash(p)), data)
	f.mutate(func(s *store.Store) error {
		_, err := s.PutDocument(f.id(), store.DocumentRef{Repository: "app", TaskID: id, Name: name, Path: p, SHA256: digest.Of([]byte(data)).Hex()})
		return err
	})
}

func (f *pruneFixture) pruner() *pruner {
	f.t.Helper()
	p, err := f.app.pruner(f.cfg, f.copy)
	if err != nil {
		f.t.Fatal(err)
	}
	if f.removeFails != nil {
		p.remove = func(path string) error {
			if f.removeFails(path) {
				return &fs.PathError{Op: "remove", Path: path, Err: errors.New("file in use")}
			}
			return os.RemoveAll(path)
		}
	}
	return p
}

func (f *pruneFixture) run(dry bool) []pruneResult {
	f.t.Helper()
	got, err := f.pruner().run(context.Background(), "", "", dry, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

// ciLogSize is the size of the job log ciLog caches.
const ciLogSize = 700

// ciLog caches a failed job log for app/id, as validate does, and returns the cache directory.
func (f *pruneFixture) ciLog(id string) string {
	f.t.Helper()
	dir := cilog.Dir(f.state, "app", id)
	writeTree(f.t, filepath.Join(dir, "123.log"), strings.Repeat("failed\n", ciLogSize/7))
	return dir
}

func (f *pruneFixture) record(id string) (store.TaskPrune, error) {
	return withStore(f.store, true, func(s *store.Store) (store.TaskPrune, error) { return s.TaskPrune("app", id) })
}

// writeTree writes data at path, making its directory.
func writeTree(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// listFiles lists the files below dir by slash path.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// skips maps each result's task to its skip reason, "" for a task pruned.
func skips(results []pruneResult) map[string]string {
	out := map[string]string{}
	for _, r := range results {
		out[r.Task.Repository+"/"+r.Task.ID] = r.Skip
	}
	return out
}

// TestPruneSelection: one done task per skip reason, the age boundary on either side, and the tasks never listed;
// a dry run lists the same tasks as the real run and removes nothing.
func TestPruneSelection(t *testing.T) {
	f := newPruneFixture(t)
	for _, id := range []string{"t-due", "t-edge", "t-recent", "t-dep", "t-audit", "t-review", "t-reviewed", "t-running", "t-judge", "t-open", "t-pruned"} {
		f.task("app", id)
	}
	f.task("app", "t-dependant", store.TaskRef{Repository: "app", ID: "t-dep"})
	f.task("lib", "t-lib")
	due := f.attempt("app", "t-due", "build", store.AttemptSucceeded, nil)
	running := f.attempt("app", "t-running", "build", "", nil)
	f.mutate(func(s *store.Store) error {
		_, err := s.OpenDecision(f.id(), store.Decision{Repository: "app", TaskID: "t-audit", ID: "audit", Kind: store.DecisionAudit,
			Subject: &store.Subject{Kind: store.SubjectHead, Digest: strings.Repeat("a", 40)}, Options: []string{"yes", "no"}, Question: "Audit?"})
		return err
	})
	for _, id := range []string{"t-due", "t-dep", "t-audit", "t-review", "t-reviewed", "t-running", "t-judge", "t-pruned"} {
		f.close("app", id)
	}
	f.close("lib", "t-lib")
	f.at = f.now.Add(-14 * 24 * time.Hour)
	f.close("app", "t-edge")
	f.at = f.now.Add(-14*24*time.Hour + time.Second)
	f.close("app", "t-recent")
	f.at = pruneClosed
	f.mutate(func(s *store.Store) error {
		if _, err := s.StartAttempt(f.id(), "app", "t-judge", workflow.JudgeStep); err != nil {
			return err
		}
		_, err := s.PruneTask("prune:app:t-pruned", store.TaskPrune{Repository: "app", TaskID: "t-pruned", Worktree: store.WorktreeRemoved, Complete: true})
		return err
	})
	selfReview := func(id, judge string) {
		writeTree(t, filepath.Join(f.copy, "quality", "app", id, "self-review-1.json"),
			`{"schema":"guildhall.self-review/1","repository":"app","task":"`+id+`","number":1,"head":"`+strings.Repeat("a", 40)+`","criteria":[{"id":"B2","kind":"score","judge":"`+judge+`"}]}`)
	}
	selfReview("t-review", "pending")
	selfReview("t-reviewed", "model")
	all := len(attemptFiles) + 2

	want := map[string]string{
		"app/t-due":      "",
		"app/t-edge":     "",
		"app/t-recent":   "too recent: closed 2026-10-02, prunable from 2026-10-16",
		"app/t-dep":      "open dependant app/t-dependant",
		"app/t-audit":    "audit audit open",
		"app/t-review":   "self-review 1 pending",
		"app/t-reviewed": "",
		"app/t-running":  "attempt build-1 unfinished",
		"app/t-judge":    "attempt quality-judge-1 unfinished",
		"lib/t-lib":      "repository lib is not in the config",
	}
	dry := f.run(true)
	if got := skips(dry); !mapsEqual(got, want) {
		t.Fatalf("dry run = %v\nwant %v", got, want)
	}
	if got := listFiles(t, due.Dir(f.state)); len(got) != all {
		t.Fatalf("a dry run removed files: %q", got)
	}
	if got := skips(f.run(false)); !mapsEqual(got, want) {
		t.Fatalf("real run = %v\nwant %v", got, want)
	}
	if got := listFiles(t, running.Dir(f.state)); len(got) != all {
		t.Fatalf("a skipped task lost files: %q", got)
	}
	if got := listFiles(t, due.Dir(f.state)); len(got) != len(keptFiles) {
		t.Fatalf("a prunable task kept %q", got)
	}

	// --task names one task; the rules still apply, and an open or pruned task says so.
	p := f.pruner()
	for id, want := range map[string]string{"t-recent": want["app/t-recent"], "t-open": "task is open", "t-pruned": "already pruned on 2026-10-01"} {
		got, err := p.run(context.Background(), "app", id, true, nil)
		if err != nil || len(got) != 1 || got[0].Skip != want {
			t.Errorf("--task %s = %+v, %v; want skipped %q", id, got, err, want)
		}
	}

	// prune_after 0 makes a task prunable as soon as it closes.
	p.after = 0
	if got, err := p.run(context.Background(), "app", "t-recent", true, nil); err != nil || len(got) != 1 || got[0].Skip != "" {
		t.Errorf("prune_after 0: %+v, %v", got, err)
	}
}

// TestPruneCancelledTask: a cancelled task is selected like a done one, from its cancel-task receipt, a voided audit
// does not hold it, and its unmerged worktree and branch are kept, saying it was cancelled, while its attempt files
// are pruned.
func TestPruneCancelledTask(t *testing.T) {
	f := newPruneFixture(t)
	for _, id := range []string{"t-cancelled", "t-recent"} {
		f.task("app", id)
	}
	k := f.attempt("app", "t-cancelled", "build", store.AttemptStopped, nil)
	w := f.workspace("t-cancelled")
	if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := (git.Program{}).Ensure(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(w.Dir, "change.txt"), "change\n")
	f.git(w.Dir, "add", "change.txt")
	f.git(w.Dir, "commit", "-q", "-m", "change")
	f.mutate(func(s *store.Store) error {
		if _, err := s.OpenDecision(f.id(), store.Decision{Repository: "app", TaskID: "t-cancelled", ID: "audit", Kind: store.DecisionAudit,
			Subject: &store.Subject{Kind: store.SubjectHead, Digest: strings.Repeat("a", 40)}, Options: []string{"yes", "no"}, Question: "Audit?"}); err != nil {
			return err
		}
		if _, err := s.VoidDecision(f.id(), "app", "t-cancelled", "audit", "duplicate"); err != nil {
			return err
		}
		_, err := s.CancelTask(f.id(), "app", "t-cancelled", "duplicate")
		return err
	})
	f.at = f.now.Add(-time.Hour)
	f.mutate(func(s *store.Store) error {
		_, err := s.CancelTask(f.id(), "app", "t-recent", "duplicate")
		return err
	})
	logs := f.ciLog("t-cancelled")

	got := f.run(false)
	want := map[string]string{"app/t-cancelled": "", "app/t-recent": "too recent: closed 2026-10-16, prunable from 2026-10-30"}
	if s := skips(got); !mapsEqual(s, want) {
		t.Fatalf("prune = %v\nwant %v", s, want)
	}
	var r pruneResult
	for _, g := range got {
		if g.Task.ID == "t-cancelled" {
			r = g
		}
	}
	if r.State != store.TaskCancelled || r.Worktree != store.WorktreeKept || !strings.Contains(r.Kept, "cancelled unmerged") || r.complete() {
		t.Fatalf("t-cancelled = %+v, want its worktree kept as cancelled", r)
	}
	if _, err := os.Lstat(w.Dir); err != nil {
		t.Errorf("worktree: %v, want it kept", err)
	}
	if _, err := os.Lstat(logs); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CI log cache: %v, want it removed", err)
	}
	var out strings.Builder
	if err := writePruneReport(&out, got, false); err != nil || !strings.Contains(out.String(), "of attempt and CI log files freed") {
		t.Errorf("report %q", out.String())
	}
	if exec.Command("git", "-C", f.repository, "show-ref", "--verify", "--quiet", "refs/heads/"+w.Branch).Run() != nil {
		t.Errorf("branch %s was removed", w.Branch)
	}
	if left := listFiles(t, k.Dir(f.state)); len(left) != len(keptFiles) {
		t.Errorf("attempt left %q", left)
	}
	if m := pruneKeptMessage(r); !strings.Contains(m.Text, "of cancelled task app/t-cancelled") {
		t.Errorf("kept message %q", m.Text)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestPruneRemovesAMergedWorktreeAndAttemptFiles: after a prune only the task lock and each attempt's result,
// outcome, manifest and lock remain; the worktree, branch and default ref are gone; the record is complete and a
// second prune is a no-op; spend report reads the same totals before and after.
func TestPruneRemovesAMergedWorktreeAndAttemptFiles(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.attempt("app", "t-1", "plan", store.AttemptSucceeded, nil)
	f.attempt("app", "t-1", "build", store.AttemptFailed, nil)
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, map[string]string{launch.PaneClaim: "{}\n", paneClosed: "{}\n"})
	f.worktree("t-1")
	f.close("app", "t-1")
	writeTree(t, filepath.Join(f.state, "app", "t-1", artisan.LockFile), "")
	logs := f.ciLog("t-1")
	spendBefore := f.spend()

	dry := f.run(true)
	got := f.run(false)
	if len(dry) != 1 || len(got) != 1 || dry[0].Bytes != got[0].Bytes {
		t.Fatalf("dry run = %+v, real run = %+v; want the same bytes freed", dry, got)
	}
	if len(got) != 1 || got[0].Skip != "" || got[0].Worktree != store.WorktreeRemoved || !got[0].complete() || got[0].Bytes <= ciLogSize {
		t.Fatalf("prune = %+v", got)
	}
	if _, err := os.Lstat(logs); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CI log cache: %v, want it removed", err)
	}
	want := []string{artisan.LockFile}
	for _, a := range []string{"build-1", "build-2", "plan-1"} {
		for _, name := range keptFiles {
			want = append(want, a+"/"+name)
		}
	}
	slices.Sort(want)
	if left := listFiles(t, filepath.Join(f.state, "app", "t-1")); !slices.Equal(left, want) {
		t.Fatalf("left %q\nwant %q", left, want)
	}
	w := f.workspace("t-1")
	if _, err := os.Lstat(w.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree: %v, want it removed", err)
	}
	for _, ref := range []string{"refs/heads/" + w.Branch, git.DefaultRef(w.Branch)} {
		if exec.Command("git", "-C", f.repository, "show-ref", "--verify", "--quiet", ref).Run() == nil {
			t.Errorf("%s survived the prune", ref)
		}
	}
	if list := f.git(f.repository, "worktree", "list", "--porcelain"); strings.Contains(filepath.ToSlash(list), "/work") {
		t.Errorf("the worktree is still registered:\n%s", list)
	}
	rec, err := f.record("t-1")
	if err != nil || !rec.Complete || rec.Worktree != store.WorktreeRemoved || rec.BytesFreed != got[0].Bytes || !rec.PrunedAt.Equal(f.now) {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	if got := f.spend(); got != spendBefore {
		t.Errorf("spend after the prune = %s\nbefore %s", got, spendBefore)
	}
	receipts := f.receipts()
	if again := f.run(false); len(again) != 0 {
		t.Errorf("second prune = %+v, want nothing listed", again)
	}
	if f.receipts() != receipts {
		t.Error("a second prune wrote to the store")
	}
	if docs := listFiles(t, filepath.Join(f.state, "documents")); len(docs) != 1 {
		t.Errorf("documents = %q, want the merge record kept", docs)
	}
}

// TestPruneRemovesAFinishedHallmarkWorktree: a done hallmark task, which has no merge record or pull request but a
// finished record naming the commit its read-only worktree stayed at, has its worktree, branch and attempt files
// pruned, its untracked report a recorded output; a finished record without a head keeps the worktree.
func TestPruneRemovesAFinishedHallmarkWorktree(t *testing.T) {
	f := newPruneFixture(t)
	for _, id := range []string{"t-1", "t-2"} {
		f.task("app", id)
		f.attempt("app", id, "hallmark", store.AttemptSucceeded, nil)
		w := f.workspace(id)
		if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := (git.Program{}).Ensure(context.Background(), w); err != nil {
			t.Fatal(err)
		}
		writeTree(t, filepath.Join(w.Dir, "report.md"), "# Hallmark\n")
		f.document(id, "hallmark-1-report", "hallmark-1/report.md", "# Hallmark\n")
		finished := `{"schema":"guildhall.finished/1"}`
		if id == "t-1" {
			finished = `{"schema":"guildhall.finished/1","head":"` + f.git(w.Dir, "rev-parse", "HEAD") + `"}`
		}
		f.document(id, interp.FinishedDocument, "finished.json", finished)
		f.close("app", id)
	}
	got := f.run(false)
	if len(got) != 2 || got[0].Task.ID != "t-1" || !got[0].complete() || got[1].Worktree != store.WorktreeKept || !strings.Contains(got[1].Kept, "no merge record") {
		t.Fatalf("prune = %+v", got)
	}
	w := f.workspace("t-1")
	if _, err := os.Lstat(w.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree: %v, want it removed", err)
	}
	if exec.Command("git", "-C", f.repository, "show-ref", "--verify", "--quiet", "refs/heads/"+w.Branch).Run() == nil {
		t.Errorf("branch %s survived the prune", w.Branch)
	}
	if _, err := os.Lstat(f.workspace("t-2").Dir); err != nil {
		t.Errorf("t-2 worktree: %v, want it kept", err)
	}
}

func (f *pruneFixture) spend() string {
	f.t.Helper()
	rep, err := withStore(f.store, true, func(s *store.Store) (spendReport, error) {
		return readSpend(s, f.state, "", "", time.Time{})
	})
	if err != nil {
		f.t.Fatal(err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

func (f *pruneFixture) receipts() int {
	f.t.Helper()
	n, err := withStore(f.store, true, func(s *store.Store) (int, error) {
		r, err := s.Receipts()
		return len(r), err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

// TestPruneKeepsAWorktreeRetireKeeps: an untracked file or an unmerged commit keeps the worktree, its files
// untouched, while the attempt files are pruned; the record says why, and the daily prune tells the Director once.
func TestPruneKeepsAWorktreeRetireKeeps(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(f *pruneFixture, w git.Workspace)
		want  string
	}{
		"an untracked file": {func(f *pruneFixture, w git.Workspace) {
			writeTree(f.t, filepath.Join(w.Dir, "scratch.txt"), "mine\n")
		}, "untracked scratch.txt"},
		"an unmerged commit": {func(f *pruneFixture, w git.Workspace) {
			writeTree(f.t, filepath.Join(w.Dir, "later.txt"), "later\n")
			f.git(w.Dir, "add", "later.txt")
			f.git(w.Dir, "commit", "-q", "-m", "later")
		}, "not the merged head"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPruneFixture(t)
			f.task("app", "t-1")
			k := f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
			f.worktree("t-1")
			w := f.workspace("t-1")
			tc.setup(f, w)
			f.close("app", "t-1")
			before := listFiles(t, w.Dir)

			p := f.pruner()
			d := &dailyPrune{p: p, stderr: f.stderr}
			got, err := p.run(context.Background(), "", "", false, d.report)
			if err != nil || len(got) != 1 || got[0].Worktree != store.WorktreeKept || !strings.Contains(got[0].Kept, tc.want) || got[0].complete() {
				t.Fatalf("prune = %+v, %v; want the worktree kept for %q", got, err, tc.want)
			}
			if after := listFiles(t, w.Dir); !slices.Equal(after, before) {
				t.Errorf("worktree files = %q, want %q untouched", after, before)
			}
			if left := listFiles(t, k.Dir(f.state)); len(left) != len(keptFiles) {
				t.Errorf("attempt files left %q, want the attempt pruned", left)
			}
			rec, err := f.record("t-1")
			if err != nil || rec.Worktree != store.WorktreeKept || rec.Kept != got[0].Kept || rec.Complete {
				t.Fatalf("record = %+v, %v", rec, err)
			}
			var out strings.Builder
			if err := writePruneReport(&out, got, false); err != nil || !strings.HasPrefix(out.String(), "skipped app/t-1: worktree kept: ") {
				t.Errorf("report %q", out.String())
			}

			// A rerun finds nothing new: no new record and no second message.
			receipts := f.receipts()
			if again, err := p.run(context.Background(), "", "", false, d.report); err != nil || len(again) != 1 || again[0].Worktree != store.WorktreeKept {
				t.Fatalf("rerun = %+v, %v", again, err)
			}
			if f.receipts() != receipts {
				t.Error("a rerun that found nothing new wrote to the store")
			}
			msgs := f.messages(store.MessagePruneKept)
			if len(msgs) != 1 || msgs[0].ID != "app.t-1."+store.MessagePruneKept || !strings.Contains(msgs[0].Text, tc.want) {
				t.Fatalf("messages = %+v, want one kept message", msgs)
			}
		})
	}
}

// TestPruneRetiresAnEarlierAttemptsOutput: a done task whose worktree a merge kept because its untracked
// outcome.json is the copy a narrowed review after a rebase recorded, while review-1 (which sorts after it) recorded
// other bytes at that path, is retired by the next prune.
func TestPruneRetiresAnEarlierAttemptsOutput(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	f.worktree("t-1")
	w := f.workspace("t-1")
	rebased := `{"verdict":"approved","attempt":"deliver-rebase-review-1"}` + "\n"
	f.document("t-1", "deliver-rebase-review-1-outcome", "deliver-rebase-review-1/outcome.json", rebased)
	f.document("t-1", "review-1-outcome", "review-1/outcome.json", `{"verdict":"approved","attempt":"review-1"}`+"\n")
	writeTree(t, filepath.Join(w.Dir, "outcome.json"), rebased)
	f.close("app", "t-1")
	f.mutate(func(s *store.Store) error {
		_, err := s.PruneTask(f.id(), store.TaskPrune{Repository: "app", TaskID: "t-1", Worktree: store.WorktreeKept, Kept: "untracked outcome.json"})
		return err
	})

	got := f.run(false)
	if len(got) != 1 || got[0].Worktree != store.WorktreeRemoved || !got[0].complete() {
		t.Fatalf("prune = %+v; want the worktree removed", got)
	}
	if _, err := os.Lstat(w.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("worktree: %v, want it removed", err)
	}
	if rec, err := f.record("t-1"); err != nil || rec.Worktree != store.WorktreeRemoved || !rec.Complete {
		t.Fatalf("record = %+v, %v", rec, err)
	}
}

func (f *pruneFixture) messages(typ string) []store.Message {
	f.t.Helper()
	var out []store.Message
	_, err := withStore(f.store, true, func(s *store.Store) (struct{}, error) {
		page, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		for _, m := range page {
			if m.Type == typ {
				out = append(out, m)
			}
		}
		return struct{}{}, err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// TestPruneLeavesAPlainWorkDirectory: a work directory that is not a worktree, from before worktrees, is reported
// and untouched, in a dry run and a real one.
func TestPruneLeavesAPlainWorkDirectory(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	f.document("t-1", "deliver-1-merge", "deliver-1/merge.json", `{"head":"`+strings.Repeat("b", 40)+`"}`)
	work := filepath.Join(f.state, "app", "t-1", "work")
	writeTree(t, filepath.Join(work, "notes.md"), "old\n")
	f.close("app", "t-1")
	for _, dry := range []bool{true, false} {
		got := f.run(dry)
		if len(got) != 1 || got[0].Worktree != store.WorktreeNotWorkspace || !strings.Contains(got[0].Kept, "is not a worktree") {
			t.Fatalf("dry %v: prune = %+v", dry, got)
		}
		var out strings.Builder
		if err := writePruneReport(&out, got, dry); err != nil || !strings.HasPrefix(out.String(), "skipped app/t-1: not a worktree: ") {
			t.Errorf("dry %v: report %q", dry, out.String())
		}
		if left := listFiles(t, work); !slices.Equal(left, []string{"notes.md"}) {
			t.Fatalf("dry %v: work left %q", dry, left)
		}
	}
	if rec, err := f.record("t-1"); err != nil || rec.Worktree != store.WorktreeNotWorkspace || rec.Complete {
		t.Fatalf("record = %+v, %v", rec, err)
	}
}

// TestPruneAfterACrashAndAPartialRemoval: removal that stopped before its record leaves the task selectable and the
// rerun completes it; a file that cannot be removed gives a partial prune, recorded incomplete, which the next run
// finishes.
func TestPruneAfterACrashAndAPartialRemoval(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.task("app", "t-2")
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	k2 := f.attempt("app", "t-2", "build", store.AttemptSucceeded, nil)
	f.worktree("t-1")
	f.close("app", "t-1")
	f.close("app", "t-2")

	// The crash: t-1's removal ran to its end and the store write never happened.
	p := f.pruner()
	h, err := withStore(f.store, true, func(s *store.Store) (store.TaskHistory, error) { return s.TaskHistory("app", "t-1") })
	if err != nil {
		t.Fatal(err)
	}
	r := pruneResult{Task: store.TaskRef{Repository: "app", ID: "t-1"}}
	if err := p.retire(context.Background(), &r, h); err != nil || r.Worktree != store.WorktreeRemoved {
		t.Fatalf("retire = %+v, %v", r, err)
	}
	if err := p.pruneAttempt(&r, artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.record("t-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("record after the crash: %v", err)
	}

	f.removeFails = func(path string) bool { return strings.HasSuffix(path, artisan.EventsFile) }
	got := f.run(false)
	results := map[string]pruneResult{}
	for _, r := range got {
		results[r.Task.ID] = r
	}
	if r := results["t-1"]; r.Skip != "" || !r.complete() {
		t.Errorf("t-1 after the crash = %+v, want it pruned completely", r)
	}
	if rec, err := f.record("t-1"); err != nil || !rec.Complete {
		t.Errorf("t-1 record = %+v, %v", rec, err)
	}
	r2 := results["t-2"]
	if r2.Skip != "" || r2.complete() || !slices.Equal(r2.Failed, []string{"build-1/" + artisan.EventsFile}) {
		t.Fatalf("t-2 = %+v, want a partial prune", r2)
	}
	var out strings.Builder
	if err := writePruneReport(&out, got, false); err != nil || !strings.Contains(out.String(), "pruned app/t-2: ") || !strings.Contains(out.String(), "(partial: could not remove build-1/"+artisan.EventsFile+")") {
		t.Errorf("report %q", out.String())
	}
	if rec, err := f.record("t-2"); err != nil || rec.Complete || rec.Runs != 1 {
		t.Fatalf("t-2 record = %+v, %v; want an incomplete record", rec, err)
	}
	if _, err := os.Lstat(filepath.Join(k2.Dir(f.state), artisan.EventsFile)); err != nil {
		t.Fatalf("the unremovable file: %v", err)
	}

	f.removeFails = nil
	again := f.run(false)
	if len(again) != 1 || again[0].Task.ID != "t-2" || !again[0].complete() {
		t.Fatalf("rerun = %+v, want t-2 completed", again)
	}
	if rec, err := f.record("t-2"); err != nil || !rec.Complete || rec.Runs != 2 {
		t.Fatalf("t-2 record = %+v, %v", rec, err)
	}
}

// TestPruneCatchesUpACILogCache: a task pruned completely whose CI log cache is back, or was left by a prune from
// before the cache was pruned, is selected again, counted by a dry run that keeps it, and pruned of the cache alone,
// recorded complete with the cache's bytes; a complete task with no cache is not selected, and the next run lists
// nothing.
func TestPruneCatchesUpACILogCache(t *testing.T) {
	f := newPruneFixture(t)
	for _, id := range []string{"t-1", "t-2"} {
		f.task("app", id)
		f.attempt("app", id, "build", store.AttemptSucceeded, nil)
		f.close("app", id)
	}
	if got := f.run(false); len(got) != 2 || !got[0].complete() || !got[1].complete() {
		t.Fatalf("first prune = %+v", got)
	}
	logs := f.ciLog("t-1")
	receipts := f.receipts()

	dry := f.run(true)
	if len(dry) != 1 || dry[0].Task.ID != "t-1" || dry[0].Skip != "" || dry[0].Bytes != ciLogSize {
		t.Fatalf("dry run = %+v, want t-1 with its cache's %d bytes", dry, ciLogSize)
	}
	if _, err := os.Lstat(logs); err != nil {
		t.Fatalf("CI log cache after a dry run: %v", err)
	}
	got := f.run(false)
	if len(got) != 1 || got[0].Task.ID != "t-1" || !got[0].complete() || got[0].Bytes != ciLogSize {
		t.Fatalf("catch-up = %+v, want t-1 pruned of its cache", got)
	}
	if _, err := os.Lstat(logs); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CI log cache: %v, want it removed", err)
	}
	if rec, err := f.record("t-1"); err != nil || rec.Runs != 2 || !rec.Complete || rec.BytesFreed != ciLogSize {
		t.Fatalf("t-1 record = %+v, %v; want run 2, complete, %d bytes", rec, err, ciLogSize)
	}
	if f.receipts() != receipts+1 {
		t.Errorf("receipts = %d, want one more than %d", f.receipts(), receipts)
	}

	receipts = f.receipts()
	if again := f.run(false); len(again) != 0 {
		t.Errorf("next prune = %+v, want nothing listed", again)
	}
	if f.receipts() != receipts {
		t.Error("the next prune wrote to the store")
	}
	if got, err := f.pruner().run(context.Background(), "app", "t-2", true, nil); err != nil || len(got) != 1 || !strings.HasPrefix(got[0].Skip, "already pruned on ") {
		t.Errorf("--task t-2 = %+v, %v; want already pruned", got, err)
	}
}

// TestPruneACILogCacheThatCannotBeRemoved: a CI log cache that cannot be removed gives a partial prune, recorded
// incomplete, which the next run finishes.
func TestPruneACILogCacheThatCannotBeRemoved(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	f.close("app", "t-1")
	logs := f.ciLog("t-1")

	f.removeFails = func(path string) bool { return path == logs }
	got := f.run(false)
	if len(got) != 1 || got[0].Skip != "" || got[0].complete() || !slices.Equal(got[0].Failed, []string{"ci-logs"}) {
		t.Fatalf("prune = %+v, want a partial prune", got)
	}
	var out strings.Builder
	if err := writePruneReport(&out, got, false); err != nil || !strings.Contains(out.String(), "(partial: could not remove ci-logs)") {
		t.Errorf("report %q", out.String())
	}
	if rec, err := f.record("t-1"); err != nil || rec.Complete || rec.Runs != 1 {
		t.Fatalf("record = %+v, %v; want an incomplete record", rec, err)
	}
	if _, err := os.Lstat(logs); err != nil {
		t.Fatalf("the unremovable cache: %v", err)
	}

	f.removeFails = nil
	again := f.run(false)
	if len(again) != 1 || !again[0].complete() || again[0].Bytes != ciLogSize {
		t.Fatalf("rerun = %+v, want the cache removed", again)
	}
	if _, err := os.Lstat(logs); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("CI log cache: %v, want it removed", err)
	}
	if rec, err := f.record("t-1"); err != nil || !rec.Complete || rec.Runs != 2 {
		t.Fatalf("record = %+v, %v", rec, err)
	}
}

// TestPruneCommandOutput is the golden output of prune, prune --dry-run and task files of a pruned task, over a
// task with attempt files and no worktree and a task closed too recently.
func TestPruneCommandOutput(t *testing.T) {
	f := newPruneFixture(t)
	f.task("app", "t-1")
	f.task("app", "t-2")
	f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	f.close("app", "t-1")
	f.at = f.now.Add(-time.Hour)
	f.close("app", "t-2")
	run := func(args ...string) string {
		t.Helper()
		f.stdout.Reset()
		if code := f.app.Run(args); code != 0 {
			t.Fatalf("%q: exit %d, stderr %s", args, code, f.stderr)
		}
		return f.stdout.String()
	}
	dry := run("prune", "--dry-run", "--copy", f.copy)
	checkGolden(t, "prune-dry-run.txt", dry)
	checkGolden(t, "prune.txt", run("prune", "--copy", f.copy))
	checkGolden(t, "task-files-pruned.json", run("task", "files", "--store", f.store, "--repo", "app", "--json", "t-1"))
	if out := run("task", "timeline", "--store", f.store, "--repo", "app", "--json", "t-1"); !strings.Contains(out, `"mutation_id":"prune:app:t-1","operation":"prune-task"`) || !strings.Contains(out, `"record":"prune"`) {
		t.Errorf("timeline %s, want the prune receipt", out)
	}
	if out := run("prune", "--copy", f.copy); out != "skipped app/t-2: too recent: closed 2026-10-16, prunable from 2026-10-30\ntotal: 0 pruned, 1 skipped, 0 B freed\n" {
		t.Errorf("second prune = %q", out)
	}

	f.stdout.Reset()
	if code := f.app.Run([]string{"prune", "--copy", f.copy, "extra"}); code != 2 {
		t.Errorf("an extra argument: exit %d, want 2", code)
	}
	if code := f.app.Run([]string{"prune"}); code != 2 {
		t.Errorf("no copy: exit %d, want 2", code)
	}
	if code := f.app.Run([]string{"prune", "--copy", f.copy, "--task", "T"}); code != 2 {
		t.Errorf("an invalid task id: exit %d, want 2", code)
	}
}

// TestPruneKeepsPaneFilesTheSweepStillCloses: an attempt whose Herdr workspace is not closed keeps its pane files
// through a prune, and the sweep then closes it; an attempt already closed loses them, and the sweep leaves it alone.
func TestPruneKeepsPaneFilesTheSweepStillCloses(t *testing.T) {
	sf := newSweepFixture(t)
	open := sf.attempt("t-1", "build", store.AttemptSucceeded, nil)
	sf.writeRecord(open, sf.presented(open, "w2", launch.WorkspaceLabel(open)))
	closed := sf.attempt("t-2", "build", store.AttemptSucceeded, nil)
	sf.writeRecord(closed, sf.presented(closed, "w3", launch.WorkspaceLabel(closed)))
	writeTree(t, filepath.Join(closed.Dir(sf.state), paneClosed), "{}\n")
	for _, k := range []artisan.AttemptKey{open, closed} {
		for name, data := range attemptFiles {
			writeTree(t, filepath.Join(k.Dir(sf.state), filepath.FromSlash(name)), data)
		}
		attemptManifest(t, sf.state, k)
	}

	p := &pruner{storePath: sf.storePath, state: sf.state}
	r := pruneResult{Task: store.TaskRef{Repository: "app", ID: "t-1"}}
	if err := p.pruneAttempt(&r, open, false); err != nil || !slices.Equal(r.Panes, []string{"build-1"}) {
		t.Fatalf("prune of the open attempt = %+v, %v", r, err)
	}
	if _, err := os.Lstat(filepath.Join(open.Dir(sf.state), launch.PaneClaim)); err != nil {
		t.Fatalf("pane record: %v, want it kept", err)
	}
	r = pruneResult{Task: store.TaskRef{Repository: "app", ID: "t-2"}}
	if err := p.pruneAttempt(&r, closed, false); err != nil || len(r.Panes) != 0 {
		t.Fatalf("prune of the closed attempt = %+v, %v", r, err)
	}
	if left := listFiles(t, closed.Dir(sf.state)); len(left) != len(keptFiles) {
		t.Fatalf("closed attempt left %q", left)
	}

	sf.sweep(5*time.Minute, time.Hour).run()
	if !slices.Equal(sf.host.closed, []string{"workspace w2"}) || !sf.marked(open) {
		t.Errorf("closed %q, marked %v; want only w2 closed and marked", sf.host.closed, sf.marked(open))
	}
	if sf.workspace("w3") == nil {
		t.Error("the sweep closed the workspace of a pruned attempt")
	}
	if sf.stderr.Len() != 0 {
		t.Errorf("stderr %q", sf.stderr.String())
	}
}

// TestDailyPrune: with prune_daily on, the supervisor's daily prune runs at session start and again a day later,
// not before, across sessions; with it off, the default, there is none.
func TestDailyPrune(t *testing.T) {
	f := newPruneFixture(t)
	if d, err := f.app.dailyPrune(f.cfg, f.copy); err != nil || d != nil {
		t.Fatalf("dailyPrune with prune_daily unset = %v, %v; want none", d, err)
	}
	if d, err := f.app.dailyPrune(directorConfig(t, `prune_daily = "false"`+"\n"), f.copy); err != nil || d != nil {
		t.Fatalf("dailyPrune with prune_daily false = %v, %v; want none", d, err)
	}
	d, err := f.app.dailyPrune(directorConfig(t, `prune_daily = "true"`+"\n"), f.copy)
	if err != nil || d == nil {
		t.Fatalf("dailyPrune with prune_daily true = %v, %v", d, err)
	}
	f.task("app", "t-1")
	k := f.attempt("app", "t-1", "build", store.AttemptSucceeded, nil)
	f.close("app", "t-1")
	f.task("app", "t-2")
	f.attempt("app", "t-2", "build", store.AttemptSucceeded, nil)

	// Session start: the first round prunes.
	w := &workspaceCloseWatch{every: pruneDailyEvery, round: d.run}
	w.tick(time.Now())
	w.wait()
	if left := listFiles(t, k.Dir(f.state)); len(left) != len(keptFiles) {
		t.Fatalf("after the start round, t-1 left %q", left)
	}
	if !strings.Contains(f.stderr.String(), "guildhall: daily prune: pruned app/t-1: ") {
		t.Errorf("supervisor log %q", f.stderr.String())
	}

	// An hour later, and a new session's start within the day: nothing, though t-2 is now due.
	f.close("app", "t-2")
	f.now = f.now.Add(time.Hour)
	d.run()
	next, err := f.app.dailyPrune(directorConfig(t, `prune_daily = "true"`+"\n"), f.copy)
	if err != nil {
		t.Fatal(err)
	}
	next.run()
	if rec, err := f.record("t-2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("t-2 pruned within the day: %+v, %v", rec, err)
	}
	// The day's tick prunes.
	f.now = f.now.Add(pruneDailyGap)
	next.run()
	if rec, err := f.record("t-2"); err != nil || !rec.Complete {
		t.Fatalf("t-2 after the day's tick = %+v, %v", rec, err)
	}
}

// TestBoundKept: a long kept reason is cut, on a rune boundary, to fit the store's text limits.
func TestBoundKept(t *testing.T) {
	if got := boundKept("untracked a.txt"); got != "untracked a.txt" {
		t.Errorf("short reason = %q", got)
	}
	long := strings.Repeat("é", maxPruneKept)
	got := boundKept(long)
	if len(got) > maxPruneKept+len(" …") || !strings.HasSuffix(got, " …") || !utf8.ValidString(got) {
		t.Errorf("long reason cut to %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if len(got) > store.MaxMessageTextBytes/2 {
		t.Errorf("cut reason is %d bytes", len(got))
	}
}
