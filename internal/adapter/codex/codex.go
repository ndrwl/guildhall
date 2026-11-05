// Package codex is the Codex CLI Artisan adapter for read-only steps: it runs one attempt as a non-interactive
// `codex exec` in Codex's read-only sandbox, where nothing the model runs can write and so no command allow-list is
// needed (d108 found that codex-cli 0.163.0 cannot enforce one). It is attempt-only: it runs no Director session.
//
// The adapter's command is `guildhall codex-artisan`, a wrapper the guildhall binary will run; this package builds
// the command and checks the settings. The configuration admits a codex profile only where a step is read-only
// (config.CheckArtisanAdapters) and never with allowed_tools, so the adapter grants nothing: it refuses a profile
// whose effective grant has any rule, as the fake adapter does.
//
// It declares no optional capabilities (R1.24).
package codex

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/grant"
)

// Subcommand is the guildhall subcommand that runs the Codex wrapper.
const Subcommand = "codex-artisan"

// Program is the Codex CLI executable the wrapper runs when none is named: looked up on PATH when the attempt
// starts.
const Program = "codex"

// Efforts are the reasoning effort levels Codex's model_reasoning_effort takes, as the API accepted them for
// codex-cli 0.163.0 (docs/evidence/codex-hooks-2026-11-03.txt).
var Efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// Adapter launches the Codex wrapper from the guildhall executable.
type Adapter struct {
	// Executable is the absolute path of the guildhall binary.
	Executable string
	// Codex is the Codex CLI program the wrapper runs; empty means Program on PATH.
	Codex string
}

// Capabilities reports the Codex adapter's optional capabilities: none.
func (Adapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

// Steerable reports that the Codex runtime cannot be steered: the wrapper runs read-only, publishes the result
// itself and never tells the Artisan about steering, so it cannot run `guildhall messages ack`.
func (Adapter) Steerable() bool { return false }

// GrantsTools reports false: the Codex adapter runs read-only and applies no allowed_tools or repository grants;
// it refuses a profile with any rule.
func (Adapter) GrantsTools() bool { return false }

// SkillPolicy reports false: the Codex adapter applies no Claude skill policy.
func (Adapter) SkillPolicy() bool { return false }

// Signin is Codex's entry in `guildhall signin` (docs/signin.md).
func (Adapter) Signin() adapter.Signin {
	return adapter.Signin{
		Title: "Codex", Program: Program,
		Status: []string{"login", "status"},
		Verify: []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", adapter.SigninPrompt},
		Act: []string{
			"run: codex login --device-auth",
			"device-code sign-in must be enabled for your ChatGPT account first: in ChatGPT, Settings > Security, turn on device code authorization for Codex (a workspace admin enables it for a workspace)",
			"help: https://developers.openai.com/codex/auth",
			"never copy ~/.codex/auth.json from another machine",
		},
	}
}

// CheckSettings honours a model that is one word not starting with "-", which the wrapper passes to codex's -m, and
// an effort among Efforts; it refuses anything else with an error wrapping adapter.ErrUnsupportedSetting. It does
// not check that the model exists.
func (Adapter) CheckSettings(s adapter.Settings) error {
	if s.Model != "" && (strings.HasPrefix(s.Model, "-") || strings.IndexFunc(s.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return fmt.Errorf("%w: codex adapter: model %q is not one word Codex's -m takes", adapter.ErrUnsupportedSetting, s.Model)
	}
	if s.Effort != "" && !slices.Contains(Efforts, s.Effort) {
		return fmt.Errorf("%w: codex adapter: effort %q is not one Codex's model_reasoning_effort takes: %s", adapter.ErrUnsupportedSetting, s.Effort, strings.Join(Efforts, ", "))
	}
	return nil
}

// Command returns the wrapper invocation for spec: an argument list run in spec's working directory with spec's
// environment, passing spec's settings as --model and --effort, each only when set. It refuses an invalid spec,
// settings CheckSettings refuses, an unset Executable, or a spec referencing a profile whose effective grant
// (grant.ProfileTools) has any rule, since Codex grants none; it returns no command then.
func (a Adapter) Command(spec adapter.Spec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if err := a.CheckSettings(spec.Settings); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("codex adapter: guildhall executable is not set")
	}
	rules, err := grant.ProfileTools(spec.Copy, spec.Profile, spec.Repository)
	if err != nil {
		return adapter.Command{}, err
	}
	if len(rules) > 0 {
		return adapter.Command{}, errors.New("codex adapter: it grants no allowed tools; Codex runs read-only")
	}
	argv := []string{a.Executable, Subcommand,
		"--task", spec.TaskID, "--attempt", spec.AttemptID,
		"--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if spec.Copy != "" {
		argv = append(argv, "--copy", spec.Copy)
	}
	if spec.Profile != "" {
		argv = append(argv, "--profile", spec.Profile)
	}
	if spec.Repository != "" {
		argv = append(argv, "--repo", spec.Repository)
	}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	if spec.Effort != "" {
		argv = append(argv, "--effort", spec.Effort)
	}
	if a.Codex != "" {
		argv = append(argv, "--codex", a.Codex)
	}
	return adapter.Command{Argv: argv, Dir: spec.WorkDir, Env: append([]string(nil), spec.Env...)}, nil
}
