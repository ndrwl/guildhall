package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/backup"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// backupCopy is a copy with a task, a document, an attempt's files and Director notes, its [backup] table naming a
// local bare repository, and git isolated from the user's configuration.
type backupFixture struct {
	dir, storePath, cfgPath, remote string
}

func newBackupFixture(t *testing.T) backupFixture {
	t.Helper()
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	f := backupFixture{dir: newCopy(t)}
	f.storePath, f.cfgPath = filepath.Join(f.dir, "director.store"), filepath.Join(f.dir, "config.toml")
	f.remote = filepath.Join(t.TempDir(), "guildhall-backups.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", f.remote).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	newTask(t, f.storePath, f.cfgPath, "app", "First", "t-1")
	newTask(t, f.storePath, f.cfgPath, "app", "Second", "t-2")
	doc := "# the spec\n"
	writeTestFile(t, filepath.Join(f.dir, "state", "documents", "tasks", "app", "t-1", "plan-1", "spec.md"), doc)
	sum := sha256.Sum256([]byte(doc))
	if _, err := withStore(f.storePath, false, func(s *store.Store) (store.Receipt, error) {
		return s.PutDocument("doc-1", store.DocumentRef{Repository: "app", TaskID: "t-1", Name: "spec", Path: "tasks/app/t-1/plan-1/spec.md", SHA256: hex.EncodeToString(sum[:])})
	}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.dir, "state", "app", "t-1", "build-1", "model", "transcript.jsonl"), "{\"turn\":1}\n")
	writeTestFile(t, filepath.Join(f.dir, "state", "app", "t-1", "work", "main.go"), "package main\n")
	recordSession(t, f.storePath, false, store.DirectorRunning)
	for _, text := range []string{"rebase t-2 after t-1 merges", "gone soon"} {
		if code, _, stderr := runCLI(t, "director", "note", "add", "--store", f.storePath, "--kind", "plan", text); code != 0 {
			t.Fatalf("note add: %s", stderr)
		}
	}
	if code, _, stderr := runCLI(t, "director", "note", "remove", "--store", f.storePath, "n2", "--reason", "done"); code != 0 {
		t.Fatalf("note remove: %s", stderr)
	}
	return f
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f backupFixture) addTable(t *testing.T, repo string) {
	t.Helper()
	data, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	// A JSON string is a TOML basic string, with a Windows path's backslashes escaped.
	table := "\n[backup]\nrepo = " + jsonString(t, repo) + "\nbranch = \"laptop/alpha\"\n"
	if err := os.WriteFile(f.cfgPath, append(data, table...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestBackupNowStatusRestore is the acceptance test: a backup restored into a new copy lists the same tasks and
// Director notes, and has the same documents and attempt files.
func TestBackupNowStatusRestore(t *testing.T) {
	f := newBackupFixture(t)
	f.addTable(t, f.remote)
	code, out, stderr := runIn(t, "", "backup", "now", "--copy", f.dir)
	if code != 0 || !strings.Contains(out, "branch: laptop/alpha\n") || !strings.Contains(out, "commit: ") {
		t.Fatalf("backup now: exit %d, %q, stderr %s", code, out, stderr)
	}
	code, out, stderr = runIn(t, "", "backup", "now", "--copy", f.dir)
	if code != 0 || !strings.Contains(out, "commit: none; nothing changed") {
		t.Fatalf("second backup now: exit %d, %q, stderr %s", code, out, stderr)
	}
	code, out, stderr = runIn(t, f.dir, "backup", "status")
	for _, want := range []string{"branch: laptop/alpha\n", "every: 1h0m0s\n", ", ok\n", "last commit: ", "last push: ", "repository size: "} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("backup status: exit %d, %q, stderr %s; want %q", code, out, stderr, want)
		}
	}
	if strings.Contains(out, "waits for the next push") || strings.Contains(out, "last error") {
		t.Fatalf("backup status of a pushed backup: %s", out)
	}

	target := filepath.Join(t.TempDir(), "restored")
	code, out, stderr = runIn(t, f.dir, "backup", "restore", "--copy", target, "laptop/alpha")
	if code != 0 || !strings.Contains(out, "store: "+filepath.Join(target, "director.store")+", ") || !strings.Contains(out, "2 tasks") {
		t.Fatalf("backup restore: exit %d, %q, stderr %s", code, out, stderr)
	}
	if err := readCopyMarker(filepath.Join(target, copyMarkerFile)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"task", "list", "--json"}, {"director", "note", "list"}} {
		_, a, _ := runIn(t, "", append(args, "--copy", f.dir)...)
		_, b, stderr := runIn(t, "", append(args, "--copy", target)...)
		if a == "" || a != b {
			t.Errorf("%q differs after the restore:\n%s\nrestored:\n%s\nstderr %s", args, a, b, stderr)
		}
	}
	for _, rel := range []string{"state/documents/tasks/app/t-1/plan-1/spec.md", "state/app/t-1/build-1/model/transcript.jsonl", "config.toml"} {
		a, _ := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(rel)))
		b, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(rel)))
		if err != nil || string(a) != string(b) {
			t.Errorf("%s restored as %q, %v; want %q", rel, b, err, a)
		}
	}
	if _, err := os.Lstat(filepath.Join(target, "state", "app", "t-1", "work")); err == nil {
		t.Errorf("the restore wrote a worktree")
	}
	if code, _, stderr := runIn(t, f.dir, "backup", "restore", "--copy", newCopy(t), "laptop/alpha"); code != 1 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("restore over a store: exit %d, stderr %s", code, stderr)
	}
	// A copy whose session runs is refused, naming the session.
	if code, _, stderr := runIn(t, "", "backup", "restore", "--copy", f.dir, "--repo", f.remote, "laptop/alpha"); code != 1 || !strings.Contains(stderr, "session 1 is running; stop it") {
		t.Fatalf("restore over a running copy: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := runIn(t, "", "backup", "restore", "--copy", filepath.Join(t.TempDir(), "x"), "laptop/alpha"); code != 2 || !strings.Contains(stderr, "pass --repo") {
		t.Fatalf("restore with no repository: exit %d, stderr %s", code, stderr)
	}
}

// TestNoBackupTable: a copy with no [backup] table starts no backup and makes no backup repository, and adding the
// table changes no task's config digest (D50: no Director feature needs a Steward, and none runs unasked).
func TestNoBackupTable(t *testing.T) {
	f := newBackupFixture(t)
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, _, _ := newTestApp(nil, nil)
	if sb, err := app.sessionBackup(cfg, f.dir, f.storePath); err != nil || sb != nil {
		t.Fatalf("sessionBackup with no table = %v, %v; want none", sb, err)
	}
	if code, _, stderr := runIn(t, "", "backup", "now", "--copy", f.dir); code != 1 || !strings.Contains(stderr, "has no [backup] table") {
		t.Fatalf("backup now with no table: exit %d, stderr %s", code, stderr)
	}
	if _, err := os.Lstat(filepath.Join(f.dir, backup.RepoDir)); err == nil {
		t.Fatalf("a copy with no [backup] table has a backup repository")
	}
	before, _, err := acceptTask(f.cfgPath, "app", "feature")
	if err != nil {
		t.Fatal(err)
	}
	f.addTable(t, f.remote)
	after, _, err := acceptTask(f.cfgPath, "app", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if before.ConfigDigest != after.ConfigDigest {
		t.Fatalf("adding [backup] changed the config digest: %s, then %s", before.ConfigDigest, after.ConfigDigest)
	}
}

// TestSessionBackup: the supervisor's round backs up, writing a guildhall: backup: line; a failed push keeps the
// commit, and director status names the failed backup in needs_me until a run succeeds.
func TestSessionBackup(t *testing.T) {
	f := newBackupFixture(t)
	f.addTable(t, filepath.Join(t.TempDir(), "offline.git"))
	cfg, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	app, _, stderr := newTestApp(nil, nil)
	sb, err := app.sessionBackup(cfg, f.dir, f.storePath)
	if err != nil || sb == nil || sb.every != config.DefaultBackupEvery || sb.opts.PushTimeout != backup.SupervisorPushTimeout {
		t.Fatalf("sessionBackup = %+v, %v", sb, err)
	}
	w := &workspaceCloseWatch{every: sb.every, round: sb.run}
	w.tick(time.Now())
	w.wait()
	if !strings.Contains(stderr.String(), "guildhall: backup: read branch laptop/alpha") {
		t.Fatalf("stderr = %q; want the failed run", stderr.String())
	}
	needs := backupNeeds(filepath.Join(f.dir, "state"))
	if len(needs) != 1 || needs[0].Kind != backupKind || !strings.Contains(needs[0].Reason, "failed") || needs[0].Command != "guildhall backup status" {
		t.Fatalf("needs_me = %+v", needs)
	}
	status, err := readDirectorStatus(t.Context(), f.storePath, filepath.Join(f.dir, "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range status.NeedsMe {
		found = found || it.Kind == backupKind
	}
	if !found {
		t.Fatalf("director status needs_me has no backup item: %+v", status.NeedsMe)
	}

	sb.opts.Config.Repo = f.remote
	stderr.Reset()
	sb.run()
	if !strings.Contains(stderr.String(), "guildhall: backup: committed and pushed ") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if needs := backupNeeds(filepath.Join(f.dir, "state")); len(needs) != 0 {
		t.Fatalf("needs_me after a good backup = %+v", needs)
	}
}

func TestStoreExportImportCommands(t *testing.T) {
	f := newBackupFixture(t)
	out := filepath.Join(t.TempDir(), "export")
	code, stdout, stderr := runIn(t, "", "store", "export", "--copy", f.dir, "--out", out)
	if code != 0 || !strings.Contains(stdout, `"receipt_sequence":`) || !strings.Contains(stdout, `"repositories/app.txt"`) {
		t.Fatalf("store export: exit %d, %q, stderr %s", code, stdout, stderr)
	}
	restored := filepath.Join(t.TempDir(), "restored.store")
	if code, stdout, stderr := runIn(t, "", "store", "import", restored, "--from", out); code != 0 || !strings.Contains(stdout, `"schema_version":1`) {
		t.Fatalf("store import: exit %d, %q, stderr %s", code, stdout, stderr)
	}
	if code, _, stderr := runIn(t, "", "store", "import", restored, "--from", out); code != 1 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("store import over a store: exit %d, stderr %s", code, stderr)
	}
	if code, _, stderr := runIn(t, "", "store", "doctor", restored); code != 0 {
		t.Fatalf("doctor of the import: %s", stderr)
	}
	for _, args := range [][]string{{"store", "export", f.storePath}, {"store", "import", restored}} {
		if code, _, _ := runIn(t, "", args...); code != 2 {
			t.Errorf("%q without its directory: exit %d, want 2", args, code)
		}
	}
	if code, out, _ := runIn(t, "", "help"); code != 0 || !strings.Contains(out, "backup now|status") || !strings.Contains(out, "store export <path> --out <dir>") || !strings.Contains(out, "backup restore --copy <dir>") {
		t.Fatalf("help does not list backup and store export:\n%s", out)
	}
}
