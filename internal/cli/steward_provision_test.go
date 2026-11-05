package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestStewardProvision runs a provisioning plan through the CLI against this machine, played as the roster's
// workstation by the takeover fixture: the dry run runs the real setup doctor, lists the Steward-written step file
// in full and creates nothing; the apply refuses until approved; then it fixes the copy directory with the real
// doctor --fix, copies the step file and waits at its human step with the wizard command, never running the
// wizard; once the person has closed the gap, the next apply goes on.
func TestStewardProvision(t *testing.T) {
	ts := newTakeoverSetup(t)
	cfgPath := filepath.Join(ts.steward, "config.toml")
	base := filepath.Dir(ts.director)
	copyDir, setupDir, done := filepath.Join(base, "ops"), filepath.Join(base, "setup"), filepath.Join(base, "done")
	manual := `schema = "guildhall.steps/1"
name = "manual"
title = "A step only a person can do"

[[step]]
id = "manual-check"
title = "Do the manual thing"
tag = "human"
os = ["linux", "darwin"]
check = ["test", "-f", "` + done + `"]
instructions = "Do the manual thing, then confirm."
undo = "Undo the manual thing."

[[step.prompt]]
kind = "confirm"
text = "Done?"
`
	requestDir := t.TempDir()
	writeFile(t, filepath.Join(requestDir, "manual.toml"), manual)
	request := filepath.Join(requestDir, "request.toml")
	writeFile(t, request, fmt.Sprintf(`schema_version = 1
machine = "workstation"
director = "ops"
copy_dir = %q
setup_dir = %q
herdr_session = "guild"
step_files = ["director", "manual.toml"]

[[repository]]
id = "infra"
remote = "git@example.com:guild/infra.git"
path = %q
`, copyDir, setupDir, filepath.Join(base, "src", "infra")))
	planDir := filepath.Join(ts.steward, "rollouts", "provision-ops")
	dry := []string{"steward", "provision", "--config", cfgPath, "--request", request, "--plan", planDir, "--dry-run"}
	code, out, stderr := ts.run(t, false, "", dry...)
	if code != 0 {
		t.Fatalf("dry run: exit %d, %s", code, stderr)
	}
	m := regexp.MustCompile(`^provision plan (sha256:[0-9a-f]{64})\n`).FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "step file manual: written by the Steward") || !strings.Contains(out, `    | id = "manual-check"`) ||
		!strings.Contains(out, "the Guildmaster runs on workstation: guildhall setup wizard --steps "+copyDir+"/state/steward/setup/manual-") ||
		!strings.Contains(out, "setup doctor --json --fix --only copy-dir --steps director") {
		t.Fatalf("dry run printed:\n%s", out)
	}
	plan := m[1]
	if _, err := os.Stat(copyDir); err == nil {
		t.Fatal("the dry run made the copy directory")
	}
	if code, again, _ := ts.run(t, false, "", dry...); code != 0 || !strings.HasPrefix(again, "provision plan "+plan+"\n") {
		t.Errorf("second dry run: exit %d, %s", code, again)
	}

	apply := []string{"steward", "provision", "--config", cfgPath, "--plan", planDir, "--apply"}
	if code, _, stderr := ts.run(t, false, "", apply...); code != 1 || !strings.Contains(stderr, "steward approval request --plan") {
		t.Errorf("apply without approval: exit %d, %s", code, stderr)
	}
	if code, _, stderr := ts.run(t, false, "", "steward", "takeover", "--config", cfgPath, "--plan", planDir, "--apply", "--approve", plan); code != 1 || !strings.Contains(stderr, "holds a provision plan") {
		t.Errorf("takeover --apply of a provision plan: exit %d, %s", code, stderr)
	}
	code, out, stderr = ts.run(t, true, "", "steward", "approval", "request", "--plan", planDir, "--copy", ts.steward)
	var req struct {
		Decision store.Decision `json:"decision"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &req) != nil || req.Decision.Subject.Digest != plan || !strings.Contains(req.Decision.Question, "waits for you to run the wizard on the machine for manual-check") {
		t.Fatalf("approval request: exit %d, %s %s", code, out, stderr)
	}

	code, out, stderr = ts.run(t, false, "", append(apply, "--approve", plan)...)
	if code != 1 || !strings.Contains(out, "copy-dir: done") || !strings.Contains(out, "steps-manual: done") ||
		!strings.Contains(out, "waiting manual-check: ") || !strings.Contains(out, "setup wizard --steps") {
		t.Fatalf("first apply: exit %d\n%s%s", code, out, stderr)
	}
	if fi, err := os.Stat(filepath.Join(copyDir, "state", "steward", "setup")); err != nil || !fi.IsDir() {
		t.Errorf("the copy directory was not made: %v", err)
	}
	for _, argv := range ts.fleet.Ran("guild-ws") {
		if strings.Contains(strings.Join(argv, " "), "setup wizard") {
			t.Errorf("the Steward ran %q", argv)
		}
	}
	writeFile(t, done, "")
	code, out, _ = ts.run(t, false, "", append(apply, "--approve", plan)...)
	if !strings.Contains(out, "manual-check: done: the doctor reports manual-check closed") || !strings.Contains(out, "config: done") || !strings.Contains(out, "store: done") {
		t.Errorf("apply after the wizard: exit %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "director.store")); err != nil {
		t.Errorf("no store after the apply: %v", err)
	}
}

// TestStewardProvisionUsage: the modes are exclusive, and the request belongs to the dry run.
func TestStewardProvisionUsage(t *testing.T) {
	for _, args := range [][]string{
		{"--config", "c.toml", "--plan", "p"},
		{"--config", "c.toml", "--plan", "p", "--dry-run", "--apply"},
		{"--config", "c.toml", "--plan", "p", "--dry-run"},
		{"--config", "c.toml", "--plan", "p", "--apply", "--request", "r.toml"},
		{"--config", "c.toml", "--plan", "p", "--dry-run", "--request", "r.toml", "--approve", "sha256:00"},
		{"--plan", "p", "--apply"},
	} {
		app, _, stderr := newTestApp(nil, nil)
		if code := app.Run(append([]string{"steward", "provision"}, args...)); code != 2 {
			t.Errorf("%q: exit %d, %s; want a usage error", args, code, stderr)
		}
	}
}
