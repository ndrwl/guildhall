package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

// demo is a cross-OS step file: argv commands through the setuptool stub, no shell.
const demo = `schema = "guildhall.steps/1"
name = "demo"
title = "Demo"

[params]
tool = ""

[[step]]
id = "ready"
title = "Already there"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "exists", "${home}"]
action = ["${tool}", "write", "${home}/never", "x"]
undo = "Nothing to undo."

[[step]]
id = "alpha"
title = "Alpha marker"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "cat", "${home}/alpha"]
expect = "alpha-ok"
action = ["${tool}", "write", "${home}/alpha", "alpha-ok"]
creates = ["${home}/alpha"]
undo = "Remove ~/alpha."

[[step]]
id = "beta"
title = "Beta marker"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "exists", "${home}/beta"]
action = ["${tool}", "write", "${home}/beta", "b"]
creates = ["${home}/beta"]
undo = "Remove ~/beta."

[[step]]
id = "person"
title = "A person's step"
tag = "human"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "exists", "${home}/person"]
instructions = "Create ~/person by hand."
undo = "Remove ~/person."

[[step.prompt]]
kind = "confirm"
text = "Have you created ~/person?"

[[step]]
id = "elsewhere"
title = "Only on another OS"
tag = "fixable"
os = ["plan9-never"]
check = ["${tool}", "fail"]
action = ["${tool}", "fail"]
undo = "None."
`

// demoFor is demo with the last step's os list made a real OS other than the running one.
func demoFor(t *testing.T) setup.Steps {
	other := "darwin"
	if runtime.GOOS == "darwin" {
		other = "linux"
	}
	return parse(t, strings.Replace(demo, `"plan9-never"`, `"`+other+`"`, 1))
}

func TestDoctorReport(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	r, err := setup.Doctor(context.Background(), s, sb.facts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != setup.ReportSchema || r.Steps != "demo" || r.Digest != s.Digest {
		t.Fatalf("report header = %+v", r)
	}
	var ids []string
	for _, g := range r.Gaps {
		ids = append(ids, g.ID+":"+string(g.Tag))
	}
	if want := []string{"alpha:fixable", "beta:fixable", "person:human"}; !slices.Equal(ids, want) {
		t.Fatalf("gaps = %v, want %v", ids, want)
	}
	if !slices.Equal(r.Skipped, []string{"elsewhere"}) {
		t.Errorf("skipped = %v", r.Skipped)
	}
	fixLine := "guildhall setup doctor --fix --only alpha --steps test.toml --digest " + s.Digest + " --set "
	if !strings.HasPrefix(r.Gaps[0].Action, fixLine) || !strings.Contains(r.Gaps[0].Action, "--setup-dir ") {
		t.Errorf("fixable action = %q, want prefix %q", r.Gaps[0].Action, fixLine)
	}
	wizLine := "guildhall setup wizard --steps test.toml --digest " + s.Digest + " --only person "
	if !strings.HasPrefix(r.Gaps[2].Action, wizLine) {
		t.Errorf("human action = %q, want prefix %q", r.Gaps[2].Action, wizLine)
	}
	// The doctor only reads: no setup directory, journal or marker appears, and a second run is the same report.
	if _, err := os.Stat(sb.setupDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("doctor wrote the setup directory: %v", err)
	}
	again, err := setup.Doctor(context.Background(), s, sb.facts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Errorf("second report differs:\n%s\n%s", a, b)
	}
}

func TestFixOnlyTouchesNamedGap(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	j := &setup.FileJournal{Dir: sb.setupDir}
	r, err := setup.Fix(context.Background(), s, sb.facts, []string{"alpha"}, j)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sb.home, "beta")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("fix --only alpha touched beta: %v", err)
	}
	var gaps []string
	for _, g := range r.Gaps {
		gaps = append(gaps, g.ID)
	}
	if !slices.Equal(gaps, []string{"beta", "person"}) {
		t.Errorf("re-derived gaps = %v, want beta, person", gaps)
	}
	m, err := setup.ReadManifest(sb.setupDir)
	if err != nil || !m.Owns(filepath.Join(sb.home, "alpha")) || m.Owns(filepath.Join(sb.home, "beta")) {
		t.Errorf("manifest = %+v, %v; want only alpha", m, err)
	}
	// A second fix of the same gap runs no action: its check passes.
	before := tree(t, sb.home)
	if _, err := setup.Fix(context.Background(), s, sb.facts, []string{"alpha"}, nil); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, sb.home); len(after) != len(before) {
		t.Errorf("re-run changed files: %v -> %v", before, after)
	}
}

func TestFixRefusals(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	for _, c := range []struct {
		ids  []string
		want error
	}{
		{[]string{"alpha", "person"}, setup.ErrHumanStep},
		{[]string{"nope"}, setup.ErrUnknownStep},
		{[]string{"elsewhere"}, setup.ErrUnsupportedOS},
	} {
		_, err := setup.Fix(context.Background(), s, sb.facts, c.ids, nil)
		if !errors.Is(err, c.want) {
			t.Errorf("Fix(%v) = %v, want %v", c.ids, err, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(sb.home, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused fix ran alpha: %v", err)
	}
	if _, err := setup.Fix(context.Background(), s, setup.Facts{Home: sb.home, Vars: sb.facts.Vars}, nil, nil); err == nil {
		t.Error("Fix with no setup directory succeeded")
	}
	f := sb.facts
	f.Vars = map[string]string{"tool": tool(), "undeclared": "x"}
	if _, err := setup.Doctor(context.Background(), s, f); !errors.Is(err, setup.ErrInvalid) {
		t.Errorf("Doctor with an undeclared value = %v, want ErrInvalid", err)
	}
}

func TestFixAllClosesOnlyFixableGaps(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	r, err := setup.Fix(context.Background(), s, sb.facts, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps) != 1 || r.Gaps[0].ID != "person" {
		t.Errorf("gaps after --fix = %+v, want only person", r.Gaps)
	}
}

func TestNotOwned(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	alpha := filepath.Join(sb.home, "alpha")
	if err := os.WriteFile(alpha, []byte("someone else's"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := setup.Fix(context.Background(), s, sb.facts, []string{"alpha"}, nil)
	if !errors.Is(err, setup.ErrNotOwned) {
		t.Fatalf("Fix over an unowned file = %v, want ErrNotOwned", err)
	}
	var se *setup.StepError
	if !errors.As(err, &se) || se.Step != "alpha" {
		t.Errorf("error = %v, want a StepError for alpha", err)
	}
	if data, _ := os.ReadFile(alpha); string(data) != "someone else's" {
		t.Errorf("unowned file overwritten: %q", data)
	}
	// A file the engine created may be rewritten: remove the expected content and fix again.
	if err := os.Remove(alpha); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Fix(context.Background(), s, sb.facts, []string{"alpha"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alpha, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Fix(context.Background(), s, sb.facts, []string{"alpha"}, nil); err != nil {
		t.Fatalf("re-fix of an owned file: %v", err)
	}
	if data, _ := os.ReadFile(alpha); string(data) != "alpha-ok" {
		t.Errorf("owned file = %q, want alpha-ok", data)
	}
}

func TestRunWaitsAtHumanStep(t *testing.T) {
	sb := newSandbox(t)
	s := demoFor(t)
	out, err := setup.Run(context.Background(), s, sb.facts, nil, &setup.FileJournal{Dir: sb.setupDir})
	if err != nil {
		t.Fatal(err)
	}
	if out.Waiting != "person" || !strings.HasPrefix(out.Action, "guildhall setup wizard ") {
		t.Errorf("outcome = %+v, want waiting person", out)
	}
	for _, f := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(sb.home, f)); err != nil {
			t.Errorf("run did not create %s: %v", f, err)
		}
	}
	if err := os.WriteFile(filepath.Join(sb.home, "person"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = setup.Run(context.Background(), s, sb.facts, nil, nil)
	if err != nil || out.Waiting != "" {
		t.Errorf("second run = %+v, %v; want done", out, err)
	}
}

func TestDigestMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.toml")
	valid := strings.Replace(demo, `"plan9-never"`, `"linux"`, 1)
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	good := setup.Digest([]byte(valid))
	if _, err := setup.LoadSteps(path, good); err != nil {
		t.Fatalf("LoadSteps with its own digest: %v", err)
	}
	changed := []byte(valid)
	changed[len(changed)-2] ^= 1 // one byte inside the last line
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.LoadSteps(path, good); !errors.Is(err, setup.ErrDigestMismatch) {
		t.Errorf("one byte changed: %v, want ErrDigestMismatch", err)
	}
	if _, err := setup.LoadBuiltin("herdr", good); !errors.Is(err, setup.ErrDigestMismatch) {
		t.Errorf("built-in under another digest: %v, want ErrDigestMismatch", err)
	}
}

func TestParseRefusals(t *testing.T) {
	head := "schema = \"guildhall.steps/1\"\nname = \"x\"\n[[step]]\nid = \"a\"\ntitle = \"A\"\nos = [\"linux\"]\nundo = \"u\"\ncheck = [\"true\"]\n"
	for name, src := range map[string]string{
		"unknown key":         head + "tag = \"fixable\"\naction = [\"true\"]\ncolour = \"red\"\n",
		"no act":              head + "tag = \"fixable\"\n",
		"both acts":           head + "tag = \"fixable\"\naction = [\"true\"]\n[step.fetch]\nurl = \"u\"\nsha256 = \"s\"\ndest = \"/d\"\n",
		"human with action":   head + "tag = \"human\"\ninstructions = \"i\"\naction = [\"true\"]\n",
		"human with nothing":  head + "tag = \"human\"\n",
		"bad tag":             head + "tag = \"maybe\"\naction = [\"true\"]\n",
		"bad shell":           head + "tag = \"fixable\"\n[step.action]\nshell = \"bash\"\nscript = \"true\"\n",
		"unknown value":       head + "tag = \"fixable\"\naction = [\"${nope}\"]\n",
		"bad prompt kind":     head + "tag = \"human\"\n[[step.prompt]]\nkind = \"shout\"\ntext = \"t\"\n",
		"secret without name": head + "tag = \"human\"\n[[step.prompt]]\nkind = \"ask_secret\"\ntext = \"t\"\n",
		"wrong schema":        strings.Replace(head, "guildhall.steps/1", "guildhall.steps/2", 1) + "tag = \"fixable\"\naction = [\"true\"]\n",
		"duplicate id":        head + "tag = \"fixable\"\naction = [\"true\"]\n" + strings.SplitN(head, "[[step]]", 2)[1] + "tag = \"fixable\"\naction = [\"true\"]\n",
	} {
		if _, err := setup.Parse([]byte(src), ""); !errors.Is(err, setup.ErrInvalid) {
			t.Errorf("%s: Parse = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := setup.Parse([]byte(head+"tag = \"fixable\"\naction = [\"true\"]\n"), ""); err != nil {
		t.Errorf("valid file refused: %v", err)
	}
}

func TestBuiltins(t *testing.T) {
	want := map[string][]string{
		"git-and-claude": {"git", "gh", "claude"},
		"go":             {"go", "go-on-path"},
		"herdr":          {"herdr"},
		"sign-in":        {"claude-sign-in", "codex-sign-in", "gh-sign-in"},
		"wsl-settings":   {"wsl-systemd", "wsl-keep-running"},
	}
	if got := setup.Builtins(); !slices.Equal(got, []string{"git-and-claude", "go", "herdr", "sign-in", "wsl-settings"}) {
		t.Fatalf("Builtins = %v", got)
	}
	seen := map[string]string{}
	for name, ids := range want {
		s, err := setup.LoadBuiltin(name, "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		data, _ := setup.BuiltinBytes(name)
		if s.Name != name || !s.Builtin || s.Ref != name || s.Digest != setup.Digest(data) {
			t.Errorf("%s: header %+v", name, s)
		}
		var got []string
		for _, st := range s.Steps {
			got = append(got, st.ID)
			if other, dup := seen[st.ID]; dup {
				t.Errorf("step id %s is in both %s and %s; gap ids must be unique across built-ins", st.ID, other, name)
			}
			seen[st.ID] = name
		}
		if !slices.Equal(got, ids) {
			t.Errorf("%s: steps %v, want %v", name, got, ids)
		}
	}
	for _, name := range []string{"git-and-claude", "sign-in", "wsl-settings"} {
		s, _ := setup.LoadBuiltin(name, "")
		for _, st := range s.Steps {
			if st.Tag != setup.Human {
				t.Errorf("%s/%s is %s; administrator installs and sign-ins are human steps", name, st.ID, st.Tag)
			}
		}
	}
	if _, err := setup.LoadBuiltin("../herdr", ""); !errors.Is(err, setup.ErrUnknownStep) {
		t.Errorf("LoadBuiltin(../herdr) = %v", err)
	}
}
