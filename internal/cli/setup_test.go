package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

// setupDemo's commands run this test binary: with -test.run=^$ it exits 0, and with an unknown flag it exits 2.
const setupDemo = `schema = "guildhall.steps/1"
name = "cli-demo"
title = "CLI demo"

[params]
me = ""

[[step]]
id = "present"
title = "Something present"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${me}", "-test.run=^$"]
action = ["${me}", "-test.run=^$"]
undo = "Nothing."

[[step]]
id = "absent"
title = "Something absent"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${me}", "-test.no-such-flag"]
action = ["${me}", "-test.no-such-flag"]
undo = "Nothing."

[[step]]
id = "person"
title = "A person's step"
tag = "human"
os = ["linux", "darwin", "windows"]
check = ["${me}", "-test.no-such-flag"]
instructions = "Do it by hand."
undo = "Undo it by hand."

[[step.prompt]]
kind = "confirm"
text = "Done?"
`

func writeSetupDemo(t *testing.T) (path, digest, setupDir string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "demo.toml")
	if err := os.WriteFile(path, []byte(setupDemo), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, setup.Digest([]byte(setupDemo)), filepath.Join(dir, "setup")
}

func TestSetupDoctor(t *testing.T) {
	path, digest, sd := writeSetupDemo(t)
	me := "me=" + os.Args[0]
	app, stdout, stderr := newTestApp(nil, nil)
	code := app.Run([]string{"setup", "doctor", "--steps", path, "--digest", digest, "--set", me, "--setup-dir", sd})
	if code != 1 {
		t.Fatalf("doctor with gaps = %d, want 1; stderr: %s", code, stderr)
	}
	out := stdout.String()
	for _, want := range []string{
		"CLI demo (file " + path + ", sha256 " + digest + ")",
		"  ok   present: Something present",
		"  gap  absent [fixable]: Something absent",
		"do:  guildhall setup doctor --fix --only absent --steps ",
		"  gap  person [human]: A person's step",
		"do:  guildhall setup wizard --steps ",
		"2 gap(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}

	app, stdout, stderr = newTestApp(nil, nil)
	if code := app.Run([]string{"setup", "doctor", "--json", "--steps", path, "--digest", digest, "--set", me, "--setup-dir", sd}); code != 0 {
		t.Fatalf("doctor --json = %d; stderr: %s", code, stderr)
	}
	var r setup.Report
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil || r.Schema != setup.ReportSchema || len(r.Gaps) != 2 || r.Gaps[0].ID != "absent" {
		t.Errorf("doctor --json = %s, %v", stdout, err)
	}

	app, _, stderr = newTestApp(nil, nil)
	if code := app.Run([]string{"setup", "doctor", "--fix", "--only", "person", "--steps", path, "--digest", digest, "--set", me, "--setup-dir", sd}); code != 1 || !strings.Contains(stderr.String(), "human step") {
		t.Errorf("--fix --only person = %d, stderr %s", code, stderr)
	}
}

func TestSetupRefusals(t *testing.T) {
	path, digest, sd := writeSetupDemo(t)
	for name, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"file without digest":  {[]string{"setup", "doctor", "--steps", path}, 1, "needs --digest"},
		"wrong digest":         {[]string{"setup", "wizard", "--steps", path, "--digest", strings.Repeat("0", 64), "--setup-dir", sd}, 1, "digest mismatch"},
		"built-in, wrong one":  {[]string{"setup", "doctor", "--steps", "herdr", "--digest", digest, "--setup-dir", sd}, 1, "digest mismatch"},
		"undeclared value":     {[]string{"setup", "doctor", "--steps", path, "--digest", digest, "--set", "colour=red", "--setup-dir", sd}, 1, "colour"},
		"only without fix":     {[]string{"setup", "doctor", "--only", "absent"}, 2, "--only belongs to --fix"},
		"json without steps":   {[]string{"setup", "doctor", "--json"}, 2, "--json reports one step file"},
		"wizard without steps": {[]string{"setup", "wizard"}, 2, "--steps is required"},
		"unknown subcommand":   {[]string{"setup", "install"}, 2, "unknown subcommand"},
		"unknown built-in":     {[]string{"setup", "run", "--steps", "nope", "--setup-dir", sd}, 1, "no built-in step file"},
	} {
		app, _, stderr := newTestApp(nil, nil)
		if code := app.Run(c.args); code != c.code || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: exit %d, stderr %q; want %d and %q", name, code, stderr, c.code, c.want)
		}
	}
	if _, err := os.Stat(sd); !os.IsNotExist(err) {
		t.Errorf("a refused command wrote the setup directory: %v", err)
	}
}

func TestSetupWizardDryRunAndRun(t *testing.T) {
	path, digest, sd := writeSetupDemo(t)
	me := "me=" + os.Args[0]
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader("")
	if code := app.Run([]string{"setup", "wizard", "--dry-run", "--steps", path, "--digest", digest, "--set", me, "--setup-dir", sd}); code != 0 {
		t.Fatalf("wizard --dry-run = %d; stderr: %s", code, stderr)
	}
	for _, want := range []string{"Step 1 of 3 · Something present [fixable]", "Step 3 of 3 · A person's step [human]", "would ask (confirm): Done?"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("dry run lacks %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(sd); !os.IsNotExist(err) {
		t.Errorf("dry run wrote the setup directory: %v", err)
	}

	app, stdout, stderr = newTestApp(nil, nil)
	if code := app.Run([]string{"setup", "run", "--only", "present,person", "--steps", path, "--digest", digest, "--set", me, "--setup-dir", sd}); code != 0 {
		t.Fatalf("run = %d; stderr: %s", code, stderr)
	}
	if out := stdout.String(); !strings.HasPrefix(out, "waiting person\nrun: guildhall setup wizard --steps ") {
		t.Errorf("run output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(sd, "journal.jsonl")); err != nil {
		t.Errorf("run kept no journal: %v", err)
	}
}

func TestSetupDoctorAllBuiltins(t *testing.T) {
	sd := filepath.Join(t.TempDir(), "setup")
	app, stdout, _ := newTestApp(nil, nil)
	app.Run([]string{"setup", "doctor", "--setup-dir", sd})
	for _, name := range setup.Builtins() {
		s, _ := setup.LoadBuiltin(name, "")
		if !strings.Contains(stdout.String(), "(built-in "+name+", sha256 "+s.Digest+")") {
			t.Errorf("doctor without --steps does not report %s:\n%s", name, stdout)
		}
	}
}

func TestSetupUsageNamesEveryBuiltin(t *testing.T) {
	app, stdout, _ := newTestApp(nil, nil)
	if code := app.Run([]string{"setup", "help"}); code != 0 {
		t.Fatalf("setup help exit = %d", code)
	}
	want := "--steps names a built-in step file (" + strings.Join(setup.Builtins(), ", ") + ")"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("usage lacks %q:\n%s", want, stdout)
	}
}
