package adapter_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
)

// scriptedRuntime is a deterministic runtime double: the test binary, started
// with the attempt on its command line, does what the prompt's single word says.
// It also declares a capability, so the suite's capability check is exercised
// with a non-zero declaration.
type scriptedRuntime struct{ exe string }

func (scriptedRuntime) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{StructuredOutput: true}
}

func (r scriptedRuntime) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	return adapter.Command{
		Argv: []string{r.exe, "-test.run=^TestScriptedRuntimeHelper$", "--", "scripted-runtime",
			spec.TaskID, spec.AttemptID, spec.PromptFile, spec.ResultFile},
		Dir: spec.WorkDir,
		Env: slices.Clone(spec.Env),
	}, nil
}

func TestScriptedRuntimeConformance(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      scriptedRuntime{exe: exe},
			Capabilities: adapter.Capabilities{StructuredOutput: true},
			Prompt:       func(b adaptertest.Behaviour) string { return string(b) },
		}
	})
}

// TestScriptedRuntimeConformanceOnInterpreterAttempts runs the suite on
// attempts rendered from internal/interp/testdata and mapped through
// interp.Spec.AdapterSpec, with the behaviour carried in the step instructions.
func TestScriptedRuntimeConformanceOnInterpreterAttempts(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		return adaptertest.Subject{
			Runtime:      scriptedRuntime{exe: exe},
			Capabilities: adapter.Capabilities{StructuredOutput: true},
			Prompt:       func(b adaptertest.Behaviour) string { return string(b) },
			Attempt: func(t *testing.T, prompt string) adapter.Spec {
				return interptest.AdapterSpec(t, interp.MockAdapter, scriptedLine+prompt)
			},
		}
	})
}

// scriptedLine starts the prompt line that carries the scripted runtime's
// behaviour inside a rendered prompt; a bare prompt is the behaviour itself.
const scriptedLine = "scripted-runtime: "

// TestScriptedRuntimeHelper is not a test: it is the scripted runtime.
func TestScriptedRuntimeHelper(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) != at+6 || os.Args[at+1] != "scripted-runtime" {
		t.Skip("runtime for TestScriptedRuntimeConformance")
	}
	id := adapter.Identity{TaskID: os.Args[at+2], AttemptID: os.Args[at+3]}
	prompt, err := os.ReadFile(os.Args[at+4])
	if err != nil {
		os.Exit(2)
	}
	result := os.Args[at+5]
	write := func(r adapter.Result) {
		if err := adapter.WriteResult(result, r); err != nil {
			if errors.Is(err, adapter.ErrResultExists) {
				os.Exit(1)
			}
			os.Exit(2)
		}
	}
	ok := adapter.Result{Identity: id, Status: adapter.StatusSucceeded, Summary: "scripted"}
	behaviour := strings.TrimSpace(string(prompt))
	for line := range strings.Lines(string(prompt)) {
		if b, ok := strings.CutPrefix(strings.TrimSpace(line), scriptedLine); ok {
			behaviour = b
		}
	}
	switch adaptertest.Behaviour(behaviour) {
	case adaptertest.Succeed:
		write(ok)
	case adaptertest.Fail:
		ok.Status = adapter.StatusFailed
		write(ok)
	case adaptertest.NoResult:
	case adaptertest.Malformed:
		if err := os.WriteFile(result, []byte(`{"schema":`), 0o600); err != nil {
			os.Exit(2)
		}
	case adaptertest.WrongIdentity:
		ok.AttemptID += "-other"
		write(ok)
	case adaptertest.ExitErrorAfterResult:
		write(ok)
		os.Exit(3)
	case adaptertest.Hang:
		time.Sleep(time.Hour)
	default:
		fmt.Fprintf(os.Stderr, "scripted runtime: unknown prompt %q\n", prompt)
		os.Exit(2)
	}
	os.Exit(0)
}
