package steward

import (
	"context"
	"errors"
	"testing"
)

// TestSSHRunKeepsStdoutOfACommandThatExitsNonZero: a command that prints a report on standard output and exits
// non-zero, as secrets install does on a refusal, fails with an *ExitError whose Stdout is the report, whatever ssh
// wrote on standard error, and whose message is the diagnostic as before: standard error, or else standard output.
func TestSSHRunKeepsStdoutOfACommandThatExitsNonZero(t *testing.T) {
	const report = `{"file":"f","new":"n","expect_sha256":"x","installed":false,"step":"decrypt","problem":"nope","names":[]}`
	const warning = "** WARNING: connection is not using a post-quantum key exchange algorithm."
	for _, tc := range []struct {
		name, stderr, want string
	}{
		{"ssh warning", warning, "ssh: exit status 1: " + warning},
		{"no stderr", "", "ssh: exit status 1: " + report},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t)
			s.answerExit("alpha", report+"\n", tc.stderr, 1)
			sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			out, err := s.runner().Run(context.Background(), "alpha", SecretsInstallCommand(DefaultGuildhall, sum, "/home/guild/directors/tools"))
			var exit *ExitError
			if out != nil || !errors.As(err, &exit) {
				t.Fatalf("Run = %q, %v; want no output and an *ExitError", out, err)
			}
			if string(exit.Stdout) != report+"\n" {
				t.Errorf("Stdout = %q, want the report", exit.Stdout)
			}
			if err.Error() != tc.want {
				t.Errorf("Error() = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSSHRunRefusalIsNotAnExitError: a command CheckRun refuses never ran, so it has no standard output to carry.
func TestSSHRunRefusalIsNotAnExitError(t *testing.T) {
	s := newStub(t)
	_, err := s.runner().Run(context.Background(), "alpha", []string{"rm", "-rf", "/"})
	var exit *ExitError
	if !errors.Is(err, ErrNotAllowed) || errors.As(err, &exit) {
		t.Fatalf("Run error = %v, want ErrNotAllowed and no *ExitError", err)
	}
}
