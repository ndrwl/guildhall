package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/store"
)

// writeRolloutReceipt writes a rollout receipt under the Steward copy's rollouts/ in which the copy alpha on
// workstation ended status at finished.
func writeRolloutReceipt(t *testing.T, stewardDir, plan, status string, finished time.Time) {
	t.Helper()
	r := steward.Receipt{Schema: steward.ReceiptSchema, PlanDigest: "sha256:" + strings.Repeat(plan, 64)[:64], StartedAt: finished.Add(-time.Minute), FinishedAt: finished,
		Copies: []steward.CopyReceipt{{Machine: "workstation", Director: "alpha", Target: "managed.toml", Status: status, Error: "config reload failed: unknown command"}}}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stewardDir, stewardRollouts, "plan-"+plan)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "receipt-"+finished.UTC().Format("20060102T150405Z")+".json"), string(data))
}

// stewardOutbox is the Steward store's outbox, every entry.
func stewardOutbox(t *testing.T, stewardDir string) []store.OutboxEntry {
	t.Helper()
	es, err := withStore(filepath.Join(stewardDir, stewardStoreFile), true, func(s *store.Store) ([]store.OutboxEntry, error) { return s.Outbox(true) })
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// directorExternal is the Director copy's unread external messages.
func directorExternal(t *testing.T, dir string) []store.ExternalMessage {
	t.Helper()
	ms, err := withStore(filepath.Join(dir, directorStoreFile), true, func(s *store.Store) ([]store.ExternalMessage, error) {
		ms, _, err := s.ExternalInbox("", 0, store.MaxInboxLimit)
		return ms, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// receiveRuns counts the messages receive commands the Fleet ran.
func receiveRuns(cs *collectSetup) int {
	n := 0
	for _, argv := range cs.fleet.Ran("guild-ws") {
		if argv[1] == "messages" && argv[2] == "receive" {
			n++
		}
	}
	return n
}

// TestScheduledStewardMessages: the Steward's scheduled run reports a failed rollout to the copy it failed on by
// scp'ing the envelope to the drop file and running messages receive, once; a refused message is never retried, a
// failed delivery stays pending and is retried by the next run, and a copy unreachable for report_unreachable_after
// gets an unreachable-recovered report once it is reached again. messages outbox shows what is not delivered.
func TestScheduledStewardMessages(t *testing.T) {
	cs := newCollectSetup(t)
	app, _, terminal := cs.app()
	stderr := newTestSupervisorLog(t)
	defer func() {
		if terminal.Len() != 0 {
			t.Errorf("the scheduled run wrote %q to the terminal", terminal)
		}
	}()
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	app.Now = func() time.Time { return now }
	cfg, err := config.LoadUnadmitted(filepath.Join(cs.steward, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Steward.CollectEvery = "1h"
	// The copy's messages arrive by the wall clock, not this run's; the Director health checks have their own test.
	cfg.Steward.ReportDirectorAfter = "100000h"
	w, err := app.stewardCollection(cfg, filepath.Join(cs.steward, stewardStoreFile), stderr.writer())
	if err != nil || w == nil {
		t.Fatalf("schedule: %v %v", w, err)
	}
	w.round()
	if es := stewardOutbox(t, cs.steward); len(es) != 0 || receiveRuns(cs) != 0 {
		t.Fatalf("a run with nothing to report sent %+v", es)
	}

	writeRolloutReceipt(t, cs.steward, "a", steward.StatusReloadFailed, now.Add(-time.Hour))
	cs.fleet.Calls()
	w.round()
	var sent []string
	for _, c := range cs.fleet.Calls() {
		if strings.Contains(c, "message") {
			sent = append(sent, c)
		}
	}
	copyDir := filepath.ToSlash(cs.director)
	want := []string{"put guild-ws:" + copyDir + "/" + steward.MessageDropPath, "run guild-ws guildhall messages receive --copy " + copyDir}
	if !slices.Equal(sent, want) {
		t.Fatalf("delivery calls %q, want %q; stderr %s", sent, want, stderr)
	}
	es := stewardOutbox(t, cs.steward)
	if len(es) != 1 || es[0].State != store.OutboxDelivered || es[0].To != "workstation/alpha" || !strings.HasPrefix(es[0].ID, "rollout-failed.") || len(es[0].Receipt) == 0 {
		t.Fatalf("outbox %+v", es)
	}
	ms := directorExternal(t, cs.director)
	if len(ms) != 1 || ms[0].Sender != store.SenderSteward || ms[0].Kind != store.ReportRolloutFailed || ms[0].ID != es[0].ID ||
		!strings.Contains(ms[0].Subject, "ended reload_failed") || !strings.Contains(ms[0].Text, "informs only") {
		t.Fatalf("the Director's external inbox %+v", ms)
	}
	w.round()
	if n := receiveRuns(cs); n != 1 {
		t.Errorf("a delivered report was sent again: %d receives", n)
	}

	// Refused: the Director closed its inbox to the Steward; the refusal is recorded and never retried.
	if code, _, stderr := runMessages(t, "", "inbox", "close", "--copy", cs.director, "--sender", "steward", "--reason", "busy"); code != 0 {
		t.Fatal(stderr)
	}
	writeRolloutReceipt(t, cs.steward, "b", steward.StatusFailed, now.Add(-30*time.Minute))
	w.round()
	w.round()
	refused := 0
	for _, e := range stewardOutbox(t, cs.steward) {
		if e.State == store.OutboxRefused {
			refused++
			if !strings.HasPrefix(e.Reason, "closed: ") || e.Attempts != 1 {
				t.Errorf("refused entry %+v", e)
			}
		}
	}
	if refused != 1 || receiveRuns(cs) != 2 {
		t.Fatalf("refused %d, receives %d; want one refusal, never retried", refused, receiveRuns(cs))
	}
	if code, out, _ := runMessages(t, "", "outbox", "--copy", cs.steward); code != 0 || !strings.Contains(out, `"state":"refused"`) || strings.Contains(out, `"state":"delivered"`) {
		t.Errorf("messages outbox: exit %d, %s", code, out)
	}

	// Pending: a delivery that fails is kept and the next run delivers it.
	if code, _, stderr := queueCmd(t, "reopen\n", "decide", "answer", "--copy", cs.director, "--repo", "messages", "--task", "inbox-steward", "--decision", "inbox-steward-1", "--answer-file", "-"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := runMessages(t, "", "inbox", "open", "--copy", cs.director, "--sender", "steward"); code != 0 {
		t.Fatal(stderr)
	}
	cs.receive = func([]string) ([]byte, bool, error) { return nil, true, fmt.Errorf("exit 1: store locked") }
	writeRolloutReceipt(t, cs.steward, "c", steward.StatusVerifyFailed, now.Add(-10*time.Minute))
	w.round()
	pending := func() []store.OutboxEntry {
		var out []store.OutboxEntry
		for _, e := range stewardOutbox(t, cs.steward) {
			if e.State == store.OutboxPending {
				out = append(out, e)
			}
		}
		return out
	}
	if p := pending(); len(p) != 1 || p[0].Attempts != 1 || !strings.Contains(p[0].LastError, "store locked") {
		t.Fatalf("pending %+v", p)
	}
	cs.receive = nil
	w.round()
	if p := pending(); len(p) != 0 {
		t.Fatalf("a pending entry was not delivered: %+v", p)
	}
	if ms := directorExternal(t, cs.director); len(ms) != 2 {
		t.Fatalf("the Director's external inbox %+v", ms)
	}

	// Unreachable for longer than report_unreachable_after, then reached again.
	cs.collectFails = "connection refused"
	w.round()
	now = now.Add(2 * time.Hour)
	w.round()
	cs.collectFails = ""
	now = now.Add(time.Minute)
	w.round()
	ms = directorExternal(t, cs.director)
	last := ms[len(ms)-1]
	if last.Kind != store.ReportUnreachableRecovered || !strings.Contains(last.Subject, "unreachable from the Steward for 2h1m") {
		t.Fatalf("newest message %+v; want unreachable-recovered", last)
	}
	w.round()
	if n := len(directorExternal(t, cs.director)); n != len(ms) {
		t.Errorf("the recovery was reported again: %d messages", n)
	}
	// A span shorter than report_unreachable_after is not reported.
	cs.collectFails = "connection refused"
	w.round()
	cs.collectFails = ""
	now = now.Add(5 * time.Minute)
	w.round()
	if n := len(directorExternal(t, cs.director)); n != len(ms) {
		t.Errorf("a short outage was reported: %d messages", n)
	}
	// A run whose round finds the lock busy still sends its reports.
	holdPrivateLock(t, cs.private, steward.LockOwner{})
	writeRolloutReceipt(t, cs.steward, "d", steward.StatusFailed, now)
	w.round()
	if n := len(directorExternal(t, cs.director)); n != len(ms)+1 {
		t.Errorf("a busy run sent no report: %d messages, want %d; stderr %s", n, len(ms)+1, stderr)
	}
}
