package fake

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter"
)

// DirectorSubcommand is the guildhall subcommand that runs the fake Director session.
const DirectorSubcommand = "fake-director"

// SessionDirective starts a role prompt line that scripts the fake Director session.
const SessionDirective = "fake-director:"

// TurnLogEnv names a file a scripted session appends one line to for each turn it takes, the count a test reads
// as the session's model calls (RunScript).
const TurnLogEnv = "GUILDHALL_SESSION_TURNS"

// SkillsDir is where a fake Director session's skills are rendered, relative to the session directory.
func (Adapter) SkillsDir() string { return "skills" }

// Program is the program a fake Director session needs on PATH: none, since it is the guildhall executable.
func (Adapter) Program() string { return "" }

// Session returns the fake Director session for spec: `guildhall fake-director --role <role prompt>
// <first message>` in the session directory with the session's environment. It refuses an invalid spec or an
// unset Executable, returning no command; it writes nothing.
func (a Adapter) Session(spec adapter.SessionSpec) (adapter.Command, error) {
	if err := spec.Validate(); err != nil {
		return adapter.Command{}, err
	}
	if a.Executable == "" {
		return adapter.Command{}, errors.New("fake adapter: guildhall executable is not set")
	}
	return adapter.Command{Argv: []string{a.Executable, DirectorSubcommand, "--role", spec.RolePrompt, spec.FirstMessage},
		Dir: spec.Dir, Env: spec.SessionEnv()}, nil
}

// DirectorMain runs `guildhall fake-director --role <file> <first message>`: it plays the role prompt's
// SessionDirective lines with RunScript and returns its exit status, or 2 for refused arguments.
func DirectorMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet(DirectorSubcommand, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	role := fl.String("role", "", "role prompt file")
	if err := fl.Parse(args); err != nil || *role == "" || fl.NArg() != 1 || strings.TrimSpace(fl.Arg(0)) == "" {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: want --role <file> and one first message\n", DirectorSubcommand)
		return 2
	}
	text, err := os.ReadFile(*role)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: %s: %v\n", DirectorSubcommand, err)
		return 2
	}
	return RunScript(SessionDirective, string(text), stdin, stdout, stderr)
}

// RunScript plays a scripted Director session, the double of an interactive runtime: every line of text
// starting with prefix is one step, in order.
//
//   - `<prefix> guildhall <args>` runs guildhall from PATH with the whitespace-separated args in the current
//     directory and environment, its output passed through; a failing command is reported on stderr and the
//     script goes on, as a model would after reading the error.
//   - `<prefix> /exit` ends the session with status 0, as typing /exit does.
//   - `<prefix> exit <n>` ends it with status n, as a crashed runtime would.
//
// Any other step is refused with status 2 before anything runs. With no ending step the session then reads
// stdin, a typed /exit line or the end of input ending it with status 0.
//
// With TurnLogEnv naming a file, the session appends "<pid> start" to it before its first step, the turn its
// first message starts, and "<pid> input" for each non-blank line it reads but the ending /exit, as a runtime
// takes a model turn for each line typed to it.
func RunScript(prefix, text string, stdin io.Reader, stdout, stderr io.Writer) int {
	steps, err := parseScript(prefix, text)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "session script: %v\n", err)
		return 2
	}
	turn := func(kind string) {
		log := os.Getenv(TurnLogEnv)
		if log == "" {
			return
		}
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = fmt.Fprintf(f, "%d %s\n", os.Getpid(), kind)
			err = errors.Join(err, f.Close())
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "session script: turn log: %v\n", err)
		}
	}
	turn("start")
	for _, s := range steps {
		switch s[0] {
		case "/exit":
			return 0
		case "exit":
			n, _ := strconv.Atoi(s[1])
			return n
		}
		prog, err := exec.LookPath("guildhall")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "session script: %v\n", err)
			continue
		}
		cmd := exec.Command(prog, s[1:]...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		if err := cmd.Run(); err != nil {
			_, _ = fmt.Fprintf(stderr, "session script: guildhall %s: %v\n", strings.Join(s[1:], " "), err)
		}
	}
	if stdin == nil {
		return 0
	}
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		switch strings.TrimSpace(sc.Text()) {
		case "/exit":
			return 0
		case "":
		default:
			turn("input")
		}
	}
	return 0
}

func parseScript(prefix, text string) ([][]string, error) {
	var steps [][]string
	for line := range strings.Lines(text) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		switch {
		case len(f) == 1 && f[0] == "/exit", len(f) > 0 && f[0] == "guildhall":
		case len(f) == 2 && f[0] == "exit":
			if n, err := strconv.Atoi(f[1]); err != nil || n < 0 || n > 125 {
				return nil, fmt.Errorf("exit status %q must be 0 to 125", f[1])
			}
		default:
			return nil, fmt.Errorf("unknown step %q", strings.TrimSpace(rest))
		}
		steps = append(steps, f)
	}
	return steps, nil
}

// TurnEndHook reports false: the fake Director session has no turn hooks; its script runs `guildhall director
// turn-started` and `turn-ended` itself where a test needs a turn to start or end.
func (Adapter) TurnEndHook() bool { return false }

// SessionFiles are the files a fake Director session needs beside its skills: none.
func (Adapter) SessionFiles(string) map[string][]byte { return nil }
