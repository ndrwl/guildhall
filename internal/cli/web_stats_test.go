package cli

import (
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
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
	// the charts' last day is the strip's today: its merges and its spend, summed as the spend limit sums them
	if len(st.Days) != web.ChartDays || !st.Days[0].Day.Equal(st.Day.AddDate(0, 0, -13)) {
		t.Fatalf("%d chart days from %v", len(st.Days), st.Days)
	}
	if last := st.Days[web.ChartDays-1]; !last.Day.Equal(st.Day) || last.Merges != st.MergedToday || last.Spend != st.Spend {
		t.Errorf("the charts' today %+v, the strip's merges %d and spend %+v", last, st.MergedToday, st.Spend)
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

// TestDayTotals: the charts' 14 UTC days end with today's; a merge record and an attempt's start count on the UTC
// day they were committed, across midnight, 23:59:59 the day before and 00:00 today; an attempt's spend counts on
// the day it started, not the day it finished; a running attempt with no spend is not counted; a day started before
// the 14 is left out; the reserved repositories' merges are left out as the strip leaves them out.
func TestDayTotals(t *testing.T) {
	today := time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)
	mid := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	start := func(task, step string, n int, at time.Time) store.Receipt {
		return store.Receipt{Operation: "start-attempt", Repository: "app", TaskID: task, Record: "attempt/" + step + "/" + strconv.Itoa(n), CommittedAt: at}
	}
	merge := func(repo, task, name string, at time.Time) store.Receipt {
		return store.Receipt{Operation: "put-document", Repository: repo, TaskID: task, Record: "document/" + name, CommittedAt: at}
	}
	spend := func(usd float64) *store.Spend {
		return &store.Spend{Tokens: store.SpendMeasured, InputTokens: 100, Cost: store.SpendMeasured, CostUSD: usd}
	}
	receipts := []store.Receipt{
		merge("app", "a1", "deliver-1-merge", mid.Add(-time.Second)),
		merge("app", "a2", "deliver-2-merge", mid),
		merge("app", "a2", "deliver-3-pr", mid.Add(time.Hour)),
		merge(config.MessagesRepository, "r1", "deliver-1-merge", mid.Add(time.Hour)),
		merge("app", "a3", "deliver-1-merge", mid.AddDate(0, 0, -14)),
		start("a1", "build", 1, mid.Add(-time.Second)),
		start("a1", "build", 2, mid),
		start("a2", "plan", 1, mid.Add(2*time.Hour)),
		start("a3", "build", 1, mid.AddDate(0, 0, -13)),
		start("a3", "build", 2, mid.AddDate(0, 0, -14)),
	}
	attempts := map[string][]store.StageAttempt{
		"a1": {
			{Repository: "app", TaskID: "a1", Step: "build", Number: 1, Outcome: store.AttemptSucceeded, Spend: spend(4)},
			{Repository: "app", TaskID: "a1", Step: "build", Number: 2, Outcome: store.AttemptSucceeded, Spend: spend(2.5)},
		},
		"a2": {{Repository: "app", TaskID: "a2", Step: "plan", Number: 1}},
		"a3": {
			{Repository: "app", TaskID: "a3", Step: "build", Number: 1, Outcome: store.AttemptFailed, Spend: spend(1)},
			{Repository: "app", TaskID: "a3", Step: "build", Number: 2, Outcome: store.AttemptFailed, Spend: spend(100)},
		},
	}
	var listed []string
	lister := func(repo, task string) ([]store.StageAttempt, error) {
		listed = append(listed, repo+"/"+task)
		return attempts[task], nil
	}
	days, err := (*dayCache)(nil).totals(receipts, lister, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != web.ChartDays || !days[0].Day.Equal(mid.AddDate(0, 0, -13)) || !days[13].Day.Equal(mid) {
		t.Fatalf("days %v", days)
	}
	for i, c := range []struct {
		i        int
		merges   int
		attempts int
		cost     float64
		measure  string
	}{
		{13, 1, 1, 2.5, "measured"}, // today: a2's merge, a1's second build; a2's running plan has no spend yet
		{12, 1, 1, 4, "measured"},   // yesterday: a1's merge and first build, at 23:59:59
		{0, 0, 1, 1, "measured"},    // 13 days ago: a3's first build; its second and its merge are 14 days ago
		{5, 0, 0, 0, "unknown"},
	} {
		d := days[c.i]
		if d.Merges != c.merges || d.Spend.Attempts != c.attempts || d.Spend.CostUSD != c.cost || d.Spend.CostMeasure != c.measure {
			t.Errorf("case %d, day %v: %+v", i, d.Day, d)
		}
	}
	if strings.Join(listed, " ") != "app/a1 app/a2 app/a3" {
		t.Errorf("attempts listed for %q", listed)
	}
}

// TestDayCache: a past day is cached once every attempt started on it has finished, so a later read lists no attempt
// for it and keeps its totals; today and a past day with a running attempt are worked out afresh each read; days
// that fall out of the 14 are dropped from the cache.
func TestDayCache(t *testing.T) {
	mid := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	receipts := []store.Receipt{
		{Operation: "start-attempt", Repository: "app", TaskID: "done", Record: "attempt/build/1", CommittedAt: mid.Add(-20 * time.Hour)},
		{Operation: "start-attempt", Repository: "app", TaskID: "long", Record: "attempt/build/1", CommittedAt: mid.Add(-2 * time.Hour)},
		{Operation: "start-attempt", Repository: "app", TaskID: "now", Record: "attempt/build/1", CommittedAt: mid.Add(time.Hour)},
		{Operation: "start-attempt", Repository: "app", TaskID: "old", Record: "attempt/build/1", CommittedAt: mid.AddDate(0, 0, -13)},
	}
	cost := 3.0
	attempt := func(task string, outcome store.AttemptOutcome) store.StageAttempt {
		return store.StageAttempt{Repository: "app", TaskID: task, Step: "build", Number: 1, Outcome: outcome,
			Spend: &store.Spend{Tokens: store.SpendMeasured, Cost: store.SpendMeasured, CostUSD: cost}}
	}
	var listed []string
	outcome := store.AttemptOutcome("")
	lister := func(_, task string) ([]store.StageAttempt, error) {
		listed = append(listed, task)
		switch task {
		case "long":
			return []store.StageAttempt{attempt(task, outcome)}, nil
		default:
			return []store.StageAttempt{attempt(task, store.AttemptSucceeded)}, nil
		}
	}
	c := newDayCache()
	read := func(now time.Time) []web.DayTotal {
		t.Helper()
		listed = nil
		days, err := c.totals(receipts, lister, now)
		if err != nil {
			t.Fatal(err)
		}
		return days
	}
	first := read(mid.Add(9 * time.Hour))
	if strings.Join(listed, " ") != "done long now old" {
		t.Errorf("first read listed %q", listed)
	}
	// "long" still runs: yesterday stays fresh; "done"'s day is the same day, so it is listed again too
	cost = 5
	second := read(mid.Add(10 * time.Hour))
	if strings.Join(listed, " ") != "done long now" {
		t.Errorf("second read listed %q, want old's day cached", listed)
	}
	if second[0].Spend.CostUSD != first[0].Spend.CostUSD || second[13].Spend.CostUSD != 5 || second[12].Spend.CostUSD != 10 {
		t.Errorf("second read: oldest %+v, yesterday %+v, today %+v", second[0], second[12], second[13])
	}
	// "long" finishes: yesterday is cached after this read, and the next lists today's task alone
	outcome = store.AttemptSucceeded
	read(mid.Add(11 * time.Hour))
	cost = 7
	third := read(mid.Add(12 * time.Hour))
	if strings.Join(listed, " ") != "now" || third[12].Spend.CostUSD != 10 || third[13].Spend.CostUSD != 7 {
		t.Errorf("third read listed %q: yesterday %+v, today %+v", listed, third[12], third[13])
	}
	// a day later the oldest day falls out of the 14 and out of the cache
	read(mid.Add(24 * time.Hour))
	if _, ok := c.days[mid.AddDate(0, 0, -13)]; ok || len(c.days) == 0 {
		t.Errorf("cache after a day: %v", c.days)
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
