package fake

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
)

// TestMain lets the test binary act as `guildhall fake-artisan`, so the
// conformance suite runs the shipped fake runtime as a real child process.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Subcommand {
		os.Exit(Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// prompts make the fake produce each conformance behaviour.
var prompts = map[adaptertest.Behaviour]string{
	adaptertest.Succeed:              "Do the task.\n",
	adaptertest.Fail:                 "guildhall-fake: outcome=failed\n",
	adaptertest.NoResult:             "guildhall-fake: fault=no-result\n",
	adaptertest.Malformed:            "guildhall-fake: fault=malformed\n",
	adaptertest.WrongIdentity:        "guildhall-fake: fault=wrong-identity\n",
	adaptertest.ExitErrorAfterResult: "guildhall-fake: fault=exit-error\n",
	adaptertest.Hang:                 "guildhall-fake: delay=10m\n",
}

func TestFakeRuntimeConformance(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      Adapter{Executable: exe},
			Capabilities: adapter.Capabilities{},
			Prompt:       func(b adaptertest.Behaviour) string { return prompts[b] },
		}
	})
}

// TestFakeRuntimeConformanceOnInterpreterAttempts runs the suite on attempts
// rendered from internal/interp/testdata and mapped through
// interp.Spec.AdapterSpec, the reference mapping onto a runtime's spec.
func TestFakeRuntimeConformanceOnInterpreterAttempts(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      Adapter{Executable: exe},
			Capabilities: adapter.Capabilities{},
			Prompt:       func(b adaptertest.Behaviour) string { return prompts[b] },
			Attempt: func(t *testing.T, prompt string) adapter.Spec {
				return interptest.AdapterSpec(t, interp.FakeAdapter, prompt)
			},
		}
	})
}

// TestFakeRuntimeConformanceThroughTheLauncher runs the suite with the fake
// started inside the Artisan launcher on this platform's unit, deciding by the
// real attempt files, so a stop is the launcher's ladder from the cancel
// marker rather than a kill.
func TestFakeRuntimeConformanceThroughTheLauncher(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      Adapter{Executable: exe},
			Capabilities: adapter.Capabilities{},
			Prompt:       func(b adaptertest.Behaviour) string { return prompts[b] },
			Start:        launcherStart,
		}
	})
}

// launched is a runtime the launcher runs; it has exited once Run returns.
type launched struct {
	l    *artisan.Launcher
	dir  string
	done chan struct{}
}

func (p *launched) Exited() <-chan struct{} { return p.done }

// Stop runs the launcher's stop ladder; a runtime the cancel marker did not
// stop, so that a signal rung was needed, has not honoured the marker.
func (p *launched) Stop() error {
	if _, err := p.l.Stop(); err != nil {
		return err
	}
	out, err := artisan.ReadOutcome(p.dir)
	if err == nil && out.State == artisan.Cancelled && out.Rung > 1 {
		return fmt.Errorf("the runtime did not honour the cancel marker: it stopped at rung %d", out.Rung)
	}
	return err
}

func launcherStart(t *testing.T, spec adapter.Spec, cmd adapter.Command) (adaptertest.Process, error) {
	u, err := artisan.NewUnit("conformance-" + rand.Text()[:8])
	if err != nil {
		return nil, err
	}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{
		Dir:   filepath.Dir(spec.ResultFile),
		Unit:  u,
		Files: launch.Files{ResultFile: spec.ResultFile, Identity: spec.Identity},
		Key:   artisan.AttemptKey{Repository: "app", TaskID: spec.TaskID, Step: "build", Attempt: 1},
	})
	if err != nil {
		return nil, errors.Join(err, u.Close())
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	p := &launched{l: l, dir: filepath.Dir(spec.ResultFile), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		if _, err := l.Run(c, nil); err != nil && !errors.Is(err, artisan.ErrDecided) {
			t.Errorf("launcher Run = %v", err)
		}
		if err := u.Close(); err != nil {
			t.Errorf("unit Close = %v", err)
		}
	}()
	return p, nil
}
