package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// queueCmd runs `guildhall <args>` with stdin.
func queueCmd(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(args)
	return code, stdout.String(), stderr.String()
}

const cliLesson = "# Rebase before review\r\n\r\n## Evidence\r\n\r\nd12 failed twice.\r\n\r\n## Limits\r\n\r\nLong-lived tasks only.\r\n"

func cliIncident(id, errText string) string {
	return `{"schema": "guildhall.incident/1", "id": "` + id + `", "repository": "app", "task": "t-1", "step": "build", "attempt": "build-1",
"release": "v1.2.3", "workflow": "sha256:` + strings.Repeat("a", 64) + `", "observed_state": "stuck in starting",
"observed_at": "2026-11-03T09:00:00Z", "error": "` + errText + `",
"evidence": [{"ref": "state/logs/build-1.log", "digest": "sha256:` + strings.Repeat("b", 64) + `"}],
"tried": "artisan stop", "done": "step retry", "workaround": "retry", "risk": "may recur"}`
}

// TestLessonCommands: lesson propose files a lesson from standard input or a file, CRLF read as LF, replays, refuses
// another body under the id and a lesson without its sections; list prints it, --pending and --json too, and resolve
// records the local decision. The suite runs standalone and managed (newCopy).
func TestLessonCommands(t *testing.T) {
	dir := newCopy(t)
	code, out, stderr := queueCmd(t, cliLesson, "lesson", "propose", "-", "--copy", dir)
	id := store.DefaultLessonID(strings.ReplaceAll(cliLesson, "\r\n", "\n"))
	if code != 0 || !strings.HasPrefix(out, "lesson "+id+" proposed: Rebase before review (receipt ") {
		t.Fatalf("propose: exit %d, %q, %s", code, out, stderr)
	}
	if code, again, _ := queueCmd(t, cliLesson, "lesson", "propose", "-", "--copy", dir); code != 0 || again != out {
		t.Fatalf("propose again: exit %d, %q; want %q", code, again, out)
	}
	file := filepath.Join(t.TempDir(), "lesson.md")
	writeFile(t, file, strings.Replace(cliLesson, "Rebase", "Trim", 1))
	if code, _, stderr := queueCmd(t, "", "lesson", "propose", "--id", "trim-notes", file, "--copy", dir); code != 0 {
		t.Fatalf("propose file: exit %d, %s", code, stderr)
	}
	if code, _, stderr := queueCmd(t, "# Other\n## Evidence\n## Limits\n", "lesson", "propose", "--id", "trim-notes", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "mutation id was already used") {
		t.Fatalf("other body under the id: exit %d, %s", code, stderr)
	}
	if code, _, stderr := queueCmd(t, "# Only a title\n", "lesson", "propose", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "## Evidence") {
		t.Fatalf("no sections: exit %d, %s", code, stderr)
	}
	if code, _, stderr := queueCmd(t, cliLesson+strings.Repeat("x", store.MaxQueueItemBytes), "lesson", "propose", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "bytes; the limit is 65536") {
		t.Fatalf("over the cap: exit %d, %s", code, stderr)
	}
	if code, _, _ := queueCmd(t, "", "lesson", "propose", "--copy", dir); code != 2 {
		t.Fatalf("no file: exit %d, want 2", code)
	}

	if code, out, _ := queueCmd(t, "", "lesson", "list", "--copy", dir); code != 0 || out != id+" proposed Rebase before review\ntrim-notes proposed Trim before review\n" {
		t.Fatalf("list: exit %d, %q", code, out)
	}
	if code, out, stderr := queueCmd(t, "", "lesson", "resolve", "trim-notes", "declined", "--copy", dir); code != 0 || out != "lesson trim-notes declined\n" {
		t.Fatalf("resolve: exit %d, %q, %s", code, out, stderr)
	}
	if code, _, _ := queueCmd(t, "", "lesson", "resolve", "trim-notes", "maybe", "--copy", dir); code != 2 {
		t.Fatalf("resolve maybe: exit %d, want 2", code)
	}
	var list lessonListJSON
	code, out, _ = queueCmd(t, "", "lesson", "list", "--json", "--pending", "--copy", dir)
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list.Lessons) != 2 || list.Truncated || len(list.Origin) != 16 || list.Lessons[1].State != store.LessonDeclined {
		t.Fatalf("list --json --pending: exit %d, %v, %s", code, err, out)
	}

	// Once collected, a lesson leaves the pending list and resolve is refused.
	withCopyStore(t, dir, func(s *store.Store) error {
		_, err := s.MarkCollected(store.QueueLesson, id, list.Lessons[0].Digest, strings.Repeat("c", 40))
		return err
	})
	code, out, _ = queueCmd(t, "", "lesson", "list", "--json", "--pending", "--copy", dir)
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list.Lessons) != 1 || list.Lessons[0].ID != "trim-notes" {
		t.Fatalf("pending after collection: exit %d, %v, %s", code, err, out)
	}
	if code, _, stderr := queueCmd(t, "", "lesson", "resolve", id, "adopted", "--copy", dir); code != 1 || !strings.Contains(stderr, "the Steward's queue owns it") {
		t.Fatalf("resolve collected: exit %d, %s", code, stderr)
	}
}

// TestIncidentCommands: incident file files strict JSON, replays, refuses another report under the id and unknown
// keys; observe links evidence; list prints it; resolve records the local decision, refused once collected.
func TestIncidentCommands(t *testing.T) {
	dir := newCopy(t)
	if code, out, stderr := queueCmd(t, cliIncident("stuck-owner", "owner live"), "incident", "file", "-", "--copy", dir); code != 0 || !strings.HasPrefix(out, "incident stuck-owner open (receipt ") {
		t.Fatalf("file: exit %d, %q, %s", code, out, stderr)
	}
	if code, _, stderr := queueCmd(t, cliIncident("stuck-owner", "owner live"), "incident", "file", "-", "--copy", dir); code != 0 {
		t.Fatalf("file again: exit %d, %s", code, stderr)
	}
	if code, _, stderr := queueCmd(t, cliIncident("stuck-owner", "other"), "incident", "file", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "mutation id was already used") {
		t.Fatalf("other report: exit %d, %s", code, stderr)
	}
	unknown := strings.Replace(cliIncident("x", "e"), `"risk"`, `"extra": 1, "risk"`, 1)
	if code, _, stderr := queueCmd(t, unknown, "incident", "file", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "extra") {
		t.Fatalf("unknown key: exit %d, %s", code, stderr)
	}
	big := strings.Replace(cliIncident("big", "e"), `"artisan stop"`, `"`+strings.Repeat("x", store.MaxQueueItemBytes)+`"`, 1)
	if code, _, stderr := queueCmd(t, big, "incident", "file", "-", "--copy", dir); code != 1 || !strings.Contains(stderr, "bytes; the limit is 65536") {
		t.Fatalf("over the cap: exit %d, %s", code, stderr)
	}
	obs := `{"schema": "guildhall.incident-observation/1", "observed_at": "2026-11-03T10:00:00Z", "note": "again"}`
	if code, out, stderr := queueCmd(t, obs, "incident", "observe", "stuck-owner", "-", "--copy", dir); code != 0 || !strings.HasPrefix(out, "incident stuck-owner observation 1 recorded") {
		t.Fatalf("observe: exit %d, %q, %s", code, out, stderr)
	}
	if code, out, _ := queueCmd(t, "", "incident", "list", "--copy", dir); code != 0 || out != "stuck-owner open app/t-1 build build-1: owner live (1 observations)\n" {
		t.Fatalf("list: exit %d, %q", code, out)
	}
	if code, _, stderr := queueCmd(t, cliIncident("second", "e"), "incident", "file", "-", "--copy", dir); code != 0 {
		t.Fatalf("file second: %s", stderr)
	}
	if code, out, _ := queueCmd(t, "", "incident", "resolve", "second", "deferred", "--copy", dir); code != 0 || out != "incident second deferred\n" {
		t.Fatalf("resolve: exit %d, %q", code, out)
	}
	var list incidentListJSON
	code, out, _ := queueCmd(t, "", "incident", "list", "--json", "--pending", "--copy", dir)
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list.Incidents) != 2 || len(list.Incidents[0].Observations) != 1 {
		t.Fatalf("list --json: exit %d, %v, %s", code, err, out)
	}
	withCopyStore(t, dir, func(s *store.Store) error {
		_, err := s.MarkCollected(store.QueueIncident, "stuck-owner", list.Incidents[0].Digest, strings.Repeat("c", 40))
		return err
	})
	if code, _, stderr := queueCmd(t, "", "incident", "resolve", "stuck-owner", "accepted", "--copy", dir); code != 1 || !strings.Contains(stderr, "the Steward's queue owns it") {
		t.Fatalf("resolve collected: exit %d, %s", code, stderr)
	}
	code, out, _ = queueCmd(t, "", "incident", "list", "--json", "--pending", "--copy", dir)
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || len(list.Incidents) != 1 || list.Incidents[0].Report.ID != "second" {
		t.Fatalf("pending after collection: exit %d, %v, %s", code, err, out)
	}
	if code, out, _ := queueCmd(t, "", "store", "doctor", "--copy", dir); code != 0 || !strings.Contains(out, "queue: lessons 0, incidents 2, incident observations 1\n") {
		t.Fatalf("store doctor: exit %d, %s", code, out)
	}
}

// TestQueueOnARefusedManagedCopy: an incident can be filed and listed on a copy whose configuration admission refuses
// it, here a managed.toml the copy is not enrolled for; a Steward copy is refused.
func TestQueueOnARefusedManagedCopy(t *testing.T) {
	t.Setenv(directorModeEnv, "")
	dir := newCopy(t)
	writeManaged(t, dir, map[string]config.ManagedSetting{"max_running_attempts": {Value: "2"}})
	if m := copyMode(t, dir); m.Mode != modeRefused {
		t.Fatalf("mode %+v, want refused", m)
	}
	if code, _, stderr := queueCmd(t, cliIncident("untrusted", "refused"), "incident", "file", "-", "--copy", dir); code != 0 {
		t.Fatalf("file on a refused copy: exit %d, %s", code, stderr)
	}
	if code, out, _ := queueCmd(t, "", "incident", "list", "--copy", dir); code != 0 || !strings.HasPrefix(out, "untrusted open") {
		t.Fatalf("list on a refused copy: exit %d, %q", code, out)
	}
	steward := stewardCopy(t)
	if code, _, stderr := queueCmd(t, cliLesson, "lesson", "propose", "-", "--copy", steward); code != 1 || !strings.Contains(stderr, "is a Steward copy") {
		t.Fatalf("propose on a Steward copy: exit %d, %s", code, stderr)
	}
}

// TestPendingListWithAFullIncident: an incident filled with observations up to store.MaxIncidentBytes, the most the
// store lets one hold, comes first in a pending JSON list with every observation, with room for the items after it,
// so no incident can hold the list back; the observation past the bound is refused with the limit.
func TestPendingListWithAFullIncident(t *testing.T) {
	dir := newCopy(t)
	for _, id := range []string{"full", "after"} {
		if code, _, stderr := queueCmd(t, cliIncident(id, "e"), "incident", "file", "-", "--copy", dir); code != 0 {
			t.Fatalf("file %s: exit %d, %s", id, code, stderr)
		}
	}
	note := strings.Repeat("n", store.MaxQueueItemBytes-200)
	observed := 0
	for i := range 20 {
		obs := fmt.Sprintf(`{"schema": "guildhall.incident-observation/1", "observed_at": "2026-11-03T10:%02d:00Z", "note": %q}`, i, note)
		code, _, stderr := queueCmd(t, obs, "incident", "observe", "full", "-", "--copy", dir)
		if code != 0 {
			if !strings.Contains(stderr, "the limit is 524288") {
				t.Fatalf("observation %d: exit %d, %s", i+1, code, stderr)
			}
			break
		}
		observed++
	}
	if observed < 2 || observed == 20 {
		t.Fatalf("%d observations recorded", observed)
	}
	var list incidentListJSON
	code, out, _ := queueCmd(t, "", "incident", "list", "--json", "--pending", "--copy", dir)
	if err := json.Unmarshal([]byte(out), &list); code != 0 || err != nil || list.Truncated || len(list.Incidents) != 2 ||
		list.Incidents[0].Report.ID != "full" || len(list.Incidents[0].Observations) != observed {
		t.Fatalf("pending list: exit %d, %v, truncated %v, %d incidents", code, err, list.Truncated, len(list.Incidents))
	}
	if len(out) > MaxPendingJSONBytes {
		t.Fatalf("pending list is %d bytes", len(out))
	}
	if store.MaxIncidentBytes+64<<10 > MaxPendingJSONBytes {
		t.Fatalf("an incident (%d bytes) leaves too little of the %d-byte pending list", store.MaxIncidentBytes, MaxPendingJSONBytes)
	}
}

func TestCapJSON(t *testing.T) {
	items := []string{strings.Repeat("a", 10), strings.Repeat("b", 10), strings.Repeat("c", 10)}
	if got, cut := capJSON(items, 30); len(got) != 2 || !cut {
		t.Fatalf("capJSON 30 = %v, %v", got, cut)
	}
	if got, cut := capJSON(items, 1000); len(got) != 3 || cut {
		t.Fatalf("capJSON 1000 = %v, %v", got, cut)
	}
}

// withCopyStore runs fn on the director.store of the copy in dir.
func withCopyStore(t *testing.T, dir string, fn func(*store.Store) error) {
	t.Helper()
	if _, err := withStore(filepath.Join(dir, directorStoreFile), false, func(s *store.Store) (struct{}, error) { return struct{}{}, fn(s) }); err != nil {
		t.Fatal(err)
	}
}
