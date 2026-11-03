package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// goodSpec is a plan's spec the approval can show: a Summary block and well-formed open questions.
const goodSpec = `# Sort the task list

## Summary

- **Goal:** sort the task list.
- **Open questions:** 1.

## Open questions

**Q1. Which order?**
Options: (a) by priority; (b) by age.
Recommended: (a).
`

// preambleSpec is goodSpec with a sentence before Q1, the shape d165's plan was refused for.
var preambleSpec = strings.Replace(goodSpec, "**Q1.", "One question is open.\n\n**Q1.", 1)

// resultCopy is a Director copy whose task app/t-1 has a running plan attempt 1, with its working directory.
type resultCopy struct {
	dir, work string
	key       artisan.AttemptKey
}

func newResultCopy(t *testing.T) resultCopy {
	t.Helper()
	dir := markCopy(t, newCopy(t))
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new: exit %d, %s", code, stderr)
	}
	s, err := store.Open(filepath.Join(dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.StartAttempt("start-plan-1", "app", "t-1", "plan")
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	key := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 1}
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(key.Dir(state), artisan.ModelDir), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(state, "app", "t-1", "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	return resultCopy{dir: dir, work: work, key: key}
}

// draft writes spec as spec.md in the working directory and, once, a succeeded result listing it as the attempt's
// model/draft.json, and returns the draft's path.
func (c resultCopy) draft(t *testing.T, spec string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.work, "spec.md"), []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.ModelDir, "draft.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if err := adapter.WriteResult(p, adapter.Result{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "plan-1"}, Status: adapter.StatusSucceeded,
		Summary: "planned", Outputs: []adapter.Output{{Path: "spec.md"}}}); err != nil {
		t.Fatal(err)
	}
	return p
}

func (c resultCopy) draftFile() string {
	return filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.ModelDir, artisan.ResultFile)
}

// run runs guildhall in the working directory with GUILDHALL_ATTEMPT naming the attempt, unless env is given.
func (c resultCopy) run(t *testing.T, env string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Getwd = func() (string, error) { return c.work, nil }
	app.Getenv = func(name string) string {
		if name == artisan.AttemptEnv {
			return env
		}
		return ""
	}
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

const preambleReason = `rejected: open questions malformed: ## Open questions line 10 is neither "None" nor a **Qn. title** question`

func TestResultCheck(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	draft := c.draft(t, goodSpec)
	if code, out, stderr := c.run(t, env, "result", "check", draft); code != 0 || out != "ok\n" {
		t.Fatalf("check of a good spec: exit %d, %q, %s", code, out, stderr)
	}
	if code, out, stderr := c.run(t, env, "result", "check", "--copy", c.dir, draft); code != 0 || out != "ok\n" {
		t.Fatalf("check --copy of a good spec: exit %d, %q, %s", code, out, stderr)
	}
	c.draft(t, preambleSpec)
	before := storeBytes(t, c.dir)
	for range 4 {
		code, out, stderr := c.run(t, env, "result", "check", draft)
		if code != 1 || !strings.HasPrefix(out, preambleReason) || strings.Count(out, "\n") != 1 {
			t.Fatalf("check of a preamble: exit %d, %q, %s", code, out, stderr)
		}
	}
	if storeBytes(t, c.dir) != before {
		t.Error("check changed the store")
	}
	if _, err := os.Stat(c.draftFile()); !os.IsNotExist(err) {
		t.Errorf("check wrote the draft: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.SubmissionsFile)); !os.IsNotExist(err) {
		t.Errorf("check counted a submission: %v", err)
	}
}

// TestResultSubmitFixesARejection: a rejected submission leaves the draft absent and says how many resubmissions
// are left; a fixed one is written as the draft.
func TestResultSubmitFixesARejection(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	draft := c.draft(t, preambleSpec)
	code, out, stderr := c.run(t, env, "result", "submit", draft)
	if code != 1 || !strings.HasPrefix(out, preambleReason) || !strings.HasSuffix(out, "\n1 of 2 resubmissions used; fix the result and submit again\n") {
		t.Fatalf("submit of a preamble: exit %d, %q, %s", code, out, stderr)
	}
	if _, err := os.Stat(c.draftFile()); !os.IsNotExist(err) {
		t.Fatalf("a rejected submission wrote the draft: %v", err)
	}
	c.draft(t, goodSpec)
	if code, out, stderr := c.run(t, env, "result", "submit", draft); code != 0 || out != "ok\n" {
		t.Fatalf("submit of the fix: exit %d, %q, %s", code, out, stderr)
	}
	want, _ := os.ReadFile(draft)
	if got, err := os.ReadFile(c.draftFile()); err != nil || string(got) != string(want) {
		t.Fatalf("draft = %q, %v; want the submitted result", got, err)
	}
	// A later rejection leaves the accepted draft as it is.
	c.draft(t, preambleSpec)
	if code, out, _ := c.run(t, env, "result", "submit", draft); code != 1 || !strings.HasSuffix(out, "\n2 of 2 resubmissions used; fix the result and submit again\n") {
		t.Fatalf("second rejection: exit %d, %q", code, out)
	}
	if got, _ := os.ReadFile(c.draftFile()); string(got) != string(want) {
		t.Error("a rejected submission replaced the accepted draft")
	}
}

// TestResultSubmitThirdRejection: the third rejected submission becomes the draft and tells the Artisan to stop;
// the counter lives outside the model directory, so clearing that does not reset it.
func TestResultSubmitThirdRejection(t *testing.T) {
	c := newResultCopy(t)
	env := c.key.String()
	draft := c.draft(t, preambleSpec)
	for k := 1; k <= 2; k++ {
		if code, _, stderr := c.run(t, env, "result", "submit", draft); code != 1 {
			t.Fatalf("rejection %d: exit %d, %s", k, code, stderr)
		}
	}
	model := filepath.Dir(c.draftFile())
	entries, _ := os.ReadDir(model)
	for _, e := range entries {
		if e.Name() != "draft.json" {
			_ = os.Remove(filepath.Join(model, e.Name()))
		}
	}
	code, out, _ := c.run(t, env, "result", "submit", draft)
	if code != 1 || !strings.HasPrefix(out, preambleReason) || !strings.HasSuffix(out, "\nno resubmissions left: this result will fail the attempt; stop now\n") {
		t.Fatalf("third rejection: exit %d, %q", code, out)
	}
	want, _ := os.ReadFile(draft)
	if got, err := os.ReadFile(c.draftFile()); err != nil || string(got) != string(want) {
		t.Fatalf("draft = %q, %v; want the third rejected result", got, err)
	}
}

func TestResultRefusals(t *testing.T) {
	c := newResultCopy(t)
	draft := c.draft(t, goodSpec)
	other := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "plan", Attempt: 2}
	if err := os.MkdirAll(other.Dir(filepath.Join(c.dir, "state")), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		name, env string
		args      []string
		says      string
	}{
		{"no attempt env", "", []string{"result", "check", draft}, "GUILDHALL_ATTEMPT is not set"},
		{"malformed attempt env", "app/t-1/plan", []string{"result", "submit", draft}, "is not <repository>/<task>/<step>/<n>"},
		{"attempt not running", other.String(), []string{"result", "submit", draft}, "attempt is not running: no attempt 2"},
		{"attempt of no copy", "app/t-1/plan/3", []string{"result", "submit", draft}, "no Guildhall copy found"},
		{"unreadable file", c.key.String(), []string{"result", "submit", "missing.json"}, "missing.json"},
	} {
		t.Run(r.name, func(t *testing.T) {
			code, out, stderr := c.run(t, r.env, r.args...)
			if code != 2 || out != "" || !strings.Contains(stderr, r.says) {
				t.Errorf("exit %d, stdout %q, stderr %q; want exit 2 saying %q", code, out, stderr, r.says)
			}
		})
	}
	// An attempt that exists, but has an outcome, is not running.
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FinishAttempt("finish-plan-1", "app", "t-1", "plan", 1, store.AttemptSucceeded)
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, out, stderr := c.run(t, c.key.String(), "result", "submit", draft); code != 2 || out != "" || !strings.Contains(stderr, "attempt is not running") {
		t.Errorf("finished attempt: exit %d, %q, %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.SubmissionsFile)); !os.IsNotExist(err) {
		t.Errorf("a refusal counted a submission: %v", err)
	}
	// Outside a copy, nothing is checked.
	app, stdout, stderr := newTestApp(nil, nil)
	wd := t.TempDir()
	app.Getwd = func() (string, error) { return wd, nil }
	app.Getenv = func(string) string { return c.key.String() }
	if code := app.Run([]string{"result", "check", draft}); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), noCopyLine(wd)) {
		t.Errorf("outside a copy: exit %d, %q, %q", code, stdout, stderr)
	}
}

// takeDraft publishes the draft as the attempt's result, as the wrapper does once the model exits, and runs step
// done on it; it returns the attempt as intake left it.
func (c resultCopy) takeDraft(t *testing.T) store.StageAttempt {
	t.Helper()
	data, err := os.ReadFile(c.draftFile())
	if err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(c.key.Dir(filepath.Join(c.dir, "state")), artisan.ResultFile)
	if err := os.WriteFile(result, data, 0o600); err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.InstallSkills = func(string) error { return nil }
	app.InstallBaseline = baseline.Install
	app.Executable = os.Executable
	// Intake records the attempt before the walk goes on, here to the grill step this fixture never ran, whose
	// working directory is no worktree, so the advance's own exit is not the point.
	code := app.Run([]string{"step", "done", "--copy", c.dir, "--repo", "app", "--task", "t-1", "--step", "plan", "--attempt", "1", "--result", result})
	t.Logf("step done: exit %d, %s %s", code, stdout, stderr)
	s, err := store.Open(filepath.Join(c.dir, directorStoreFile), store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	attempts, err := s.Attempts("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.Step == "plan" && a.Number == 1 {
			return a
		}
	}
	t.Fatal("plan attempt 1 absent")
	return store.StageAttempt{}
}

// TestResultSubmitThenIntake: intake takes the draft submit left: after a rejection and a fix the attempt succeeds
// with no intake rejection; after the third rejection it fails with that submission's reason.
func TestResultSubmitThenIntake(t *testing.T) {
	t.Run("fixed", func(t *testing.T) {
		c := newResultCopy(t)
		draft := c.draft(t, preambleSpec)
		if code, _, _ := c.run(t, c.key.String(), "result", "submit", draft); code != 1 {
			t.Fatalf("rejection: exit %d", code)
		}
		c.draft(t, goodSpec)
		if code, _, stderr := c.run(t, c.key.String(), "result", "submit", draft); code != 0 {
			t.Fatalf("fix: exit %d, %s", code, stderr)
		}
		if a := c.takeDraft(t); a.Outcome != store.AttemptSucceeded || a.IntakeRejection != "" {
			t.Errorf("attempt = %+v, want succeeded with no rejection", a)
		}
	})
	t.Run("third rejection", func(t *testing.T) {
		c := newResultCopy(t)
		draft := c.draft(t, preambleSpec)
		var out string
		for range 3 {
			_, out, _ = c.run(t, c.key.String(), "result", "submit", draft)
		}
		reason, _, _ := strings.Cut(strings.TrimPrefix(out, "rejected: "), "\n")
		if a := c.takeDraft(t); a.Outcome != store.AttemptFailed || a.IntakeRejection != reason {
			t.Errorf("attempt = %+v, want failed with %q", a, reason)
		}
	})
}

// storeBytes is the copy's store file's content.
func storeBytes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, directorStoreFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
