package steward

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rollout writer lock is .rollout.lock in the private config directory, on real files.
// Apply creates it exclusively before any transport, refuses while it exists without touching it,
// and removes it when Apply returns. A lock left by a writer that stopped stays until an operator removes it.

func lockPath(r *Rollout) string { return filepath.Join(r.privateDir, ".rollout.lock") }

func lockExists(t *testing.T, r *Rollout) bool {
	t.Helper()
	_, err := os.Stat(lockPath(r))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// checkLockRefusal checks that Apply was refused by a held lock, before any transport and without removing the lock.
func checkLockRefusal(t *testing.T, s *stub, r *Rollout, err error) {
	t.Helper()
	if !errors.Is(err, fs.ErrExist) || !strings.Contains(err.Error(), lockPath(r)) || !strings.Contains(err.Error(), "confirm the writer has stopped") {
		t.Errorf("Apply error = %v, want the held lock %s and the recovery advice", err, lockPath(r))
	}
	if calls := s.calls(); len(calls) != 0 {
		t.Errorf("refused Apply ran scp %q", calls)
	}
	if !lockExists(t, r) {
		t.Error("refused Apply removed the held lock")
	}
}

// gateCopier holds every fetch until release is closed, telling entered about the first.
type gateCopier struct {
	Copier
	entered chan<- struct{}
	release <-chan struct{}
}

func (c gateCopier) Fetch(ctx context.Context, src Remote, dst string) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	return c.Copier.Fetch(ctx, src, dst)
}

func TestApplyWriterLockExcludesASecondWriter(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation")
	entered, release := make(chan struct{}, 1), make(chan struct{})
	transport := r.Transport
	r.Transport = gateCopier{transport, entered, release}
	done := make(chan error, 1)
	go func() {
		_, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first Apply returned before transport: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("first Apply did not reach transport")
	}
	if !lockExists(t, r) {
		t.Fatal("running Apply holds no lock")
	}

	second, err := LoadRollout(r.privateDir, transport)
	if err != nil {
		t.Fatal(err)
	}
	s.calls()
	_, err = second.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	checkLockRefusal(t, s, second, err)

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if lockExists(t, r) {
		t.Error("lock left after the first Apply returned")
	}
}

func TestApplyReleasesWriterLock(t *testing.T) {
	for _, c := range []struct {
		name string
		// setup prepares a failure, if any, and returns the approval to pass.
		setup func(t *testing.T, s *stub, p *Plan) string
		want  error
	}{
		{"applied", func(_ *testing.T, _ *stub, p *Plan) string { return p.Digest }, nil},
		{"stale plan", func(_ *testing.T, s *stub, p *Plan) string {
			s.put("guild-ws", toolsPath, s.get("guild-ws", toolsPath)+"\n# changed\n")
			return p.Digest
		}, ErrPlanStale},
		{"approval required", func(*testing.T, *stub, *Plan) string { return "" }, ErrApprovalRequired},
		{"copy failed", func(t *testing.T, _ *stub, p *Plan) string {
			t.Setenv(stubFailEnv, "managed.toml guild-ws:")
			return p.Digest
		}, ErrIncomplete},
		{"unreachable machine", func(t *testing.T, _ *stub, p *Plan) string {
			t.Setenv(stubFailEnv, "guild-ws:")
			return p.Digest
		}, ErrPlanStale},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, r := fleet(t)
			p := dryRun(t, r, "workstation")
			approve := c.setup(t, s, p)
			if _, err := r.Apply(context.Background(), p.StageDir, approve, p.Machines...); !errors.Is(err, c.want) {
				t.Fatalf("Apply error = %v, want %v", err, c.want)
			}
			if lockExists(t, r) {
				t.Error("lock left after Apply returned")
			}
		})
	}
}

func TestApplyRefusedBeforeLockingLeavesNoLock(t *testing.T) {
	_, r := fleet(t)
	p := dryRun(t, r, "workstation")
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, "laptop"); err == nil {
		t.Fatal("Apply accepted a different machine set")
	}
	if _, err := r.Apply(context.Background(), filepath.Join(t.TempDir(), "no-plan"), p.Digest, p.Machines...); err == nil {
		t.Fatal("Apply accepted a missing plan")
	}
	if lockExists(t, r) {
		t.Error("refused Apply left a lock")
	}
}

// Environment for the lock owner process, the test binary running Apply until it is killed.
const (
	ownerPrivateEnv = "GUILDHALL_TEST_LOCK_OWNER_PRIVATE"
	ownerPlanEnv    = "GUILDHALL_TEST_LOCK_OWNER_PLAN"
)

// sleepCopier never finishes a copy, so Apply holds its lock until the process is killed.
type sleepCopier struct{}

func (sleepCopier) Fetch(context.Context, Remote, string) error { time.Sleep(time.Hour); return nil }
func (sleepCopier) Put(context.Context, string, Remote) error   { time.Sleep(time.Hour); return nil }

// lockOwner is the owner process's main: Apply the plan for workstation, blocking in transport.
func lockOwner(private, plan string) int {
	r, err := LoadRollout(private, sleepCopier{})
	if err == nil {
		_, err = r.Apply(context.Background(), plan, "", "workstation")
	}
	_, _ = os.Stderr.WriteString("lock owner: " + err.Error() + "\n")
	return 1
}

func TestStaleWriterLockNeedsManualRecoveryAfterOwnerStops(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(exe, "-test.run=^$")
	owner.Env = append(os.Environ(), ownerPrivateEnv+"="+r.privateDir, ownerPlanEnv+"="+p.StageDir)
	var stderr strings.Builder
	owner.Stderr = &stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- owner.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for !lockExists(t, r) {
		select {
		case err := <-exited:
			t.Fatalf("owner exited before locking: %v: %s", err, stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			_ = owner.Process.Kill()
			t.Fatal("owner did not take the lock within 30 seconds")
		}
	}

	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-exited //nolint:errcheck // Killing the owner is expected to return an exit error.
	if !lockExists(t, r) {
		t.Fatal("killed owner's lock disappeared")
	}
	s.calls()
	_, err = r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	checkLockRefusal(t, s, r, err)

	// The operator has confirmed the owner stopped, so removes its lock by hand.
	if err := os.Remove(lockPath(r)); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if err != nil {
		t.Fatalf("Apply after recovery: %v", err)
	}
	for _, c := range receipt.Copies {
		if c.Status != StatusApplied {
			t.Errorf("receipt after recovery: %+v", c)
		}
	}
	if lockExists(t, r) {
		t.Error("lock left after recovery Apply")
	}
}
