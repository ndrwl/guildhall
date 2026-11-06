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
	setupDir        = "/home/guild/.guildhall/setup"
	windowsSetup    = "C:/Users/guild/AppData/Local/Guildhall/setup"
	bareProgram     = "/home/guild/.local/bin/guildhall"
	sha             = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
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
		{"the takeover commands and their values reach the machine unchanged", func(t *testing.T, h RunnerHarness) {
			cmds := [][]string{
				steward.StatusCommand(steward.DefaultGuildhall, posixCopy),
				steward.PauseCommand(steward.DefaultGuildhall, posixCopy),
				steward.ResumeCommand(steward.DefaultGuildhall, posixCopy),
				steward.BackupCommand(steward.DefaultGuildhall, posixCopy),
				steward.ExportCommand(steward.DefaultGuildhall, posixCopy+"/state/steward/takeover/plan-1", posixCopy),
				steward.EnrolCommand(steward.DefaultGuildhall, "steward", posixCopy),
			}
			for _, argv := range cmds {
				if _, err := h.Runner().Run(context.Background(), Alpha, argv); err != nil {
					t.Fatalf("Run %q: %v", argv, err)
				}
			}
			if got := h.Ran(t, Alpha); !slices.EqualFunc(got, cmds, slices.Equal) {
				t.Errorf("%s ran %q, want exactly %q", Alpha, got, cmds)
			}
		}},
		{"the provisioning commands and their values reach the machine unchanged", func(t *testing.T, h RunnerHarness) {
			cmds := [][]string{
				steward.DoctorCommand(steward.DefaultGuildhall, "herdr", sha, setupDir, nil),
				steward.DoctorCommand(steward.DefaultGuildhall, "director", sha, setupDir, []string{"copy_dir=" + posixCopy}),
				steward.DoctorCommand(steward.DefaultGuildhall, posixCopy+"/state/steward/setup/custom-0123456789ab.toml", sha, setupDir, nil),
				steward.FixCommand(steward.DefaultGuildhall, "herdr", "herdr", sha, setupDir,
					[]string{"herdr_sha256=" + sha, "herdr_url=https://example.com/herdr-0.5.0", "herdr_version=0.5.0"}),
				steward.FixCommand(steward.DefaultGuildhall, "copy-dir,custom-tool", "director", sha, setupDir, nil),
				steward.StoreInitCommand(steward.DefaultGuildhall, posixCopy),
				steward.AdoptCommand(steward.DefaultGuildhall, posixCopy),
				steward.StartTabCommand(steward.DefaultGuildhall, posixCopy),
				steward.CloneCommand("git@github.com:guild/infra.git", "/home/guild/src/infra"),
				steward.CloneCommand("https://github.com/guild/infra.git", "/home/guild/src/infra"),
				steward.HashCommand(steward.OSLinux, bareProgram),
				steward.InstallFromCommand(bareProgram, "v0.0.0-20261105160445-9fb699fffce8", bareProgram, sha),
			}
			for _, argv := range cmds {
				if _, err := h.Runner().Run(context.Background(), Alpha, argv); err != nil {
					t.Fatalf("Run %q: %v", argv, err)
				}
			}
			if got := h.Ran(t, Alpha); !slices.EqualFunc(got, cmds, slices.Equal) {
				t.Errorf("%s ran %q, want exactly %q", Alpha, got, cmds)
			}
		}},
		{"the Windows forms of the takeover, provisioning and bootstrap commands reach the machine unchanged", func(t *testing.T, h RunnerHarness) {
			cmds := [][]string{
				steward.StatusCommand(windowsProgram, windowsCopy),
				steward.PauseCommand(windowsProgram, windowsCopy),
				steward.ExportCommand(windowsProgram, windowsCopy+"/state/steward/takeover/plan-1", windowsCopy),
				steward.EnrolCommand(windowsProgram, "steward", windowsCopy),
				steward.DoctorCommand(windowsProgram, "new-copy", sha, windowsSetup, []string{"copy_dir=" + windowsCopy, "clone_paths=C:/Users/guild/src/a:D:/src/b"}),
				steward.DoctorCommand(windowsProgram, windowsCopy+"/state/steward/setup/custom-0123456789ab.toml", sha, windowsSetup, nil),
				steward.FixCommand(windowsProgram, "go", "go", sha, windowsSetup, []string{"go_url=https://go.dev/dl/go1.27.2.windows-amd64.zip"}),
				steward.StoreInitCommand(windowsProgram, windowsCopy),
				steward.AdoptCommand(windowsProgram, windowsCopy),
				steward.StartTabCommand(windowsProgram, windowsCopy),
				steward.CloneCommand("git@github.com:guild/games.git", "C:/Users/guild/src/games"),
				steward.HashCommand(steward.OSWindows, windowsProgram),
				steward.InstallFromCommand(windowsProgram, "v0.9.0", windowsProgram, sha),
			}
			if got := steward.HashCommand(steward.OSWindows, windowsProgram); !slices.Equal(got, []string{"certutil", "-hashfile", windowsProgram, "SHA256"}) {
				t.Errorf("Windows hash command %q", got)
			}
			for _, argv := range cmds {
				if _, err := h.Runner().Run(context.Background(), Alpha, argv); err != nil {
					t.Fatalf("Run %q: %v", argv, err)
				}
			}
			if got := h.Ran(t, Alpha); !slices.EqualFunc(got, cmds, slices.Equal) {
				t.Errorf("%s ran %q, want exactly %q", Alpha, got, cmds)
			}
		}},
		{"provisioning commands outside their shapes are refused and run nothing", func(t *testing.T, h RunnerHarness) {
			doctor := func(extra ...string) []string {
				return append(steward.DoctorCommand(steward.DefaultGuildhall, "herdr", sha, setupDir, nil), extra...)
			}
			for _, argv := range [][]string{
				// setup doctor only reads, or fixes the ids it names; never a wizard or a run, and never an unchecked file.
				{steward.DefaultGuildhall, "setup", "wizard", "--steps", "sign-in", "--digest", sha, "--setup-dir", setupDir},
				{steward.DefaultGuildhall, "setup", "run", "--steps", "herdr", "--digest", sha, "--setup-dir", setupDir},
				{steward.DefaultGuildhall, "setup", "doctor", "--json", "--steps", "herdr", "--setup-dir", setupDir},
				{steward.DefaultGuildhall, "setup", "doctor", "--json", "--fix", "--steps", "herdr", "--digest", sha, "--setup-dir", setupDir},
				steward.DoctorCommand(steward.DefaultGuildhall, "herdr", "sha256:"+sha, setupDir, nil),
				steward.DoctorCommand(steward.DefaultGuildhall, "../herdr", sha, setupDir, nil),
				steward.DoctorCommand(steward.DefaultGuildhall, "steps/custom.toml", sha, setupDir, nil),
				steward.DoctorCommand(steward.DefaultGuildhall, "herdr", sha, "relative/setup", nil),
				steward.FixCommand(steward.DefaultGuildhall, "herdr;id", "herdr", sha, setupDir, nil),
				steward.FixCommand(steward.DefaultGuildhall, "", "herdr", sha, setupDir, nil),
				doctor("--set", "copy_dir=/home/guild/x y"),
				doctor("--set", "copy_dir=$(id)"),
				doctor("--set", "Copy=/x"),
				doctor("--set"),
				doctor("--extra"),
				// git runs only clone, of an https:// or git@ remote, into an absolute path.
				{"git", "clone", "git@github.com:guild/infra.git", "/home/guild/src/infra"},
				{"git", "clone", "--", "--upload-pack=id", "/home/guild/src/infra"},
				steward.CloneCommand("ext::sh -c id", "/home/guild/src/infra"),
				steward.CloneCommand("file:///etc", "/home/guild/src/infra"),
				steward.CloneCommand("http://github.com/guild/infra.git", "/home/guild/src/infra"),
				steward.CloneCommand("https://github.com/guild/../infra", "/home/guild/src/infra"),
				steward.CloneCommand("git@github.com:guild/infra.git;id", "/home/guild/src/infra"),
				steward.CloneCommand("git@github.com:guild/infra.git", "src/infra"),
				{"git", "pull", "--", "git@github.com:guild/infra.git", "/home/guild/src/infra"},
				{"/usr/bin/git", "clone", "--", "git@github.com:guild/infra.git", "/home/guild/src/infra"},
				{steward.DefaultGuildhall, "clone", "--", "git@github.com:guild/infra.git", "/home/guild/src/infra"},
				// store init, release adopt and director start take only their flags and the copy.
				{steward.DefaultGuildhall, "store", "init", posixCopy + "/director.store"},
				{steward.DefaultGuildhall, "release", "adopt", "--launcher", "/tmp/l", "--copy", posixCopy},
				{steward.DefaultGuildhall, "release", "install", "v1", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "start", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "start", "claude", "--tab", "--copy", posixCopy},
				// sha256sum reads one absolute path; release install takes a version, the file and its sha256 only.
				{"sha256sum", "guildhall"},
				{"sha256sum", "-c", bareProgram},
				{"sha256sum", bareProgram, "/etc/shadow"},
				{"sha256sum", bareProgram + ";id"},
				{"/usr/bin/sha256sum", bareProgram},
				{"sha512sum", bareProgram},
				// certutil only hashes one absolute file with SHA256: no other verb, algorithm or file.
				{"certutil", "-hashfile", windowsProgram},
				{"certutil", "-hashfile", windowsProgram, "MD5"},
				{"certutil", "-hashfile", "guildhall.exe", "SHA256"},
				{"certutil", "-hashfile", `C:\Users\guild\bin\guildhall.exe`, "SHA256"},
				{"certutil", "-urlcache", "-f", "https://example.com/x", windowsProgram},
				{"certutil", "-decode", windowsProgram, windowsProgram + ".exe"},
				{"C:/Windows/System32/certutil.exe", "-hashfile", windowsProgram, "SHA256"},
				steward.InstallFromCommand(bareProgram, "v1;id", bareProgram, sha),
				steward.InstallFromCommand(bareProgram, "v1", "guildhall", sha),
				steward.InstallFromCommand(bareProgram, "v1", bareProgram, "sha256:"+sha),
				{bareProgram, "release", "install", "v1", "--from", bareProgram},
				{bareProgram, "release", "install", "v1", "--from", bareProgram, "--sha256", sha, "--copy", posixCopy},
				{bareProgram, "release", "install", "--launcher", "--from", bareProgram},
			} {
				if _, err := h.Runner().Run(context.Background(), Alpha, argv); !errors.Is(err, steward.ErrNotAllowed) {
					t.Errorf("Run %q: error %v, want steward.ErrNotAllowed", argv, err)
				}
			}
			if got := h.Ran(t, Alpha); len(got) != 0 {
				t.Errorf("%s ran %q, want nothing", Alpha, got)
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
				// messages receive takes only --copy: no sender, recipient, envelope or standard input.
				{steward.DefaultGuildhall, "messages", "receive", "--copy"},
				{steward.DefaultGuildhall, "messages", "receive", "--from", "steward", "--copy", posixCopy},
				{steward.DefaultGuildhall, "messages", "receive", "--copy", posixCopy, "-"},
				{steward.DefaultGuildhall, "messages", "receive", "-", "--copy", posixCopy},
				{steward.DefaultGuildhall, "messages", "send", "--copy", posixCopy},
				{steward.DefaultGuildhall, "messages", "receive", "--copy", posixCopy + "/state/steward/message.json;id"},
				{steward.DefaultGuildhall, "director", "enrol", "--steward", "s;id", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "enrol", "--copy", posixCopy},
				{steward.DefaultGuildhall, "store", "export", "--out", "out", "--copy", posixCopy},
				{steward.DefaultGuildhall, "store", "export", "--out", posixCopy + "/x y", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "stop", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "status", "--copy", posixCopy},
				{steward.DefaultGuildhall, "backup", "restore", "--copy", posixCopy},
				// director status takes only --json and --copy: no store, state or config of its own choosing.
				{steward.DefaultGuildhall, "director", "status", "--copy", posixCopy, "--json"},
				{steward.DefaultGuildhall, "director", "status", "--json", "--store", "/tmp/x.store", "--copy", posixCopy},
				{steward.DefaultGuildhall, "director", "status", "--json", "--copy", "relative/copy"},
				{steward.DefaultGuildhall, "director", "status", "--json", "--copy", posixCopy, "extra"},
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
