package codex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/adapter/codex/codextest"
	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/workbench"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// answers are the contracts the shipped `guildhall codex-artisan` runs under: the judge's alone.
var answers = codex.Answers{workflow.JudgeStep: quality.CodexAnswer}

// TestMain lets the test binary act as the stub `codex` and as `guildhall codex-artisan`, so the suites run the
// shipped wrapper over a Codex CLI that makes no model calls.
func TestMain(m *testing.M) {
	codextest.RunIfStub()
	if len(os.Args) > 1 && os.Args[1] == codex.Subcommand {
		os.Exit(codex.Main(os.Args[2:], answers, os.Stdout, os.Stderr))
	}
	os.Exit(testenv.RunWithGuards(m))
}

func subject(t *testing.T) adaptertest.Subject {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codextest.Install(t)
	return adaptertest.Subject{
		Runtime:      codex.Adapter{Executable: exe},
		Capabilities: adapter.Capabilities{},
		Prompt:       codextest.Prompt,
		Attempt:      codextest.Attempt,
		WritesResult: true,
	}
}

func TestCodexRuntimeConformance(t *testing.T) {
	adaptertest.RunConformance(t, subject)
}

// run runs the wrapper in-process for a judge attempt whose prompt plays b, with settings, in the attempt's working
// directory, and returns the spec and Run's error; lastOut and lastErr hold Run's stdout and stderr.
func run(t *testing.T, b adaptertest.Behaviour, settings adapter.Settings) (adapter.Spec, error) {
	t.Helper()
	codextest.Install(t)
	spec := codextest.Attempt(t, codextest.Prompt(b))
	return spec, runSpec(t, spec, settings, answers)
}

// lastOut and lastErr are the stdout and stderr of the last in-process Run; tests using them are not parallel.
var lastOut, lastErr bytes.Buffer

func runSpec(t *testing.T, spec adapter.Spec, settings adapter.Settings, ans codex.Answers) error {
	t.Helper()
	t.Chdir(spec.WorkDir)
	lastOut.Reset()
	lastErr.Reset()
	a := codex.Args{Identity: spec.Identity, PromptFile: spec.PromptFile, ResultFile: spec.ResultFile, Settings: settings}
	return codex.Run(context.Background(), a, ans, &lastOut, &lastErr)
}

func noResult(t *testing.T, spec adapter.Spec) {
	t.Helper()
	if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a result was published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, quality.AnswerFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an answer was written: %v", err)
	}
}

// TestRunPassesReadOnlySwitches: every run is `codex exec --json --ephemeral` in the read-only sandbox in the
// working directory, with apps, web search, user MCP servers and network off, the profile's model and effort, and
// the final message's schema.
func TestRunPassesReadOnlySwitches(t *testing.T) {
	spec, err := run(t, adaptertest.Succeed, adapter.Settings{Model: "gpt-6.1-sol", Effort: "low"})
	if err != nil {
		t.Fatalf("Run = %v; stderr %s", err, lastErr.String())
	}
	got, err := codextest.RecordedArgs(spec.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	schema := filepath.Join(filepath.Dir(spec.ResultFile), codex.OutputSchemaFile)
	want := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "-C", spec.WorkDir,
		"--disable", "apps", "-c", `web_search="disabled"`, "-c", "mcp_servers={}", "-c", "sandbox_workspace_write.network_access=false",
		"-m", "gpt-6.1-sol", "-c", "model_reasoning_effort=low", "--output-schema", schema}
	if !slices.Equal(got[:len(got)-1], want) {
		t.Errorf("codex args = %q\nwant %q", got[:len(got)-1], want)
	}
	if instruction := got[len(got)-1]; !strings.Contains(instruction, "Prompt file: "+spec.PromptFile) || !strings.Contains(instruction, "read-only sandbox") {
		t.Errorf("instruction = %q", instruction)
	}
	var s map[string]any
	if data, err := os.ReadFile(schema); err != nil || json.Unmarshal(data, &s) != nil || s["additionalProperties"] != false {
		t.Errorf("output schema = %v, %v", s, err)
	}
}

// TestRunWithoutSettingsOrSchemaFlag: no model or effort is passed when none is set, and no --output-schema when
// the installed `codex exec --help` does not list it; the sandbox switches are passed all the same.
func TestRunWithoutSettingsOrSchemaFlag(t *testing.T) {
	t.Setenv(codextest.HelpDropEnv, codex.OutputSchemaFlag)
	spec, err := run(t, adaptertest.Succeed, adapter.Settings{})
	if err != nil {
		t.Fatalf("Run = %v; stderr %s", err, lastErr.String())
	}
	got, err := codextest.RecordedArgs(spec.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"-m", codex.OutputSchemaFlag} {
		if slices.Contains(got, absent) {
			t.Errorf("codex args %q have %s", got, absent)
		}
	}
	if !slices.Contains(got, "mcp_servers={}") || !slices.Contains(got, "read-only") {
		t.Errorf("codex args %q lack the read-only switches", got)
	}
}

// TestRunWritesAnswerAndResult: a valid final message becomes judgement.json in the working directory, which the
// judge accepts, and a succeeded result naming it, with usage mapped from the completed turn and the version and
// progress in the logs.
func TestRunWritesAnswerAndResult(t *testing.T) {
	for _, b := range []adaptertest.Behaviour{adaptertest.Succeed, codextest.Fenced} {
		t.Run(string(b), func(t *testing.T) {
			spec, err := run(t, b, adapter.Settings{})
			if err != nil {
				t.Fatalf("Run = %v; stderr %s", err, lastErr.String())
			}
			r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != adapter.StatusSucceeded || r.Summary != codextest.Summary || !slices.Equal(r.Outputs, []adapter.Output{{Path: quality.AnswerFile}}) {
				t.Errorf("result = %+v", r)
			}
			doc, err := os.ReadFile(filepath.Join(spec.WorkDir, quality.AnswerFile))
			if err != nil {
				t.Fatal(err)
			}
			if err := quality.CheckAnswer(filepath.Dir(spec.PromptFile), doc); err != nil {
				t.Errorf("written answer refused: %v\n%s", err, doc)
			}
			u, err := adapter.ReadUsage(spec.ResultFile)
			if err != nil || u != codextest.Usage {
				t.Errorf("usage = %+v, %v; want %+v", u, err, codextest.Usage)
			}
			if !strings.Contains(lastErr.String(), "codex-cli 0.163.0") {
				t.Errorf("stderr does not name the version: %s", lastErr.String())
			}
			if !strings.Contains(lastOut.String(), "tool: shell /usr/bin/zsh -lc 'touch notes.txt'") || !strings.Contains(lastOut.String(), "exit 1") {
				t.Errorf("stdout lacks progress: %s", lastOut.String())
			}
		})
	}
}

// TestRunWritesWorkbenchEvents: each command's call, and its result with its real exit code, reach the Workbench
// event file, ending with the run's own exit.
func TestRunWritesWorkbenchEvents(t *testing.T) {
	spec, err := run(t, adaptertest.Succeed, adapter.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(codex.WorkbenchPath(spec.ResultFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	exits := map[string]int{}
	var kinds []workbench.Kind
	for _, e := range workbench.ReadAll(f) {
		kinds = append(kinds, e.Kind)
		if e.Kind == workbench.KindToolResult && e.Exit != nil {
			exits[e.ID] = *e.Exit
		}
	}
	if exits["item_1"] != 0 || exits["item_2"] != 1 || len(exits) != 2 {
		t.Errorf("tool result exits = %v, want item_1 0 and item_2 1", exits)
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != workbench.KindEnd || !slices.Contains(kinds, workbench.KindToolCall) || !slices.Contains(kinds, workbench.KindText) {
		t.Errorf("event kinds = %v", kinds)
	}
}

// TestRunWithoutUsageRecordsUnknown: a completed turn with no usage block records unknown usage, not a failure.
func TestRunWithoutUsageRecordsUnknown(t *testing.T) {
	spec, err := run(t, codextest.NoUsage, adapter.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	u, err := adapter.ReadUsage(spec.ResultFile)
	if err != nil || u != adapter.UnknownUsage {
		t.Errorf("usage = %+v, %v; want unknown", u, err)
	}
	if _, err := adapter.ReadResult(spec.ResultFile, spec.Identity); err != nil {
		t.Error(err)
	}
}

// TestRunPublishesFailed: a failed final message publishes a failed result with no outputs and writes no answer.
func TestRunPublishesFailed(t *testing.T) {
	spec, err := run(t, adaptertest.Fail, adapter.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
	if err != nil || r.Status != adapter.StatusFailed || len(r.Outputs) != 0 {
		t.Errorf("result = %+v, %v", r, err)
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, quality.AnswerFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed answer wrote %s: %v", quality.AnswerFile, err)
	}
}

// TestRunCarriesTheOutcome: under an answer contract that lets a step report an outcome, the instruction and the
// output schema name the field and a failed final message's outcome reaches the result, its null fields dropped;
// the judge's contract, which does not, publishes nothing for the same message.
func TestRunCarriesTheOutcome(t *testing.T) {
	codextest.Install(t)
	spec := codextest.Attempt(t, codextest.Prompt(codextest.Delivered))
	outcomes := quality.CodexAnswer
	outcomes.Outcomes = true
	if err := runSpec(t, spec, adapter.Settings{}, codex.Answers{workflow.JudgeStep: outcomes}); err != nil {
		t.Fatalf("Run = %v; stderr %s", err, lastErr.String())
	}
	r, err := adapter.ReadResult(spec.ResultFile, spec.Identity)
	want := adapter.Outcome{Kind: adapter.OutcomeAlreadyDelivered, ByPR: codextest.DeliveredBy}
	if err != nil || r.Status != adapter.StatusFailed || len(r.Outputs) != 0 || r.Outcome == nil || *r.Outcome != want {
		t.Errorf("result = %+v, %v; want a failed result with outcome %+v", r, err, want)
	}
	got, err := codextest.RecordedArgs(spec.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got[len(got)-1], `- "outcome": null, unless status is "failed"`) {
		t.Errorf("instruction does not name the outcome: %q", got[len(got)-1])
	}
	var s struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(spec.ResultFile), codex.OutputSchemaFile)); err != nil || json.Unmarshal(data, &s) != nil ||
		!slices.Contains(s.Required, "outcome") || !strings.Contains(string(s.Properties["outcome"]), `"already-delivered"`) {
		t.Errorf("output schema = %+v, %v", s, err)
	}

	spec, err = run(t, codextest.Delivered, adapter.Settings{})
	if !errors.Is(err, codex.ErrNoAnswer) || !strings.Contains(err.Error(), "reports no outcome") {
		t.Errorf("judge Run = %v, want ErrNoAnswer naming the outcome", err)
	}
	noResult(t, spec)
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(spec.ResultFile), codex.OutputSchemaFile)); err != nil || strings.Contains(string(data), "outcome") {
		t.Errorf("judge output schema names an outcome: %s, %v", data, err)
	}
}

// TestRunPublishesNothingWithoutAValidAnswer: a malformed final message, an answer the judge's contract refuses, a
// succeeded message with no answer, no final message, and a quota error with a failed turn each publish nothing,
// and the error names why.
func TestRunPublishesNothingWithoutAValidAnswer(t *testing.T) {
	for _, tc := range []struct {
		b    adaptertest.Behaviour
		want string
	}{
		{adaptertest.Malformed, "not the answer object"},
		{adaptertest.WrongIdentity, `criterion "H9" is not pending`},
		{codextest.NoAnswer, "has no answer"},
		{adaptertest.NoResult, "no final message"},
		{codextest.TurnFailed, codextest.QuotaMessage},
	} {
		t.Run(string(tc.b), func(t *testing.T) {
			spec, err := run(t, tc.b, adapter.Settings{})
			if !errors.Is(err, codex.ErrNoAnswer) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Run = %v, want ErrNoAnswer naming %q", err, tc.want)
			}
			noResult(t, spec)
			if _, err := adapter.ReadUsage(spec.ResultFile); err != nil {
				t.Errorf("usage: %v", err)
			}
		})
	}
	t.Run("the failed turn is named on stderr", func(t *testing.T) {
		_, err := run(t, codextest.TurnFailed, adapter.Settings{})
		if !strings.Contains(lastErr.String(), "Codex turn failed: "+codextest.QuotaMessage) {
			t.Errorf("Run = %v; stderr = %s", err, lastErr.String())
		}
	})
}

// TestRunRefusesBeforeRunning: an old or unreadable version, no login, a user configuration declaring MCP
// servers, a `codex exec` lacking a needed flag, and a step with no answer contract are each refused before
// `codex exec` runs.
func TestRunRefusesBeforeRunning(t *testing.T) {
	mcp := func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.files]\ncommand = \"files-mcp\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CODEX_HOME", home)
	}
	for _, tc := range []struct {
		name  string
		set   func(t *testing.T)
		want  error
		step  string
		label string
	}{
		{"older version", func(t *testing.T) { t.Setenv(codextest.VersionEnv, "codex-cli 0.162.9") }, codex.ErrVersion, "", "older than 0.163.0"},
		{"unparseable version", func(t *testing.T) { t.Setenv(codextest.VersionEnv, "codex 1.0") }, codex.ErrVersion, "", "not codex-cli X.Y.Z"},
		{"not logged in", func(t *testing.T) { t.Setenv(codextest.LoggedOutEnv, "1") }, codex.ErrNotLoggedIn, "", "Not logged in"},
		{"user MCP servers", mcp, codex.ErrUserMCPServers, "", "files"},
		{"missing flag", func(t *testing.T) { t.Setenv(codextest.HelpDropEnv, "--ephemeral") }, codex.ErrMissingFlag, "", "--ephemeral"},
		{"no answer contract", func(*testing.T) {}, codex.ErrNoAnswerContract, "review-1", "review-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codextest.Install(t)
			tc.set(t)
			spec := codextest.Attempt(t, codextest.Prompt(adaptertest.Succeed))
			if tc.step != "" {
				spec.AttemptID = tc.step
			}
			err := runSpec(t, spec, adapter.Settings{}, answers)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.label) {
				t.Errorf("Run = %v, want %v naming %q", err, tc.want, tc.label)
			}
			if _, err := codextest.RecordedArgs(spec.PromptFile); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("codex exec ran: %v", err)
			}
			noResult(t, spec)
		})
	}
}

// TestVersionFloor: the floor accepts its own version and newer ones, with or without a suffix.
func TestVersionFloor(t *testing.T) {
	codextest.Install(t)
	program, err := exec.LookPath(codex.Program)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"codex-cli 0.163.0", "codex-cli 0.164.0-alpha.1", "codex-cli 1.0.0"} {
		t.Setenv(codextest.VersionEnv, v)
		if got, err := codex.CheckVersion(context.Background(), program); err != nil || "codex-cli "+got != v {
			t.Errorf("CheckVersion for %q = %q, %v", v, got, err)
		}
	}
}

// TestRunRefusesExistingOrCancelled: an attempt with a result or a cancel marker runs nothing.
func TestRunRefusesExistingOrCancelled(t *testing.T) {
	codextest.Install(t)
	spec := codextest.Attempt(t, codextest.Prompt(adaptertest.Succeed))
	if err := adapter.MarkCancelled(spec.ResultFile, spec.Identity); err != nil {
		t.Fatal(err)
	}
	if err := runSpec(t, spec, adapter.Settings{}, answers); !errors.Is(err, adapter.ErrAttemptCancelled) {
		t.Errorf("cancelled Run = %v", err)
	}
	spec = codextest.Attempt(t, codextest.Prompt(adaptertest.Succeed))
	if err := os.WriteFile(spec.ResultFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runSpec(t, spec, adapter.Settings{}, answers); !errors.Is(err, adapter.ErrResultExists) {
		t.Errorf("Run over a result = %v", err)
	}
	if _, err := codextest.RecordedArgs(spec.PromptFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("codex exec ran: %v", err)
	}
}

// TestRunStopsOnCancelMarker: the cancel marker stops a running Codex, publishes nothing, and is not an error.
func TestRunStopsOnCancelMarker(t *testing.T) {
	codextest.Install(t)
	spec := codextest.Attempt(t, codextest.Prompt(adaptertest.Hang))
	done := make(chan error, 1)
	t.Chdir(spec.WorkDir)
	var out, errOut bytes.Buffer
	go func() {
		a := codex.Args{Identity: spec.Identity, PromptFile: spec.PromptFile, ResultFile: spec.ResultFile}
		done <- codex.Run(context.Background(), a, answers, &out, &errOut)
	}()
	deadline := time.Now().Add(testenv.Ceiling(30 * time.Second))
	for {
		if _, err := codextest.RecordedArgs(spec.PromptFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("codex exec never started")
		}
		time.Sleep(10 * time.Millisecond) // timing:allow waiting for the stub's start record.
	}
	if err := adapter.MarkCancelled(spec.ResultFile, spec.Identity); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil for a cancel", err)
		}
	case <-time.After(testenv.Ceiling(30 * time.Second)):
		t.Fatal("Run did not stop on the cancel marker")
	}
	if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cancelled attempt published: %v", err)
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, quality.AnswerFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a cancelled attempt wrote an answer: %v", err)
	}
}

// TestParseArgs: the wrapper's flags round-trip from Command, and a relative program or bad effort is refused.
func TestParseArgs(t *testing.T) {
	s := spec(t)
	s.Settings = adapter.Settings{Model: "gpt-6.1-sol", Effort: "xhigh"}
	program := filepath.Join(s.WorkDir, "codex") // absolute on every platform; "/opt/codex" is not absolute on Windows.
	cmd, err := codex.Adapter{Executable: "/bin/guildhall", Codex: program}.Command(s)
	if err != nil {
		t.Fatal(err)
	}
	a, err := codex.ParseArgs(cmd.Argv[2:])
	if err != nil || a.Identity != s.Identity || a.PromptFile != s.PromptFile || a.ResultFile != s.ResultFile || a.Codex != program || a.Settings != s.Settings {
		t.Errorf("ParseArgs(Command) = %+v, %v", a, err)
	}
	for _, bad := range [][]string{
		{"--task", "t", "--attempt", "quality-judge-1", "--prompt", s.PromptFile, "--result", s.ResultFile, "--codex", "codex"},
		{"--task", "t", "--attempt", "quality-judge-1", "--prompt", s.PromptFile, "--result", s.ResultFile, "--effort", "huge"},
		{"--task", "t", "--attempt", "quality-judge-1", "--prompt", "prompt.md", "--result", s.ResultFile},
		{"--task", "t", "--attempt", "quality-judge-1", "--prompt", s.PromptFile},
	} {
		if _, err := codex.ParseArgs(bad); err == nil {
			t.Errorf("ParseArgs(%q) = nil, want a refusal", bad)
		}
	}
}
