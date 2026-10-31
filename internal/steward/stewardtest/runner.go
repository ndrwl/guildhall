package stewardtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// RunnerHarness gives the runner contract suite a fresh Runner and the enrolled machines it reaches.
// Each call to the suite's factory returns a new one on which every machine is reachable, has run nothing and
// answers every command by printing nothing and succeeding.
type RunnerHarness interface {
	Runner() steward.Runner
	// Answer makes every later command on the machine with alias print output and succeed, or, when failure is
	// not empty, print failure as its diagnostic and exit non-zero.
	Answer(t *testing.T, alias string, output []byte, failure string)
	// Ran returns the commands the machine with alias has received, each as the argument list it ran, oldest first.
	Ran(t *testing.T, alias string) [][]string
	// Refuse makes every command on the machine with alias fail, as an unreachable or refusing machine does.
	Refuse(t *testing.T, alias string)
	// Hang makes every command on the machine with alias wait, running nothing, until its context ends.
	Hang(t *testing.T, alias string)
}

// The copies and Guildhall programs the runner suite names.
const (
	posixCopy       = "/home/guild/directors/tools"
	windowsCopy     = "C:/Users/guild/directors/games"
	windowsProgram  = "C:/Users/guild/bin/guildhall.exe"
	unknownCommand  = `guildhall: unknown command "config"`
	modeJSON        = `{"mode":"managed","authorized":true}` + "\n"
	runnerReturnMax = 30 * time.Second
)

// RunRunnerContract runs the Runner contract against harnesses from newHarness.
func RunRunnerContract(t *testing.T, newHarness func(t *testing.T) RunnerHarness) {
	for _, c := range []struct {
		name string
		run  func(t *testing.T, h RunnerHarness)
	}{
		{"runs the fixed command on its machine and returns its output", func(t *testing.T, h RunnerHarness) {
			h.Answer(t, Alpha, []byte(modeJSON), "")
			argv := steward.ModeCommand(steward.DefaultGuildhall, posixCopy)
			out, err := h.Runner().Run(context.Background(), Alpha, argv)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if string(out) != modeJSON {
				t.Errorf("output %q, want %q", out, modeJSON)
			}
			if got := h.Ran(t, Alpha); len(got) != 1 || !slices.Equal(got[0], argv) {
				t.Errorf("%s ran %q, want exactly %q", Alpha, got, argv)
			}
			if got := h.Ran(t, Bravo); len(got) != 0 {
				t.Errorf("%s ran %q, want nothing", Bravo, got)
			}
		}},
		{"a Windows guildhall_path and copy reach the machine unchanged", func(t *testing.T, h RunnerHarness) {
			argv := steward.ReloadCommand(windowsProgram, windowsCopy)
			if _, err := h.Runner().Run(context.Background(), Bravo, argv); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := h.Ran(t, Bravo); len(got) != 1 || !slices.Equal(got[0], argv) {
				t.Errorf("%s ran %q, want exactly %q", Bravo, got, argv)
			}
		}},
		{"commands outside the allow-list are refused and run nothing", func(t *testing.T, h RunnerHarness) {
			for _, argv := range [][]string{
				nil,
				{steward.DefaultGuildhall},
				{"sh", "-c", "id"},
				{steward.DefaultGuildhall, "task", "new", "--copy", posixCopy},
				{steward.DefaultGuildhall, "config", "reload"},
				{steward.DefaultGuildhall, "config", "reload", "--copy", posixCopy, "--extra"},
				{steward.DefaultGuildhall, "config", "reload", "--copy", "directors/tools"},
				{steward.DefaultGuildhall, "config", "reload", "--copy", posixCopy + ";id"},
				{steward.DefaultGuildhall, "config", "reload", "--copy", "/home/guild/../tools"},
				{steward.DefaultGuildhall, "director", "mode", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "reload", "--copy", posixCopy},
				{"bin/guildhall", "config", "reload", "--copy", posixCopy},
				{"/home/guild/bin/guildhall;id", "config", "reload", "--copy", posixCopy},
				{`C:\Users\guild\guildhall.exe`, "config", "reload", "--copy", windowsCopy},
			} {
				if _, err := h.Runner().Run(context.Background(), Alpha, argv); !errors.Is(err, steward.ErrNotAllowed) {
					t.Errorf("Run %q: error %v, want steward.ErrNotAllowed", argv, err)
				}
			}
			argv := steward.ReloadCommand(steward.DefaultGuildhall, posixCopy)
			for _, alias := range []string{"-oProxyCommand=id", "guild@alpha", ""} {
				if _, err := h.Runner().Run(context.Background(), alias, argv); !errors.Is(err, steward.ErrNotAllowed) {
					t.Errorf("Run on alias %q: error %v, want steward.ErrNotAllowed", alias, err)
				}
			}
			if got := h.Ran(t, Alpha); len(got) != 0 {
				t.Errorf("%s ran %q, want nothing", Alpha, got)
			}
		}},
		{"a failing command fails with its diagnostic", func(t *testing.T, h RunnerHarness) {
			h.Answer(t, Alpha, nil, unknownCommand)
			_, err := h.Runner().Run(context.Background(), Alpha, steward.ReloadCommand(steward.DefaultGuildhall, posixCopy))
			if err == nil || !strings.Contains(err.Error(), unknownCommand) {
				t.Errorf("Run: error %v, want one carrying %q", err, unknownCommand)
			}
		}},
		{"a refused machine fails with a diagnostic and others still run", func(t *testing.T, h RunnerHarness) {
			h.Refuse(t, Alpha)
			argv := steward.ReloadCommand(steward.DefaultGuildhall, posixCopy)
			if _, err := h.Runner().Run(context.Background(), Alpha, argv); err == nil || err.Error() == "" {
				t.Errorf("Run on a refused machine: error %v, want a diagnostic", err)
			}
			if _, err := h.Runner().Run(context.Background(), Bravo, argv); err != nil {
				t.Errorf("Run on a reachable machine: %v", err)
			}
		}},
		{"deadline stops a hung command", func(t *testing.T, h RunnerHarness) {
			h.Hang(t, Alpha)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond) // timing:allow intentionally hung command; no startup or success must fit this deadline.
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := h.Runner().Run(ctx, Alpha, steward.ReloadCommand(steward.DefaultGuildhall, posixCopy))
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("hung Run: error %v, want context.DeadlineExceeded", err)
				}
			case <-time.After(testenv.Ceiling(runnerReturnMax)):
				t.Fatal("hung Run did not return after its deadline")
			}
		}},
		{"cancelled context runs nothing", func(t *testing.T, h RunnerHarness) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := h.Runner().Run(ctx, Alpha, steward.ReloadCommand(steward.DefaultGuildhall, posixCopy)); !errors.Is(err, context.Canceled) {
				t.Errorf("Run: error %v, want context.Canceled", err)
			}
			if got := h.Ran(t, Alpha); len(got) != 0 {
				t.Errorf("%s ran %q, want nothing", Alpha, got)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) { c.run(t, newHarness(t)) })
	}
}
