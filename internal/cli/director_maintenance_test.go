package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// startMaintenanceSession records session n, running, with the maintenance policy p (zero for a session from before
// the upgrade).
func startMaintenanceSession(t *testing.T, path string, n int, p store.MaintenancePolicy) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		d := store.DirectorSession{Number: n, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: fmt.Sprintf("boot:%d", n)},
			Release: "test", ConfigDigest: "sha256:" + strings.Repeat("c", 64), Maintenance: p}
		r, err := s.StartDirectorSession(d)
		if err != nil {
			return r, err
		}
		return s.SetDirectorState(n, store.DirectorRunning, r.Sequence)
	}); err != nil {
		t.Fatal(err)
	}
}

// sendMergedMessages sends count merged messages for repo/task, their ids prefixed by prefix.
func sendMergedMessages(t *testing.T, path, repo, task, prefix string, count int) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		for i := range count {
			if _, err := s.SendMessage(store.Message{ID: fmt.Sprintf("%s-%d", prefix, i), Recipient: store.RecipientDirector, Repository: repo, TaskID: task,
				Type: store.MessageMerged, Text: "Merged."}); err != nil {
				return store.Receipt{}, err
			}
		}
		return store.Receipt{}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// maintenanceItems lists the maintenance items of status as "<repository>: <reason> | <command>".
func maintenanceItems(st directorStatus) []string {
	var out []string
	for _, it := range st.NeedsMe {
		if it.Kind == maintenanceKind {
			out = append(out, fmt.Sprintf("%s: %s | %s", it.Repository, it.Reason, it.Command))
		}
	}
	return out
}

// TestDirectorStatusMaintenance: nothing before the threshold or before the upgrade; once a session with a policy
// started, a note audit is due 3 sessions after the last and a hallmark per repository after 4 later merges, each
// gone once recorded, with the counts in the maintenance object.
func TestDirectorStatusMaintenance(t *testing.T) {
	d := newRunDirector(t)
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		digest := "sha256:" + strings.Repeat("a", 64)
		return s.CreateTask("task:web:w-1", store.Task{Repository: "web", ID: "w-1", Title: "Web", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest})
	}); err != nil {
		t.Fatal(err)
	}
	policy := store.MaintenancePolicy{NoteAuditEverySessions: 3, HallmarkAfterMerges: 4}
	status := func() directorStatus { t.Helper(); return readStatus(t, d.store, d.state) }

	// Before the upgrade: old sessions and many merges count for nothing.
	for n := 1; n <= 4; n++ {
		startMaintenanceSession(t, d.store, n, store.MaintenancePolicy{})
	}
	sendMergedMessages(t, d.store, "app", "t-1", "old", 9)
	if st := status(); st.Maintenance != nil || len(maintenanceItems(st)) != 0 {
		t.Fatalf("before the upgrade: maintenance %+v, items %q; want none", st.Maintenance, maintenanceItems(st))
	}
	startMaintenanceSession(t, d.store, 5, policy)
	st := status()
	if len(maintenanceItems(st)) != 0 {
		t.Fatalf("right after the upgrade: items %q; want none", maintenanceItems(st))
	}
	if m := st.Maintenance; m == nil || m.BaselineSession != 5 || m.NoteAudit.SessionsSince != 0 || *m.NoteAudit.SessionsUntilDue != 3 ||
		len(m.Hallmarks) != 2 || m.Hallmarks[0].Repository != "app" || m.Hallmarks[0].MergedSince != 0 || *m.Hallmarks[0].MergesUntilDue != 4 {
		t.Fatalf("maintenance after the upgrade = %+v", m)
	}

	// Merges after the upgrade count per repository.
	sendMergedMessages(t, d.store, "app", "t-1", "new", 3)
	sendMergedMessages(t, d.store, "web", "w-1", "web", 2)
	startMaintenanceSession(t, d.store, 6, policy)
	startMaintenanceSession(t, d.store, 7, policy)
	if items := maintenanceItems(status()); len(items) != 0 {
		t.Fatalf("below both thresholds: items %q; want none", items)
	}
	sendMergedMessages(t, d.store, "app", "t-1", "more", 2)
	startMaintenanceSession(t, d.store, 8, policy)
	st = status()
	want := []string{
		": note audit due: 3 sessions since the upgrade (session 5) | guildhall director maintenance record --notes --summary <text>",
		"app: hallmark due: 5 merged since the upgrade | guildhall task new --repo app --kind hallmark --title <text> <id>",
	}
	if got := maintenanceItems(st); !slices.Equal(got, want) {
		t.Fatalf("items = %q\nwant %q", got, want)
	}
	if last := st.NeedsMe[len(st.NeedsMe)-1]; last.Kind != maintenanceKind || last.Count != 5 {
		t.Fatalf("last item = %+v; want the app hallmark with count 5", last)
	}
	if m := st.Maintenance; m.NoteAudit.SessionsSince != 3 || *m.NoteAudit.SessionsUntilDue != 0 || m.Hallmarks[1].Repository != "web" ||
		m.Hallmarks[1].MergedSince != 2 || *m.Hallmarks[1].MergesUntilDue != 2 {
		t.Fatalf("maintenance = %+v", m)
	}

	// Recording the note audit clears its item, and launching a hallmark task clears the hallmark's while it runs; once
	// it finishes it is the last hallmark. The other repository keeps counting.
	run := func(args ...string) (int, string, string) {
		return runCLI(t, append(append([]string{"director", "maintenance"}, args...), "--store", d.store)...)
	}
	if code, out, stderr := run("record", "--notes", "--summary", "removed 4 (held by d47, D94, config); proposed 2: d58, skill change"); code != 0 || out != "run 1 recorded\n" {
		t.Fatalf("record notes: exit %d, %q, stderr %s", code, out, stderr)
	}
	createHallmarkTask(t, d.store, "app", "h-1")
	st = status()
	if items := maintenanceItems(st); len(items) != 0 {
		t.Fatalf("after recording and launching: items %q; want none", items)
	}
	if m := st.Maintenance; m.NoteAudit.LastSession != 8 || m.NoteAudit.LastAt.IsZero() || *m.NoteAudit.SessionsUntilDue != 3 ||
		m.Hallmarks[0].RunningTask != "h-1" || m.Hallmarks[0].LastTask != "" || m.Hallmarks[0].MergedSince != 5 || m.Hallmarks[1].RunningTask != "" {
		t.Fatalf("maintenance while the hallmark runs = %+v", m)
	}
	finishHallmarkTask(t, d.store, "app", "h-1")
	st = status()
	if m := st.Maintenance; len(maintenanceItems(st)) != 0 || m.Hallmarks[0].RunningTask != "" ||
		m.Hallmarks[0].LastTask != "h-1" || m.Hallmarks[0].MergedSince != 0 || m.Hallmarks[1].LastTask != "" || m.Hallmarks[1].MergedSince != 2 {
		t.Fatalf("maintenance after the hallmark finished = %+v, items %q", m, maintenanceItems(st))
	}
	sendMergedMessages(t, d.store, "web", "w-1", "web-more", 2)
	sendMergedMessages(t, d.store, "app", "t-1", "after", 3)
	for n := 9; n <= 11; n++ {
		startMaintenanceSession(t, d.store, n, policy)
	}
	want = []string{
		": note audit due: 3 sessions since the last (session 8) | guildhall director maintenance record --notes --summary <text>",
		"web: hallmark due: 4 merged since the upgrade | guildhall task new --repo web --kind hallmark --title <text> <id>",
	}
	if got := maintenanceItems(status()); !slices.Equal(got, want) {
		t.Fatalf("items = %q\nwant %q", got, want)
	}

	// A session with both parts off shows the counts and no item.
	startMaintenanceSession(t, d.store, 12, store.MaintenancePolicy{})
	st = status()
	if m := st.Maintenance; len(maintenanceItems(st)) != 0 || m == nil || m.NoteAudit.SessionsUntilDue != nil || m.Hallmarks[0].MergesUntilDue != nil {
		t.Fatalf("both off: maintenance %+v, items %q", m, maintenanceItems(st))
	}
}

// TestDirectorMaintenanceCommands: record prints the note audit's run, list prints the history with old hallmark
// runs, and recording a hallmark and bad flags are usage errors.
func TestDirectorMaintenanceCommands(t *testing.T) {
	d := newRunDirector(t)
	run := func(args ...string) (int, string, string) {
		return runCLI(t, append(append([]string{"director", "maintenance"}, args...), "--store", d.store)...)
	}
	if code, _, stderr := run("record", "--notes", "--summary", "x"); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Fatalf("record with no session: exit %d, stderr %s", code, stderr)
	}
	if code, out, stderr := run("list"); code != 0 || out != "No maintenance runs.\n" {
		t.Fatalf("list of none: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, _ := run("list", "--json"); code != 0 || out != "[]\n" {
		t.Fatalf("list --json of none: exit %d, %q", code, out)
	}
	startMaintenanceSession(t, d.store, 1, store.MaintenancePolicy{NoteAuditEverySessions: 3})
	if code, _, stderr := run("record", "--notes", "--summary", "removed 2; proposed none"); code != 0 {
		t.Fatalf("record notes: exit %d, stderr %s", code, stderr)
	}
	code, out, stderr := run("list")
	if code != 0 || !strings.HasPrefix(out, "1 s1 ") || !strings.HasSuffix(out, " notes: removed 2; proposed none\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("list: exit %d, stderr %s\n%s", code, stderr, out)
	}
	if code, out, _ := run("list", "--json"); code != 0 || !strings.Contains(out, `"kind":"notes","session":1,"summary":"removed 2; proposed none"`) {
		t.Fatalf("list --json: exit %d, %s", code, out)
	}
	// A hallmark run recorded before hallmarks were recorded when their task finishes still lists.
	old := store.MaintenanceRun{Sequence: 2, Kind: store.MaintenanceHallmark, Repository: "app", Task: "t-1", Session: 1, Summary: "launched",
		Time: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	if got, want := maintenanceLine(old), "2 s1 2026-10-01T09:00:00Z hallmark app/t-1: launched"; got != want {
		t.Fatalf("old hallmark line = %q, want %q", got, want)
	}
	// Recording a hallmark is retired, with the reason, and records nothing.
	if code, out, stderr := run("record", "--hallmark", "--repo", "app", "--task", "t-1", "--summary", "launched"); code != 2 || out != "" ||
		!strings.Contains(stderr, "director maintenance record: hallmarks are recorded when the task finishes") {
		t.Fatalf("record --hallmark: exit %d, %q, stderr %s; want the usage error", code, out, stderr)
	}
	if _, out, _ := run("list"); strings.Count(out, "\n") != 1 {
		t.Fatalf("list after the refused hallmark:\n%s", out)
	}
	for name, args := range map[string][]string{
		"multi-line":    {"record", "--notes", "--summary", "a\nb"},
		"over 1024":     {"record", "--notes", "--summary", strings.Repeat("x", store.MaxMaintenanceSummaryBytes+1)},
		"blank summary": {"record", "--notes", "--summary", " "},
	} {
		if code, out, _ := run(args...); code != 1 || out != "" {
			t.Errorf("%s: exit %d, %q; want 1 and no output", name, code, out)
		}
	}
	for name, args := range map[string][]string{
		"no subcommand":      nil,
		"unknown":            {"show"},
		"no part":            {"record", "--summary", "x"},
		"with a hallmark":    {"record", "--notes", "--hallmark", "--repo", "app", "--task", "t-1", "--summary", "x"},
		"notes with a repo":  {"record", "--notes", "--repo", "app", "--summary", "x"},
		"hallmark alone":     {"record", "--hallmark", "--summary", "x"},
		"no summary":         {"record", "--notes"},
		"record an argument": {"record", "--notes", "--summary", "x", "extra"},
		"list an argument":   {"list", "extra"},
	} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "director maintenance") {
			t.Errorf("%s: exit %d, stderr %s; want a usage error", name, code, stderr)
		}
	}
}

// TestResumeDoesNotWakeForMaintenance: a maintenance item waits for the Director's next turn.
func TestResumeDoesNotWakeForMaintenance(t *testing.T) {
	items := []needsMeItem{{Kind: maintenanceKind, Reason: "note audit due"}, {Kind: maintenanceKind, Repository: "app", Count: 4}}
	if n := waitingKinds(items); len(n) != 0 {
		t.Fatalf("waitingKinds = %v; want nothing to wake for", n)
	}
	if n := waitingKinds(append(items, needsMeItem{Kind: "retry"})); len(n) != 1 || n["retry"] != 1 {
		t.Fatalf("waitingKinds = %v; want only the retry", n)
	}
}

// TestDirectorStartRecordsMaintenanceAndListsIt: director start records the config's maintenance policy on the
// session, and a hallmark due after merges since the first such session is in the next session's startup.md.
func TestDirectorStartRecordsMaintenanceAndListsIt(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first session: exit %d\n%s\n%s", code, out, stderr)
	}
	storePath := filepath.Join(dir, "director.store")
	newTask(t, storePath, filepath.Join(dir, "config.toml"), "app", "Merged", "t-1")
	sendMergedMessages(t, storePath, "app", "t-1", "m", 4)
	if code, out, stderr := runDirectorCmd(t, "start", "fake", "--copy", dir); code != 0 {
		t.Fatalf("second session: exit %d\n%s\n%s", code, out, stderr)
	}
	d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.DirectorSession(2) })
	if err != nil {
		t.Fatal(err)
	}
	if d.Maintenance != (store.MaintenancePolicy{NoteAuditEverySessions: 3, HallmarkAfterMerges: 4}) {
		t.Fatalf("session 2 maintenance = %+v; want the defaults", d.Maintenance)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-2", "startup.md"))
	if err != nil {
		t.Fatal(err)
	}
	line := "- maintenance: hallmark due: 4 merged since the upgrade; run `guildhall task new --repo app --kind hallmark --title <text> <id>`\n"
	if !strings.Contains(string(b), line) {
		t.Errorf("startup.md lacks %q:\n%s", line, b)
	}
	if strings.Contains(string(b), "note audit due") {
		t.Errorf("startup.md has a note audit one session after the upgrade:\n%s", b)
	}
}
