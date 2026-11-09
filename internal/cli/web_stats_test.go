package cli

import (
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestWebSourceStats: over the Clerk fixture the strip reads the running cap from the config, every merge queue's
// entries with each repository's front in repository order, the start queue, the UTC day of clerkNow and its spend,
// and the machine's cores; a merge record recorded today adds one; a held store is ErrBusy.
func TestWebSourceStats(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	src := webSourceFor(t, dir)
	before, err := src.Stats()
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path, store.Options{Now: func() time.Time { return clerkNow.Add(-time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("m-today", store.DocumentRef{Repository: "app", TaskID: "a-sliced", Name: "deliver-2-merge", Path: "tasks/app/a-sliced/deliver-2/merge.json", SHA256: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := src.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if !st.ReadAt.Equal(clerkNow) || !st.Day.Equal(utcDay(clerkNow)) {
		t.Errorf("read at %v, day %v", st.ReadAt, st.Day)
	}
	if st.RunningLimit < 1 || st.Running < 0 || st.StartQueue < 0 {
		t.Errorf("running %d of %d, start queue %d", st.Running, st.RunningLimit, st.StartQueue)
	}
	if st.MergeQueue != 4 || len(st.Fronts) != 2 || st.Fronts[0] != (web.TaskRef{Repository: "app", ID: "a-q1"}) || st.Fronts[1] != (web.TaskRef{Repository: "lib", ID: "l-front"}) {
		t.Errorf("merge queue %d, fronts %+v", st.MergeQueue, st.Fronts)
	}
	if st.MergedToday != before.MergedToday+1 {
		t.Errorf("merged today = %d, want one more than %d", st.MergedToday, before.MergedToday)
	}
	if st.Spend.CostMeasure == "" || st.Machine.Cores != runtime.NumCPU() {
		t.Errorf("spend %+v, machine %+v", st.Spend, st.Machine)
	}

	held, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	old := webLockWait
	// timing:allow deliberate refusal input: the writer holds the lock for the whole call, so any wait is refused.
	webLockWait = 10 * time.Millisecond
	defer func() { webLockWait = old }()
	if _, err := src.Stats(); !errors.Is(err, web.ErrBusy) {
		t.Errorf("held store: %v, want ErrBusy", err)
	}
}

// TestMergesToday: merges today count one per merge record recorded on the UTC day, across a UTC midnight: a record
// at 23:59 the day before is yesterday's and one at 00:00 today's; a slice's records count each; a document that is
// not a merge record, and a merge record replayed under its mutation id, add nothing.
func TestMergesToday(t *testing.T) {
	day := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	var clock time.Time
	s, err := store.Open(filepath.Join(t.TempDir(), "director.store"), store.Options{Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	clock = day.Add(-48 * time.Hour)
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask("m-task", store.Task{Repository: "app", ID: "a1", Title: "A", Workflow: def.Revision(), ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	put := func(id, name string, at time.Time) {
		t.Helper()
		clock = at
		if _, err := s.PutDocument(id, store.DocumentRef{Repository: "app", TaskID: "a1", Name: name, Path: "tasks/app/a1/" + name + ".json", SHA256: strings.Repeat("0", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	put("m-1", "deliver-1-merge", day.Add(-time.Minute))
	put("m-2", "deliver-2-merge", day)
	put("m-3", "outside-3-merge", day.Add(24*time.Hour-time.Second))
	put("m-4", "deliver-4-pr", day.Add(time.Hour))
	put("m-5", "deliver-5-merge", day.Add(24*time.Hour))
	put("m-2", "deliver-2-merge", day.Add(2*time.Hour))
	receipts, err := s.Receipts()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day  time.Time
		want int
	}{{day.Add(-24 * time.Hour), 1}, {day, 2}, {day.Add(13 * time.Hour), 2}, {day.Add(24 * time.Hour), 1}, {day.Add(-48 * time.Hour), 0}} {
		if got := mergesOn(receipts, c.day); got != c.want {
			t.Errorf("merges on %v = %d, want %d", c.day, got, c.want)
		}
	}
}

// TestWebSpend: every token kind counts, and a day with no measure reads unknown.
func TestWebSpend(t *testing.T) {
	got := webSpend(store.SpendTotal{Attempts: 3, InputTokens: 1, OutputTokens: 20, CacheCreationInputTokens: 300, CacheReadInputTokens: 4000, CostUSD: 2.5, Cost: store.SpendEstimated})
	if got != (web.Spend{Attempts: 3, Tokens: 4321, CostUSD: 2.5, CostMeasure: "estimated"}) {
		t.Errorf("spend = %+v", got)
	}
	if got := webSpend(store.SpendTotal{}); got.CostMeasure != "unknown" {
		t.Errorf("no measure = %+v", got)
	}
}
