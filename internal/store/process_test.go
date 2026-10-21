package store

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

const helperEnv = "GUILDHALL_STORE_TEST_HOLDER"

// TestHelperHoldStore is not a test: the process tests run it as a separate
// process that opens the store named by helperEnv, writes one task, reports
// "held" and keeps the store open until stdin closes or it is killed.
func TestHelperHoldStore(t *testing.T) {
	path := os.Getenv(helperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	s, err := Open(path, testOptions())
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	if _, err := s.CreateTask("holder-1", sampleTask("app", "held")); err != nil {
		fmt.Println("create:", err)
		os.Exit(1)
	}
	fmt.Println("held")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := s.Close(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func startHolder(t *testing.T, path string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldStore$")
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("holder did not take the store: %q, %v", line, err)
	}
	return cmd, stdin
}

func TestWriterInAnotherProcessIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guildhall.store")
	cmd, stdin := startHolder(t, path)

	if _, err := Open(path, testOptions()); !errors.Is(err, ErrLocked) {
		t.Fatalf("Open while another process writes: err = %v, want ErrLocked", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("holder: %v", err)
	}
	s := openTest(t, path)
	if _, err := s.Task("app", "held"); err != nil {
		t.Fatalf("holder's committed task: %v", err)
	}
}

func TestKilledWriterReleasesLockAndKeepsCommittedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guildhall.store")
	cmd, _ := startHolder(t, path)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	s := openTest(t, path)
	if _, err := s.Task("app", "held"); err != nil {
		t.Fatalf("committed task after the writer was killed: %v", err)
	}
	r, err := s.CreateTask("holder-1", sampleTask("app", "held"))
	if err != nil || r.Sequence != 1 {
		t.Fatalf("replaying the killed writer's mutation = %+v, %v; want its original receipt", r, err)
	}
}

const ownerEnv = "GUILDHALL_STORE_TEST_OWNER"

// TestHelperOwnAttempt is not a test: TestOwnerInAnotherProcessIsRefused runs
// it as a separate process that starts an attempt owned by its own identity,
// closes the store, reports "owned" and stays alive until killed.
func TestHelperOwnAttempt(t *testing.T) {
	path := os.Getenv(ownerEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	err := func() error {
		self, err := artisan.Self()
		if err != nil {
			return err
		}
		s, err := Open(path, testOptions())
		if err != nil {
			return err
		}
		if _, err := s.StartOwnedAttempt("owner-start", sampleOwnership(self)); err != nil {
			_ = s.Close()
			return err
		}
		return s.Close()
	}()
	if err != nil {
		fmt.Println("own:", err)
		os.Exit(1)
	}
	fmt.Println("owned")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestTakeOwnershipPreservesContainmentAfterLauncherExit(t *testing.T) {
	for _, kind := range []string{ContainmentGroup, ContainmentSubreaper, ContainmentCgroup, ContainmentJob} {
		t.Run(kind, func(t *testing.T) {
			launcher, _ := startHolder(t, filepath.Join(t.TempDir(), "launcher.store"))
			launcherID, err := artisan.Lookup(launcher.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			worker, _ := startHolder(t, filepath.Join(t.TempDir(), "worker.store"))
			workerID, err := artisan.Lookup(worker.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			s, path := newStore(t)
			mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
			if _, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherID)); err != nil {
				t.Fatal(err)
			}
			owner := mustOwnership(t, s, 1)
			owner.State = OwnerRunning
			owner.Containment = Containment{Kind: kind, Worker: workerID, Reason: "recorded detection result"}
			if kind == ContainmentCgroup || kind == ContainmentJob {
				owner.Containment.Name = "recorded-unit"
			}
			if _, err := s.UpdateOwnership("m-running", owner); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = openTest(t, path)
			if err := launcher.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = launcher.Wait()
			if lv, err := artisan.Probe(launcherID); err != nil || lv != artisan.Gone {
				t.Fatalf("launcher = %v, %v; want Gone", lv, err)
			}
			for i, probe := range []OwnerProbe{nil, ProbeLauncher} {
				mutation := fmt.Sprintf("m-take-%d", i)
				if _, err := s.TakeOwnership(mutation, takeB(1), probe); !errors.Is(err, ErrOwnerUnknown) {
					t.Errorf("take with recorded %s: %v; want ErrOwnerUnknown", kind, err)
				}
				if got := mustOwnership(t, s, 1); got != owner {
					t.Errorf("ownership evidence changed: %+v; want %+v", got, owner)
				}
				if _, err := s.Receipt(mutation); !errors.Is(err, ErrNotFound) {
					t.Errorf("refused takeover left receipt: %v", err)
				}
			}
			if lv, err := artisan.Probe(workerID); err != nil || lv != artisan.Alive {
				t.Fatalf("surviving worker = %v, %v; want Alive", lv, err)
			}
			if err := worker.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = worker.Wait()
			if _, err := s.TakeOwnership("m-take-0", takeB(1), func(o Ownership) (artisan.Liveness, error) {
				if o != owner {
					t.Errorf("recovery probe lost evidence: %+v", o)
				}
				return artisan.Probe(o.Containment.Worker)
			}); err != nil {
				t.Fatalf("explicit recovery after worker exit: %v", err)
			}
		})
	}
}

// TestProgressUpdateCannotEraseRecoveryEvidence pins that a progress update
// omitting recorded containment is refused, so the default probe still sees the
// containment and refuses a take-over while the recorded worker survives.
func TestProgressUpdateCannotEraseRecoveryEvidence(t *testing.T) {
	launcher, _ := startHolder(t, filepath.Join(t.TempDir(), "launcher.store"))
	launcherID, err := artisan.Lookup(launcher.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	worker, _ := startHolder(t, filepath.Join(t.TempDir(), "worker.store"))
	workerID, err := artisan.Lookup(worker.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := newStore(t)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if _, err := s.StartOwnedAttempt("m-start", sampleOwnership(launcherID)); err != nil {
		t.Fatal(err)
	}
	owner := mustOwnership(t, s, 1)
	owner.State = OwnerRunning
	owner.Containment = Containment{Kind: ContainmentGroup, Worker: workerID}
	if _, err := s.UpdateOwnership("m-contained", owner); err != nil {
		t.Fatal(err)
	}
	// A stale or partial full-record value: right owner and state, no containment.
	progress := owner
	progress.Endpoint = "/state/endpoints/app.t-1.build.1.sock"
	progress.Containment = Containment{}
	if _, err := s.UpdateOwnership("m-endpoint", progress); !errors.Is(err, ErrInvalid) {
		t.Fatalf("progress update clearing containment: %v; want ErrInvalid", err)
	}
	if got := mustOwnership(t, s, 1); got != owner {
		t.Fatalf("refused update changed the record: %+v; want %+v", got, owner)
	}
	if err := launcher.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = launcher.Wait()
	if lv, err := artisan.Probe(launcherID); err != nil || lv != artisan.Gone {
		t.Fatalf("launcher = %v, %v; want Gone", lv, err)
	}
	if lv, err := artisan.Probe(workerID); err != nil || lv != artisan.Alive {
		t.Fatalf("worker = %v, %v; want Alive", lv, err)
	}
	for i, probe := range []OwnerProbe{nil, ProbeLauncher} {
		if _, err := s.TakeOwnership(fmt.Sprintf("m-take-%d", i), takeB(1), probe); !errors.Is(err, ErrOwnerUnknown) {
			t.Errorf("take with a live recorded worker: %v; want ErrOwnerUnknown", err)
		}
		if got := mustOwnership(t, s, 1); got.Containment != owner.Containment {
			t.Errorf("containment = %+v; want %+v", got.Containment, owner.Containment)
		}
	}
}

func TestOwnerInAnotherProcessIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guildhall.store")
	s := openTest(t, path)
	mustCreate(t, s, "m-task", sampleTask("app", "t-1"))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperOwnAttempt$")
	cmd.Env = append(os.Environ(), ownerEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "owned\n" {
		t.Fatalf("owner did not take the attempt: %q, %v", line, err)
	}

	s = openTest(t, path)
	owner := mustOwnership(t, s, 1)
	if owner.LauncherIdentity.PID != cmd.Process.Pid {
		t.Fatalf("recorded owner = %+v, want the helper's pid %d", owner.LauncherIdentity, cmd.Process.Pid)
	}
	if _, err := s.TakeOwnership("m-take", takeB(1), nil); !errors.Is(err, ErrOwnerLive) {
		t.Fatalf("take while the owning process lives: err = %v, want ErrOwnerLive", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := s.TakeOwnership("m-take", takeB(1), nil); err != nil {
		t.Fatalf("take after the owning process was killed and reaped: %v", err)
	}
}
