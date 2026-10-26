// Package claudetest is a stand-in Claude Code for tests: the test binary
// itself, installed on PATH under the name `claude`, plays a print-mode
// session that makes no model calls, or an interactive Director session
// scripted by `claude-stub:` lines of its role prompt (Script). It checks the arguments the Claude
// adapter passes, reads the prompt file and draft result file the
// instruction names, and acts on one prompt line `claude-stub: <behaviour>`,
// an adaptertest.Behaviour; a prompt without one succeeds.
package claudetest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/interp"
)

// Directive starts the prompt line that chooses the stub's behaviour.
const Directive = "claude-stub:"

// Summary is the summary of every result the stub writes.
const Summary = "The stub did the task."

// Usage is the usage every print-mode stub session reports in its JSON output (Output): fixed numbers, so tests
// of what the wrapper records are deterministic.
var Usage = adapter.Usage{Tokens: adapter.Measured, InputTokens: 1200, OutputTokens: 340, CacheCreationInputTokens: 5000, CacheReadInputTokens: 18000, Cost: adapter.Estimated, CostUSD: 0.0421}

// Denied is the stub behaviour that succeeds after print mode denied it one shell command, `go test ./...`, which
// its result message reports as Claude Code's does.
const Denied = "denied"

// Output is the print-mode JSON output the stub writes on standard output when it exits, shaped as Claude Code's
// result message, reporting Usage.
func Output() []byte { return output(nil) }

// output is Output reporting denials as the message's permission_denials.
func output(denials []any) []byte {
	msg := map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "num_turns": 3,
		"result": Summary, "session_id": "00000000-0000-0000-0000-000000000000",
		"total_cost_usd": Usage.CostUSD,
		"usage": map[string]any{
			"input_tokens": Usage.InputTokens, "output_tokens": Usage.OutputTokens,
			"cache_creation_input_tokens": Usage.CacheCreationInputTokens, "cache_read_input_tokens": Usage.CacheReadInputTokens,
			"service_tier": "standard",
		},
	}
	if denials != nil {
		msg["permission_denials"] = denials
	}
	data, _ := json.Marshal(msg)
	return append(data, '\n')
}

// HangLimit bounds how long a hanging stub works when nothing stops it.
const HangLimit = 2 * time.Minute

// Instruction lines the stub reads the attempt's files from.
const (
	PromptLine = "Prompt file: "
	DraftLine  = "Result file: "
)

// Prompt is the prompt that makes the stub produce b.
func Prompt(b adaptertest.Behaviour) string {
	return "Do the task.\n" + Directive + " " + string(b) + "\n"
}

// Script is the role prompt that makes the interactive stub play steps in order (fake.RunScript).
func Script(steps ...string) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(Directive + " " + s + "\n")
	}
	return b.String()
}

// Install puts the stub on PATH as `claude` for t and its child processes,
// ahead of any real Claude Code, and names it in interp.ClaudeStubEnv so the
// interpreter's test-binary guard admits the claude adapter.
func Install(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := claude.Program
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.Link(exe, dst); err != nil {
		if err := copyFile(exe, dst); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(interp.ClaudeStubEnv, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// RunIfStub makes the test binary act as the stub, and exit, when it was
// started as `claude`. Call it first in TestMain.
func RunIfStub() {
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if base != claude.Program {
		return
	}
	if err := stub(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "claude stub: %v\n", err)
		os.Exit(exitCode(err))
	}
	os.Exit(0)
}

// errExit is the exit-error behaviour's failing exit after its draft.
type errExit struct{}

func (errExit) Error() string { return "simulated failing exit after writing the result" }

func exitCode(err error) int {
	if _, ok := err.(errExit); ok {
		return 1
	}
	return 2
}

// stub checks args as the Claude adapter passes them and plays the prompt's
// behaviour into the draft the instruction names.
func stub(args []string) error {
	if slices.Equal(args, []string{"--version"}) {
		fmt.Println("2.1.310 (Claude Code)")
		return nil
	}
	if len(args) > 0 && args[0] == "--append-system-prompt-file" {
		os.Exit(session(args))
	}
	if len(args) < 2 || args[0] != "--print" {
		return fmt.Errorf("want --print and an instruction first, got %q", args)
	}
	instruction, flags := args[1], args[2:]
	i := slices.Index(flags, "--permission-mode")
	if i < 0 || i+1 >= len(flags) || flags[i+1] != claude.PermissionMode {
		return fmt.Errorf("want --permission-mode %s, got %q", claude.PermissionMode, flags)
	}
	if i := slices.Index(flags, "--output-format"); i < 0 || i+1 >= len(flags) || flags[i+1] != "stream-json" {
		return fmt.Errorf("want --output-format stream-json, got %q", flags)
	}
	if !slices.Contains(flags, "--verbose") {
		return fmt.Errorf("stream-json requires --verbose, got %q", flags)
	}
	var promptFile, draft string
	sc := bufio.NewScanner(strings.NewReader(instruction))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), PromptLine); ok {
			promptFile = v
		}
		if v, ok := strings.CutPrefix(sc.Text(), DraftLine); ok {
			draft = v
		}
	}
	if promptFile == "" || draft == "" {
		return fmt.Errorf("instruction names no prompt or result file: %q", instruction)
	}
	added := addDirs(flags)
	for _, f := range []string{promptFile, draft} {
		if !slices.Contains(added, filepath.Dir(f)) {
			return fmt.Errorf("%s is outside the added directories %q", f, added)
		}
	}
	granted, err := settings(flags, added)
	if err != nil {
		return err
	}
	data, err := json.Marshal(granted)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(draft), GrantedFile), data, 0o600); err != nil {
		return err
	}
	given, err := settingsFlags(flags)
	if err != nil {
		return err
	}
	if data, err = json.Marshal(given); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(draft), SettingsFile), data, 0o600); err != nil {
		return err
	}
	id, err := identity(instruction)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		return err
	}
	b := adaptertest.Succeed
	sc = bufio.NewScanner(strings.NewReader(string(prompt)))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), Directive); ok {
			b = adaptertest.Behaviour(strings.TrimSpace(v))
		}
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"type":"assistant","message":{"content":[{"type":"text","text":"I am inspecting the task."},{"type":"tool_use","name":"Read","input":{"file_path":"src/main.go"}},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	if b != adaptertest.Hang {
		// Claude Code prints its result message as it exits, whatever the session did.
		var denials []any
		if b == Denied {
			denials = []any{map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_stub", "tool_input": map[string]any{"command": "go test ./..."}}}
		}
		defer func() { _, _ = os.Stdout.Write(output(denials)) }()
	}
	result := map[string]any{
		"schema": adapter.ResultSchema, "version": adapter.ResultVersion,
		"task_id": id.TaskID, "attempt_id": id.AttemptID,
		"status": "succeeded", "summary": Summary, "outputs": []any{},
	}
	switch b {
	case adaptertest.Succeed, adaptertest.ExitErrorAfterResult, Denied:
	case adaptertest.Fail:
		result["status"] = "failed"
	case adaptertest.WrongIdentity:
		result["attempt_id"] = id.AttemptID + "-other"
	case adaptertest.NoResult:
		return nil
	case adaptertest.Malformed:
		return os.WriteFile(draft, []byte(`{"schema": "`+adapter.ResultSchema+`", "version": 1, "task_id": `), 0o600)
	case adaptertest.Hang:
		hang()
		return nil
	default:
		return fmt.Errorf("unknown behaviour %q", b)
	}
	data, err = json.Marshal(result)
	if err != nil {
		return err
	}
	if err := os.WriteFile(draft, data, 0o600); err != nil {
		return err
	}
	if b == adaptertest.ExitErrorAfterResult {
		return errExit{}
	}
	return nil
}

// SettingsFile is the record, in the model directory, of the --model and --effort a print-mode stub session was
// given, as the JSON of an adapter.Settings.
const SettingsFile = "claude-stub-settings.json"

// Settings returns the model and effort the stub's print-mode session for the result file result was given; a
// session given neither has the zero Settings.
func Settings(result string) (adapter.Settings, error) {
	data, err := os.ReadFile(filepath.Join(claude.ModelDir(result), SettingsFile))
	if err != nil {
		return adapter.Settings{}, err
	}
	var s adapter.Settings
	err = json.Unmarshal(data, &s)
	return s, err
}

// settingsFlags reads the one --model and the one --effort in flags, each optional; an effort must be one of
// claude.Efforts, as Claude Code's own --effort refuses any other.
func settingsFlags(flags []string) (adapter.Settings, error) {
	var s adapter.Settings
	for _, f := range []struct {
		name string
		dst  *string
	}{{"--model", &s.Model}, {"--effort", &s.Effort}} {
		i := slices.Index(flags, f.name)
		if i < 0 {
			continue
		}
		if i+1 >= len(flags) || flags[i+1] == "" || slices.Contains(flags[i+1:], f.name) {
			return s, fmt.Errorf("want at most one %s with a value, got %q", f.name, flags)
		}
		*f.dst = flags[i+1]
	}
	if s.Effort != "" && !slices.Contains(claude.Efforts, s.Effort) {
		return s, fmt.Errorf("--effort %q is not one of %q", s.Effort, claude.Efforts)
	}
	return s, nil
}

// GrantedFile is the record, in the model directory, of the allowed tools a print-mode stub session was granted.
const GrantedFile = "claude-stub-granted.json"

// Granted returns the allowed tools the stub's print-mode session for the result file result was granted, in
// order, as the --settings file it was given lists them; a session given no settings file was granted none.
func Granted(result string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(claude.ModelDir(result), GrantedFile))
	if err != nil {
		return nil, err
	}
	var granted []string
	if err := json.Unmarshal(data, &granted); err != nil {
		return nil, err
	}
	return granted, nil
}

// settings reads the allow rules of the one --settings file in flags: an absolute path outside the added
// directories, holding a permissions allow list (possibly empty) and optional skill-policy controls.
// No --settings grants none.
func settings(flags, added []string) ([]string, error) {
	i := slices.Index(flags, "--settings")
	if i < 0 {
		return []string{}, nil
	}
	if i+1 >= len(flags) || slices.Contains(flags[i+1:], "--settings") {
		return nil, fmt.Errorf("want one --settings file, got %q", flags)
	}
	path := flags[i+1]
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("--settings %q must be an absolute file", path)
	}
	for _, d := range added {
		if rel, err := filepath.Rel(d, path); err == nil && !strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("--settings %s is inside the added directory %s, which Claude may write", path, d)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s struct {
		DisableBundledSkills bool              `json:"disableBundledSkills"`
		SyncClaudeAiSkills   bool              `json:"syncClaudeAiSkills"`
		SkillOverrides       map[string]string `json:"skillOverrides"`
		EnabledPlugins       map[string]bool   `json:"enabledPlugins"`
		Permissions          struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("--settings %s: %w", path, err)
	}
	if s.Permissions.Allow == nil {
		return nil, fmt.Errorf("--settings %s must declare an allow list, even when empty", path)
	}
	return s.Permissions.Allow, nil
}

// addDirs are the directories after each --add-dir.
func addDirs(flags []string) []string {
	var dirs []string
	for i, f := range flags {
		if f != "--add-dir" {
			continue
		}
		for _, d := range flags[i+1:] {
			if strings.HasPrefix(d, "--") {
				break
			}
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// identity reads the task and attempt ids the instruction's result template
// names.
func identity(instruction string) (adapter.Identity, error) {
	start := strings.Index(instruction, "{")
	end := strings.Index(instruction[max(start, 0):], "}")
	if start < 0 || end < 0 {
		return adapter.Identity{}, fmt.Errorf("instruction has no result template: %q", instruction)
	}
	var tmpl struct {
		Task    string `json:"task_id"`
		Attempt string `json:"attempt_id"`
	}
	if err := json.Unmarshal([]byte(instruction[start:start+end+1]), &tmpl); err != nil {
		return adapter.Identity{}, fmt.Errorf("result template: %w", err)
	}
	return adapter.Identity{TaskID: tmpl.Task, AttemptID: tmpl.Attempt}, nil
}

// hang works without a result until stopped, its parent (the wrapper) ends,
// or HangLimit, so a stub whose wrapper was killed does not linger.
func hang() {
	deadline := time.Now().Add(HangLimit)
	gone := parentGone()
	for time.Now().Before(deadline) {
		select {
		case <-gone:
			return
		case <-time.After(50 * time.Millisecond): // timing:allow parent-liveness polling; HangLimit bounds the fixture lifetime.
		}
	}
}

// session plays an interactive Director session: it checks the arguments claude.Adapter.Session passes and the
// session directory's settings, then plays the role prompt's Directive lines with fake.RunScript over standard
// input. Refused arguments exit 2.
func session(args []string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "claude stub: "+format+"\n", a...)
		return 2
	}
	if len(args) < 6 || args[2] != "--add-dir" {
		return fail("want --append-system-prompt-file <role> --add-dir <copy> [--model <m>] -- <first message>, got %q", args)
	}
	role, copyDir, rest := args[1], args[3], args[4:]
	separator := slices.Index(rest, "--")
	if separator < 0 {
		return fail("want -- followed by one first message line last, got %q", rest)
	}
	given, err := settingsFlags(rest[:separator])
	if err != nil {
		return fail("session settings flags: %v", err)
	}
	for i := 0; i < separator; i += 2 {
		if i+1 >= separator || (rest[i] != "--model" && rest[i] != "--effort" && rest[i] != "--settings" && rest[i] != "--plugin-dir") {
			return fail("unknown session flag: %q", rest[i:separator])
		}
	}
	rest = rest[separator:]
	data, err := json.Marshal(given)
	if err != nil {
		return fail("settings record: %v", err)
	}
	if err := os.WriteFile(fake.SettingsFile, data, 0o600); err != nil {
		return fail("settings record: %v", err)
	}
	if len(rest) != 2 || rest[0] != "--" || strings.TrimSpace(rest[1]) == "" || strings.ContainsAny(rest[1], "\r\n") {
		return fail("want -- followed by one first message line last, got %q", rest)
	}
	if !filepath.IsAbs(role) || !filepath.IsAbs(copyDir) {
		return fail("role %q and copy %q must be absolute", role, copyDir)
	}
	if got := os.Getenv(adapter.CopyEnv); got != copyDir {
		return fail("%s = %q, want the added copy %q", adapter.CopyEnv, got, copyDir)
	}
	// Opt-in trust gate: a fresh working directory cannot consume the startup message.
	// The test supplies the one directory whose approval a person has already saved.
	if trusted := os.Getenv("GUILDHALL_TEST_CLAUDE_TRUSTED_DIR"); trusted != "" {
		cwd, err := os.Getwd()
		trusted, trustErr := filepath.EvalSymlinks(trusted)
		if err == nil {
			cwd, err = filepath.EvalSymlinks(cwd)
		}
		if err != nil || trustErr != nil || cwd != trusted {
			fmt.Fprintln(os.Stderr, "Is this a project you created or one you trust?")
			return 1
		}
	}
	settings, err := os.ReadFile(filepath.Join(".claude", "settings.json"))
	if err != nil || !json.Valid(settings) {
		return fail("session settings: %v", err)
	}
	text, err := os.ReadFile(role)
	if err != nil {
		return fail("%v", err)
	}
	return fake.RunScript(Directive, string(text), os.Stdin, os.Stdout, os.Stderr)
}
