package launch_test

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/ndrwl/guildhall/internal/launch"
)

const noWorkerEnv = "GUILDHALL_TEST_UNNAMED_BEFORE_START"

func TestStopProvesPreStartCancellationWithoutNamedContainment(t *testing.T) {
	a := newAttempt(t)
	if err := a.Files().Mark(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "artisan", "start", "--store", a.Store, "--state", a.StateDir,
		"--repo", a.Key.Repository, "--task", a.Key.TaskID, "--step", a.Key.Step, "--attempt", "1",
		"--adapter", "fake", "--prompt", "unused", "--workdir", t.TempDir())
	cmd.Env = append(os.Environ(), guildhallEnv+"=1", noWorkerEnv+"=1")
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 5 {
		t.Fatalf("start = %v, %s; want stopped", err, out)
	}
	if rep, err := launch.Stop(context.Background(), a); err != nil {
		t.Fatalf("stop after launcher exited = %+v, %v; want proven stopped", rep, err)
	}
}
