package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/git"
)

// privateCheckout makes a remote and a clone of it whose main branch tracks it, with roster.toml and managed.toml
// committed and pushed, as the Steward's private config checkout is.
func privateCheckout(t *testing.T, global string) (checkout, remote repo) {
	t.Helper()
	remote = repo{t, t.TempDir(), global}
	remote.run("init", "-q", "--bare", "-b", "main")
	checkout = repo{t, t.TempDir(), global}
	checkout.run("clone", "-q", remote.dir, ".")
	checkout.run("checkout", "-q", "-b", "main")
	checkout.commit("private config", map[string]string{"roster.toml": "roster\n", "managed.toml": "managed\n"})
	checkout.run("push", "-q", "-u", "origin", "main")
	return checkout, remote
}

var sig = git.Signature{Name: "Guildhall Steward", Email: "steward@guildhall.invalid", When: time.Unix(1_800_000_000, 0)}

// TestWorkTreeCommitFiles: a commit of named files keeps the Guildmaster's staged and unstaged edits out of the
// commit and in the checkout, commits nothing when the files are unchanged, and refuses bad paths.
func TestWorkTreeCommitFiles(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	co, _ := privateCheckout(t, global)
	w := git.WorkTree{Dir: co.dir}
	// The Guildmaster's uncommitted edits: one staged, one not.
	write(t, co.dir, "roster.toml", "roster\nstaged edit\n")
	co.run("add", "roster.toml")
	write(t, co.dir, "managed.toml", "managed\nunstaged edit\n")

	files := map[string][]byte{"lessons/alpha/0123-l-1.md": []byte("# One\n"), "incidents/alpha/0123-i-1.json": []byte("{}\n")}
	commit, err := w.CommitFiles(ctx, files, "Collect from alpha", sig)
	if err != nil {
		t.Fatalf("CommitFiles: %v", err)
	}
	if got := co.run("rev-parse", "HEAD"); got != commit {
		t.Fatalf("HEAD = %s, want %s", got, commit)
	}
	if got := co.run("show", "--name-only", "--format=", commit); got != "incidents/alpha/0123-i-1.json\nlessons/alpha/0123-l-1.md" {
		t.Errorf("commit changed %q, want only the collected files", got)
	}
	if staged, unstaged := co.run("diff", "--cached", "--name-only"), co.run("diff", "--name-only"); staged != "roster.toml" || unstaged != "managed.toml" {
		t.Errorf("staged %q and unstaged %q, want the Guildmaster's roster.toml and managed.toml edits kept", staged, unstaged)
	}
	if data, _ := os.ReadFile(filepath.Join(co.dir, "lessons/alpha/0123-l-1.md")); string(data) != "# One\n" {
		t.Errorf("work tree lesson = %q", data)
	}

	again, err := w.CommitFiles(ctx, files, "Collect from alpha", sig)
	if err != nil || again != commit {
		t.Errorf("CommitFiles of unchanged files = %s, %v; want HEAD %s", again, err, commit)
	}
	if data, ok, err := w.Show(ctx, "HEAD", "lessons/alpha/0123-l-1.md"); err != nil || !ok || string(data) != "# One\n" {
		t.Errorf("Show = %q, %v, %v", data, ok, err)
	}
	if _, ok, err := w.Show(ctx, "HEAD", "lessons/alpha/none.md"); err != nil || ok {
		t.Errorf("Show of a missing file = %v, %v; want false", ok, err)
	}
	for _, bad := range []string{"../escape", "/abs", "a//b", "-flag", ".git/config", "a/./b"} {
		if _, err := w.CommitFiles(ctx, map[string][]byte{bad: []byte("x")}, "bad", sig); err == nil {
			t.Errorf("CommitFiles(%q) succeeded, want refused", bad)
		}
	}
	// A collected file the Guildmaster changed locally is never overwritten.
	write(t, co.dir, "lessons/alpha/0123-l-1.md", "# Local edit\n")
	if _, err := w.CommitFiles(ctx, map[string][]byte{"lessons/alpha/0123-l-1.md": []byte("# Triage\n")}, "triage", sig); err == nil {
		t.Error("CommitFiles over a local edit succeeded, want refused")
	}
	if got := co.run("rev-parse", "HEAD"); got != commit {
		t.Errorf("HEAD moved to %s after a refused commit", got)
	}
}

// TestWorkTreeCommitChanges: a commit can remove files too, from HEAD and the work tree, keeping other local edits; a
// file HEAD lacks is no change, and a path both set and removed is refused.
func TestWorkTreeCommitChanges(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	co, _ := privateCheckout(t, global)
	w := git.WorkTree{Dir: co.dir}
	write(t, co.dir, "roster.toml", "roster\nunstaged edit\n")
	commit, err := w.CommitChanges(ctx, map[string][]byte{"retired/a.json": []byte("{}\n")}, []string{"managed.toml", "absent.toml"}, "Retire", sig)
	if err != nil {
		t.Fatal(err)
	}
	if got := co.run("show", "--name-status", "--format=", commit); got != "D\tmanaged.toml\nA\tretired/a.json" {
		t.Errorf("commit changed %q", got)
	}
	if _, err := os.Stat(filepath.Join(co.dir, "managed.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("managed.toml in the work tree: %v", err)
	}
	if got := co.run("diff", "--name-only"); got != "roster.toml" {
		t.Errorf("unstaged %q, want the roster.toml edit kept", got)
	}
	if again, err := w.CommitChanges(ctx, nil, []string{"managed.toml"}, "Retire", sig); err != nil || again != commit {
		t.Errorf("removing a removed file = %s, %v; want HEAD %s", again, err, commit)
	}
	if _, err := w.CommitChanges(ctx, map[string][]byte{"x": []byte("x")}, []string{"x"}, "both", sig); err == nil {
		t.Error("setting and removing one path succeeded")
	}
}

// TestWorkTreePublish: Publish pushes what the remote lacks, pushes nothing when it is up to date, and replays the
// local commits onto a remote that moved ahead, keeping local edits.
func TestWorkTreePublish(t *testing.T) {
	ctx := context.Background()
	global := isolate(t)
	co, remote := privateCheckout(t, global)
	w := git.WorkTree{Dir: co.dir}
	commit, err := w.CommitFiles(ctx, map[string][]byte{"lessons/a/o-l.md": []byte("one\n")}, "Collect one", sig)
	if err != nil {
		t.Fatal(err)
	}
	if pushed, err := w.Publish(ctx, sig); err != nil || pushed != commit {
		t.Fatalf("Publish = %s, %v; want %s", pushed, err, commit)
	}
	if got := remote.run("rev-parse", "main"); got != commit {
		t.Fatalf("remote main = %s, want %s", got, commit)
	}
	if pushed, err := w.Publish(ctx, sig); err != nil || pushed != commit {
		t.Errorf("Publish when up to date = %s, %v", pushed, err)
	}

	// Another clone moves the remote ahead.
	other := repo{t, t.TempDir(), global}
	other.run("clone", "-q", remote.dir, ".")
	moved := other.commit("Guildmaster edit", map[string]string{"managed.toml": "managed v2\n"})
	other.run("push", "-q", "origin", "main")

	write(t, co.dir, "roster.toml", "roster\nlocal edit\n")
	second, err := w.CommitFiles(ctx, map[string][]byte{"incidents/a/o-i.json": []byte("{}\n")}, "Collect two", sig)
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := w.Publish(ctx, sig)
	if err != nil {
		t.Fatalf("Publish after the remote moved: %v", err)
	}
	if pushed == second {
		t.Fatal("Publish pushed the commit unchanged; want it replayed onto the remote's tip")
	}
	if got := remote.run("rev-parse", "main"); got != pushed {
		t.Errorf("remote main = %s, want %s", got, pushed)
	}
	if got := remote.run("rev-parse", "main^"); got != moved {
		t.Errorf("replayed commit's parent = %s, want the remote's tip %s", got, moved)
	}
	if got := remote.run("log", "-1", "--format=%s", "main"); got != "Collect two" {
		t.Errorf("replayed message = %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(co.dir, "managed.toml")); string(data) != "managed v2\n" {
		t.Errorf("checkout managed.toml = %q, want the remote's change", data)
	}
	if data, _ := os.ReadFile(filepath.Join(co.dir, "roster.toml")); string(data) != "roster\nlocal edit\n" {
		t.Errorf("checkout roster.toml = %q, want the local edit kept", data)
	}
}

// TestWorkTreePublishNoUpstream: a branch tracking nothing is refused before any push.
func TestWorkTreePublishNoUpstream(t *testing.T) {
	global := isolate(t)
	r := repo{t, t.TempDir(), global}
	r.run("init", "-q", "-b", "main")
	r.commit("init", map[string]string{"roster.toml": "r\n"})
	_, err := git.WorkTree{Dir: r.dir}.Publish(context.Background(), sig)
	if err == nil || !strings.Contains(err.Error(), "tracks no remote branch") || errors.Is(err, git.ErrPushRefused) {
		t.Errorf("Publish = %v, want the missing upstream named", err)
	}
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
