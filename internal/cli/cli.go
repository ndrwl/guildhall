// Package cli implements the guildhall command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
)

// version is set at release build time with
// -ldflags "-X github.com/ndrwl/guildhall/internal/cli.version=<tag>".
var version = "dev"

const usage = `Usage: guildhall <command>

Commands:
  version        print the guildhall version and platform
  doctor         report the platform and the tools guildhall uses
  store doctor <path>
                 check a store file read-only and count its records
  steward rollout ...
                 plan and apply rollouts of managed settings to enrolled Director copies
  messages send|list|read|ack --store <path> ...
                 send, list, read and acknowledge local Director and Artisan messages
  workflow approve|approvals --store <path> ...
                 record and list Guildmaster approvals of workflow revisions
  render <request.json>
                 render a prompt with embedded release skills first
  artisan start|stop|status|resume
                 start, stop, classify and resume one owned Artisan attempt
  artisan harness inventory|kill-owned|probe --lab
                 list, kill and probe an attempt's processes: lab test tooling
  fake-artisan   run the test-only fake Artisan
  task new|show|list|timeline|files|finding rule|review --store <path> ...
                 create, list, show and inspect Director tasks and record rulings and full-review requests
  task start --store <path> --config <file> --state <dir> ...
                 advance a task, launching its next ready stage
  step done --store <path> --config <file> --state <dir> ...
                 take one attempt's result file and advance its task
  decide list|show|answer --store <path> ...
                 list open decisions, show one and record the Guildmaster's answer
  help           print this help
`

// toolProbeTimeout bounds each external version command run by doctor.
const toolProbeTimeout = 10 * time.Second

// doctorTools are the external tools doctor looks for, with the arguments that
// make each print its version.
var doctorTools = []struct {
	name string
	args []string
}{
	{"git", []string{"--version"}},
	{"ssh", []string{"-V"}},
	{"claude", []string{"--version"}},
	{"herdr", []string{"--version"}},
}

// App holds the process boundaries the commands use, so tests can replace them.
type App struct {
	InstallSkills func(string) error
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	// LookPath resolves a tool name on PATH.
	LookPath func(file string) (string, error)
	// Output runs a command and returns its combined stdout and stderr.
	Output func(ctx context.Context, path string, args ...string) ([]byte, error)
	// Executable returns the path of the running guildhall binary.
	Executable func() (string, error)
	// Copier reaches enrolled machines for steward rollouts; nil means scp, found with LookPath.
	Copier steward.Copier
}

// New returns an App wired to the real process environment.
func New() *App {
	return &App{
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		LookPath: exec.LookPath,
		Output: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, args...).CombinedOutput()
		},
		Executable: os.Executable,
	}
}

// Version returns the release version injected at build time, else the module
// version the Go toolchain stamped into the build, else "dev".
func Version() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// Run executes the command named by args and returns the process exit code:
// 0 on success, 1 when output cannot be written and 2 on a usage error.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		return a.usageError("missing command")
	}
	cmd, rest := args[0], args[1:]
	if cmd == "steward" {
		return a.steward(rest)
	}
	var out string
	switch cmd {
	case "help", "-h", "--help":
		out = usage
	case "version":
		if len(rest) > 0 {
			return a.usageError("version takes no arguments")
		}
		out = fmt.Sprintf("guildhall %s %s/%s %s\n", Version(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	case "doctor":
		if len(rest) > 0 {
			return a.usageError("doctor takes no arguments")
		}
		out = a.doctor()
	case "store":
		if len(rest) != 2 || rest[0] != "doctor" {
			return a.usageError("usage: guildhall store doctor <path>")
		}
		return a.storeDoctor(rest[1])
	case "messages":
		return a.messages(rest)
	case "workflow":
		return a.workflowCmd(rest)
	case "render":
		return a.renderPrompt(rest)
	case "artisan":
		return a.artisan(rest)
	case "fake-artisan":
		return a.fakeArtisan(rest)
	case "task":
		if len(rest) > 0 && rest[0] == "start" {
			return a.taskStart(rest[1:])
		}
		return a.task(rest)
	case "step":
		return a.stepDone(rest)
	case "decide":
		return a.decide(rest)
	default:
		return a.usageError(fmt.Sprintf("unknown command %q", cmd))
	}
	if _, err := io.WriteString(a.Stdout, out); err != nil {
		return 1
	}
	return 0
}

func (a *App) usageError(msg string) int {
	// The exit code already reports the usage error if stderr is unwritable.
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n\n%s", msg, usage)
	return 2
}

// doctor returns one line per fact it checked. A missing tool is reported, not
// treated as a failure.
func (a *App) doctor() string {
	var b strings.Builder
	fmt.Fprintf(&b, "os: %s\narch: %s\nguildhall: %s\ngo: %s\n", runtime.GOOS, runtime.GOARCH, Version(), runtime.Version())
	for _, tool := range doctorTools {
		fmt.Fprintf(&b, "%s: %s\n", tool.name, a.probeTool(tool.name, tool.args))
	}
	return b.String()
}

func (a *App) probeTool(name string, args []string) string {
	path, err := a.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "not found on PATH"
		}
		if path != "" {
			return fmt.Sprintf("PATH lookup failed: %v (%s)", err, path)
		}
		return fmt.Sprintf("PATH lookup failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolProbeTimeout)
	defer cancel()
	out, err := a.Output(ctx, path, args...)
	if err != nil {
		return fmt.Sprintf("version check failed: %v (%s)", err, path)
	}
	line := firstLine(string(out))
	if line == "" {
		return fmt.Sprintf("version check printed nothing (%s)", path)
	}
	return fmt.Sprintf("%s (%s)", line, path)
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
