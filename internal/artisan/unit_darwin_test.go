package artisan_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// The exited leader remains unreaped until Close. Darwin can report EPERM
// when signalling this zombie-only group, which must be a successful no-op.
func TestSignalsAfterWorkerExit(t *testing.T) {
	u, err := artisan.NewUnit("exited-worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	cmd := exec.Command("/bin/sh", "-c", "read value")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if _, err := u.Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not exit")
	}
	for _, action := range []struct {
		name string
		run  func() error
	}{
		{"Interrupt", u.Interrupt},
		{"Terminate", u.Terminate},
		{"Kill", u.Kill},
		{"Close", u.Close},
		{"Close again", u.Close},
	} {
		if err := action.run(); err != nil {
			t.Errorf("%s after worker exit = %v", action.name, err)
		}
	}
}

func platformHarness(_ *testing.T, h *artisantest.Harness) {
	h.Terminate = true
	h.SetsidEscapes = true
	h.Kind = artisan.KindGroup
}
