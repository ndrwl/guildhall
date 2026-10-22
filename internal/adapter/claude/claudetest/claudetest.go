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
	if i := slices.Index(flags, "--output-format"); i < 0 || i+1 >= len(flags) || flags[i+1] != claude.OutputFormat {
		return fmt.Errorf("want --output-format %s, got %q", claude.OutputFormat, flags)
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
	data, err := json.Marshal(result)
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
		case <-time.After(50 * time.Millisecond):
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
	if len(args) < 5 || args[2] != "--add-dir" {
		return fail("want --append-system-prompt-file <role> --add-dir <copy> [--model <m>] <first message>, got %q", args)
	}
	role, copyDir, rest := args[1], args[3], args[4:]
	if len(rest) == 3 && rest[0] == "--model" && rest[1] != "" {
		rest = rest[2:]
	}
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" || strings.ContainsAny(rest[0], "\r\n") {
		return fail("want one first message line last, got %q", rest)
	}
	if !filepath.IsAbs(role) || !filepath.IsAbs(copyDir) {
		return fail("role %q and copy %q must be absolute", role, copyDir)
	}
	if got := os.Getenv(adapter.CopyEnv); got != copyDir {
		return fail("%s = %q, want the added copy %q", adapter.CopyEnv, got, copyDir)
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
