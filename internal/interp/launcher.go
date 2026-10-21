// Package interp is the workflow interpreter: it advances a task through its bound workflow revision.
// Engine is the interpreter (docs/interpreter.md); this file holds its launch seam, declared here by
// the consumer that calls it (docs/mock-agent.md), and records.go its other seams.
package interp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/render"
)

// Launcher starts and stops Artisan attempts for the interpreter.
//
// Launch starts the attempt spec describes and returns at once; it never waits for the attempt to finish.
// Completion comes only from a valid result file at spec.ResultFile for spec.Identity (adapter.ReadResult),
// never from Launch returning, process exit or a terminal.
// Launch refuses a spec that fails Spec.Validate or names an adapter the launcher does not run, and then starts nothing.
// A result file that already exists is never replaced.
//
// Stop stops a launched attempt; an attempt stopped before it wrote its result leaves no result.
// Stopping an attempt again, or one that already finished, is not an error.
// Stop refuses a Handle this launcher never returned.
// Both are safe for concurrent use.
type Launcher interface {
	Launch(ctx context.Context, spec Spec) (Handle, error)
	Stop(h Handle) error
}

// Spec is what the interpreter gives a Launcher for one attempt: the rendered prompt and where its result goes.
// AdapterSpec maps it onto the adapter.Spec a runtime adapter receives.
type Spec struct {
	// Identity is the attempt identity the result file must name.
	Identity adapter.Identity
	// Adapter names the runtime adapter that runs the attempt, from the stage's profile, such as "claude".
	Adapter string
	// WorkDir is the absolute working directory the Artisan runs in.
	WorkDir string
	// PromptFile is the absolute path of the rendered prompt; its inputs and skills
	// directories are beside it (render.InputsDir, render.SkillsDir).
	PromptFile string
	// ResultFile is the absolute path the Artisan writes its result to.
	ResultFile string
	// Manifest is the manifest render.Prompt returned for PromptFile.
	Manifest render.Manifest
}

// Validate checks the identity, that every path is absolute, that the result file is distinct
// from the prompt file, that an adapter is named and that the manifest is a prompt manifest.
func (s Spec) Validate() error {
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	if s.Adapter == "" {
		return errors.New("adapter is not named")
	}
	for _, p := range []struct{ name, path string }{{"working directory", s.WorkDir}, {"prompt file", s.PromptFile}, {"result file", s.ResultFile}} {
		if !filepath.IsAbs(p.path) {
			return fmt.Errorf("%s %q must be an absolute path", p.name, p.path)
		}
	}
	if filepath.Clean(s.PromptFile) == filepath.Clean(s.ResultFile) {
		return errors.New("prompt file and result file must differ")
	}
	if s.Manifest.Schema != render.ManifestSchema {
		return fmt.Errorf("manifest schema %q, want %q", s.Manifest.Schema, render.ManifestSchema)
	}
	return nil
}

// AdapterSpec returns the adapter.Spec a runtime adapter receives for s: the same identity, working
// directory, prompt file and result file, and no extra environment. The adapter name and manifest stay
// with the launcher, which chooses the runtime and has already checked the prompt against its manifest.
// A spec Validate accepts maps to one adapter.Spec.Validate accepts.
func (s Spec) AdapterSpec() adapter.Spec {
	return adapter.Spec{Identity: s.Identity, WorkDir: s.WorkDir, PromptFile: s.PromptFile, ResultFile: s.ResultFile}
}

// Handle names one launched attempt. It carries no authority: only the launcher that returned it accepts it.
type Handle struct {
	Identity adapter.Identity
	// Ref is the launcher's own reference to the attempt, such as a process or pane id.
	Ref string
}

// Test-only adapters: the only adapters CheckAdapter allows inside a test binary.
const (
	// MockAdapter is the scripted in-process double, interptest.Script.
	MockAdapter = "mock"
	// FakeAdapter is the fake Artisan subcommand, package adapter/fake.
	FakeAdapter = "fake"
)

// ErrAdapterRefused reports an adapter that may not be constructed here.
var ErrAdapterRefused = errors.New("adapter refused")

// CheckAdapter is the guard every Launcher must call before it constructs or runs an adapter.
// Inside a test binary (testing.Testing) it refuses every adapter but MockAdapter and FakeAdapter.
// It guards callers of this seam, not direct adapter construction elsewhere.
// Outside tests it allows any name; whether the adapter exists is the launcher's concern.
func CheckAdapter(name string) error {
	return checkAdapter(name, testing.Testing())
}

func checkAdapter(name string, inTest bool) error {
	if inTest && !slices.Contains([]string{MockAdapter, FakeAdapter}, name) {
		return fmt.Errorf("%w: %q in a test binary; only %q and %q run under test", ErrAdapterRefused, name, MockAdapter, FakeAdapter)
	}
	return nil
}

// BlockedQuestionSchema identifies a blocked question file.
const BlockedQuestionSchema = "guildhall.blocked-question/1"

// BlockedQuestionFile is the output path, relative to the working directory, of the question an Artisan
// asks when it cannot continue without the Guildmaster.
// A blocked attempt writes it and then a failed result listing it as an output;
// the interpreter's intake of it is separate work.
const BlockedQuestionFile = "blocked-question.json"

// BlockedQuestion is the content of BlockedQuestionFile.
type BlockedQuestion struct {
	Schema   string   `json:"schema"`
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}
