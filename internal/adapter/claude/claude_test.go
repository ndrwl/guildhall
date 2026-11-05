package claude_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
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
	"github.com/ndrwl/guildhall/internal/adapter/grant"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/secrets"
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
		if d, err := time.ParseDuration(os.Getenv(exitGraceEnv)); err == nil {
			claude.ExitGrace = d
		}
		os.Exit(claude.Main(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(testenv.RunWithGuards(m))
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
	want := []string{"--print", claude.Instruction(a), "--output-format", claude.OutputFormat, "--verbose", "--permission-mode", claude.PermissionMode,
		"--disallowedTools", strings.Join(claude.DisallowedTools, ","), "--settings", claude.ArtisanSettingsPath(spec.ResultFile), "--add-dir", model,
		"--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	if !slices.Equal(got, want) {
		t.Errorf("ClaudeArgs = %q, want %q", got, want)
	}
	a.Settings = adapter.Settings{Model: "opus", Effort: "high"}
	got = claude.ClaudeArgs(a)
	d, s := slices.Index(got, "--disallowedTools"), slices.Index(got, "--settings")
	if d < 0 || s < 0 || d > slices.Index(got, "--model") || d > s || !slices.Equal(got[len(got)-7:], want[len(want)-7:]) {
		t.Errorf("ClaudeArgs = %q; want --disallowedTools before --model and --settings, and --add-dir, --setting-sources and the MCP config last", got)
	}
	dropped := strings.Split(got[d+1], ",")
	for _, tool := range []string{"Workflow", "ScheduleWakeup", "ReportFindings", "ListAgents"} {
		if !slices.Contains(dropped, tool) {
			t.Errorf("--disallowedTools %q lacks %s", got[d+1], tool)
		}
	}
	for _, tool := range keptTools {
		if slices.Contains(dropped, tool) {
			t.Errorf("--disallowedTools %q drops kept tool %s", got[d+1], tool)
		}
	}
	if claude.PromptCopy(spec.ResultFile) != filepath.Join(model, "prompt.md") || claude.DraftFile(spec.ResultFile) != filepath.Join(model, artisan.ResultFile) ||
		claude.SubmitFile(spec.ResultFile) != filepath.Join(model, artisan.DraftFile) {
		t.Errorf("prompt copy %s, draft %s and submit file %s; want all in %s", claude.PromptCopy(spec.ResultFile), claude.DraftFile(spec.ResultFile), claude.SubmitFile(spec.ResultFile), model)
	}
	settings := claude.ArtisanSettingsPath(spec.ResultFile)
	if settings != filepath.Join(filepath.Dir(spec.ResultFile), claude.ArtisanSettingsFile) {
		t.Errorf("settings file %s; want %s beside the result", settings, claude.ArtisanSettingsFile)
	}
	in := claude.Instruction(a)
	for _, s := range []string{
		claudetest.PromptLine + claude.PromptCopy(spec.ResultFile) + "\n",
		claudetest.DraftLine + claude.SubmitFile(spec.ResultFile) + "\n",
		"\n" + claudetest.SubmitLine + claude.SubmitFile(spec.ResultFile) + "\n",
		"fix what the reason names", "and submit again; stop once it prints ok",
		"Only if the command cannot run", "write the result to " + claude.DraftFile(spec.ResultFile) + " instead",
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

// TestArtisanSettings: the settings file grants exactly the rules, in order, as Claude Code's permissions.allow, and
// registers the steering PostToolUse hook for every tool, and turns auto memory off, and nothing else.
func TestArtisanSettings(t *testing.T) {
	got := string(claude.ArtisanSettings([]string{"Bash(go test:*)", "Bash(git commit:*)"}))
	want := `{"permissions":{"allow":["Bash(go test:*)","Bash(git commit:*)"]},` +
		`"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"guildhall messages hook","timeout":10}]}]},` +
		`"autoMemoryEnabled":false}` + "\n"
	if got != want {
		t.Errorf("ArtisanSettings = %q, want %q", got, want)
	}
}

// TestArtisanPolicySettingsHooks: the settings launch and the Director preflight render for every shipped profile,
// under every Artisan skill preset, keep the grant, auto memory off and the steering hook as the only hook, and never set
// disableAllHooks, which would switch the steering hook off; user and project hooks stay out because ClaudeArgs
// loads no settings file source.
func TestArtisanPolicySettingsHooks(t *testing.T) {
	for _, preset := range []string{"artisan-v1", "builder-v1", "reviewer-v1"} {
		p, err := config.ParseClaudeSkills(`{"preset":"` + preset + `"}`)
		if err != nil {
			t.Fatal(err)
		}
		for name, rules := range grant.ShippedDefaultGrants {
			b := claude.ArtisanPolicySettings(append(slices.Clone(rules), grant.GuildhallRules...), p)
			var settings struct {
				DisableAllHooks   *bool                      `json:"disableAllHooks"`
				AutoMemoryEnabled *bool                      `json:"autoMemoryEnabled"`
				Hooks             map[string]json.RawMessage `json:"hooks"`
				Permissions       struct {
					Allow []string `json:"allow"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal(b, &settings); err != nil {
				t.Fatalf("%s %s: %v", preset, name, err)
			}
			if settings.DisableAllHooks != nil || len(settings.Permissions.Allow) == 0 {
				t.Errorf("%s %s settings = %s; want the grant and no disableAllHooks", preset, name, b)
			}
			if settings.AutoMemoryEnabled == nil || *settings.AutoMemoryEnabled {
				t.Errorf("%s %s settings = %s; want autoMemoryEnabled false", preset, name, b)
			}
			if len(settings.Hooks) != 1 || !strings.Contains(string(settings.Hooks["PostToolUse"]), claude.SteeringHookCommand) {
				t.Errorf("%s %s hooks = %v; want only the steering PostToolUse hook", preset, name, settings.Hooks)
			}
			if err := claude.ValidatePolicySettings(b, p); err != nil {
				t.Errorf("%s %s: %v", preset, name, err)
			}
		}
	}
}

// TestSuggestedGoAllowedToolsAreValidConfig: the documented repository grant for a Go repository is go test and go
// vet, one a repository grant and a profile accept; the git it once held comes from the builder's shipped default.
func TestSuggestedGoAllowedToolsAreValidConfig(t *testing.T) {
	if want := []string{"Bash(go test:*)", "Bash(go vet:*)"}; !slices.Equal(grant.SuggestedGoAllowedTools, want) {
		t.Errorf("suggested set = %q, want %q", grant.SuggestedGoAllowedTools, want)
	}
	for _, rule := range grant.SuggestedGoAllowedTools {
		if err := config.CheckAllowedTool(rule); err != nil {
			t.Errorf("suggested rule %q refused: %v", rule, err)
		}
	}
	for name, rules := range grant.ShippedDefaultGrants {
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
		"suggested": grant.SuggestedGoAllowedTools,
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
			want := append(slices.Clone(rules), grant.GuildhallRules...)
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
	for repo, extra := range map[string][]string{"app": grant.SuggestedGoAllowedTools, "site": {"Bash(npm test:*)"}, "": nil} {
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
			want := append(append(slices.Clone(grant.ShippedDefaultGrants["builder"]), extra...), grant.GuildhallRules...)
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

// TestRunRedactsInheritedSecrets: a value the Artisan inherited, named in secrets.RedactEnv, is redacted from the
// wrapper's output, which the launcher appends to launcher.log, from the Workbench event file and from the
// published result.
func TestRunRedactsInheritedSecrets(t *testing.T) {
	claudetest.Install(t)
	t.Setenv(secrets.RedactEnv, "STUB_SECRET")
	t.Setenv("STUB_SECRET", claudetest.Summary)
	spec := testSpec(t)
	if err := os.WriteFile(spec.PromptFile, []byte(claudetest.Prompt(adaptertest.Succeed)), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := claude.Run(context.Background(), args(spec), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), claudetest.Summary) || !strings.Contains(stdout.String(), secrets.Redacted) {
		t.Errorf("output %q holds the value or no redaction", stdout.String()+stderr.String())
	}
	r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
	if err != nil || r.Summary != secrets.Redacted {
		t.Errorf("result %+v, %v; want its summary redacted", r, err)
	}
	if events, err := os.ReadFile(claude.WorkbenchPath(spec.ResultFile)); err != nil || strings.Contains(string(events), claudetest.Summary) {
		t.Errorf("event file holds the value, or %v", err)
	}
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

// LiveGrantEnv chooses what TestLiveClaudeBuildPermissions grants: unset, nothing, so the check's command is
// expected to be denied, since no user, project or local settings load to allow it; "configured", the profile
// grant a Go builder would be configured with, the builder's shipped default, SuggestedGoAllowedTools and the
// check's own command, so the run shows those rules suffice.
const LiveGrantEnv = "GUILDHALL_CLAUDE_LIVE_GRANT"

// liveGrant is the configured mode's grant.
var liveGrant = append(append(slices.Clone(grant.ShippedDefaultGrants["builder"]), grant.SuggestedGoAllowedTools...), "Bash(sh check.sh)")

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
// wrapper, in a git repository, asked to run a check and commit as the build stage asks. With
// GUILDHALL_CLAUDE_LIVE_GRANT=configured the profile grant liveGrant must allow those shell commands; unset, with
// no profile grant, the check's command must be denied, because the machine's own settings no longer load
// (docs/adapters.md#artisan-permissions). Run it by hand with
// GUILDHALL_CLAUDE_LIVE=1 [GUILDHALL_CLAUDE_LIVE_GRANT=configured] go test ./internal/adapter/claude -run TestLiveClaudeBuildPermissions -v -timeout 15m -claude-program "$(command -v claude)"
// or, where only `go test` is granted, with -claude-live [-claude-live-grant configured] in place of the variables.
func TestLiveClaudeBuildPermissions(t *testing.T) {
	program := liveClaude(t)
	spec := testSpec(t)
	configured := false
	mode := os.Getenv(LiveGrantEnv)
	if *claudeLiveGrant != "" {
		mode = *claudeLiveGrant
	}
	switch mode {
	case "":
		spec.Copy, spec.Profile = grantProfile(t, nil)
		t.Log("granting nothing; expecting the check to be denied")
	case "configured":
		configured = true
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
	a := args(spec)
	a.Claude = program
	runErr := claude.Run(ctx, a, os.Stdout, io.MultiWriter(os.Stderr, &stderr))
	r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
	if err != nil {
		t.Fatalf("result: %v (run: %v)", err, runErr)
	}
	t.Logf("result: %+v", r)
	if !configured {
		if !strings.Contains(stderr.String(), "Claude was denied") || !strings.Contains(stderr.String(), "Bash(sh check.sh)") {
			t.Errorf("want sh check.sh denied without a profile grant. Wrapper output:\n%s", stderr.String())
		}
		if _, err := os.Stat(filepath.Join(spec.WorkDir, ".git", "guildhall-checked")); err == nil {
			t.Error("check.sh ran without a grant")
		}
		return
	}
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

// keptTools are the tools an Artisan keeps, which DisallowedTools must not name.
var keptTools = []string{"Agent", "Task", "Skill", "ToolSearch", "WebSearch", "WebFetch", "Monitor", "TaskStop", "NotebookEdit"}

// claudeLive opens TestLiveClaudeTools, TestLiveClaudeIsolation and TestLiveClaudeBuildPermissions as LiveEnv
// does, for a runner whose grant allows `go test` but not an environment assignment before it.
var claudeLive = flag.Bool("claude-live", false, "run the live Claude Code tests that take -claude-program; they make model calls")

// claudeLiveGrant is LiveGrantEnv as a flag, for the same runner; when set it wins over the variable.
var claudeLiveGrant = flag.String("claude-live-grant", "", "TestLiveClaudeBuildPermissions grant mode: empty or configured")

// claudeProgram is the real Claude Code the -claude-live tests run. The test binary's claude guard
// (testenv.InstallClaudeGuard) shadows every `claude` on PATH, so the probe needs the program's absolute path.
var claudeProgram = flag.String("claude-program", "", "absolute path of the real claude for the -claude-live tests")

// toolsProbe is what one print-mode run reports: its init event's tools and its first assistant event's usage.
type toolsProbe struct {
	Tools                           []string
	Input, CacheCreation, CacheRead int
}

func (p toolsProbe) prompt() int { return p.Input + p.CacheCreation + p.CacheRead }

// probeTools runs `claude --print "reply ok"` in an empty directory with the Artisan's print-mode flags and extra,
// and reads the init event's tools and the first assistant event's usage from its stream.
func probeTools(t *testing.T, extra ...string) toolsProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	argv := append([]string{"--print", "reply ok", "--output-format", claude.OutputFormat, "--verbose", "--permission-mode", claude.PermissionMode}, extra...)
	cmd := exec.CommandContext(ctx, *claudeProgram, argv...)
	cmd.Dir = t.TempDir()
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("claude %q: %v", argv, err)
	}
	var p toolsProbe
	var inited, assisted bool
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var ev struct {
			Type, Subtype string
			Tools         []string
			Message       struct {
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				}
			}
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "system" && ev.Subtype == "init" && !inited:
			p.Tools, inited = ev.Tools, true
		case ev.Type == "assistant" && !assisted:
			u := ev.Message.Usage
			p.Input, p.CacheCreation, p.CacheRead, assisted = u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens, true
		}
	}
	if !inited || !assisted {
		t.Fatalf("claude %q stream lacks an init event (%t) or an assistant event (%t):\n%s", argv, inited, assisted, out)
	}
	return p
}

// TestLiveClaudeTools is the probe that DisallowedTools leave the context, not only that their calls are denied:
// it runs a trivial print-mode prompt without and then with ClaudeArgs' --disallowedTools and passes only when
// every dropped tool is missing from the second init event's tools, every kept tool present before is still there,
// and the first call's prompt (input plus cache creation plus cache read tokens) is at least 3,000 tokens smaller.
// Its log is the evidence in docs/evidence. Run it by hand with
// GUILDHALL_CLAUDE_LIVE=1 go test ./internal/adapter/claude -run TestLiveClaudeTools -v -claude-program "$(command -v claude)"
// or, where only `go test` is granted, with -claude-live in place of the variable.
func TestLiveClaudeTools(t *testing.T) {
	if os.Getenv(LiveEnv) != "1" && !*claudeLive {
		t.Skipf("set %s=1 or -claude-live to run two real Claude Code calls", LiveEnv)
	}
	if !filepath.IsAbs(*claudeProgram) {
		t.Fatalf("-claude-program %q; want the real claude's absolute path, since the test's guard shadows claude on PATH", *claudeProgram)
	}
	version, err := exec.Command(*claudeProgram, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --version: %v\n%s", err, version)
	}
	before := probeTools(t)
	after := probeTools(t, claude.DisallowedToolsArgs()...)
	t.Logf("claude --version: %s", strings.TrimSpace(string(version)))
	t.Logf("--disallowedTools %s", strings.Join(claude.DisallowedTools, ","))
	for _, r := range []struct {
		name string
		p    toolsProbe
	}{{"before", before}, {"after", after}} {
		t.Logf("%s tools (%d): %s", r.name, len(r.p.Tools), strings.Join(r.p.Tools, ","))
		t.Logf("%s first call: input %d, cache_creation %d, cache_read %d, prompt %d", r.name, r.p.Input, r.p.CacheCreation, r.p.CacheRead, r.p.prompt())
	}
	delta := before.prompt() - after.prompt()
	t.Logf("prompt delta: %d tokens; cache_creation delta: %d", delta, before.CacheCreation-after.CacheCreation)
	for _, tool := range claude.DisallowedTools {
		if slices.Contains(after.Tools, tool) {
			t.Errorf("after: %s is still in the init tools", tool)
		}
	}
	for _, tool := range keptTools {
		if slices.Contains(before.Tools, tool) && !slices.Contains(after.Tools, tool) {
			t.Errorf("after: kept tool %s is missing from the init tools", tool)
		}
	}
	// The core kept tools must be present outright, so a probe on an install without them cannot pass vacuously.
	// Agent is reported as Task by some Claude Code versions, so either name counts.
	for _, tool := range []string{"Skill", "ToolSearch", "WebSearch", "WebFetch", "Monitor", "TaskStop", "NotebookEdit"} {
		if !slices.Contains(after.Tools, tool) {
			t.Errorf("after: core kept tool %s is missing from the init tools", tool)
		}
	}
	if !slices.Contains(after.Tools, "Agent") && !slices.Contains(after.Tools, "Task") {
		t.Errorf("after: neither Agent nor Task is in the init tools")
	}
	if delta < 3000 {
		t.Errorf("first-call prompt shrank by %d tokens; want at least 3000", delta)
	}
}

// userEntries are the names in the user's Claude directory dir, such as ~/.claude/skills or ~/.claude/agents, each
// without a .md extension: the user skills and agents an Artisan must not inherit.
func userEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	return names
}

// isolationProbe is what one print-mode run reports: its init event, the hook events in its stream and its first
// assistant event's usage.
type isolationProbe struct {
	Init                            []byte
	Tools, SlashCommands, Skills    []string
	Agents, MCPServers, Hooks       []string
	Input, CacheCreation, CacheRead int
	Text                            string
}

func (p isolationProbe) prompt() int { return p.Input + p.CacheCreation + p.CacheRead }

// memoryCanary is the word the project CLAUDE.md TestLiveClaudeIsolation writes asks for, so a reply carrying it
// shows the memory file was loaded.
const memoryCanary = "PINEAPPLE"

// probeIsolation runs program with argv in dir and reads the stream-json events isolationProbe reports.
func probeIsolation(t *testing.T, program, dir string, argv []string) isolationProbe {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Dir, cmd.Stderr = dir, os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("claude %q: %v\n%s", argv[2:], err, out)
	}
	var p isolationProbe
	var sawInit, sawUsage bool
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var event struct {
			Type          string   `json:"type"`
			Subtype       string   `json:"subtype"`
			HookName      string   `json:"hook_name"`
			HookEvent     string   `json:"hook_event"`
			Tools         []string `json:"tools"`
			SlashCommands []string `json:"slash_commands"`
			Skills        []string `json:"skills"`
			Agents        []string `json:"agents"`
			MCPServers    []struct {
				Name string `json:"name"`
			} `json:"mcp_servers"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Usage *struct {
					InputTokens              int `json:"input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &event) != nil {
			continue
		}
		switch {
		case event.Type == "system" && event.Subtype == "init" && !sawInit:
			sawInit = true
			p.Init = slices.Clone(sc.Bytes())
			p.Tools, p.SlashCommands, p.Skills, p.Agents = event.Tools, event.SlashCommands, event.Skills, event.Agents
			for _, s := range event.MCPServers {
				p.MCPServers = append(p.MCPServers, s.Name)
			}
		case event.Type == "system" && strings.Contains(event.Subtype, "hook"):
			p.Hooks = append(p.Hooks, string(sc.Bytes()))
		case event.Type == "assistant":
			for _, c := range event.Message.Content {
				if c.Type == "text" {
					p.Text += c.Text
				}
			}
			if event.Message.Usage != nil && !sawUsage {
				sawUsage = true
				u := event.Message.Usage
				p.Input, p.CacheCreation, p.CacheRead = u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawInit || !sawUsage {
		t.Fatalf("claude %q stream lacks an init event (%t) or an assistant event (%t):\n%s", argv[2:], sawInit, sawUsage, out)
	}
	return p
}

// TestLiveClaudeIsolation runs a trivial print session with ClaudeArgs and the generated settings, as the wrapper
// launches an Artisan, and before it the same session without --setting-sources and the strict MCP config, as the
// wrapper launched one before d191. In the isolated run the system init event must list no MCP server or mcp__ tool
// and none of the user's skills or agents, and no hook may run. It logs both runs' init events, skills, agents, MCP
// servers, hook events and first-call tokens for the evidence note (docs/evidence/artisan-isolation.md). Run it by
// hand with
// GUILDHALL_CLAUDE_LIVE=1 go test ./internal/adapter/claude -run TestLiveClaudeIsolation -v -timeout 10m -claude-program "$(command -v claude)"
// or, where only `go test` is granted, with -claude-live in place of the variable.
func TestLiveClaudeIsolation(t *testing.T) {
	program := liveClaude(t)
	spec := testSpec(t)
	a := args(spec)
	if err := os.Mkdir(claude.ModelDir(spec.ResultFile), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := config.ParseClaudeSkills(`{"preset":"builder-v1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claude.ArtisanSettingsPath(spec.ResultFile), claude.ArtisanPolicySettings(grant.GuildhallRules, p), 0o600); err != nil {
		t.Fatal(err)
	}
	// A project CLAUDE.md in the working directory, as a repository's would be: the reply shows whether it loads.
	if err := os.WriteFile(filepath.Join(spec.WorkDir, "CLAUDE.md"), []byte("After any reply, add the word "+memoryCanary+" on its own line.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := claude.ClaudeArgs(a)
	after[1] = "This is a Guildhall isolation check. Reply with the single word ok and use no tools."
	isolation := []string{"--setting-sources", claude.SettingSources, "--strict-mcp-config", "--mcp-config", claude.MCPConfig}
	if !slices.Equal(after[len(after)-len(isolation):], isolation) {
		t.Fatalf("ClaudeArgs %q do not end with %q", after, isolation)
	}
	before := slices.Clone(after[:len(after)-len(isolation)])
	// Print-mode stream-json reports hook lifecycle events only with --include-hook-events; without it the
	// "no hook ran" check below could never fail.
	before, after = append(before, "--include-hook-events"), append(after, "--include-hook-events")
	version, err := exec.Command(program, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("claude --version: %v\n%s", err, version)
	}
	t.Logf("claude --version: %s", strings.TrimSpace(string(version)))
	runs := []struct {
		name string
		p    isolationProbe
	}{{"before", probeIsolation(t, program, spec.WorkDir, before)}, {"after", probeIsolation(t, program, spec.WorkDir, after)}}
	for _, r := range runs {
		t.Logf("%s init event: %s", r.name, r.p.Init)
		t.Logf("%s MCP servers (%d): %s", r.name, len(r.p.MCPServers), strings.Join(r.p.MCPServers, ","))
		t.Logf("%s skills (%d): %s", r.name, len(r.p.Skills), strings.Join(r.p.Skills, ","))
		t.Logf("%s agents (%d): %s", r.name, len(r.p.Agents), strings.Join(r.p.Agents, ","))
		t.Logf("%s tools (%d)", r.name, len(r.p.Tools))
		t.Logf("%s hook events (%d)", r.name, len(r.p.Hooks))
		for _, h := range r.p.Hooks {
			if len(h) > 400 {
				h = h[:400] + "..."
			}
			t.Logf("%s hook event: %s", r.name, h)
		}
		t.Logf("%s first call: input %d, cache_creation %d, cache_read %d, prompt %d", r.name, r.p.Input, r.p.CacheCreation, r.p.CacheRead, r.p.prompt())
		t.Logf("%s reply %q; project CLAUDE.md loaded: %t", r.name, r.p.Text, strings.Contains(r.p.Text, memoryCanary))
	}
	t.Logf("prompt delta: %d tokens", runs[0].p.prompt()-runs[1].p.prompt())
	got := runs[1].p
	if len(got.MCPServers) > 0 {
		t.Errorf("MCP servers %q; want none", got.MCPServers)
	}
	for _, tool := range got.Tools {
		if strings.HasPrefix(tool, "mcp__") {
			t.Errorf("MCP tool %s; want none", tool)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range userEntries(t, filepath.Join(home, ".claude", "skills")) {
		if slices.Contains(got.Skills, name) || slices.Contains(got.SlashCommands, name) {
			t.Errorf("user skill %s is loaded", name)
		}
	}
	for _, name := range userEntries(t, filepath.Join(home, ".claude", "agents")) {
		if slices.Contains(got.Agents, name) {
			t.Errorf("user agent %s is loaded", name)
		}
	}
	for _, h := range got.Hooks {
		t.Errorf("a hook ran: %s", h)
	}
	// An init event's memory_paths names the auto memory directory only when auto memory is on.
	if bytes.Contains(got.Init, []byte(`"memory_paths"`)) {
		t.Errorf("auto memory is on; want autoMemoryEnabled false to hold")
	}
}

// liveClaude skips the test unless LiveEnv is 1 or -claude-live is set, and returns the real Claude Code it runs:
// -claude-program, an absolute path, since the test binary's claude guard shadows every claude on PATH.
func liveClaude(t *testing.T) string {
	t.Helper()
	if os.Getenv(LiveEnv) != "1" && !*claudeLive {
		t.Skipf("set %s=1 or -claude-live to run a real Claude Code session; it makes a model call", LiveEnv)
	}
	if !filepath.IsAbs(*claudeProgram) {
		t.Fatalf("-claude-program %q; want the real claude's absolute path, since the test's guard shadows claude on PATH", *claudeProgram)
	}
	return *claudeProgram
}
