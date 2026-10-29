package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkRevisions binds the test outcomes to a code head, a spec digest and a standards revision.
var checkRevisions = `"revisions": {"subject": {"code_head": "` + strings.Repeat("a", 40) + `"}, "spec_digest": "sha256:` + strings.Repeat("b", 64) + `", "standards_revision": "standards-1"}`

// checkOutcome is a /2 outcome with the given verdict and body keys, bound to checkRevisions.
func checkOutcome(verdict, body string) string {
	if body != "" {
		body += ", "
	}
	return `{"schema": "guildhall.review-outcome/2", "reviewer": "automated", "verdict": "` + verdict + `", "summary": "The change does what the spec asks.", ` + body + checkRevisions + `}`
}

// runReviewCheck writes each outcome to a file, runs guildhall review check with the given arguments,
// in which "{outcome}" and "{previous}" name those files, and returns the exit code, stdout and stderr.
func runReviewCheck(t *testing.T, outcome, previous string, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"{outcome}": outcome, "{previous}": previous}
	for i, arg := range args {
		data, ok := files[arg]
		if !ok {
			continue
		}
		path := filepath.Join(dir, strings.Trim(arg, "{}")+".json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		args[i] = path
	}
	var stdout, stderr bytes.Buffer
	app := New()
	app.Stdout, app.Stderr = &stdout, &stderr
	code := app.Run(append([]string{"review", "check"}, args...))
	return code, stdout.String(), stderr.String()
}

func TestReviewCheckAcceptsAValidOutcome(t *testing.T) {
	v2 := checkOutcome("changes_requested", `"findings": [{"id": "f1", "rule": "S2", "summary": "missing error check"}], "suggestions": [{"id": "s1", "summary": "a shorter name"}]`)
	v1 := `{"schema": "guildhall.review-outcome/1", "reviewer": "automated", "verdict": "approved", ` + checkRevisions + `}`
	for name, outcome := range map[string]string{"v2": v2, "v1": v1} {
		code, stdout, stderr := runReviewCheck(t, outcome, "", "{outcome}")
		if code != 0 || stdout != "ok\n" || stderr != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}

// TestReviewCheckRefusesWhatIntakeRefuses runs the outcomes that lost d12 review-3 and d27 review-5 a round.
func TestReviewCheckRefusesWhatIntakeRefuses(t *testing.T) {
	cases := []struct{ name, outcome string }{
		{"d12: a resolved previous suggestion listed again", checkOutcome("approved",
			`"suggestions": [{"id": "suggest-4", "summary": "a shorter name"}], "dispositions": [{"finding": "suggest-4", "state": "resolved"}]`)},
		{"d27: a resolved finding listed again", checkOutcome("changes_requested",
			`"findings": [{"id": "relaunch-base", "rule": "S2", "summary": "relaunch starts from a stale base"}], "dispositions": [{"finding": "relaunch-base", "state": "resolved"}]`)},
	}
	want := map[string]string{
		cases[0].name: `resolved finding "suggest-4" is still listed in findings or suggestions`,
		cases[1].name: `resolved finding "relaunch-base" is still listed in findings or suggestions`,
	}
	for _, c := range cases {
		code, stdout, stderr := runReviewCheck(t, c.outcome, "", "{outcome}")
		if code != 1 || stdout != "" || !strings.Contains(stderr, want[c.name]) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 1 and %q", c.name, code, stdout, stderr, want[c.name])
		}
	}
}

func TestReviewCheckReportsEveryProblem(t *testing.T) {
	outcome := checkOutcome("changes_requested", `"findings": [{"id": "f1", "summary": "missing error check"}], "dispositions": [{"finding": "f0", "state": "disputed"}]`)
	code, _, stderr := runReviewCheck(t, outcome, "", "{outcome}")
	for _, want := range []string{`finding "f1" cites no rule`, `disputed finding "f0" is not listed in findings`} {
		if code != 1 || !strings.Contains(stderr, want) {
			t.Errorf("exit %d, stderr %q; want exit 1 and %q", code, stderr, want)
		}
	}
}

func TestReviewCheckRefusesAnUnknownKey(t *testing.T) {
	outcome := checkOutcome("approved", `"verdicts": []`)
	if code, _, stderr := runReviewCheck(t, outcome, "", "{outcome}"); code != 1 || !strings.Contains(stderr, "verdicts") {
		t.Errorf("exit %d, stderr %q; want exit 1 naming the unknown key", code, stderr)
	}
}

func TestReviewCheckPrevious(t *testing.T) {
	previous := checkOutcome("changes_requested", `"findings": [{"id": "x", "rule": "S2", "summary": "missing error check"}], "suggestions": [{"id": "s1", "summary": "a shorter name"}]`)
	covered := checkOutcome("approved", `"dispositions": [{"finding": "x", "state": "resolved"}]`)
	if code, stdout, stderr := runReviewCheck(t, covered, previous, "{outcome}", "--previous", "{previous}"); code != 0 || stdout != "ok\n" {
		t.Errorf("a previous suggestion left without a disposition: exit %d, stdout %q, stderr %q; want ok", code, stdout, stderr)
	}
	// The flag may also come first.
	if code, _, stderr := runReviewCheck(t, covered, previous, "--previous", "{previous}", "{outcome}"); code != 0 {
		t.Errorf("--previous before the outcome: exit %d, stderr %q", code, stderr)
	}
	uncovered := checkOutcome("approved", "")
	code, _, stderr := runReviewCheck(t, uncovered, previous, "{outcome}", "--previous", "{previous}")
	if want := `previous finding "x" has no disposition`; code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr %q; want exit 1 and %q", code, stderr, want)
	}
	if code, _, stderr := runReviewCheck(t, covered, "{", "{outcome}", "--previous", "{previous}"); code != 1 || !strings.Contains(stderr, "previous") {
		t.Errorf("an invalid previous outcome: exit %d, stderr %q; want exit 1 naming it", code, stderr)
	}
}

func TestReviewCheckUsage(t *testing.T) {
	valid := checkOutcome("approved", "")
	for name, args := range map[string][]string{
		"missing argument":  {},
		"extra argument":    {"{outcome}", "{outcome}"},
		"unknown flag":      {"--bogus", "{outcome}"},
		"previous no value": {"{outcome}", "--previous"},
	} {
		if code, _, _ := runReviewCheck(t, valid, "", args...); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	var stdout, stderr bytes.Buffer
	app := New()
	app.Stdout, app.Stderr = &stdout, &stderr
	if code := app.Run([]string{"review"}); code != 2 {
		t.Errorf("review without a subcommand: exit %d, want 2", code)
	}
	if code := app.Run([]string{"review", "bogus"}); code != 2 {
		t.Errorf("unknown review subcommand: exit %d, want 2", code)
	}
}

func TestReviewCheckUnreadableFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	var stdout, stderr bytes.Buffer
	app := New()
	app.Stdout, app.Stderr = &stdout, &stderr
	if code := app.Run([]string{"review", "check", missing}); code != 1 || !strings.Contains(stderr.String(), "missing.json") {
		t.Errorf("exit %d, stderr %q; want exit 1 with the read error", code, stderr.String())
	}
}
