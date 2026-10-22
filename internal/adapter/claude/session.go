package claude

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
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
// at the start of every turn and the Stop hook TurnEndCommand at the end of every turn. It is JSON with a trailing newline, the same bytes for the same copy.
func Settings(copyDir string) []byte {
	allow := []string{"Bash(guildhall)", "Bash(guildhall *)"}
	for _, g := range readOnlyGit {
		allow = append(allow, "Bash(git "+g+")", "Bash(git "+g+" *)")
	}
	allow = append(allow, "Bash(git remote -v)")
	deny := []string{"Bash(git *--output*)"}
	for _, name := range []string{"config.toml", "managed.toml"} {
		deny = append(deny, "Edit("+absoluteRule(filepath.Join(copyDir, name))+")")
	}
	type permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	type command struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Hooks []command `json:"hooks"`
	}
	b, _ := json.MarshalIndent(struct {
		Permissions permissions          `json:"permissions"`
		Hooks       map[string][]matcher `json:"hooks"`
	}{permissions{allow, deny}, map[string][]matcher{
		"UserPromptSubmit": {{[]command{{"command", TurnStartCommand}}}},
		"Stop":             {{[]command{{"command", TurnEndCommand}}}},
	}}, "", "  ")
	return append(b, '\n')
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
// --add-dir <copy> [--model <model>] <first message>` in the session directory with the session's environment,
// the interactive command shape beside the Artisan's print mode. Effort is not passed: Claude Code takes it from
// its own settings. Session refuses an invalid spec with no command and writes nothing; the caller writes
// Settings at SettingsFile before starting it.
func (a Adapter) Session(spec adapter.SessionSpec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	prog := a.Claude
	if prog == "" {
		prog = Program
	}
	if prog != Program && !filepath.IsAbs(prog) {
		return adapter.Command{}, errors.New("claude adapter: Claude must be an absolute path")
	}
	argv := []string{prog, "--append-system-prompt-file", spec.RolePrompt, "--add-dir", spec.Copy}
	if spec.Model != "" {
		argv = append(argv, "--model", spec.Model)
	}
	return adapter.Command{Argv: append(argv, spec.FirstMessage), Dir: spec.Dir, Env: spec.SessionEnv()}, nil
}

// TurnEndHook reports true: Settings registers TurnStartCommand as the session's UserPromptSubmit hook and
// TurnEndCommand as its Stop hook.
func (Adapter) TurnEndHook() bool { return true }

// TurnStartCommand and TurnEndCommand are the UserPromptSubmit and Stop hooks of a Director session. They name
// no copy: the hooks inherit the session's GUILDHALL_COPY, so no path crosses the shell Claude Code runs hooks in.
const (
	TurnStartCommand = "guildhall director turn-started"
	TurnEndCommand   = "guildhall director turn-ended"
)

// SessionFiles are the files a Claude Code Director session needs beside its skills, by slash path relative to
// the session directory: Settings at SettingsFile.
func (Adapter) SessionFiles(copyDir string) map[string][]byte {
	return map[string][]byte{SettingsFile: Settings(copyDir)}
}
