package testenv_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// recorder stands in for *testing.T so a test can see which of Skipf and
// Fatalf was called, and with what message, without stopping itself.
type recorder struct {
	skips, fatals []string
}

func (r *recorder) Helper() {}

func (r *recorder) Skipf(format string, args ...any) {
	r.skips = append(r.skips, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func TestSkipUnlessRequired(t *testing.T) {
	cases := []struct {
		name, env, need string
		fatal, unknown  bool
		mentions        []string
	}{
		{name: "unset skips", need: "pwsh"},
		{name: "other name skips", env: "pwsh", need: "ksh"},
		{name: "required fails", env: "pwsh", need: "pwsh", fatal: true, mentions: []string{"pwsh", testenv.RequireEnv}},
		{name: "one of several fails", env: "pwsh,powershell,windows", need: "powershell", fatal: true},
		{name: "spaces and empty entries ignored", env: " ksh ,, pwsh ,", need: "pwsh", fatal: true},
		{name: "names are exact", env: "pwsh", need: "PWSH"},
		{name: "unknown name fails", env: "pwsh,pwssh", need: "ksh", fatal: true, unknown: true, mentions: []string{`"pwssh"`, testenv.RequireEnv}},
		{name: "case-changed name is unknown", env: "PWSH", need: "pwsh", fatal: true, unknown: true, mentions: []string{`"PWSH"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(testenv.RequireEnv, c.env)
			var r recorder
			testenv.SkipUnlessRequired(&r, c.need, "%s is not installed", c.need)
			if len(r.skips)+len(r.fatals) != 1 {
				t.Fatalf("skips %q, fatals %q; want exactly one call", r.skips, r.fatals)
			}
			msg := ""
			if c.fatal {
				if len(r.fatals) != 1 {
					t.Fatalf("skipped %q, want a failure", r.skips)
				}
				msg = r.fatals[0]
			} else {
				if len(r.skips) != 1 {
					t.Fatalf("failed %q, want a skip", r.fatals)
				}
				msg = r.skips[0]
			}
			if !c.unknown {
				c.mentions = append(c.mentions, c.need+" is not installed")
			}
			for _, m := range c.mentions {
				if !strings.Contains(msg, m) {
					t.Errorf("message %q does not mention %q", msg, m)
				}
			}
		})
	}
}

func TestRequired(t *testing.T) {
	t.Setenv(testenv.RequireEnv, " windows ,,pwsh")
	got, err := testenv.Required()
	if err != nil || strings.Join(got, ",") != "windows,pwsh" {
		t.Fatalf("Required() = %q, %v; want [windows pwsh]", got, err)
	}
	t.Setenv(testenv.RequireEnv, "")
	if got, err := testenv.Required(); err != nil || len(got) != 0 {
		t.Fatalf("Required() unset = %q, %v; want none", got, err)
	}
	t.Setenv(testenv.RequireEnv, "bash")
	if _, err := testenv.Required(); err == nil || !strings.Contains(err.Error(), `"bash"`) {
		t.Fatalf("Required() = %v, want an error naming \"bash\"", err)
	}
}

// TestCIRequirementsAreKnown checks this job's own RequireEnv, so a misspelt
// requirement fails every runner that sets it, even where no test consults it.
func TestCIRequirementsAreKnown(t *testing.T) {
	if _, err := testenv.Required(); err != nil {
		t.Fatal(err)
	}
}
