package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestReviewAfterARebaseReviewsTheResolution: over a real repository and remote, an Artisan review that sends the
// build back, and a second build whose head rebases the first onto the moved default branch, make the second
// review a rebase round: its binding records scope rebase over the new range and the resolution document is
// recorded, with Base and Rebases from git.Program over the repository's configured path.
func TestReviewAfterARebaseReviewsTheResolution(t *testing.T) {
	// Every git here, the test's and guildhall's alike, runs without the user's or system's configuration, so the
	// worktree guildhall checks out (CRLF under Windows' system core.autocrlf) is clean to the test's git.
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	d, dir := newRunCopyOn(t, "fake-review")
	cfg, err := config.Load(d.config)
	if err != nil {
		t.Fatal(err)
	}
	app, err := cfg.ListedRepository("app")
	if err != nil {
		t.Fatal(err)
	}
	gitIn := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(dir, name, data string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(dir, "add", name)
		gitIn(dir, "commit", "-q", "-m", name)
		return gitIn(dir, "rev-parse", "HEAD")
	}
	// guide makes the copy's stage guide for kind play the fake Artisan writing one output.
	guide := func(kind, output string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "baseline", "stages", kind+".md"), []byte("# "+kind+"\n\nguildhall-fake: output="+output+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// step runs guildhall and wants exit 0 and, unless want is empty, want printed.
	step := func(want string, args ...string) {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 || want != "" && strings.TrimSpace(out) != want {
			t.Fatalf("guildhall %q: exit %d, %q, stderr %s; want %s", args, code, out, stderr, want)
		}
	}
	done := func(name string, n int, want string) {
		t.Helper()
		d.waitResult(t, name, n)
		step(want, append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", name, "--attempt", fmt.Sprint(n), "--result", d.result(name, n))...)...)
	}
	documents := func() map[string]store.DocumentRef {
		t.Helper()
		s, err := store.Open(d.store, store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		refs, err := s.Documents("app", "t-1")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]store.DocumentRef{}
		for _, r := range refs {
			out[r.Name] = r
		}
		return out
	}
	binding := func(name string) (b struct {
		Scope git.Scope  `json:"scope"`
		Range *git.Range `json:"range"`
	}) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(d.state, "documents", filepath.FromSlash(documents()[name].Path)))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return b
	}

	b1 := gitIn(app.Remote, "rev-parse", "trunk")
	step(`{"wait":"attempt","step":"plan","attempt":1}`, append([]string{"task", "start"}, d.flags("t-1")...)...)
	done("plan", 1, `{"wait":"decision","step":"accept","decision":"accept-1"}`)
	// The plan attempt made the worktree; the first build commits the task's change in it.
	work := filepath.Join(d.state, "app", "t-1", "work")
	h1 := commit(work, "task.txt", "task\n")
	guide("build", "head:"+h1)
	answer := filepath.Join(t.TempDir(), "answer")
	if err := os.WriteFile(answer, []byte("approved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	step("", "decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", "accept-1", "--answer-file", answer)
	step(`{"wait":"attempt","step":"build","attempt":1}`, append([]string{"task", "start"}, d.flags("--decision", "accept-1", "t-1")...)...)
	spec := "sha256:" + documents()["plan-1-spec"].SHA256
	guide("review", fmt.Sprintf(`outcome.json:{"schema":"guildhall.review-outcome/1","reviewer":"automated","verdict":"changes_requested","findings":[{"id":"f1","rule":"S2","summary":"Another round."}],"revisions":{"subject":{"code_head":%q},"spec_digest":%q,"standards_revision":"standards-1"}}`, h1, spec))
	done("build", 1, `{"wait":"attempt","step":"review","attempt":1}`)
	if b := binding("review-1-inputs"); b.Scope != git.ScopeFull || b.Range == nil || *b.Range != (git.Range{Base: b1, Head: h1}) {
		t.Errorf("review-1 binding = %+v, want full over %s..%s", b, b1, h1)
	}

	// The default branch moves and the second build rebases the task onto it.
	upstream := t.TempDir()
	gitIn(upstream, "clone", "-q", app.Remote, ".")
	b2 := commit(upstream, "upstream.txt", "upstream\n")
	gitIn(upstream, "push", "-q", "origin", "HEAD:trunk")
	gitIn(work, "fetch", "-q", app.Remote, "trunk")
	gitIn(work, "rebase", "-q", "FETCH_HEAD")
	h2 := gitIn(work, "rev-parse", "HEAD")
	guide("build", "head:"+h2)
	done("review", 1, `{"wait":"attempt","step":"build","attempt":2}`)
	done("build", 2, `{"wait":"attempt","step":"review","attempt":2}`)
	if b := binding("review-2-inputs"); b.Scope != git.ScopeRebase || b.Range == nil || *b.Range != (git.Range{Base: b2, Head: h2}) {
		t.Errorf("review-2 binding = %+v, want rebase over %s..%s", b, b2, h2)
	}
	if _, ok := documents()["review-2-resolution"]; !ok {
		t.Errorf("no review-2-resolution document")
	}
}
