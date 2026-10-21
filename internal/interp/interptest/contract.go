package interptest

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
)

// Outcome is an attempt outcome the contract suite asks a launcher to produce.
type Outcome string

// The outcomes every launcher under test must produce on request.
const (
	// Succeeds writes a valid succeeded result.
	Succeeds Outcome = "succeeds"
	// Fails writes a valid failed result.
	Fails Outcome = "fails"
	// Hangs keeps working without a result until stopped.
	Hangs Outcome = "hangs"
)

// Harness is one launcher under the contract suite.
type Harness struct {
	Launcher interp.Launcher
	// Adapter is the adapter name the launcher runs.
	Adapter string
	// Prepare renders the attempt that will produce o by calling render with the step instructions
	// the launcher needs (a scripted launcher passes none), arranges for that outcome,
	// and returns the rendered spec.
	Prepare func(t *testing.T, o Outcome, render func(instructions string) interp.Spec) interp.Spec
}

// RunLauncherContract runs the interp.Launcher contract against harnesses from newHarness.
// Each case creates independent launchers with newHarness.
func RunLauncherContract(t *testing.T, newHarness func(t *testing.T) Harness) {
	attempt := func(t *testing.T, h Harness, instructions string) interp.Spec {
		a := Build(t, "spec-v1.md")
		if instructions != "" {
			raw, err := json.Marshal(instructions)
			if err != nil {
				t.Fatal(err)
			}
			a.Step.Settings["instructions"] = raw
		}
		a.Adapter = h.Adapter
		return a.Render(t)
	}
	prepare := func(t *testing.T, h Harness, o Outcome) interp.Spec {
		return h.Prepare(t, o, func(instructions string) interp.Spec { return attempt(t, h, instructions) })
	}

	t.Run("refuses an invalid spec and starts nothing", func(t *testing.T) {
		h := newHarness(t)
		good := attempt(t, h, "")
		bad := map[string]func(s *interp.Spec){
			"no identity":         func(s *interp.Spec) { s.Identity = adapter.Identity{} },
			"no adapter":          func(s *interp.Spec) { s.Adapter = "" },
			"relative result":     func(s *interp.Spec) { s.ResultFile = "result.json" },
			"result is prompt":    func(s *interp.Spec) { s.ResultFile = s.PromptFile },
			"not a manifest":      func(s *interp.Spec) { s.Manifest.Schema = "" },
			"relative work dir":   func(s *interp.Spec) { s.WorkDir = "work" },
			"relative prompt":     func(s *interp.Spec) { s.PromptFile = "prompt.md" },
			"ended context first": nil,
		}
		for name, breakSpec := range bad {
			spec, ctx := good, context.Background()
			if breakSpec != nil {
				breakSpec(&spec)
			} else {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if _, err := h.Launcher.Launch(ctx, spec); err == nil {
				t.Errorf("%s: Launch succeeded", name)
			}
		}
		if _, err := os.Stat(good.ResultFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a refused launch left a result: %v", err)
		}
	})

	for _, tc := range []struct {
		o    Outcome
		want adapter.Status
	}{{Succeeds, adapter.StatusSucceeded}, {Fails, adapter.StatusFailed}} {
		t.Run("completes from the result file: "+string(tc.o), func(t *testing.T) {
			h := newHarness(t)
			spec := prepare(t, h, tc.o)
			hd, err := h.Launcher.Launch(context.Background(), spec)
			if err != nil {
				t.Fatalf("Launch = %v", err)
			}
			if hd.Identity != spec.Identity {
				t.Errorf("handle identity = %+v, want %+v", hd.Identity, spec.Identity)
			}
			r, err := wait(spec, time.Minute)
			if err != nil {
				t.Fatalf("WaitResult = %v", err)
			}
			if r.Identity != spec.Identity || r.Status != tc.want {
				t.Errorf("result = %+v, want status %s for %+v", r, tc.want, spec.Identity)
			}
			// A readable result does not mean the launcher has finished writing
			// its outcome and heartbeat files. Stop joins it before checking cleanup.
			if err := h.Launcher.Stop(hd); err != nil {
				t.Fatalf("Stop after finishing = %v, want nil", err)
			}
			entries, err := os.ReadDir(filepath.Dir(spec.ResultFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".tmp") {
					t.Errorf("temporary file %s left beside the result", e.Name())
				}
			}
		})
	}

	t.Run("a stopped attempt leaves no result", func(t *testing.T) {
		h := newHarness(t)
		spec := prepare(t, h, Hangs)
		hd, err := h.Launcher.Launch(context.Background(), spec)
		if err != nil {
			t.Fatalf("Launch = %v", err)
		}
		if _, err := wait(spec, 300*time.Millisecond); !errors.Is(err, adapter.ErrNoResultYet) {
			t.Fatalf("WaitResult while working = %v, want ErrNoResultYet", err)
		}
		for i := range 2 {
			if err := h.Launcher.Stop(hd); err != nil {
				t.Fatalf("Stop %d = %v", i+1, err)
			}
		}
		// A launcher that marks the attempt cancelled (docs/adapters.md) makes the wait refuse it instead.
		if _, err := wait(spec, 300*time.Millisecond); !errors.Is(err, adapter.ErrNoResultYet) && !errors.Is(err, adapter.ErrAttemptCancelled) {
			t.Errorf("WaitResult after Stop = %v, want ErrNoResultYet or ErrAttemptCancelled", err)
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a stopped attempt left a result: %v", err)
		}
	})

	t.Run("refuses handles from another launcher", func(t *testing.T) {
		harnesses := []Harness{newHarness(t), newHarness(t)}
		handles := make([]interp.Handle, len(harnesses))
		for i, h := range harnesses {
			spec := prepare(t, h, Hangs)
			hd, err := h.Launcher.Launch(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			handles[i] = hd
			t.Cleanup(func() { _ = h.Launcher.Stop(hd) })
		}
		if handles[0].Identity != handles[1].Identity {
			t.Fatal("cross-launcher case requires identical attempt identities")
		}
		for i, h := range harnesses {
			for range 2 {
				if err := h.Launcher.Stop(handles[1-i]); err == nil {
					t.Error("Stop accepted another launcher's handle")
				}
				if err := h.Launcher.Stop(handles[i]); err != nil {
					t.Errorf("Stop of own handle = %v", err)
				}
			}
		}
	})

	t.Run("refuses a handle it never returned", func(t *testing.T) {
		h := newHarness(t)
		if err := h.Launcher.Stop(interp.Handle{Identity: adapter.Identity{TaskID: "t7", AttemptID: "build-1"}, Ref: "never-returned"}); err == nil {
			t.Error("Stop of a foreign handle succeeded")
		}
	})
}

func wait(spec interp.Spec, timeout time.Duration) (adapter.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Poll: 10 * time.Millisecond})
}
