//go:build unix

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
)

// hangingMerge is a forge whose merge answers only when its context ends, as a hung gh does.
type hangingMerge struct {
	interp.Forge
	merging chan struct{}
}

func (h hangingMerge) Merge(ctx context.Context, _ forge.Merge) (forge.PullRequest, error) {
	close(h.merging)
	<-ctx.Done()
	return forge.PullRequest{}, ctx.Err()
}

// TestInterruptedAdvanceReleasesTheTask: an interrupt ends an advance held up in the forge, the task lock is
// free once it returns, and the next advance replays the merge.
func TestInterruptedAdvanceReleasesTheTask(t *testing.T) {
	d := newRunDirectorWith(t, "publish.toml")
	const remote, head = "git@github.com:example/app.git", "0123456789abcdef0123456789abcdef01234567"
	f := forgetest.NewFake()
	f.SetBranch(remote, "main", strings.Repeat("a", 40))
	f.AddCommit(appRepo, head)
	d.forge = f
	step := func(args ...string) string {
		t.Helper()
		code, out, stderr := d.run(t, args...)
		if code != 0 {
			t.Fatalf("guildhall %q: exit %d, %s", args, code, stderr)
		}
		return strings.TrimSpace(out)
	}
	done := func(name string) {
		d.waitResult(t, name, 1)
		step(append([]string{"step", "done"}, d.flags("--task", "t-1", "--step", name, "--attempt", "1", "--result", d.result(name, 1))...)...)
	}
	answer := func(decision, words string) {
		t.Helper()
		file := filepath.Join(t.TempDir(), "answer")
		if err := os.WriteFile(file, []byte(words), 0o600); err != nil {
			t.Fatal(err)
		}
		step("decide", "answer", "--store", d.store, "--repo", "app", "--task", "t-1", "--decision", decision, "--answer-file", file)
	}
	step(append([]string{"task", "start"}, d.flags("t-1")...)...)
	done("plan")
	answer("accept-1", "approved\n")
	step(append([]string{"task", "start"}, d.flags("--decision", "accept-1", "t-1")...)...)
	done("build")
	answer("sign-off-1", "approved\n")
	step(append([]string{"task", "start"}, d.flags("--decision", "sign-off-1", "t-1")...)...)
	answer("deliver-1", "merge\n")

	merging := make(chan struct{})
	d.forge = hangingMerge{Forge: f, merging: merging}
	go func() {
		select {
		case <-merging:
			_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		case <-time.After(time.Minute):
		}
	}()
	if code, out, _ := d.run(t, append([]string{"task", "start"}, d.flags("--decision", "deliver-1", "t-1")...)...); code == 0 {
		t.Fatalf("interrupted task start: exit 0, %s", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := artisan.LockAttempt(ctx, filepath.Join(d.state, "app", "t-1"))
	if err != nil {
		t.Fatalf("task lock after the interrupt: %v", err)
	}
	release()

	d.forge = f
	if got := step(append([]string{"task", "start"}, d.flags("--decision", "deliver-1", "t-1")...)...); got != `{"wait":"done","step":"deliver"}` {
		t.Fatalf("replayed advance: %s", got)
	}
	if prs := f.PullRequests(remote); len(prs) != 1 || prs[0].State != forge.Merged {
		t.Errorf("pull requests = %+v, want one merged", prs)
	}
}
