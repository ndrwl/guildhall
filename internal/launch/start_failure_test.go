package launch_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestRunPreservesStartFailureAndRecordsOutcome(t *testing.T) {
	a := newAttempt(t)
	cmd, inv := worker(t, a, artisantest.Worker{})
	cmd.Dir = filepath.Join(t.TempDir(), "missing-workdir")
	probe := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	probe.Dir, probe.Env = cmd.Dir, cmd.Env
	var want *os.PathError
	if err := probe.Run(); !errors.As(err, &want) {
		t.Fatalf("direct start error = %v; want native path error", err)
	}
	done, cancel := start(a, cmd)
	defer cancel()
	r := wait(t, done)
	if !errors.Is(r.err, want.Err) {
		t.Fatalf("Run error = %v; want native missing working directory error %v", r.err, want.Err)
	}
	if r.r.Outcome.State != artisan.Failed || r.r.AttemptOutcome != store.AttemptFailed || storedOutcome(t, a) != store.AttemptFailed {
		t.Fatalf("Run report = %+v; want recorded failed outcome", r.r)
	}
	data, err := os.ReadFile(filepath.Join(a.Dir(), artisan.OutcomeFile))
	if err != nil {
		t.Fatal(err)
	}
	var outcome artisan.Outcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Schema != artisan.OutcomeSchema || outcome.State != artisan.Failed || outcome.Detail != r.r.Outcome.Detail {
		t.Fatalf("persisted outcome = %+v; report = %+v", outcome, r.r.Outcome)
	}
	if o := ownership(t, a); o.State != store.OwnerReleased {
		t.Fatalf("ownership = %+v; want released", o)
	}
	if entries, err := artisantest.Inventory(inv); err != nil || len(entries) != 0 {
		t.Fatalf("worker inventory = %v, %v; want no worker", entries, err)
	}
}
