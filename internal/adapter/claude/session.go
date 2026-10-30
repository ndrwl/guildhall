package claude

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/clerk"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/lanterns"
)

// SettingsFile is the Claude Code project settings file of a Director session, relative to the session directory.
const SettingsFile = ".claude/settings.json"

// SkillsDir is where a Claude Code Director session's skills are rendered, relative to the session directory:
// Claude Code loads project skills from there.
func (Adapter) SkillsDir() string { return ".claude/skills" }

// Program is the program a Claude Code Director session needs on PATH.
func (Adapter) Program() string { return Program }

// readOnlyGit are the git subcommands a Director session may run with any arguments without asking.
var readOnlyGit = []string{"status", "log", "diff", "show", "rev-parse", "ls-files", "branch --list"}

// Settings is the session's Claude Code settings for the Director copy dir: Bash may run guildhall, the
// read-only git commands and `git remote -v` without asking but no git command writing an --output file, no
// edit tool may change the copy's config.toml or managed.toml, the UserPromptSubmit hook runs TurnStartCommand
// at the start of every turn, the Stop and StopFailure hooks TurnEndCommand at the end of every turn, the second
// when a turn ends in an API error, and the status line runs ContextReportCommand with --lanterns; each of these
// and WaitWakeCommand names the copy as CopyCommand does.
// It is JSON with a trailing newline, the same bytes for the same copy.
func Settings(copyDir string) []byte {
	allow := []string{"Bash(guildhall)", "Bash(guildhall *)"}
	for _, g := range readOnlyGit {
		allow = append(allow, "Bash(git "+g+")", "Bash(git "+g+" *)")
	}
	allow = append(allow, "Bash(git remote -v)")
	deny := []string{
		"Bash(git *--output*)",
		"Bash(guildhall claude-artisan:*)",
		"Bash(guildhall fake-artisan:*)",
		"Bash(guildhall fake-director:*)",
		"Bash(guildhall artisan start:*)",
	}
	for _, name := range []string{"config.toml", "managed.toml"} {
		deny = append(deny, "Edit("+absoluteRule(filepath.Join(copyDir, name))+")")
	}
	type permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	type command struct {
		Type        string `json:"type"`
		Command     string `json:"command"`
		Async       bool   `json:"async,omitempty"`
		AsyncRewake bool   `json:"asyncRewake,omitempty"`
		Timeout     int    `json:"timeout,omitempty"`
	}
	type matcher struct {
		Hooks []command `json:"hooks"`
	}
	waiter := command{Type: "command", Command: CopyCommand(WaitWakeCommand, copyDir), Async: true, AsyncRewake: true, Timeout: WaitWakeTimeout}
	turnStart := command{Type: "command", Command: CopyCommand(TurnStartCommand, copyDir)}
	turnEnd := command{Type: "command", Command: CopyCommand(TurnEndCommand, copyDir)}
	b, _ := json.MarshalIndent(struct {
		Permissions permissions          `json:"permissions"`
		Hooks       map[string][]matcher `json:"hooks"`
		StatusLine  command              `json:"statusLine"`
	}{permissions{allow, deny}, map[string][]matcher{
		"SessionStart":     {{[]command{waiter}}},
		"Notification":     {{[]command{waiter}}},
		"UserPromptSubmit": {{[]command{turnStart, waiter}}},
		"Stop":             {{[]command{turnEnd, waiter}}},
		"StopFailure":      {{[]command{turnEnd}}},
	}, command{Type: "command", Command: CopyCommand(ContextReportCommand, copyDir) + " --lanterns"}}, "", "  ")
	return append(b, '\n')
}

// CopyCommand is a hook or status-line command given the copy explicitly, `<command> --copy '<copy>'`, so it does
// not depend on the directory Claude Code runs it in (docs/decisions/copy-discovery.md). The copy is single-quoted
// for a POSIX shell, with any ' in it escaped.
func CopyCommand(command, copyDir string) string {
	return command + " --copy '" + strings.ReplaceAll(copyDir, "'", `'\''`) + "'"
}

// absoluteRule writes an absolute path as a Claude Code permission rule path: slash-separated after a leading
// "//", which Claude Code reads as the filesystem root rather than the settings file's directory, with a Windows
// drive in the POSIX form Claude Code matches it in, so C:\copy is //c/copy.
func absoluteRule(path string) string {
	p := filepath.ToSlash(path)
	if len(p) >= 2 && p[1] == ':' {
		p = strings.ToLower(p[:1]) + p[2:]
	}
	return "//" + strings.TrimLeft(p, "/")
}

// Session returns the Claude Code Director session for spec: `claude --append-system-prompt-file <role prompt>
// --add-dir <copy> --settings <session settings> --plugin-dir <bundled Lanterns> --plugin-dir <bundled Clerk> [--model <model>] [--effort <level>] -- <first message>` in the session directory with the session's environment,
// the interactive command shape beside the Artisan's print mode. Session refuses invalid specs and settings
// with no command and writes nothing; the caller writes
// SessionFiles before starting it.
func (a Adapter) Session(spec adapter.SessionSpec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if err := a.CheckSettings(adapter.Settings{Model: spec.Model, Effort: spec.Effort}); err != nil {
		return adapter.Command{}, err
	}
	prog := a.Claude
	if prog == "" {
		prog = Program
	}
	if prog != Program && !filepath.IsAbs(prog) {
		return adapter.Command{}, errors.New("claude adapter: Claude must be an absolute path")
	}
	argv := []string{prog, "--append-system-prompt-file", spec.RolePrompt, "--add-dir", spec.Copy, "--settings", filepath.Join(spec.Dir, SettingsFile)}
	argv = append(argv, "--plugin-dir", filepath.Join(spec.Dir, PluginDir), "--plugin-dir", filepath.Join(spec.Dir, ClerkPluginDir))
	argv = append(argv, settingsArgs(adapter.Settings{Model: spec.Model, Effort: spec.Effort})...)
	return adapter.Command{Argv: append(argv, "--", spec.FirstMessage), Dir: spec.Dir, Env: spec.SessionEnv()}, nil
}

// TurnEndHook reports true: Settings registers TurnStartCommand as the session's UserPromptSubmit hook and
// TurnEndCommand as its Stop and StopFailure hooks.
func (Adapter) TurnEndHook() bool { return true }

// TurnStartCommand and TurnEndCommand are the UserPromptSubmit and Stop (and StopFailure) hooks of a Director
// session. Settings gives each the copy with CopyCommand, so a turn's end reaches its supervisor wherever the
// session has moved its working directory.
const (
	TurnStartCommand = "guildhall director turn-started"
	TurnEndCommand   = "guildhall director turn-ended"
)

// VersionCommandFile is the Director session's /version slash command, relative to the session directory.
const VersionCommandFile = ".claude/commands/version.md"

// versionCommand runs `guildhall version`, or `guildhall version --startup` when its first argument is startup,
// has the model reply with only the output, then read and follow the startup file a further argument names.
//
//go:embed version.md
var versionCommand []byte

// StartupMessage is the first message of a Claude Code Director session whose startup instructions are at the
// absolute path startup: the /version command with startup and that path, so the version line comes first.
func StartupMessage(startup string) string { return fmt.Sprintf("/version startup %q", startup) }

// WaitWakeCommand is the Director session's async rewake hook (docs/decisions/director-wake-hook.md): it waits
// for the supervisor to hand it a wake and exits 2 with the counts-only line, which wakes the Director without
// touching its input box. Settings arms it on SessionStart, UserPromptSubmit, Stop and Notification.
const WaitWakeCommand = "guildhall director wait-wake"

// WaitWakeTimeout is the hook timeout of WaitWakeCommand, in seconds: 24 hours, which the Phase 0 probe showed
// Claude Code accepts (docs/evidence/director-input-2026-10-27.txt). WaitWakeRefresh is when a waiter still
// registered refreshes itself, five minutes before Claude Code would end it.
const (
	WaitWakeTimeout = 24 * 60 * 60
	WaitWakeRefresh = WaitWakeTimeout*time.Second - 5*time.Minute
)

// SessionFiles are the files a Claude Code Director session needs beside its skills, by slash path relative to
// the session directory: merged settings at SettingsFile, a policy receipt at SkillPolicyFile, the /version
// command at VersionCommandFile, the embedded Lanterns plugin at PluginDir and the embedded Clerk plugin at
// ClerkPluginDir.
func (a Adapter) SessionFiles(copyDir string) map[string][]byte {
	p := a.SkillPolicy
	if p == nil {
		policy, _ := config.ParseClaudeSkills(`{"preset":"director-v1"}`)
		policy.Key = config.ClaudeSkillsDirectorKey
		p = &policy
	}
	files := map[string][]byte{SettingsFile: mergePolicy(Settings(copyDir), *p), SkillPolicyFile: p.Receipt(),
		VersionCommandFile: append([]byte(nil), versionCommand...)}
	for name, data := range lanterns.Files() {
		files[filepath.ToSlash(filepath.Join(PluginDir, name))] = data
	}
	for name, data := range clerk.Files() {
		files[filepath.ToSlash(filepath.Join(ClerkPluginDir, name))] = data
	}
	files[CalmFile] = calmFile(copyDir)
	return files
}

// CalmFile tells the Lanterns plugin where the copy's Calm preference is, relative to the session directory; a
// plugin with no CalmFile offers no Calm (docs/lanterns.md).
const CalmFile = PluginDir + "/calm.json"

// CalmPreference is the copy's Calm preference file, relative to the copy.
const CalmPreference = "director/lanterns-calm"

// calmFile is CalmFile's JSON for copyDir: {"preference":"<copy>/director/lanterns-calm"}.
func calmFile(copyDir string) []byte {
	b, _ := json.Marshal(struct {
		Preference string `json:"preference"`
	}{filepath.Join(copyDir, filepath.FromSlash(CalmPreference))})
	return append(b, '\n')
}

// PluginDir is the bundled Guildhall plugin in a generated Director workspace.
const PluginDir = ".claude/plugins/lanterns"

// ClerkPluginDir is the bundled Clerk plugin in a generated Director workspace, beside Lanterns; the Guildmaster's
// own sessions load the same directory with --plugin-dir (docs/clerk.md).
const ClerkPluginDir = ".claude/plugins/clerk"

// ContextReportCommand is the status-line command of a Director session: Claude Code runs it after each
// assistant message with the session's status as JSON on standard input.
const ContextReportCommand = "guildhall director context-report"

// ContextReport reports true: the core turn-ended hook reports transcript context.
// Settings also registers ContextReportCommand with --lanterns as a display-only status line.
func (Adapter) ContextReport() bool { return true }

// ParseStatusLine reads the share of the context window in use from the JSON Claude Code passes a status-line
// command: context_window.used_percentage, rounded to the nearest whole percent and clamped to 0 to 100. It
// reports false, with no error, when the input has no used percentage yet, such as before the first message, and
// an error for input that is not a JSON object.
func ParseStatusLine(data []byte) (int, bool, error) {
	var in struct {
		ContextWindow *struct {
			UsedPercentage *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&in); err != nil {
		return 0, false, fmt.Errorf("claude adapter: status line input: %w", err)
	}
	if dec.More() {
		return 0, false, errors.New("claude adapter: status line input: data after the object")
	}
	if in.ContextWindow == nil || in.ContextWindow.UsedPercentage == nil {
		return 0, false, nil
	}
	return int(min(max(math.Round(*in.ContextWindow.UsedPercentage), 0), 100)), true, nil
}
