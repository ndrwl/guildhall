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
	// CodexName is the Codex CLI adapter for read-only steps, package adapter/codex.
	CodexName = "codex"
)

// Declaration is what a runtime, by name, declares to the packages that cannot import adapter/runtimes, such as
// config and adapter/grant. Each runtime's own methods (runtimes.Runtime) return the same values, which the
// registry test in adapter/runtimes checks for every runtime.
type Declaration struct {
	// GrantsTools is set when the runtime applies a profile's allowed_tools and repository grants.
	GrantsTools bool
	// SkillPolicy is set when the runtime applies a Claude skill policy (config.ClaudeSkillPolicy).
	SkillPolicy bool
	// ReadOnly is set when the runtime runs only in a read-only sandbox, so it may run only the step kinds
	// admitted as read-only.
	ReadOnly bool
}

// declarations are the runtimes' declarations by name; every runtime has one.
var declarations = map[string]Declaration{
	FakeName:   {},
	ClaudeName: {GrantsTools: true, SkillPolicy: true},
	CodexName:  {ReadOnly: true},
}

// Declared returns the declaration of the runtime named name, and whether it has one.
func Declared(name string) (Declaration, bool) {
	d, ok := declarations[name]
	return d, ok
}

// GrantsTools reports whether the runtime named name applies a profile's allowed_tools and repository grants
// (package adapter/grant): only claude does. A name no runtime has grants none. It is declared here, beside the
// names, so a package that cannot import adapter/runtimes reads it without comparing names.
func GrantsTools(name string) bool {
	return declarations[name].GrantsTools
}

// SkillPolicy reports whether the runtime named name applies a Claude skill policy: only claude does. A name no
// runtime has applies none.
func SkillPolicy(name string) bool {
	return declarations[name].SkillPolicy
}

// ReadOnly reports whether the runtime named name runs only in a read-only sandbox: only codex does. A name no
// runtime has is not read-only.
func ReadOnly(name string) bool {
	return declarations[name].ReadOnly
}

// Signin is a runtime's entry in `guildhall signin` (docs/signin.md): how to check, verify and act on its sign-in
// on this machine. The zero Signin means the runtime needs no sign-in.
type Signin struct {
	// Title names the runtime in output.
	Title string
	// Program is the executable looked up on PATH.
	Program string
	// Status is the check: the runtime's own sign-in status arguments, exit 0 when it says signed in.
	Status []string
	// Verify is a minimal real call that only a working sign-in passes, since a status can pass while real calls
	// fail.
	Verify []string
	// Env is added to the environment of both commands.
	Env []string
	// Act is the lines printed when the runtime is not signed in: the exact command and the link, never run or
	// opened by Guildhall.
	Act []string
}

// Zero reports whether s is the zero entry: the runtime needs no sign-in.
func (s Signin) Zero() bool {
	return s.Title == "" && s.Program == "" && len(s.Status) == 0 && len(s.Verify) == 0 && len(s.Env) == 0 && len(s.Act) == 0
}

// SigninPrompt is the whole prompt of a model runtime's sign-in verify call: a minimal real model call.
const SigninPrompt = "Reply with the single word OK."

// Links are the task pages a session links ticket tokens to: the pages' base URL and the copy's repository ids, in
// config order.
type Links struct {
	URL   string   `json:"url"`
	Repos []string `json:"repos"`
}

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
