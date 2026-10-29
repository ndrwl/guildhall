// Package adapter defines the baseline contract between Guildhall and an
// Artisan runtime (R1.23): Guildhall launches the runtime with a prompt file and
// working directory, the Artisan writes a result file, and completion comes only
// from that validated file, never from process exit or the terminal.
// Optional runtime capabilities are declared per adapter and default to none
// (R1.24). The contract is documented in docs/adapters.md.
package adapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// The names of the runtime adapters Guildhall can launch: the one spelling that package
// adapter/runtimes keys its lookup by and the interpreter's adapter guard refers to.
const (
	// FakeName is the fake Artisan, package adapter/fake.
	FakeName = "fake"
	// ClaudeName is the Claude Code adapter, package adapter/claude.
	ClaudeName = "claude"
)

// Spec is what Guildhall gives an Artisan for one task attempt.
type Spec struct {
	Identity
	// WorkDir is the absolute working directory the Artisan runs in.
	WorkDir string
	// PromptFile is the absolute path of the prompt the Artisan reads.
	PromptFile string
	// ResultFile is the absolute path the Artisan writes its result to.
	ResultFile string
	// Env holds extra NAME=VALUE entries for the Artisan's environment.
	Env     []string
	Copy    string
	Profile string
	// Repository is the task's repository id: with Copy and Profile, the reference a runtime that grants tools
	// resolves its effective grant from, never a rule list.
	Repository string
	// Settings are the profile's model and effort, which the runtime runs the attempt on.
	Settings
}

// Settings are a profile's model and effort for one attempt (docs/configuration.md#profiles), passed to the
// runtime as given; an empty one is the runtime's default. A runtime that cannot honour a non-empty one refuses
// it, before it starts anything, with an error wrapping ErrUnsupportedSetting, rather than ignoring it.
type Settings struct {
	Model, Effort string
}

// ErrUnsupportedSetting reports a model or effort the runtime cannot honour.
var ErrUnsupportedSetting = errors.New("the runtime cannot honour the setting")

// Validate checks the identity, that the working directory, prompt and result paths
// are absolute, and that the result file is distinct from the prompt file.
func (s Spec) Validate() error {
	if err := s.Identity.Validate(); err != nil {
		return err
	}
	for name, p := range map[string]string{"working directory": s.WorkDir, "prompt file": s.PromptFile, "result file": s.ResultFile} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s %q must be an absolute path", name, p)
		}
	}
	if filepath.Clean(s.PromptFile) == filepath.Clean(s.ResultFile) {
		return errors.New("prompt file and result file must differ")
	}
	for _, kv := range s.Env {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "" {
			return fmt.Errorf("environment entry %q is not NAME=VALUE", kv)
		}
	}
	return nil
}

// Capabilities are the optional runtime features an adapter has proven
// (R1.24). The zero value, no capabilities, is the baseline every adapter
// meets; the core never relies on a capability that is not declared.
type Capabilities struct {
	PreassignedSession bool
	Background         bool
	Hooks              bool
	StructuredOutput   bool
	RemoteControl      bool
}

// Any reports whether any optional capability is declared.
func (c Capabilities) Any() bool { return c != Capabilities{} }

// Command is a program invocation: an argument list, never shell text.
type Command struct {
	Argv []string
	Dir  string
	Env  []string
}

// Wait errors.
var (
	// ErrNoResultYet reports that the wait ended before a result file appeared.
	// The attempt may still be running: this is not a failure verdict.
	ErrNoResultYet = errors.New("no artisan result yet")
	// ErrExitedWithoutResult reports that a directly supervised Artisan process
	// exited and no result file exists.
	ErrExitedWithoutResult = errors.New("artisan process exited without a result")
)

// WaitOptions tunes WaitResult.
type WaitOptions struct {
	// Poll is the interval between result file checks.
	Poll time.Duration
	// Exited, when set, is closed once a directly supervised process has
	// exited. Herdr tabs have no such signal: a closed or vanished pane says
	// nothing about the attempt.
	Exited <-chan struct{}
}

// WaitResult polls the result file until it holds a valid result for id, the
// file is invalid, the supervised process exited without one, or ctx ends.
func WaitResult(ctx context.Context, path string, id Identity, opts WaitOptions) (Result, error) {
	poll := opts.Poll
	if poll <= 0 {
		poll = time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		// Read exit state before the file, so a result written just before
		// exit is always seen.
		exited := false
		if opts.Exited != nil {
			select {
			case <-opts.Exited:
				exited = true
			default:
			}
		}
		r, err := ReadResult(path, id)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Result{}, err
		}
		if exited {
			return Result{}, ErrExitedWithoutResult
		}
		select {
		case <-ctx.Done():
			return Result{}, fmt.Errorf("%w: %v", ErrNoResultYet, ctx.Err())
		case <-ticker.C:
		case <-opts.Exited:
		}
	}
}
