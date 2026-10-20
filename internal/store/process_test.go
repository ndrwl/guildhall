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
