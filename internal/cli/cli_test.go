package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestApp returns an App whose PATH lookups and commands are answered from
// the given tables, so no test depends on what is installed on the runner.
func newTestApp(paths map[string]string, outputs map[string]result) (*App, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	f := &fakeTools{paths: paths, outputs: outputs}
	app := &App{Stdout: &stdout, Stderr: &stderr, LookPath: f.lookPath, Output: f.output}
	return app, &stdout, &stderr
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestVersionPrintsInjectedVersionAndPlatform(t *testing.T) {
	setVersion(t, "v1.2.3")
	app, stdout, stderr := newTestApp(nil, nil)

	if code := app.Run([]string{"version"}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	want := "guildhall v1.2.3 " + runtime.GOOS + "/" + runtime.GOARCH + " " + runtime.Version() + "\n"
	if got := stdout.String(); got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestVersionDefaultsToDev(t *testing.T) {
	if got := Version(); got != "dev" {
		t.Errorf("Version() = %q, want %q for a test build without -ldflags", got, "dev")
	}
}

func TestDoctorReportsPlatformAndEachTool(t *testing.T) {
	setVersion(t, "v1.2.3")
	paths := map[string]string{
		"git":   "/bin/git",
		"ssh":   "/bin/ssh",
		"herdr": "/bin/herdr",
	}
	outputs := map[string]result{
		"/bin/git --version":   {out: "git version 2.43.0\n"},
		"/bin/ssh -V":          {out: "\nOpenSSH_9.6p1, OpenSSL 3.0.13\nsecond line\n"},
		"/bin/herdr --version": {out: "boom\n", err: errors.New("exit status 1")},
	}
	app, stdout, stderr := newTestApp(paths, outputs)

	if code := app.Run([]string{"doctor"}); code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	want := strings.Join([]string{
		"os: " + runtime.GOOS,
		"arch: " + runtime.GOARCH,
		"guildhall: v1.2.3",
		"go: " + runtime.Version(),
		"git: git version 2.43.0 (/bin/git)",
		"ssh: OpenSSH_9.6p1, OpenSSL 3.0.13 (/bin/ssh)",
		"claude: not found on PATH",
		"herdr: version check failed: exit status 1 (/bin/herdr)",
		"",
	}, "\n")
	if got := stdout.String(); got != want {
		t.Errorf("stdout =\n%s\nwant\n%s", got, want)
	}
}

func TestDoctorReportsLookupErrors(t *testing.T) {
	for _, tool := range doctorTools {
		for _, tc := range []struct {
			name string
			path string
			err  error
			want string
		}{
			{"missing", "", &exec.Error{Name: tool.name, Err: exec.ErrNotFound}, "not found on PATH"},
			{"relative", "node_modules/.bin/" + tool.name, &exec.Error{Name: tool.name, Err: exec.ErrDot}, ""},
			{"other", "", errors.New("lookup refused"), "PATH lookup failed: lookup refused"},
		} {
			t.Run(tool.name+"/"+tc.name, func(t *testing.T) {
				app, stdout, stderr := newTestApp(nil, nil)
				app.LookPath = func(name string) (string, error) {
					if name == tool.name {
						return tc.path, tc.err
					}
					return "", exec.ErrNotFound
				}
				app.Output = func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("executed a tool after a failed lookup")
					return nil, nil
				}
				if code := app.Run([]string{"doctor"}); code != 0 {
					t.Fatalf("exit code = %d, want 0", code)
				}
				want := tc.want
				if tc.name == "relative" {
					want = "PATH lookup failed: " + tc.err.Error() + " (" + tc.path + ")"
				}
				if !strings.Contains(stdout.String(), tool.name+": "+want+"\n") {
					t.Errorf("stdout = %q, want lookup report %q", stdout, want)
				}
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty", stderr)
				}
			})
		}
	}
}

func TestDoctorReportsEmptyVersionOutput(t *testing.T) {
	paths := map[string]string{"git": "/bin/git"}
	outputs := map[string]result{"/bin/git --version": {out: "  \n"}}
	app, stdout, _ := newTestApp(paths, outputs)

	app.Run([]string{"doctor"})

	if !strings.Contains(stdout.String(), "git: version check printed nothing (/bin/git)\n") {
		t.Errorf("stdout = %q, want an empty-output report for git", stdout)
	}
}

func TestDoctorBoundsEachVersionCommand(t *testing.T) {
	paths := map[string]string{}
	for _, tool := range doctorTools {
		paths[tool.name] = "/bin/" + tool.name
	}
	app, _, _ := newTestApp(paths, nil)
	var ran []string
	app.Output = func(ctx context.Context, path string, _ ...string) ([]byte, error) {
		ran = append(ran, path)
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("%s ran without a deadline", path)
		} else if left := time.Until(deadline); left > toolProbeTimeout {
			t.Errorf("%s deadline is %v away, want at most %v", path, left, toolProbeTimeout)
		}
		return []byte("v1\n"), nil
	}

	app.Run([]string{"doctor"})

	if len(ran) != len(doctorTools) {
		t.Errorf("ran %q, want one version command per tool", ran)
	}
}

func TestHelpPrintsUsageToStdout(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		t.Run(arg, func(t *testing.T) {
			app, stdout, stderr := newTestApp(nil, nil)
			if code := app.Run([]string{arg}); code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if !strings.HasPrefix(stdout.String(), "Usage: guildhall <command>") {
				t.Errorf("stdout = %q, want usage", stdout)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr)
			}
		})
	}
}

// TestHelpNamesReadmeCommands pins the help text to the README command list:
// every `guildhall <command>` the list names appears in the usage.
func TestHelpNamesReadmeCommands(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(readme), "\n## Commands\n")
	if !ok {
		t.Fatal("README.md has no Commands section")
	}
	list, _, _ = strings.Cut(list, "\n## ")
	commands := regexp.MustCompile("`guildhall ([^`<]+?)( <[^`]*)?`").FindAllStringSubmatch(list, -1)
	if len(commands) == 0 {
		t.Fatal("README.md lists no commands")
	}
	app, stdout, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	for _, c := range commands {
		if !strings.Contains(stdout.String(), "  "+c[1]) {
			t.Errorf("help does not name README command %q", c[1])
		}
	}
}

// TestArtisanRoleGrammar pins the role grammar of docs/decisions/0053-cli-role-grammar.md
// for the Artisan: help lists every artisan verb, and the pre-release `artisan
// run` is refused with a pointer to `artisan start` rather than kept as an alias.
func TestArtisanRoleGrammar(t *testing.T) {
	app, stdout, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"help"}); code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	for _, want := range []string{"artisan start|stop|status|resume", "artisan harness inventory|kill-owned|probe --lab"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help = %q, want it to list %q", stdout, want)
		}
	}

	for _, args := range [][]string{{"artisan"}, {"artisan", "frobnicate"}} {
		app, _, stderr := newTestApp(nil, nil)
		if code := app.Run(args); code != 2 {
			t.Errorf("%q exit code = %d, want 2", args, code)
		}
		if want := "guildhall: artisan: the commands are start, stop, status, resume and harness\n"; !strings.HasPrefix(stderr.String(), want) {
			t.Errorf("%q stderr = %q, want prefix %q", args, stderr, want)
		}
	}

	app, stdout, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"artisan", "run", "--adapter", "fake"}); code != 2 {
		t.Errorf("artisan run exit code = %d, want 2", code)
	}
	if want := "guildhall: artisan run is now artisan start\n"; !strings.HasPrefix(stderr.String(), want) {
		t.Errorf("artisan run stderr = %q, want prefix %q", stderr, want)
	}
	if stdout.Len() != 0 {
		t.Errorf("artisan run stdout = %q, want empty", stdout)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no command", nil, "guildhall: missing command\n"},
		{"unknown command", []string{"frobnicate"}, "guildhall: unknown command \"frobnicate\"\n"},
		{"extra argument", []string{"version", "extra"}, "guildhall: version takes no arguments\n"},
		{"doctor argument", []string{"doctor", "--fix"}, "guildhall: doctor takes no arguments\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, stdout, stderr := newTestApp(nil, nil)
			if code := app.Run(tt.args); code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.HasPrefix(stderr.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want prefix %q", stderr, tt.wantErr)
			}
			if !strings.Contains(stderr.String(), "Usage: guildhall <command>") {
				t.Errorf("stderr = %q, want usage", stderr)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestUnwritableStdoutExitsOne(t *testing.T) {
	app, _, _ := newTestApp(nil, nil)
	app.Stdout = failingWriter{}
	if code := app.Run([]string{"version"}); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}
