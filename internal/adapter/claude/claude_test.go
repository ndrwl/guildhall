package claude_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestMain lets the test binary act as the stub `claude`, as
// `guildhall claude-artisan` and as the session suite's recording guildhall,
// so the suites run the shipped wrapper and session over a Claude Code that
// makes no model calls.
func TestMain(m *testing.M) {
	adaptertest.RunIfRecorder()
	claudetest.RunIfStub()
	if len(os.Args) > 1 && os.Args[1] == claude.Subcommand {
		os.Exit(claude.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(testenv.RunWithClaudeGuard(m))
}

func subject(t *testing.T) adaptertest.Subject {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	claudetest.Install(t)
	return adaptertest.Subject{
		Runtime:      claude.Adapter{Executable: exe},
		Capabilities: adapter.Capabilities{},
		Prompt:       claudetest.Prompt,
	}
}

func TestClaudeRuntimeConformance(t *testing.T) {
	adaptertest.RunConformance(t, subject)
}

// TestClaudeRuntimeConformanceOnInterpreterAttempts runs the suite on attempts
// rendered by the interpreter, whose prompt and result live in an attempt
// directory outside the working directory.
func TestClaudeRuntimeConformanceOnInterpreterAttempts(t *testing.T) {
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		s := subject(t)
		s.Attempt = func(t *testing.T, prompt string) adapter.Spec {
			return interptest.AdapterSpec(t, interp.FakeAdapter, prompt)
		}
		return s
	})
}

// TestClaudeRuntimeConformanceThroughTheLauncher runs the suite with the
// wrapper inside the Artisan launcher, the owned-worker stop path: a stop must
// end at rung 1, the cancel marker, which the wrapper honours by stopping
// Claude.
func TestClaudeRuntimeConformanceThroughTheLauncher(t *testing.T) {
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		s := subject(t)
		s.Start = launcherStart
		return s
	})
}

// launched is a runtime the launcher runs; it has exited once Run returns.
type launched struct {
	l    *artisan.Launcher
	dir  string
	done chan struct{}
}

func (p *launched) Exited() <-chan struct{} { return p.done }

func (p *launched) Stop() error {
	if _, err := p.l.Stop(); err != nil {
		return err
	}
	out, err := artisan.ReadOutcome(p.dir)
	if err == nil && out.State == artisan.Cancelled && out.Rung > 1 {
		return fmt.Errorf("the wrapper did not honour the cancel marker: it stopped at rung %d", out.Rung)
	}
	return err
}

func launcherStart(t *testing.T, spec adapter.Spec, cmd adapter.Command) (adaptertest.Process, error) {
	u, err := artisan.NewUnit("claude-" + rand.Text()[:8])
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

func testSpec(t *testing.T) adapter.Spec {
	t.Helper()
	work, attempt := t.TempDir(), t.TempDir()
	spec := adapter.Spec{
		Identity:   adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"},
		WorkDir:    work,
		PromptFile: filepath.Join(attempt, "prompt.md"),
		ResultFile: filepath.Join(attempt, "result.json"),
		Env:        []string{"A=1"},
	}
	if err := os.WriteFile(spec.PromptFile, []byte("Do the task.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return spec
}

func args(spec adapter.Spec) claude.Args {
	return claude.Args{Identity: spec.Identity, PromptFile: spec.PromptFile, ResultFile: spec.ResultFile, Copy: spec.Copy, Profile: spec.Profile, Repository: spec.Repository}
}

func TestCommand(t *testing.T) {
	spec := testSpec(t)
	cmd, err := claude.Adapter{Executable: "/bin/guildhall"}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", claude.Subcommand, "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.WorkDir || !slices.Equal(cmd.Env, spec.Env) {
		t.Errorf("Command = %+v, want argv %q in %s with %q", cmd, want, spec.WorkDir, spec.Env)
	}
	program := filepath.Join(t.TempDir(), "claude")
	cmd, err = claude.Adapter{Executable: "/bin/guildhall", Claude: program}.Command(spec)
	if err != nil || !slices.Equal(cmd.Argv[len(cmd.Argv)-2:], []string{"--claude", program}) {
		t.Errorf("Command with Claude = %+v, %v; want it to end --claude %s", cmd, err, program)
	}
	parsed, err := claude.ParseArgs(cmd.Argv[2:])
	if err != nil || parsed.Identity != spec.Identity || parsed.PromptFile != spec.PromptFile || parsed.ResultFile != spec.ResultFile || parsed.Claude != program {
		t.Errorf("ParseArgs(Command) = %+v, %v", parsed, err)
	}
	if cmd, err := (claude.Adapter{}).Command(spec); err == nil || len(cmd.Argv) != 0 {
		t.Errorf("Command without Executable = %+v, %v; want a refusal", cmd, err)
	}
}

func TestCommandPassesAllowedTools(t *testing.T) {
	spec := testSpec(t)
	spec.Copy, spec.Profile = grantProfile(t, []string{"Bash(go test:*)", "Bash(git commit:*)"})
	spec.Repository = "app"
	program := filepath.Join(t.TempDir(), "claude")
	cmd, err := claude.Adapter{Executable: "/bin/guildhall", Claude: program}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", claude.Subcommand, "--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile,
		"--copy", spec.Copy, "--profile", spec.Profile, "--repo", "app", "--claude", program}
	if !slices.Equal(cmd.Argv, want) {
		t.Errorf("Command argv = %q, want %q", cmd.Argv, want)
	}
	parsed, err := claude.ParseArgs(cmd.Argv[2:])
	if err != nil || parsed.Copy != spec.Copy || parsed.Profile != spec.Profile || parsed.Repository != "app" {
		t.Errorf("ParseArgs(Command) = %+v, %v; want copy, profile and repository references", parsed, err)
	}
	spec.Copy, spec.Profile, spec.Repository = "", "", ""
	cmd, err = claude.Adapter{Executable: "/bin/guildhall"}.Command(spec)
	if err != nil || slices.Contains(cmd.Argv, "--allow-tool") {
		t.Errorf("Command without allowed tools = %q, %v; want no --allow-tool", cmd.Argv, err)
	}
}

func TestParseArgsRefuses(t *testing.T) {
	spec := testSpec(t)
	good := []string{"--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile}
	for name, argv := range map[string][]string{
		"missing result":   good[:6],
		"relative prompt":  {"--task", "task-1", "--attempt", "attempt-1", "--prompt", "prompt.md", "--result", spec.ResultFile},
		"same files":       {"--task", "task-1", "--attempt", "attempt-1", "--prompt", spec.ResultFile, "--result", spec.ResultFile},
		"bad identity":     {"--task", "", "--attempt", "attempt-1", "--prompt", spec.PromptFile, "--result", spec.ResultFile},
		"extra argument":   append(slices.Clone(good), "more"),
		"unknown flag":     append(slices.Clone(good), "--temperature", "x"),
		"relative program": append(slices.Clone(good), "--claude", "bin/claude"),
		"prompt in model":  {"--task", "task-1", "--attempt", "attempt-1", "--prompt", claude.PromptCopy(spec.ResultFile), "--result", spec.ResultFile},
		"empty allow tool": append(slices.Clone(good), "--allow-tool", ""),
		"control in rule":  append(slices.Clone(good), "--allow-tool", "Bash(go test\n:*)"),
	} {
		if _, err := claude.ParseArgs(argv); err == nil {
			t.Errorf("%s: ParseArgs(%q) accepted", name, argv)
		}
		var stderr bytes.Buffer
		if code := claude.Main(argv, &bytes.Buffer{}, &stderr); code != 2 {
			t.Errorf("%s: Main exit %d, want 2; stderr %q", name, code, stderr.String())
		}
	}
}

func TestClaudeArgs(t *testing.T) {
	spec := testSpec(t)
	a := args(spec)
	got := claude.ClaudeArgs(a)
	model := filepath.Join(filepath.Dir(spec.ResultFile), artisan.ModelDir)
	want := []string{"--print", claude.Instruction(a), "--output-format", claude.OutputFormat, "--verbose", "--permission-mode", claude.PermissionMode, "--settings", claude.ArtisanSettingsPath(spec.ResultFile), "--add-dir", model}
	if !slices.Equal(got, want) {
		t.Errorf("ClaudeArgs = %q, want %q", got, want)
	}
	if claude.PromptCopy(spec.ResultFile) != filepath.Join(model, "prompt.md") || claude.DraftFile(spec.ResultFile) != filepath.Join(model, artisan.ResultFile) {
		t.Errorf("prompt copy %s and draft %s; want both in %s", claude.PromptCopy(spec.ResultFile), claude.DraftFile(spec.ResultFile), model)
	}
	settings := claude.ArtisanSettingsPath(spec.ResultFile)
	if settings != filepath.Join(filepath.Dir(spec.ResultFile), claude.ArtisanSettingsFile) {
		t.Errorf("settings file %s; want %s beside the result", settings, claude.ArtisanSettingsFile)
	}
	in := claude.Instruction(a)
	for _, s := range []string{
		claudetest.PromptLine + claude.PromptCopy(spec.ResultFile) + "\n",
		claudetest.DraftLine + claude.DraftFile(spec.ResultFile) + "\n",
		`"schema":"guildhall.artisan-result"`, `"version":1`, `"task_id":"task-1"`, `"attempt_id":"attempt-1"`,
		`"succeeded"`, `"failed"`, "outputs", "Use the prompt's Result template, including its required outputs entries.",
	} {
		if !strings.Contains(in, s) {
			t.Errorf("Instruction lacks %q:\n%s", s, in)
		}
	}
	if strings.Contains(in, `"outputs":[]`) || strings.Contains(in, "and may be empty") {
		t.Errorf("Instruction supplies a contradictory empty-output default:\n%s", in)
	}
	if strings.Contains(in, spec.ResultFile+"\n") || strings.Contains(in, spec.PromptFile+"\n") {
		t.Errorf("Instruction names the result or prompt file itself, outside the model directory:\n%s", in)
	}
}

// TestModelDirExcludesControlFiles: the only directory Claude may write
// beyond its working directory holds none of the attempt's control files, so
// the model cannot write the result, the outcome or the cancel marker, nor
// touch the launcher's lock, events or log.
func TestModelDirExcludesControlFiles(t *testing.T) {
	spec := interptest.AdapterSpec(t, interp.FakeAdapter, "Do the task.\n")
	got := claude.ClaudeArgs(args(spec))
	i := slices.Index(got, "--add-dir")
	if i < 0 || i+1 >= len(got) {
		t.Fatalf("ClaudeArgs = %q; want --add-dir and its directories", got)
	}
	added := got[i+1:]
	attempt := filepath.Dir(spec.ResultFile)
	for _, f := range []string{artisan.ResultFile, artisan.ResultFile + ".cancelled", artisan.OutcomeFile, artisan.LockFile, artisan.EventsFile, launch.LogFile, claude.ArtisanSettingsFile} {
		path := filepath.Join(attempt, f)
		for _, d := range added {
			if rel, err := filepath.Rel(d, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Errorf("Claude may write %s: it is inside the added directory %s", path, d)
			}
		}
	}
}

// TestRunRefusesWithoutStartingClaude: a cancelled attempt or an existing
// result starts nothing, so a missing Claude is never reached.
func TestRunRefusesWithoutStartingClaude(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Run("cancelled", func(t *testing.T) {
		spec := testSpec(t)
		if err := adapter.MarkCancelled(spec.ResultFile, spec.Identity); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, adapter.ErrAttemptCancelled) {
			t.Errorf("Run = %v, want ErrAttemptCancelled", err)
		}
	})
	t.Run("existing result", func(t *testing.T) {
		spec := testSpec(t)
		if err := os.WriteFile(spec.ResultFile, []byte("kept"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, adapter.ErrResultExists) {
			t.Errorf("Run = %v, want ErrResultExists", err)
		}
		if b, _ := os.ReadFile(spec.ResultFile); string(b) != "kept" {
			t.Errorf("result replaced: %q", b)
		}
	})
	t.Run("missing claude", func(t *testing.T) {
		spec := testSpec(t)
		err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{})
		if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), claude.Program) {
			t.Errorf("Run = %v, want exec.ErrNotFound naming %s", err, claude.Program)
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("result written: %v", err)
		}
	})
}

// TestRunDraftHandling: a stale draft is never published, and a draft is
// published once from a model directory holding the prompt copy and the
// inputs and skills beside the prompt.
func TestRunDraftHandling(t *testing.T) {
	claudetest.Install(t)
	t.Run("stale draft removed", func(t *testing.T) {
		spec := testSpec(t)
		if err := os.MkdirAll(claude.ModelDir(spec.ResultFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(claude.DraftFile(spec.ResultFile), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.NoResult)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, claude.ErrNoDraft) {
			t.Errorf("Run = %v, want ErrNoDraft", err)
		}
		if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale draft published: %v", err)
		}
	})
	t.Run("published once", func(t *testing.T) {
		spec := interptest.AdapterSpec(t, interp.FakeAdapter, "Do the task.\n")
		if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
		if err != nil || r.Status != adapter.StatusSucceeded {
			t.Errorf("ReadResult = %+v, %v", r, err)
		}
		model := claude.ModelDir(spec.ResultFile)
		for _, name := range []string{"inputs", "skills"} {
			want, _ := os.ReadDir(filepath.Join(filepath.Dir(spec.PromptFile), name))
			got, err := os.ReadDir(filepath.Join(model, name))
			if err != nil || len(got) != len(want) {
				t.Errorf("model directory %s: %d entries, %v; want the %d beside the prompt", name, len(got), err, len(want))
			}
		}
	})
}

// TestParseUsage: Claude Code's result message gives measured tokens and an estimated cost; whatever it does not
// report is unknown, never a guess.
func TestParseUsage(t *testing.T) {
	got, err := claude.ParseUsage(claudetest.Output())
	if err != nil || got != claudetest.Usage {
		t.Fatalf("ParseUsage(stub output) = %+v, %v; want %+v", got, err, claudetest.Usage)
	}
	if got.Tokens != adapter.Measured || got.Cost != adapter.Estimated {
		t.Errorf("stub usage is %s tokens and %s cost; want measured tokens and an estimated cost", got.Tokens, got.Cost)
	}
	noCost := `{"type":"result","result":"done","usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":5,"cache_read_input_tokens":6}}`
	want := adapter.Usage{Tokens: adapter.Measured, InputTokens: 3, OutputTokens: 4, CacheCreationInputTokens: 5, CacheReadInputTokens: 6, Cost: adapter.Unknown}
	if got, err := claude.ParseUsage([]byte(noCost)); err != nil || got != want {
		t.Errorf("ParseUsage without a cost = %+v, %v; want %+v", got, err, want)
	}
	noUsage := `{"type":"result","result":"done","total_cost_usd":0.5}`
	want = adapter.Usage{Tokens: adapter.Unknown, Cost: adapter.Estimated, CostUSD: 0.5}
	if got, err := claude.ParseUsage([]byte(noUsage)); err != nil || got != want {
		t.Errorf("ParseUsage without usage = %+v, %v; want %+v", got, err, want)
	}
	for name, out := range map[string]string{
		"text output":    "I did the task.\n",
		"empty":          "",
		"two objects":    `{"type":"result"} {"type":"result"}`,
		"negative count": `{"type":"result","usage":{"input_tokens":-1,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`,
		"negative cost":  `{"type":"result","total_cost_usd":-1}`,
	} {
		if got, err := claude.ParseUsage([]byte(out)); err == nil {
			t.Errorf("%s: ParseUsage = %+v; want an error", name, got)
		}
	}
}

// TestDenials: the tool uses Claude Code's print mode denied, from its result message's permission_denials, each as
// the tool and its command or file; output without any, or that is not a result message, has none.
func TestDenials(t *testing.T) {
	out := `{"type":"result","result":"done","permission_denials":[` +
		`{"tool_name":"Bash","tool_use_id":"t1","tool_input":{"command":"go test ./...","description":"Run tests"}},` +
		`{"tool_name":"Bash","tool_use_id":"t2","tool_input":{"command":"git commit -m 'Sort tasks'"}},` +
		`{"tool_name":"Write","tool_use_id":"t3","tool_input":{"file_path":"/etc/hosts","content":"x"}},` +
		`{"tool_name":"WebFetch","tool_use_id":"t4","tool_input":{"url":"https://example.com"}}]}`
	want := []string{"Bash(go test ./...)", "Bash(git commit -m 'Sort tasks')", "Write(/etc/hosts)", "WebFetch"}
	if got := claude.Denials([]byte(out)); !slices.Equal(got, want) {
		t.Errorf("Denials = %q, want %q", got, want)
	}
	for name, out := range map[string]string{
		"stub output": string(claudetest.Output()),
		"none":        `{"type":"result","permission_denials":[]}`,
		"text output": "I did the task.\n",
	} {
		if got := claude.Denials([]byte(out)); len(got) != 0 {
			t.Errorf("%s: Denials = %q, want none", name, got)
		}
	}
}

// TestRunReportsDenials: the wrapper names on its error output each tool use print mode denied, so an attempt that
// could not run its checks or commit says why in its launcher log; a session denied nothing reports nothing.
func TestRunReportsDenials(t *testing.T) {
	claudetest.Install(t)
	for _, b := range []adaptertest.Behaviour{adaptertest.Succeed, adaptertest.Behaviour(claudetest.Denied)} {
		t.Run(string(b), func(t *testing.T) {
			spec := testSpec(t)
			if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(b)), 0o600); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &stderr); err != nil {
				t.Fatal(err)
			}
			denied := strings.Contains(stderr.String(), "denied 1 tool use: Bash(go test ./...)")
			if want := b != adaptertest.Succeed; denied != want || want && !strings.Contains(stderr.String(), "docs/adapters.md#artisan-permissions") {
				t.Errorf("stderr = %q; want the denial reported %v, naming the docs", stderr.String(), want)
			}
		})
	}
}

// TestArtisanSettings: the settings file grants exactly the rules, in order, as Claude Code's permissions.allow and
// nothing else.
func TestArtisanSettings(t *testing.T) {
	got := string(claude.ArtisanSettings([]string{"Bash(go test:*)", "Bash(git commit:*)"}))
	want := `{"permissions":{"allow":["Bash(go test:*)","Bash(git commit:*)"]}}` + "\n"
	if got != want {
		t.Errorf("ArtisanSettings = %q, want %q", got, want)
	}
}

// TestSuggestedGoAllowedToolsAreValidConfig: the documented repository grant for a Go repository is go test and go
// vet, one a repository grant and a profile accept; the git it once held comes from the builder's shipped default.
func TestSuggestedGoAllowedToolsAreValidConfig(t *testing.T) {
	if want := []string{"Bash(go test:*)", "Bash(go vet:*)"}; !slices.Equal(claude.SuggestedGoAllowedTools, want) {
		t.Errorf("suggested set = %q, want %q", claude.SuggestedGoAllowedTools, want)
	}
	for _, rule := range claude.SuggestedGoAllowedTools {
		if err := config.CheckAllowedTool(rule); err != nil {
			t.Errorf("suggested rule %q refused: %v", rule, err)
		}
	}
	for name, rules := range claude.ShippedDefaultGrants {
		for _, rule := range rules {
			if err := config.CheckAllowedTool(rule); err != nil {
				t.Errorf("%s default rule %q refused: %v", name, rule, err)
			}
		}
	}
}

// TestRunGrantsAllowedTools: the session Claude runs is granted exactly the attempt's effective grant, through a
// settings file outside the directories Claude may write; empty grants still receive the skill policy. A builder
// whose copy sets no list gets its shipped default plus its repository's grant, and nothing of another repository's.
// Every grant ends with guildhall ci log.
func TestRunGrantsAllowedTools(t *testing.T) {
	claudetest.Install(t)
	for name, rules := range map[string][]string{
		"none":      nil,
		"suggested": claude.SuggestedGoAllowedTools,
		"one":       {"Bash(sh check.sh)"},
	} {
		t.Run(name, func(t *testing.T) {
			spec := testSpec(t)
			spec.Copy, spec.Profile = grantProfile(t, rules)
			spec.Repository = "app"
			var stderr bytes.Buffer
			if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &stderr); err != nil {
				t.Fatalf("Run = %v; stderr %s", err, stderr.String())
			}
			want := append(slices.Clone(rules), claude.CILogRule)
			got, err := claudetest.Granted(spec.ResultFile)
			if err != nil || !slices.Equal(got, want) {
				t.Errorf("granted %q, %v; want exactly %q", got, err, want)
			}
			_, err = os.Stat(claude.ArtisanSettingsPath(spec.ResultFile))
			if err != nil {
				t.Errorf("settings file missing: %v", err)
			}
		})
	}
	for repo, extra := range map[string][]string{"app": claude.SuggestedGoAllowedTools, "site": {"Bash(npm test:*)"}, "": nil} {
		t.Run("default and repository "+repo, func(t *testing.T) {
			spec := testSpec(t)
			spec.Copy = t.TempDir()
			text := "schema_version = 1\nmode = \"director\"\nname = \"test\"\n[profile.builder]\nadapter = \"claude\"\n" +
				"[[repository]]\nid = \"app\"\npath = \"/src/app\"\nremote = \"git@example.com:app.git\"\nowner_machine = \"test\"\n" +
				"[repository.profiles.builder]\nallowed_tools = [\"Bash(go test:*)\", \"Bash(go vet:*)\", \"Bash(git commit:*)\"]\n" +
				"[[repository]]\nid = \"site\"\npath = \"/src/site\"\nremote = \"git@example.com:site.git\"\nowner_machine = \"test\"\n" +
				"[repository.profiles.builder]\nallowed_tools = [\"Bash(npm test:*)\"]\n"
			if err := os.WriteFile(filepath.Join(spec.Copy, "config.toml"), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			spec.Profile, spec.Repository = "builder", repo
			var stderr bytes.Buffer
			if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &stderr); err != nil {
				t.Fatalf("Run = %v; stderr %s", err, stderr.String())
			}
			want := append(append(slices.Clone(claude.ShippedDefaultGrants["builder"]), extra...), claude.CILogRule)
			if got, err := claudetest.Granted(spec.ResultFile); err != nil || !slices.Equal(got, want) {
				t.Errorf("granted %q, %v; want exactly %q", got, err, want)
			}
		})
	}
}

// TestRunWritesUsage: once Claude exits the wrapper writes the usage its output reports beside the result, a
// draft or not, and passes the session's result text through; a stopped session writes none, so its usage is
// unknown.
func TestRunWritesUsage(t *testing.T) {
	claudetest.Install(t)
	for _, b := range []adaptertest.Behaviour{adaptertest.Succeed, adaptertest.Fail, adaptertest.NoResult} {
		t.Run(string(b), func(t *testing.T) {
			spec := testSpec(t)
			if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(b)), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			_ = claude.Run(context.Background(), args(spec), &stdout, &bytes.Buffer{})
			if got, err := adapter.ReadUsage(spec.ResultFile); err != nil || got != claudetest.Usage {
				t.Errorf("usage = %+v, %v; want %+v", got, err, claudetest.Usage)
			}
			if !strings.Contains(stdout.String(), claudetest.Summary) {
				t.Errorf("stdout = %q; want the session's result text", stdout.String())
			}
		})
	}
	t.Run("stopped", func(t *testing.T) {
		spec := testSpec(t)
		if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Hang)), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = claude.Run(ctx, args(spec), &bytes.Buffer{}, &bytes.Buffer{})
		if _, err := os.Stat(adapter.UsageFile(spec.ResultFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a stopped session wrote a usage file: %v", err)
		}
	})
}

// TestRunPublishesDespiteUsageFailure: a usage file that cannot be written is an error, but the draft is still
// published.
func TestRunPublishesDespiteUsageFailure(t *testing.T) {
	claudetest.Install(t)
	spec := interptest.AdapterSpec(t, interp.FakeAdapter, "Do the task.\n")
	if err := os.Mkdir(adapter.UsageFile(spec.ResultFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("Run succeeded although the usage file could not be written")
	}
	if r, err := adapter.ReadResult(spec.ResultFile, spec.Identity); err != nil || r.Status != adapter.StatusSucceeded {
		t.Errorf("ReadResult = %+v, %v; want the published draft", r, err)
	}
}

// TestRunStopsOnContext: ending ctx stops Claude and publishes nothing.
func TestRunStopsOnContext(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Hang)), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- claude.Run(ctx, args(spec), &bytes.Buffer{}, &bytes.Buffer{}) }()
	testenv.WaitFor(t, "Claude stub startup", func() bool {
		_, err := claudetest.Granted(spec.ResultFile)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context cancellation", err)
		}
	case <-time.After(testenv.Ceiling(30 * time.Second)):
		t.Fatal("Claude did not stop after cancellation")
	}
	if _, err := os.Stat(spec.ResultFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stopped attempt has a result: %v", err)
	}
}

// LiveEnv opts into TestLiveClaude, which runs a real Claude Code session and
// so makes a model call; CI never sets it.
const LiveEnv = "GUILDHALL_CLAUDE_LIVE"

// LiveGrantEnv chooses what TestLiveClaudeBuildPermissions grants: unset, nothing, so only the machine's own
// settings can allow its commands; "configured", the profile grant a Go builder would be configured with,
// the builder's shipped default, SuggestedGoAllowedTools and the check's own command, so the run shows those rules
// suffice.
const LiveGrantEnv = "GUILDHALL_CLAUDE_LIVE_GRANT"

// liveGrant is the configured mode's grant.
var liveGrant = append(append(slices.Clone(claude.ShippedDefaultGrants["builder"]), claude.SuggestedGoAllowedTools...), "Bash(sh check.sh)")

// TestLiveClaude runs one real attempt through the wrapper with the `claude`
// on PATH, signed in as the developer: run it by hand with
// GUILDHALL_CLAUDE_LIVE=1 go test ./internal/adapter/claude -run TestLiveClaude -v -timeout 15m
func TestLiveClaude(t *testing.T) {
	if os.Getenv(LiveEnv) != "1" {
		t.Skipf("set %s=1 to run a real Claude Code session; it makes a model call", LiveEnv)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t)
	prompt := "This is a Guildhall adapter check. Create the file hello.txt in the working directory containing the single line `hello`, " +
		"then report success, listing hello.txt as your one output.\n"
	if err := os.WriteFile(spec.PromptFile, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := claude.Adapter{Executable: exe}.Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	p, err := adapter.StartProcess(cmd, os.Stderr, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(); <-p.Exited() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r, err := adapter.WaitResult(ctx, spec.ResultFile, spec.Identity, adapter.WaitOptions{Exited: p.Exited()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result: %+v", r)
	got, err := os.ReadFile(filepath.Join(spec.WorkDir, "hello.txt"))
	if r.Status != adapter.StatusSucceeded || err != nil || strings.TrimSpace(string(got)) != "hello" {
		t.Errorf("result %+v; hello.txt %q, %v", r, got, err)
	}
}

// TestLiveClaudeBuildPermissions is the Artisan permissions' live acceptance: one real attempt through the
// wrapper, in a git repository, asked to run a check and commit as the build stage asks. It passes only where
// Claude's settings, or with GUILDHALL_CLAUDE_LIVE_GRANT=configured the profile grant liveGrant, allow those shell
// commands (docs/adapters.md#artisan-permissions); a denial is named in the failure. Run it by hand with
// GUILDHALL_CLAUDE_LIVE=1 [GUILDHALL_CLAUDE_LIVE_GRANT=configured] go test ./internal/adapter/claude -run TestLiveClaudeBuildPermissions -v -timeout 15m
func TestLiveClaudeBuildPermissions(t *testing.T) {
	if os.Getenv(LiveEnv) != "1" {
		t.Skipf("set %s=1 to run a real Claude Code session; it makes a model call", LiveEnv)
	}
	spec := testSpec(t)
	switch mode := os.Getenv(LiveGrantEnv); mode {
	case "":
	case "configured":
		spec.Copy, spec.Profile = grantProfile(t, liveGrant)
		t.Logf("granting %q", liveGrant)
	default:
		t.Fatalf("%s=%q; want unset or configured", LiveGrantEnv, mode)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", spec.WorkDir, "-c", "user.name=Guildhall", "-c", "user.email=guildhall@example.invalid"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(spec.WorkDir, "check.sh"), []byte(": > .git/guildhall-checked\necho 'checks passed'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "check.sh")
	git("commit", "-q", "-m", "base")
	base := git("rev-parse", "HEAD")
	prompt := "This is a Guildhall Artisan permissions check. Create the file hello.txt in the working directory containing the single line `hello`. " +
		"Run the repository's check, `sh check.sh`, and confirm it prints `checks passed`, then commit hello.txt with git, message `Add hello`. " +
		"Report success only if the commit was made, listing hello.txt as your one output; otherwise report failure and say which command was refused.\n"
	if err := os.WriteFile(spec.PromptFile, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(spec.WorkDir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var stderr bytes.Buffer
	runErr := claude.Run(ctx, args(spec), os.Stdout, io.MultiWriter(os.Stderr, &stderr))
	r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
	if err != nil {
		t.Fatalf("result: %v (run: %v)", err, runErr)
	}
	t.Logf("result: %+v", r)
	if head := git("rev-parse", "HEAD"); r.Status != adapter.StatusSucceeded || head == base {
		t.Errorf("result %s, head %s from base %s; want a new commit. Wrapper output:\n%s", r.Status, head, base, stderr.String())
	} else if files := git("diff", "--name-only", base, "HEAD"); files != "hello.txt" {
		t.Errorf("commit changed %q; want only hello.txt", files)
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, ".git", "guildhall-checked")); err != nil {
		t.Errorf("check.sh did not run: %v. Wrapper output:\n%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "Claude was denied") {
		t.Errorf("a tool use was denied. Wrapper output:\n%s", stderr.String())
	}
}
