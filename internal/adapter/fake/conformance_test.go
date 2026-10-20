package fake

import (
	"context"
	"os"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
)

// TestMain lets the test binary act as `guildhall fake-artisan`, so the
// conformance suite runs the shipped fake runtime as a real child process.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == Subcommand {
		a, err := ParseArgs(os.Args[2:])
		if err != nil {
			os.Exit(2)
		}
		if err := Run(context.Background(), a, os.Stdout, Sleep); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestFakeRuntimeConformance(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prompts := map[adaptertest.Behaviour]string{
		adaptertest.Succeed:              "Do the task.\n",
		adaptertest.Fail:                 "guildhall-fake: outcome=failed\n",
		adaptertest.NoResult:             "guildhall-fake: fault=no-result\n",
		adaptertest.Malformed:            "guildhall-fake: fault=malformed\n",
		adaptertest.WrongIdentity:        "guildhall-fake: fault=wrong-identity\n",
		adaptertest.ExitErrorAfterResult: "guildhall-fake: fault=exit-error\n",
		adaptertest.Hang:                 "guildhall-fake: delay=10m\n",
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      Adapter{Executable: exe},
			Capabilities: adapter.Capabilities{},
			Prompt:       func(b adaptertest.Behaviour) string { return prompts[b] },
		}
	})
}
