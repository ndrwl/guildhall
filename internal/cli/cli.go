// Package cli implements the guildhall command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// version is set at release build time with
// -ldflags "-X github.com/ndrwl/guildhall/internal/cli.version=<tag>".
var version = "dev"

const usage = `Usage: guildhall <command>

Commands:
  version [--startup | --json] [--copy <dir>]
                 print the guildhall version, revision, latest merge and how far it is behind main
  doctor         report the platform and the tools guildhall uses
  store init <path>
                 create and initialize an empty store file
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
  fake-artisan   run the deterministic fake Artisan for explicit no-model demos/tests
  claude-artisan run one attempt in Claude Code; artisan start --adapter claude runs it
  fake-director  run the test-only fake Director session
  task new|show|list|timeline|files|finding rule|review|priority|hold-merge --store <path> ...
                 create, list, show and inspect Director tasks and record rulings, full-review requests, priorities and merge holds
  task start --store <path> --config <file> --state <dir> ...
                 advance a task, launching its next ready stage
  step done --store <path> --config <file> --state <dir> ...
                 take one attempt's result file and advance its task
  step finish --store <path> --config <file> --state <dir> ...
                 finish a Director stage with its outputs and advance its task
  step retry --store <path> --config <file> --state <dir> ...
                 start the next attempt after a failed or stopped one
  step send-back --repo <id> --task <id> --step <id> --attempt <n> --feedback <instructions> [--copy <dir> | --store <path> --config <file> --state <dir>]
                 send a succeeded Artisan step back with Director feedback
  step park --repo <id> --task <id> --step <id> --attempt <n> --reason <text> [--until-merged <repo>/<task>]... [--copy <dir> | --store <path> --config <file> --state <dir>]
                 hold a failed or stopped attempt on purpose, out of needs_me, until lifted or until the named tasks merge
  step unpark --repo <id> --task <id> --step <id> --attempt <n> [--copy <dir> | --store <path> --config <file> --state <dir>]
                 lift a park, so the attempt is back in needs_me
  decide list|show|answer --store <path> ...
                 list open decisions, show one and record the Guildmaster's answer
  decide defer|undefer --store <path> --repo <id> --task <id> --decision <id> [--until-merged <repo>/<task>... --reason <text>]
                 hold an open decision out of needs_me until the named tasks merge, or lift that hold
  director status --json --store <path> --state <dir>
                 print the Director's status feed and what needs the Director or Guildmaster
  director start [runtime] [--tab] [--check] --copy <dir>
                 run a Director session in this terminal, or with --tab in its own Herdr tab
  director ready --copy <dir>
                 say the recovering Director session is ready to start new work
  director pause --copy <dir>
                 hold new attempts while running ones finish
  director resume --copy <dir>
                 lift the hold and print what needs the Director
  director stop [--now] --copy <dir>
                 stop the Director session and its running attempts
  director note add --kind promised|guildmaster|plan [--task <repo>/<id>] <text> --copy <dir>
                 keep a one-line note, given to every new Director session at startup
  director note edit <id> <text> --copy <dir>
                 replace a note's text
  director note remove <id> [--reason <text>] --copy <dir>
                 remove a note; its history stays in the store
  director note list --copy <dir>
                 print the live notes, grouped by kind
  director follow-up add --until-merged <repo>/<task>... <text> --copy <dir>
                 keep a one-line follow-up, back in needs_me once the named tasks merge
  director follow-up list|done <id> --copy <dir>
                 print the follow-ups not done, or mark one done
  director reload [--now] --copy <dir>
                 restart the Director session at a quiet point, or at the end of the turn
  director turn-started --copy <dir>
                 tell the session supervisor the runtime's turn has started (its turn-start hook)
  director turn-ended --copy <dir>
                 tell the session supervisor the runtime's turn has ended (its end-of-turn hook)
  director wait-wake --copy <dir>
                 wait for the session supervisor to wake the Director, then exit 2 (its rewake hook)
  director context-report [--lanterns] --copy <dir>
                 display status and context only with --lanterns (generated workspaces);
                 without it, record runtime context for legacy workspaces
  spend report --copy <dir> [--repo <name>] [--task <id>] [--since <RFC3339>] [--json]
                 report recorded attempt tokens and cost without changing state
  ci log [--copy <dir>] [--tail <lines>] [--out <file>] <repo>/<task> <job|check>
                 print the full log of a failed CI job the task's validate results record
  prune [--dry-run] [--repo <name>] [--task <id>] [--copy <dir>]
                 reclaim the disk space of done tasks closed longer ago than prune_after
  quality judge|judged|agreement --copy <dir> ...
                 judge a delivered task's self-review in a separate session, take a finished judge
                 attempt as its launcher does, and print the judge's agreement with the audits
  review check <outcome-file> [--previous <previous-outcome-file>]
                 check a review outcome file as intake does before the reviewer finishes; prints ok
  help          print this help
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
	// InstallBaseline writes the release baseline, the role prompts and stage guides, into an empty directory.
	InstallBaseline func(string) error
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	// LookPath resolves a tool name on PATH.
	LookPath func(file string) (string, error)
	// Output runs a command and returns its combined stdout and stderr.
	Output func(ctx context.Context, path string, args ...string) ([]byte, error)
	// Executable returns the path of the running guildhall binary.
	Executable func() (string, error)
	// Copier reaches enrolled machines for steward rollouts; nil means scp, found with LookPath.
	Copier steward.Copier
	// Forge publishes and merges task branches for task start and step done; nil means forge.GitHub over git
	// and gh found on PATH.
	Forge interp.Forge
	// Workspaces makes each task's working directory a worktree of its repository for task start, step done and
	// step retry; nil means git.Program over git found on PATH.
	Workspaces interp.Workspaces
	// Getenv reads the environment, such as GUILDHALL_COPY; nil means an empty environment.
	Getenv func(string) string
	// DirectorPane types wake lines into the Director's recorded pane; nil means Herdr, found on PATH as herdr.
	DirectorPane messaging.DirectorPane
	// WakeNotice is the wake notice of a Director's recorded pane, shown while no waiter is registered; nil means
	// the pane's Herdr title.
	WakeNotice func(store.DirectorPane) wakeNotice
	// Notify shows a toast on the Herdr server of a Director's recorded pane, as `messages send` surfaces a note;
	// nil means Herdr, found on PATH as herdr.
	Notify func(ctx context.Context, pane store.DirectorPane, title, body string) error
	// SessionPoll is how often a Director session's supervisor reads its session while the runtime runs; zero
	// means one second.
	SessionPoll time.Duration
	// ChecksWatchFloor is the shortest time between a Director session's checks watcher rounds; zero means
	// checksWatchFloor.
	ChecksWatchFloor time.Duration
	// StopGrace is how long the supervisor of a stopping session lets the runtime end by itself before ending
	// it; zero means 60 seconds.
	StopGrace time.Duration
	// Now is the clock director status counts today's attempts by; nil means wall time.
	Now func() time.Time
	// AdvanceContext subscribes task advances to interruption; nil uses
	// signal.NotifyContext. Tests can observe cancellation without clock sleeps.
	AdvanceContext func(context.Context, ...os.Signal) (context.Context, context.CancelFunc)
	// DirectorTabs opens the terminal host director start opens the Director's tab in, for the named Herdr
	// session, after checking the session's server, and returns the server's version; nil means Herdr, found
	// on PATH as herdr.
	DirectorTabs func(ctx context.Context, session string) (adapter.TabHost, string, error)
	// BuildInfo reads the running binary's build information for version and director status; nil means
	// debug.ReadBuildInfo.
	BuildInfo func() (*debug.BuildInfo, bool)
	// VersionGit reads Guildhall's own repository for version and director status; nil means git.Program with a
	// five-second bound on each run.
	VersionGit VersionGit
	// ArtisanWorkspaces opens the terminal host a Director session's supervisor closes finished Artisans' Herdr
	// workspaces in, for the named Herdr session, after checking the session's server; nil means Herdr, found on
	// PATH as herdr.
	ArtisanWorkspaces func(ctx context.Context, session string) (WorkspaceCloseHost, error)
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
		Getenv:     os.Getenv,
		BuildInfo:  debug.ReadBuildInfo,
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
		return a.versionCmd(rest)
	case "doctor":
		if len(rest) > 0 {
			return a.usageError("doctor takes no arguments")
		}
		out = a.doctor()
	case "spend":
		return a.spend(rest)
	case "prune":
		return a.prune(rest)
	case "store":
		return a.storeCmd(rest)
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
	case "claude-artisan":
		return a.claudeArtisan(rest)
	case "fake-director":
		return a.fakeDirector(rest)
	case "task":
		if len(rest) > 0 && rest[0] == "start" {
			return a.taskStart(rest[1:])
		}
		return a.task(rest)
	case "step":
		return a.step(rest)
	case "decide":
		return a.decide(rest)
	case "director":
		return a.director(rest)
	case "quality":
		return a.qualityCmd(rest)
	case "review":
		return a.review(rest)
	case "ci":
		return a.ciCmd(rest)
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
