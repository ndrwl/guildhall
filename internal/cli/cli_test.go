package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// newTestApp returns an App whose PATH lookups and commands are answered from
// the given tables, so no test depends on what is installed on the runner.
func newTestApp(paths map[string]string, outputs map[string]result) (*App, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	app := &App{
		Stdout: &stdout,
		Stderr: &stderr,
		LookPath: func(file string) (string, error) {
			if p, ok := paths[file]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		Output: func(_ context.Context, path string, args ...string) ([]byte, error) {
			r, ok := outputs[path+" "+strings.Join(args, " ")]
			if !ok {
				return nil, errors.New("unexpected command")
			}
			return []byte(r.out), r.err
		},
	}
	return app, &stdout, &stderr
}

type result struct {
	out string
	err error
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

func TestDoctorReportsEmptyVersionOutput(t *testing.T) {
	paths := map[string]string{"git": "/bin/git"}
	outputs := map[string]result{"/bin/git --version": {out: "  \n"}}
	app, stdout, _ := newTestApp(paths, outputs)

	app.Run([]string{"doctor"})

	if !strings.Contains(stdout.String(), "git: version check printed nothing (/bin/git)\n") {
		t.Errorf("stdout = %q, want an empty-output report for git", stdout)
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
