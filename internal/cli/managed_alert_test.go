package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// alertDecisionsIn reads the alert decisions of the copy in dir, from its reserved task.
func alertDecisionsIn(t *testing.T, dir string) []store.Decision {
	t.Helper()
	ds, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) ([]store.Decision, error) {
		return s.Decisions(admit.AlertRepository, admit.AlertTask)
	})
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

// answerAlert answers the copy's alert decision id with answer through decide answer.
func answerAlert(t *testing.T, dir, id, answer string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(answer)
	code := app.Run([]string{"decide", "answer", "--copy", dir, "--repo", admit.AlertRepository, "--task", admit.AlertTask, "--decision", id, "--answer-file", "-"})
	return code, stdout.String(), stderr.String()
}

// handEdited is a copy enrolled with steward hq whose authorized managed.toml was then edited by hand, and the
// digests of both revisions.
func handEdited(t *testing.T) (dir, authorized, edited string) {
	t.Helper()
	dir = newCopy(t)
	authorized = writeManaged(t, dir, pinned)
	enrolManaged(t, dir)
	edited = writeManaged(t, dir, map[string]config.ManagedSetting{"max_running_attempts": {Value: "1"}, config.LockWaitKey: {Value: "40s", Overridable: true}})
	return dir, authorized, edited
}

// TestHandEditedManagedTomlRaisesOneAlert: a hand edit of an enrolled copy's managed.toml is recorded
// observed-untrusted once, with the digest it replaced, and opens one approve/reject decision on the reserved task
// that shows in needs_me; the revision is not applied, so new tasks are refused, however many commands load it.
func TestHandEditedManagedTomlRaisesOneAlert(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, authorized, edited := handEdited(t)
	for _, id := range []string{"t-1", "t-2"} {
		if code, stderr := newTaskIn(t, dir, id); code != 1 || !strings.Contains(stderr, "managed.toml "+edited+" is not authorized") ||
			!strings.Contains(stderr, "waits for the Guildmaster's answer to decision managed-mode/untrusted/untrusted-1-") {
			t.Fatalf("task new %s: exit %d, %s", id, code, stderr)
		}
	}
	v := managedView(t, dir)
	observed := 0
	for _, r := range v.Revisions {
		if r.Source == store.SourceObservedUntrusted {
			observed++
			if r.Digest != edited || r.Previous != authorized {
				t.Errorf("observed event = %+v", r)
			}
		}
	}
	if observed != 1 {
		t.Fatalf("observed-untrusted events = %d, want 1: %+v", observed, v.Revisions)
	}
	ds := alertDecisionsIn(t, dir)
	id := admit.AlertDecisionID(1, edited)
	if len(ds) != 1 || ds[0].ID != id || !ds[0].Open() || ds[0].Kind != store.DecisionApproval || ds[0].Subject == nil || ds[0].Subject.Digest != edited ||
		strings.Join(ds[0].Options, ",") != "approve,reject" || !strings.Contains(ds[0].Question, "Approve managed.toml "+edited) {
		t.Fatalf("alert decisions = %+v", ds)
	}

	// The decision is in needs_me, and the copy reports the untrusted digest with nothing applied.
	st := copyStatus(t, dir)
	found := false
	for _, it := range st.NeedsMe {
		found = found || it.Kind == decisionKind && it.Repository == admit.AlertRepository && it.TaskID == admit.AlertTask && it.Decision == id
	}
	if !found {
		t.Fatalf("needs_me lacks the alert decision: %+v", st.NeedsMe)
	}
	if m := st.Mode; m.Name != modeManaged || m.Untrusted == nil || m.Untrusted.Digest != edited || m.Untrusted.State != admit.AlertWaiting ||
		m.Untrusted.Decision != id || m.AppliedDigest != "" || m.Steward != "hq" || m.Refusal == "" {
		t.Fatalf("status mode = %+v", m)
	}
	if got := copyMode(t, dir); got.Authorized || got.Untrusted == nil || got.Untrusted.State != admit.AlertWaiting {
		t.Fatalf("director mode = %+v", got)
	}
}

// copyStatus is director status --json of the copy in dir.
func copyStatus(t *testing.T, dir string) directorStatus {
	t.Helper()
	code, out, stderr := directorCmd(t, nil, "status", "--json", "--copy", dir)
	if code != 0 {
		t.Fatalf("status: exit %d, %s", code, stderr)
	}
	var st directorStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	return st
}

// TestAlertApproveUnblocks: answering the alert approve records a guildmaster-approved revision naming the decision
// and the exact digest, which admits it, so new tasks get its settings.
func TestAlertApproveUnblocks(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, authorized, edited := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	id := admit.AlertDecisionID(1, edited)
	if code, _, stderr := answerAlert(t, dir, id, "approve\nthe Guildmaster: I lowered the cap myself\n"); code != 0 {
		t.Fatalf("decide answer: exit %d, %s", code, stderr)
	}
	v := managedView(t, dir)
	auth := v.Authorization(edited)
	if auth == nil || auth.Source != store.SourceGuildmasterApproved || auth.Decision != id || auth.Digest != edited || auth.Previous != authorized {
		t.Fatalf("authorization = %+v", auth)
	}
	// Answering again changes nothing.
	if code, _, _ := answerAlert(t, dir, id, "approve\nthe Guildmaster: I lowered the cap myself\n"); code != 0 || len(managedView(t, dir).Revisions) != len(v.Revisions) {
		t.Fatalf("a repeated answer: exit %d, revisions %+v", code, managedView(t, dir).Revisions)
	}
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new after approval: exit %d, %s", code, stderr)
	}
	got := copyMode(t, dir)
	if !got.Authorized || got.Untrusted != nil || got.Authorization == nil || got.Authorization.Source != store.SourceGuildmasterApproved {
		t.Fatalf("director mode = %+v", got)
	}
	if st := copyStatus(t, dir); st.Mode.AppliedDigest != edited || st.Mode.AppliedBy != string(store.SourceGuildmasterApproved) || st.Mode.AppliedAt == nil || st.Mode.Untrusted != nil {
		t.Fatalf("status mode = %+v", st.Mode)
	}
}

// TestAlertApprovalRecordedLate: an approve answered but not yet recorded as a revision, as when decide answer was
// interrupted, is recorded by the next command that needs effective settings, which is then admitted.
func TestAlertApprovalRecordedLate(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, _, edited := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	id := admit.AlertDecisionID(1, edited)
	if _, err := withStore(filepath.Join(dir, directorStoreFile), false, func(s *store.Store) (store.Receipt, error) {
		return s.AnswerDecision("late-answer", admit.AlertRepository, admit.AlertTask, id, "approve\n")
	}); err != nil {
		t.Fatal(err)
	}
	if got := copyMode(t, dir); got.Untrusted == nil || got.Untrusted.State != admit.AlertApproved {
		t.Fatalf("director mode = %+v", got)
	}
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new after the answer: exit %d, %s", code, stderr)
	}
	if auth := managedView(t, dir).Authorization(edited); auth == nil || auth.Decision != id {
		t.Fatalf("authorization = %+v", auth)
	}
}

// TestAlertRejectKeepsTheRefusal: a reject records no authorization and the refusal names it, until a reload
// authorizes the digest.
func TestAlertRejectKeepsTheRefusal(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, _, edited := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	id := admit.AlertDecisionID(1, edited)
	if code, _, stderr := answerAlert(t, dir, id, "reject\n"); code != 0 {
		t.Fatalf("decide answer: exit %d, %s", code, stderr)
	}
	before := receiptCount(t, dir)
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 1 || !strings.Contains(stderr, "the Guildmaster rejected it (decision managed-mode/untrusted/"+id+")") {
		t.Fatalf("task new after reject: exit %d, %s", code, stderr)
	}
	if managedView(t, dir).Authorization(edited) != nil || receiptCount(t, dir) != before {
		t.Fatal("a reject authorized or recorded something")
	}
	if st := copyStatus(t, dir); st.Mode.Untrusted == nil || st.Mode.Untrusted.State != admit.AlertRejected {
		t.Fatalf("status mode = %+v", st.Mode)
	}
	if code, _, stderr := configCmd(t, "reload", "--copy", dir); code != 0 {
		t.Fatalf("reload: exit %d, %s", code, stderr)
	}
	if code, stderr := newTaskIn(t, dir, "t-1"); code != 0 {
		t.Fatalf("task new after reload: exit %d, %s", code, stderr)
	}
}

// TestLeaveVoidsWaitingAlerts: restoring the authorized managed.toml admits the copy again, and leave then voids the
// alert still waiting on the hand-edited digest, so nothing waits for it.
func TestLeaveVoidsWaitingAlerts(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, _, _ := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	writeManaged(t, dir, pinned)
	if code, _, stderr := modeCmd(t, "leave", "--copy", dir); code != 0 {
		t.Fatalf("leave: exit %d, %s", code, stderr)
	}
	if ds := alertDecisionsIn(t, dir); len(ds) != 1 || !ds[0].Void || !strings.Contains(ds[0].VoidReason, "left managed mode") {
		t.Fatalf("alert decisions after leave = %+v", ds)
	}
	for _, it := range copyStatus(t, dir).NeedsMe {
		if it.Repository == admit.AlertRepository {
			t.Fatalf("needs_me still has %+v", it)
		}
	}
}

// TestReservedAlertTaskStaysOutOfTheWay: the reserved task is absent from task list, the status feed's tasks and
// prune, though its decision is in decide list; config validation refuses its repository id for a real repository.
func TestReservedAlertTaskStaysOutOfTheWay(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, _, _ := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	code, out, stderr := runTask(t, "list", "--copy", dir, "--json")
	if code != 0 || strings.Contains(out, admit.AlertRepository) {
		t.Fatalf("task list: exit %d, %s\n%s", code, stderr, out)
	}
	for _, task := range copyStatus(t, dir).Tasks {
		if task.Repository == admit.AlertRepository {
			t.Fatalf("status lists %+v", task)
		}
	}
	app, stdout, stderr2 := newTestApp(nil, nil)
	if code := app.Run([]string{"decide", "list", "--copy", dir}); code != 0 || !strings.Contains(stdout.String(), `"repository":"managed-mode"`) {
		t.Fatalf("decide list: exit %d, %s\n%s", code, stderr2, stdout)
	}
	app, stdout, stderr2 = newTestApp(nil, nil)
	if code := app.Run([]string{"prune", "--copy", dir, "--dry-run"}); code != 0 || strings.Contains(stdout.String(), admit.AlertRepository) {
		t.Fatalf("prune: exit %d, %s\n%s", code, stderr2, stdout)
	}
	cfg := strings.Replace(directorTOML, `id = "app"`, `id = "managed-mode"`, 1)
	if _, err := config.Parse([]byte(cfg)); err == nil || !strings.Contains(err.Error(), `repository id "managed-mode" is reserved`) {
		t.Fatalf("config with the reserved id: %v", err)
	}
}

// TestDirectorStatusModeFieldsGolden: the status feed's mode object for a managed copy with an observed-untrusted
// digest, and for the same copy once a reload applies it.
func TestDirectorStatusModeFieldsGolden(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir, _, _ := handEdited(t)
	if code, _ := newTaskIn(t, dir, "t-1"); code != 1 {
		t.Fatal("the hand edit was not refused")
	}
	untrusted := copyStatus(t, dir).Mode
	if code, _, stderr := configCmd(t, "reload", "--copy", dir); code != 0 {
		t.Fatalf("reload: exit %d, %s", code, stderr)
	}
	applied := copyStatus(t, dir).Mode
	b, err := json.MarshalIndent(map[string]statusMode{"untrusted": untrusted, "applied": applied}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(string(b), abs, "$COPY")
	got = regexp.MustCompile(`"\d{4}-\d\d-\d\dT[0-9:.]+Z"`).ReplaceAllString(got, `"$$TIME"`)
	checkGolden(t, "director-status-mode.json", got+"\n")
}

// splitFleet is a fleet whose machine with alias down cannot be reached, and whose other machines run this
// binary's own commands on the copies under root, as diskFleet does.
type splitFleet struct {
	diskFleet
	down string
}

func (f splitFleet) Run(ctx context.Context, alias string, argv []string) ([]byte, error) {
	if alias == f.down {
		return nil, fmt.Errorf("ssh: exit status 255: ssh: connect to host %s port 22: Connection timed out", alias)
	}
	return f.diskFleet.Run(ctx, alias, argv)
}

// TestStewardStatusReadsEachCopysMode: steward status reads each roster copy's mode and digest with director mode
// --json over SSH; an unreachable machine is that copy's problem, and the command still succeeds.
func TestStewardStatusReadsEachCopysMode(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := stewardCopy(t, "/exit")
	writeFile(t, filepath.Join(dir, "private", "roster.toml"), cliRoster+`
[[machine]]
label = "laptop"
ssh_alias = "guild-lt"
os = "linux"
roles = ["director"]

[[machine.director]]
name = "docs"
copy_dir = "/home/guild/docs"
repositories = ["docs"]
`)
	fleet := splitFleet{diskFleet: diskFleet{root: t.TempDir()}, down: "guild-lt"}
	copyDir := fleet.local("/home/guild/tools")
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(copyDir, "config.toml"), cliCopy)
	d := writeManaged(t, copyDir, pinned)
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"store", "init", filepath.Join(copyDir, directorStoreFile)}); code != 0 {
		t.Fatalf("store init: exit %d, %s", code, stderr)
	}
	enrolManaged(t, copyDir)

	app, stdout, stderr := newTestApp(nil, nil)
	app.Runner = fleet
	if code := app.Run([]string{"steward", "status", "--copy", dir}); code != 0 {
		t.Fatalf("steward status: exit %d, %s", code, stderr)
	}
	var st stewardStatus
	if err := json.Unmarshal(stdout.Bytes(), &st); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if len(st.Roster) != 2 {
		t.Fatalf("roster = %+v", st.Roster)
	}
	tools, docs := st.Roster[0].Directors[0], st.Roster[1].Directors[0]
	if m := tools.Mode; m == nil || m.Mode != modeManaged || m.Steward != "hq" || m.ManagedDigest != d || !m.Authorized || m.Refusal != "" || tools.Problem != "" {
		t.Fatalf("tools = %+v, mode %+v", tools, tools.Mode)
	}
	if docs.Mode != nil || !strings.Contains(docs.Problem, "Connection timed out") {
		t.Fatalf("docs = %+v", docs)
	}
	if len(st.Problems) != 1 || !strings.HasPrefix(st.Problems[0], "laptop/docs: mode: ") {
		t.Fatalf("problems = %q", st.Problems)
	}
}
