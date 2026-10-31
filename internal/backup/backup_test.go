package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestMain(m *testing.M) {
	store.SkipSyncForTests()
	os.Exit(m.Run())
}

// isolateGit runs every git of the test without the user's or system's configuration.
func isolateGit(t *testing.T) {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hexDigest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

// fixture is a copy with a store holding one task and its document, and a bare remote.
type fixture struct {
	copy, remote string
	opts         Options
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	isolateGit(t)
	root := t.TempDir()
	f := &fixture{copy: filepath.Join(root, "copy"), remote: filepath.Join(root, "backups.git")}
	gitOut(t, root, "init", "--quiet", "--bare", f.remote)
	write(t, filepath.Join(f.copy, ".guildhall-copy"), `{"schema":"guildhall.copy","version":1,"name":"copy"}`+"\n")
	write(t, filepath.Join(f.copy, "config.toml"), "schema_version = 1\n")
	write(t, filepath.Join(f.copy, "managed.toml"), "# managed\n")
	write(t, filepath.Join(f.copy, "workflows", "mine.json"), "{}\n")
	write(t, filepath.Join(f.copy, "quality", "app", "t-1", "self-review-1.json"), "{}\n")
	write(t, filepath.Join(f.copy, "baseline", "roles", "director.md"), "# role\n")
	write(t, filepath.Join(f.copy, "roles", "mine.md"), "# my role\n")
	write(t, filepath.Join(f.copy, "director", "session-1", "startup.md"), "start\n")
	write(t, filepath.Join(f.copy, "brand-new", "kind.txt"), "a kind of copy file nobody listed\n")
	write(t, filepath.Join(f.copy, "state", "prune-last.json"), "{}\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "build-1", "result.json"), "{}\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "build-1", "launcher.log"), "launched\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "build-1", "events.jsonl"), "{}\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "build-1", "model", "transcript.jsonl"), "{}\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "attempt.lock"), "")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "work", "main.go"), "package main\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "work", ".git"), "gitdir: elsewhere\n")
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "ci-logs", "job.log"), "log\n")
	// A Claude config dir with a project for this copy is outside the copy and contributes nothing.
	write(t, filepath.Join(root, "claude", "projects", "-copy-state-app-t-1-work", "session.jsonl"), "{}\n")

	doc := "# the plan\n"
	write(t, filepath.Join(f.copy, "state", "documents", "tasks", "app", "t-1", "plan-1", "spec.md"), doc)
	sum := sha256.Sum256([]byte(doc))
	storePath := filepath.Join(f.copy, "director.store")
	s, err := store.Open(storePath, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	task := store.Task{Repository: "app", ID: "t-1", Title: "A task", Workflow: workflow.Revision{Name: "default", Digest: hexDigest('a')}, ConfigDigest: hexDigest('b')}
	if _, err := s.CreateTask("create-1", task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("doc-1", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "spec", Path: "tasks/app/t-1/plan-1/spec.md", SHA256: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	f.opts = Options{Copy: f.copy, Store: storePath, Config: config.Backup{Repo: f.remote, Branch: "laptop/alpha"}, LockWait: 30 * time.Second}
	return f
}

func (f *fixture) run(t *testing.T) Result {
	t.Helper()
	res, err := Run(context.Background(), f.opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func (f *fixture) remoteTip(t *testing.T) string {
	t.Helper()
	return gitOut(t, f.remote, "rev-parse", "--verify", "--quiet", "refs/heads/laptop/alpha")
}

func treePaths(t *testing.T, repo, commit string) []string {
	t.Helper()
	return strings.Split(gitOut(t, repo, "ls-tree", "-r", "--name-only", commit), "\n")
}

func TestBackupCoverage(t *testing.T) {
	f := newFixture(t)
	res := f.run(t)
	if res.Commit == "" || res.Pushed != res.Commit || f.remoteTip(t) != res.Commit {
		t.Fatalf("result %+v, remote tip %s; want one commit pushed", res, f.remoteTip(t))
	}
	var copyFiles []string
	for _, p := range treePaths(t, f.remote, res.Commit) {
		if rest, ok := strings.CutPrefix(p, "copy/"); ok {
			copyFiles = append(copyFiles, rest)
		} else if !strings.HasPrefix(p, "store/") && p != ".gitattributes" {
			t.Errorf("unexpected path %s in the backup", p)
		}
	}
	want := []string{".guildhall-copy", "baseline/roles/director.md", "brand-new/kind.txt", "config.toml", "director/session-1/startup.md",
		"managed.toml", "quality/app/t-1/self-review-1.json", "roles/mine.md",
		"state/app/t-1/build-1/events.jsonl", "state/app/t-1/build-1/launcher.log", "state/app/t-1/build-1/model/transcript.jsonl",
		"state/app/t-1/build-1/result.json", "state/documents/tasks/app/t-1/plan-1/spec.md", "state/prune-last.json", "workflows/mine.json"}
	if !slices.Equal(copyFiles, want) {
		t.Fatalf("copy files backed up:\n%s\nwant:\n%s", strings.Join(copyFiles, "\n"), strings.Join(want, "\n"))
	}
	if got := gitOut(t, f.remote, "show", res.Commit+":.gitattributes"); got != "* -text" {
		t.Errorf(".gitattributes = %q", got)
	}
	if got := gitOut(t, f.remote, "show", res.Commit+":store/repositories/app.txt"); !strings.Contains(got, "record\t2\tt-1\t") {
		t.Errorf("store export of app:\n%s", got)
	}
	if msg := gitOut(t, f.remote, "log", "-1", "--format=%s", res.Commit); msg != "Back up laptop/alpha at receipt sequence 2" {
		t.Errorf("commit subject = %q", msg)
	}
	stamp, err := ReadStamp(filepath.Join(f.copy, "state"))
	if err != nil || !stamp.OK || stamp.Commit != res.Commit || stamp.Pushed != res.Commit || stamp.ReceiptSequence != 2 || stamp.Error != "" {
		t.Fatalf("stamp = %+v, %v", stamp, err)
	}
}

func TestBackupIncremental(t *testing.T) {
	f := newFixture(t)
	first := f.run(t)
	again := f.run(t)
	if again.Commit != "" || again.Tip != first.Commit {
		t.Fatalf("a run over an unchanged copy made a commit: %+v", again)
	}
	write(t, filepath.Join(f.copy, "state", "documents", "tasks", "app", "t-1", "plan-1", "notes.md"), "new document\n")
	third := f.run(t)
	if third.Commit == "" || f.remoteTip(t) != third.Commit {
		t.Fatalf("a new document made no commit: %+v", third)
	}
	diff := gitOut(t, f.remote, "diff", "--name-only", first.Commit, third.Commit)
	if diff != "copy/state/documents/tasks/app/t-1/plan-1/notes.md" {
		t.Fatalf("diff = %q", diff)
	}
	// A file gone from the copy, as a prune removes one, leaves the next commit's tree.
	if err := os.RemoveAll(filepath.Join(f.copy, "state", "app", "t-1", "build-1")); err != nil {
		t.Fatal(err)
	}
	fourth := f.run(t)
	for _, p := range treePaths(t, f.remote, fourth.Commit) {
		if strings.Contains(p, "build-1") {
			t.Fatalf("pruned %s is still in the tip", p)
		}
	}
}

func TestBackupSizeGuard(t *testing.T) {
	f := newFixture(t)
	f.opts.SizeLimit = 4096
	write(t, filepath.Join(f.copy, "state", "app", "t-1", "build-1", "huge.log"), strings.Repeat("x", 5000))
	res := f.run(t)
	if len(res.LeftOut) != 1 || res.LeftOut[0] != (LeftOut{Path: "state/app/t-1/build-1/huge.log", Size: 5000}) {
		t.Fatalf("left out = %+v", res.LeftOut)
	}
	paths := treePaths(t, f.remote, res.Commit)
	if slices.Contains(paths, "copy/state/app/t-1/build-1/huge.log") || !slices.Contains(paths, "copy/config.toml") {
		t.Fatalf("tree = %v", paths)
	}
	if stamp, _ := ReadStamp(filepath.Join(f.copy, "state")); len(stamp.LeftOut) != 1 {
		t.Fatalf("the stamp does not name the left-out file: %+v", stamp)
	}
}

func TestBackupDocumentMismatch(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.copy, "state", "documents", "tasks", "app", "t-1", "plan-1", "spec.md"), "damaged\n")
	_, err := Run(context.Background(), f.opts)
	if err == nil || !strings.Contains(err.Error(), "tasks/app/t-1/plan-1/spec.md") || !strings.Contains(err.Error(), "nothing was committed") {
		t.Fatalf("Run = %v; want the mismatch", err)
	}
	if out, _ := exec.Command("git", "-C", filepath.Join(f.copy, RepoDir), "rev-parse", "--verify", "--quiet", "refs/heads/laptop/alpha").Output(); len(out) > 0 {
		t.Fatalf("a mismatch made commit %s", out)
	}
	if stamp, _ := ReadStamp(filepath.Join(f.copy, "state")); stamp.OK || !strings.Contains(stamp.Error, "spec.md") {
		t.Fatalf("stamp = %+v", stamp)
	}
}

func TestBackupPushOfflineThenBoth(t *testing.T) {
	f := newFixture(t)
	first := f.run(t)
	online := f.opts.Config.Repo
	f.opts.Config.Repo = filepath.Join(t.TempDir(), "offline.git")
	write(t, filepath.Join(f.copy, "brand-new", "two.txt"), "2\n")
	second, err := Run(context.Background(), f.opts)
	if err == nil || second.Commit == "" || second.Pushed != first.Commit {
		t.Fatalf("offline run = %+v, %v; want a kept commit and an error", second, err)
	}
	write(t, filepath.Join(f.copy, "brand-new", "three.txt"), "3\n")
	third, err := Run(context.Background(), f.opts)
	if err == nil || third.Commit == "" {
		t.Fatalf("second offline run = %+v, %v", third, err)
	}
	if stamp, _ := ReadStamp(filepath.Join(f.copy, "state")); stamp.OK || stamp.Commit != third.Commit || stamp.Pushed != first.Commit {
		t.Fatalf("stamp = %+v", stamp)
	}
	f.opts.Config.Repo = online
	fourth := f.run(t)
	if fourth.Commit != "" || fourth.Pushed != third.Commit || f.remoteTip(t) != third.Commit {
		t.Fatalf("online run = %+v, remote %s; want both kept commits pushed", fourth, f.remoteTip(t))
	}
	if gitOut(t, f.remote, "rev-list", "--count", third.Commit) != "3" || gitOut(t, f.remote, "rev-parse", third.Commit+"^") != second.Commit {
		t.Fatalf("remote history does not hold every commit")
	}
}

func TestBackupNeverForces(t *testing.T) {
	f := newFixture(t)
	f.run(t)
	// Another copy pushes its own history to the same branch, so ours no longer fast-forwards.
	other := newFixture(t)
	theirs := other.run(t)
	f.opts.Config.Repo = other.remote
	write(t, filepath.Join(f.copy, "brand-new", "two.txt"), "2\n")
	res, err := Run(context.Background(), f.opts)
	if err == nil || res.Commit == "" || !strings.Contains(err.Error(), "push to") {
		t.Fatalf("diverged run = %+v, %v; want a refused push", res, err)
	}
	if other.remoteTip(t) != theirs.Commit {
		t.Fatalf("the diverged push replaced the remote branch")
	}
}

func TestBackupPushBound(t *testing.T) {
	f := newFixture(t)
	f.opts.PushTimeout = time.Nanosecond
	res, err := Run(context.Background(), f.opts)
	if !errors.Is(err, context.DeadlineExceeded) || res.Commit == "" || res.Pushed != "" {
		t.Fatalf("bounded run = %+v, %v; want the push timed out and the commit kept", res, err)
	}
	f.opts.PushTimeout = 0
	if res := f.run(t); res.Pushed == "" || f.remoteTip(t) != res.Pushed {
		t.Fatalf("the next run did not push: %+v", res)
	}
}

func TestBackupRestore(t *testing.T) {
	f := newFixture(t)
	res := f.run(t)
	target := filepath.Join(t.TempDir(), "restored")
	got, err := Restore(context.Background(), RestoreOptions{Target: target, Repo: f.remote, Rev: "laptop/alpha"})
	if err != nil || got.Commit != res.Commit {
		t.Fatalf("Restore = %+v, %v", got, err)
	}
	for _, rel := range []string{"config.toml", "state/documents/tasks/app/t-1/plan-1/spec.md", "state/app/t-1/build-1/model/transcript.jsonl", "roles/mine.md"} {
		a, _ := os.ReadFile(filepath.Join(f.copy, filepath.FromSlash(rel)))
		b, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(rel)))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s restored as %q, %v; want %q", rel, b, err, a)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, ".guildhall-copy")); err == nil {
		t.Errorf("the restore wrote the source's copy marker")
	}
	s, err := store.Open(filepath.Join(target, "director.store"), store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks("app")
	_ = s.Close()
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t-1" {
		t.Fatalf("restored tasks = %+v, %v", tasks, err)
	}
	if _, err := Restore(context.Background(), RestoreOptions{Target: target, Repo: f.remote, Rev: res.Commit}); !errors.Is(err, store.ErrExists) {
		t.Fatalf("restore over a store: %v; want ErrExists", err)
	}
	// A restore by commit id writes the same copy.
	byID := filepath.Join(t.TempDir(), "by-id")
	if got, err := Restore(context.Background(), RestoreOptions{Target: byID, Repo: f.remote, Rev: res.Commit}); err != nil || got.Commit != res.Commit {
		t.Fatalf("restore by id = %+v, %v", got, err)
	}
	// The restored copy's next backup to the same branch continues its history.
	write(t, filepath.Join(target, "brand-new", "after.txt"), "after\n")
	next, err := Run(context.Background(), Options{Copy: target, Store: filepath.Join(target, "director.store"), Config: f.opts.Config, LockWait: 30 * time.Second})
	if err != nil || gitOut(t, f.remote, "rev-parse", next.Commit+"^") != res.Commit {
		t.Fatalf("backup after restore = %+v, %v", next, err)
	}
}

func TestExcluded(t *testing.T) {
	for rel, want := range map[string]bool{
		"director.store": true, "backup.git": true, "state/backup-last.json": true, "state/app/t-1/attempt.lock": true,
		"state/app/t-1/work": true, "state/app/t-1/ci-logs": true, "state/app/t-1/build-1/model/x.jsonl": false,
		"state/documents/tasks/work": false, "a/.git": true, "config.toml": false, "state/prune-last.json": false,
	} {
		dir := !strings.Contains(filepath.Base(rel), ".") || strings.HasSuffix(rel, ".git")
		if got := excluded(rel, dir); got != want {
			t.Errorf("excluded(%q, %t) = %t, want %t", rel, dir, got, want)
		}
	}
}
