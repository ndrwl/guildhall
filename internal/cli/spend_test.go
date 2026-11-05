package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestSpendReportRecordedUsage(t *testing.T) {
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range []string{"one", "two", "three"} {
		if _, err := s.StartAttempt(step, "app", "t1", step); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := s.RecordAttemptSpend("spend", "app", "t1", step, 1, store.Spend{Tokens: store.SpendMeasured, InputTokens: 10, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 4, Cost: store.SpendEstimated, CostUSD: 0.1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"one", "two"} {
		dir := (artisan.AttemptKey{Repository: "app", TaskID: "t1", Step: step, Attempt: 1}).Dir(d.state)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := adapter.WriteUsage(filepath.Join(dir, artisan.ResultFile), adapter.Usage{Tokens: adapter.Estimated, InputTokens: 20, OutputTokens: 5, CacheCreationInputTokens: 6, CacheReadInputTokens: 7, Cost: adapter.Measured, CostUSD: 0.2}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(d.store)
	if err != nil {
		t.Fatal(err)
	}
	app, out, stderr := newTestApp(nil, nil)
	args := []string{"spend", "report", "--store", d.store, "--state", d.state, "--json"}
	if code := app.Run(args); code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
	}
	var got spendReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Total.Attempts != 3 || !got.Total.Partial || got.Total.Measured.InputTokens != 10 || got.Total.Estimated.InputTokens != 20 || got.Total.TokensUnknown != 1 || got.Total.Measured.CostUSD != 0.2 || got.Total.Estimated.CostUSD != 0.1 {
		t.Fatalf("total=%+v", got.Total)
	}
	if len(got.Rows) != 3 || got.Rows[0].Step != "one" || got.Rows[0].Model != "unavailable" || got.Rows[0].Outcome != "running" {
		t.Fatalf("rows=%+v", got.Rows)
	}
	after, _ := os.ReadFile(d.store)
	if string(before) != string(after) {
		t.Fatal("report mutated store")
	}
	out.Reset()
	if code := app.Run(append(args[:len(args)-1], "--since", "2999-01-01T00:00:00Z", "--json")); code != 0 {
		t.Fatalf("filter code=%d: %s", code, stderr)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Total.Attempts != 0 {
		t.Fatalf("empty=%+v err=%v", got, err)
	}
	out.Reset()
	if code := app.Run(args[:len(args)-1]); code != 0 || !strings.Contains(out.String(), "partial") || !strings.Contains(out.String(), "CACHE-READ") {
		t.Fatalf("table code=%d: %s", code, out)
	}
}

func TestSpendReportMetadataFiltersAndMalformedEvidence(t *testing.T) {
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.StartAttempt("start", "app", "t1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishAttempt("finish", "app", "t1", "build", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := (artisan.AttemptKey{Repository: "app", TaskID: "t1", Step: "build", Attempt: 1}).Dir(d.state)
	if err = os.MkdirAll(filepath.Join(dir, "prompt"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := render.Manifest{Schema: render.ManifestSchema, Identity: render.Identity{Repository: "app", Task: "t1", Step: "build", Attempt: 1, Profile: "builder"}, Role: render.File{Path: "roles/builder.md"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "prompt", render.ManifestFile)
	if err = os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := readSpend(s, d.state, "app", "t1", started.CommittedAt)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Profile != "builder" || got.Rows[0].Role != "roles/builder.md" || got.Total.TokensUnknown != 1 {
		t.Fatalf("report=%+v err=%v", got, err)
	}
	unfiltered, err := readSpend(s, d.state, "", "", time.Time{})
	if err != nil || len(unfiltered.Rows) != 1 || unfiltered.Rows[0].Profile != "builder" || unfiltered.Rows[0].Role != "roles/builder.md" {
		t.Fatalf("unfiltered=%+v err=%v", unfiltered, err)
	}
	for _, filter := range []struct {
		repo, task string
		since      time.Time
	}{{"other", "", time.Time{}}, {"", "other", time.Time{}}, {"", "", started.CommittedAt.Add(time.Nanosecond)}} {
		got, err = readSpend(s, d.state, filter.repo, filter.task, filter.since)
		if err != nil || got.Total.Attempts != 0 {
			t.Fatalf("filter=%+v report=%+v err=%v", filter, got, err)
		}
	}
	if err = os.WriteFile(adapter.UsageFile(filepath.Join(dir, artisan.ResultFile)), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readSpend(s, d.state, "", "", time.Time{}); err == nil {
		t.Fatal("malformed usage accepted")
	}
	if err = os.Remove(adapter.UsageFile(filepath.Join(dir, artisan.ResultFile))); err != nil {
		t.Fatal(err)
	}
	m.Identity.Task = "another-task"
	data, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readSpend(s, d.state, "", "", time.Time{}); err == nil {
		t.Fatal("mismatched manifest accepted")
	}
}

func TestSpendReportRetriesAggregateWithoutTranscriptReads(t *testing.T) {
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		r, err := s.StartAttempt(id, "app", "t1", "build")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RecordAttemptSpend(id+"-spend", "app", "t1", "build", r.Attempt, store.Spend{Tokens: store.SpendMeasured, InputTokens: 9, Cost: store.SpendMeasured, CostUSD: 0.25}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FinishAttempt(id+"-finish", "app", "t1", "build", r.Attempt, store.AttemptFailed); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// A non-usage artifact must not influence a report, even when it cannot be decoded.
	if err = os.WriteFile(filepath.Join(d.state, "transcript.jsonl"), []byte("invalid transcript"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := readSpend(s, d.state, "", "", time.Time{})
	if err != nil || len(got.Rows) != 2 || got.Total.Attempts != 2 || got.Total.Partial || got.Total.Measured.InputTokens != 18 || got.Total.Measured.CostUSD != 0.5 {
		t.Fatalf("report=%+v err=%v", got, err)
	}
	for n, want := range []string{"after-failure", "first"} {
		if r := got.Rows[n]; r.Rerun != want || r.Attempts != 1 || r.Measured.InputTokens != 9 || r.Measured.CostUSD != 0.25 {
			t.Fatalf("row %d=%+v, want rerun %s", n, r, want)
		}
	}
}

// spendLabelOf records attempt 1 of t1/build with finish, then starts attempt 2,
// and returns attempt 2's label from an unfiltered report.
func spendLabelOf(t *testing.T, finish func(*store.Store) error) string {
	t.Helper()
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.StartAttempt("start-1", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if err = finish(s); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAttempt("start-2", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	got, err := readSpend(s, d.state, "", "", time.Time{})
	if err != nil || len(got.Rows) != 2 || got.Rows[0].Rerun == got.Rows[1].Rerun {
		t.Fatalf("report=%+v err=%v", got, err)
	}
	labels := map[string]bool{got.Rows[0].Rerun: true, got.Rows[1].Rerun: true}
	if !labels["first"] {
		t.Fatalf("no first row: %+v", got.Rows)
	}
	delete(labels, "first")
	for label := range labels {
		return label
	}
	return ""
}

func TestSpendReportLabelsRerunsByPredecessor(t *testing.T) {
	finish := func(o store.AttemptOutcome) func(*store.Store) error {
		return func(s *store.Store) error {
			_, err := s.FinishAttempt("finish-1", "app", "t1", "build", 1, o)
			return err
		}
	}
	reject := func(s *store.Store) error {
		_, err := s.RejectAttempt("reject-1", "app", "t1", "build", 1, "missing output")
		return err
	}
	for _, c := range []struct {
		name   string
		finish func(*store.Store) error
		want   string
	}{
		{"refused", reject, "after-refusal"},
		{"refused after launcher success", func(s *store.Store) error {
			if err := finish(store.AttemptSucceeded)(s); err != nil {
				return err
			}
			return reject(s)
		}, "after-refusal"},
		{"failed", finish(store.AttemptFailed), "after-failure"},
		{"stopped", finish(store.AttemptStopped), "after-stop"},
		{"succeeded", finish(store.AttemptSucceeded), "send-back"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := spendLabelOf(t, c.finish); got != c.want {
				t.Fatalf("label %q, want %q", got, c.want)
			}
		})
	}
}

type fixedSpendStore struct {
	attempts []store.StageAttempt
	receipts []store.Receipt
}

func (s fixedSpendStore) Repositories() ([]string, error) { return []string{"app"}, nil }
func (s fixedSpendStore) Tasks(repo string) ([]store.Task, error) {
	return []store.Task{{Repository: repo, ID: "t1"}}, nil
}
func (s fixedSpendStore) Attempts(string, string) ([]store.StageAttempt, error) {
	return s.attempts, nil
}
func (s fixedSpendStore) Receipts() ([]store.Receipt, error) { return s.receipts, nil }

func TestSpendReportLabelsUnknownPredecessors(t *testing.T) {
	at := func(step string, n int, o store.AttemptOutcome) store.StageAttempt {
		return store.StageAttempt{Repository: "app", TaskID: "t1", Step: step, Number: n, Outcome: o}
	}
	s := fixedSpendStore{attempts: []store.StageAttempt{
		at("build", 1, ""),            // running predecessor
		at("build", 2, ""),            // unknown
		at("review", 3, ""),           // missing predecessor: unknown
		at("build--s2", 1, "failed"),  // a sliced step is its own step: first
		at("build--s2", 2, "stopped"), // after-failure
	}}
	got, err := readSpend(s, t.TempDir(), "", "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, r := range got.Rows {
		labels[r.Step] += r.Rerun + ","
	}
	if labels["build"] != "first,unknown," || labels["review"] != "unknown," || labels["build--s2"] != "after-failure,first," {
		t.Fatalf("labels=%v", labels)
	}
	if got.ByRerun["unknown"].Attempts != 2 || got.ByRerun["first"].Attempts != 2 || got.ByRerun["after-failure"].Attempts != 1 || len(got.ByRerun) != 3 {
		t.Fatalf("by_rerun=%+v", got.ByRerun)
	}
}

func TestSpendReportSinceKeepsLabelAndSubtotals(t *testing.T) {
	d := newRunDirector(t)
	// Each receipt is a second after the last, so attempt 2 starts strictly after attempt 1.
	clock := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s, err := store.Open(d.store, store.Options{RequireStore: true, Now: func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAttempt("start-1", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordAttemptSpend("spend-1", "app", "t1", "build", 1, store.Spend{Tokens: store.SpendMeasured, InputTokens: 5, Cost: store.SpendMeasured, CostUSD: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishAttempt("finish-1", "app", "t1", "build", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	second, err := s.StartAttempt("start-2", "app", "t1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordAttemptSpend("spend-2", "app", "t1", "build", 2, store.Spend{Tokens: store.SpendMeasured, InputTokens: 7, Cost: store.SpendMeasured, CostUSD: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishAttempt("finish-2", "app", "t1", "build", 2, store.AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartAttempt("start-3", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readSpend(s, d.state, "", "", second.CommittedAt)
	_ = s.Close()
	if err != nil || got.Total.Attempts != 2 || len(got.Rows) != 2 || got.Rows[0].Rerun != "after-failure" || got.Rows[1].Rerun != "send-back" || got.Rows[1].Measured.InputTokens != 7 {
		t.Fatalf("since report=%+v err=%v", got, err)
	}
	if _, ok := got.ByRerun["first"]; ok {
		t.Fatalf("attempt 1 counted: %+v", got.ByRerun)
	}
	var sum spendSummary
	for _, v := range got.ByRerun {
		sum.Attempts += v.Attempts
		sum.Measured.InputTokens += v.Measured.InputTokens
		sum.Measured.CostUSD += v.Measured.CostUSD
		sum.TokensUnknown += v.TokensUnknown
		sum.CostUnknown += v.CostUnknown
	}
	if sum.Attempts != got.Total.Attempts || sum.Measured != got.Total.Measured || sum.TokensUnknown != got.Total.TokensUnknown || sum.CostUnknown != got.Total.CostUnknown {
		t.Fatalf("by_rerun %+v does not sum to total %+v", got.ByRerun, got.Total)
	}
	app, out, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"spend", "report", "--store", d.store, "--state", d.state}); code != 0 {
		t.Fatalf("code=%d err=%s", code, stderr)
	}
	table := out.String()
	if !strings.Contains(table, "RERUN") {
		t.Fatalf("no RERUN column: %s", table)
	}
	for _, label := range []string{"first", "send-back", "after-failure"} {
		found := false
		for _, line := range strings.Split(table, "\n") {
			f := strings.Fields(line)
			if len(f) > 2 && f[0] == "TOTAL" && f[1] == label {
				found = true
			}
		}
		if !found {
			t.Fatalf("no TOTAL %s line: %s", label, table)
		}
	}
}

func TestSpendReportSplitsByOutcome(t *testing.T) {
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = s.Close()
		}
	})
	// Each attempt finishes before the next starts; the launcher-succeeded one is refused at intake.
	for i, end := range []string{"succeeded", "failed", "stopped", "refused-running", "refused-succeeded", "running"} {
		id := fmt.Sprintf("a%d", i)
		r, err := s.StartAttempt(id, "app", "t1", "build")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RecordAttemptSpend(id+"-spend", "app", "t1", "build", r.Attempt, store.Spend{Tokens: store.SpendMeasured, InputTokens: int64(1) << i, Cost: store.SpendMeasured, CostUSD: 1}); err != nil {
			t.Fatal(err)
		}
		switch end {
		case "succeeded", "failed", "stopped":
			_, err = s.FinishAttempt(id+"-finish", "app", "t1", "build", r.Attempt, store.AttemptOutcome(end))
		case "refused-succeeded":
			if _, err = s.FinishAttempt(id+"-finish", "app", "t1", "build", r.Attempt, store.AttemptSucceeded); err != nil {
				t.Fatal(err)
			}
			fallthrough
		case "refused-running":
			_, err = s.RejectAttempt(id+"-reject", "app", "t1", "build", r.Attempt, "no head")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	closed = true
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	app, out, stderr := newTestApp(nil, nil)
	args := []string{"spend", "report", "--store", d.store, "--state", d.state, "--json"}
	if code := app.Run(args); code != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out, stderr)
	}
	var got spendReport
	if err = json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Rows also split by rerun label, so each attempt is its own row, sorted by rerun before outcome.
	rows := []struct {
		rerun, outcome string
		input          int64
	}{
		{"after-failure", "stopped", 4},
		{"after-refusal", "refused", 16},
		{"after-refusal", "running", 32},
		{"after-stop", "refused", 8},
		{"first", "succeeded", 1},
		{"send-back", "failed", 2},
	}
	if len(got.Rows) != len(rows) {
		t.Fatalf("rows=%+v", got.Rows)
	}
	for i, w := range rows {
		r := got.Rows[i]
		if r.Rerun != w.rerun || r.Outcome != w.outcome || r.Attempts != 1 || r.Measured.InputTokens != w.input || r.Model != "unavailable" {
			t.Fatalf("row %d=%+v, want %+v", i, r, w)
		}
	}
	// Summing rows by outcome gives the audit's "By outcome" split; both refusals land in refused, never failed.
	type sum struct {
		attempts int
		input    int64
	}
	byOutcome := map[string]sum{}
	for _, r := range got.Rows {
		s := byOutcome[r.Outcome]
		byOutcome[r.Outcome] = sum{s.attempts + r.Attempts, s.input + r.Measured.InputTokens}
	}
	wantOutcome := map[string]sum{"failed": {1, 2}, "refused": {2, 8 + 16}, "running": {1, 32}, "stopped": {1, 4}, "succeeded": {1, 1}}
	if len(byOutcome) != len(wantOutcome) {
		t.Fatalf("by outcome=%+v", byOutcome)
	}
	for k, w := range wantOutcome {
		if byOutcome[k] != w {
			t.Fatalf("outcome %s=%+v, want %+v", k, byOutcome[k], w)
		}
	}
	if got.Total.Attempts != 6 || got.Total.Measured.InputTokens != 63 || got.Total.Measured.CostUSD != 6 || got.Total.Partial {
		t.Fatalf("total=%+v", got.Total)
	}
	var raw struct {
		Rows []map[string]any `json:"rows"`
	}
	if err = json.Unmarshal(out.Bytes(), &raw); err != nil || raw.Rows[0]["outcome"] != "stopped" {
		t.Fatalf("json rows=%v err=%v", raw.Rows, err)
	}
	out.Reset()
	if code := app.Run(args[:len(args)-1]); code != 0 {
		t.Fatalf("table code=%d: %s", code, stderr)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(lines[0], "MODEL") || strings.Index(lines[0], "OUTCOME") <= strings.Index(lines[0], "MODEL") || strings.Index(lines[0], "MEASURE") <= strings.Index(lines[0], "OUTCOME") {
		t.Fatalf("header=%q", lines[0])
	}
	// Line 1 is the first row's measured line, line 3 its unknown line: MEASURE, measured and unknown share a column.
	if col := strings.Index(lines[0], "MEASURE"); strings.Index(lines[1], "measured") != col || strings.Index(lines[3], "unknown") != col || !strings.Contains(lines[1], "stopped") {
		t.Fatalf("misaligned table:\n%s", out)
	}
}

type countedSpendStore struct {
	*store.Store
	receiptReads int
	attemptReads int
}

func (s *countedSpendStore) Receipts() ([]store.Receipt, error) {
	s.receiptReads++
	return s.Store.Receipts()
}

func (s *countedSpendStore) Attempts(repo, task string) ([]store.StageAttempt, error) {
	s.attemptReads++
	return s.Store.Attempts(repo, task)
}

func TestSpendReportScansReceiptsOnlyOnceWhenFiltered(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Second task")
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var boundary time.Time
	for _, task := range []string{"t1", "t2"} {
		for _, step := range []string{"build", "review"} {
			r, err := s.StartAttempt(task+step, "app", task, step)
			if err != nil {
				t.Fatal(err)
			}
			if boundary.IsZero() {
				boundary = r.CommittedAt
			}
		}
	}
	for _, since := range []time.Time{{}, boundary} {
		counted := &countedSpendStore{Store: s}
		got, err := readSpend(counted, d.state, "", "", since)
		if err != nil || got.Total.Attempts != 4 || got.ExcludedUnknownStart != 0 {
			t.Fatalf("since=%v report=%+v err=%v", since, got, err)
		}
		wantReceipts := 0
		if !since.IsZero() {
			wantReceipts = 1
		}
		if counted.receiptReads != wantReceipts || counted.attemptReads != 2 {
			t.Fatalf("since=%v receipt reads=%d, attempt reads=%d", since, counted.receiptReads, counted.attemptReads)
		}
	}
}

type startIndexSpendStore struct {
	boundary time.Time
}

func (s startIndexSpendStore) Repositories() ([]string, error) {
	return []string{"app", "other"}, nil
}

func (s startIndexSpendStore) Tasks(repo string) ([]store.Task, error) {
	return []store.Task{{Repository: repo, ID: "t1"}, {Repository: repo, ID: "t2"}}, nil
}

func (s startIndexSpendStore) Attempts(repo, task string) ([]store.StageAttempt, error) {
	return []store.StageAttempt{
		{Repository: repo, TaskID: task, Step: "build", Number: 1, Outcome: store.AttemptFailed},
		{Repository: repo, TaskID: task, Step: "build", Number: 2},
	}, nil
}

func (s startIndexSpendStore) Receipts() ([]store.Receipt, error) {
	return []store.Receipt{
		{Repository: "app", TaskID: "t1", Operation: "start-attempt", Record: "attempt/build/1", CommittedAt: s.boundary},
		{Repository: "app", TaskID: "t1", Operation: "start-owned-attempt", Record: "attempt/build/2", CommittedAt: s.boundary.Add(time.Second)},
		{Repository: "app", TaskID: "t2", Operation: "start-attempt", Record: "attempt/build/1", CommittedAt: s.boundary.Add(-time.Second)},
		{Repository: "app", TaskID: "t2", Operation: "start-attempt", CommittedAt: s.boundary},
		{Repository: "other", TaskID: "t1", Operation: "start-attempt", Record: "attempt/build/1", CommittedAt: s.boundary.Add(-time.Second)},
		{Repository: "other", TaskID: "t1", Operation: "start-owned-attempt", Record: "attempt/build/2", CommittedAt: s.boundary.Add(-time.Second)},
		{Repository: "other", TaskID: "t2", Operation: "finish-attempt", Record: "attempt/build/1", CommittedAt: s.boundary},
		{Repository: "other", TaskID: "t2", Operation: "start-attempt", Record: "attempt/build/2", CommittedAt: s.boundary.Add(-time.Second)},
	}, nil
}

func TestSpendReportStartIndexSeparatesAttemptIdentities(t *testing.T) {
	s := startIndexSpendStore{boundary: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	state := t.TempDir()
	got, err := readSpend(s, state, "", "", s.boundary)
	if err != nil || got.Total.Attempts != 2 || got.ExcludedUnknownStart != 2 || len(got.Rows) != 2 || got.Rows[0].Repository != "app" || got.Rows[0].Task != "t1" || got.Rows[1].Task != "t1" || got.Rows[0].Rerun != "after-failure" || got.Rows[1].Rerun != "first" {
		t.Fatalf("report=%+v err=%v", got, err)
	}
	got, err = readSpend(s, state, "", "", time.Time{})
	if err != nil || got.Total.Attempts != 8 || got.ExcludedUnknownStart != 0 {
		t.Fatalf("unfiltered=%+v err=%v", got, err)
	}
}

func TestSpendReportCopyAndHelp(t *testing.T) {
	d := newRunDirector(t)
	dir := t.TempDir()
	data, err := os.ReadFile(d.store)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "director.store"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	app, out, stderr := newTestApp(nil, nil)
	markCopy(t, dir)
	app.Getwd = func() (string, error) { return dir, nil }
	if code := app.Run([]string{"spend", "report", "--json"}); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	var got spendReport
	if err = json.Unmarshal(out.Bytes(), &got); err != nil || got.Total.Attempts != 0 || got.Rows == nil {
		t.Fatalf("empty=%+v err=%v", got, err)
	}
	out.Reset()
	if code := app.Run([]string{"spend", "report", "--help"}); code != 0 || !strings.Contains(out.String(), "--since") {
		t.Fatalf("help code %d: %s", code, out)
	}
}

func TestSpendReportRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{{"--since", "yesterday"}, {"--typo"}, {"extra"}} {
		app, _, _ := newTestApp(nil, nil)
		if code := app.Run(append([]string{"spend", "report"}, args...)); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
	app, _, _ := newTestApp(nil, nil)
	dir := t.TempDir()
	if code := app.Run([]string{"spend", "report", "--copy", dir, "--json"}); code != 1 {
		t.Fatalf("missing store code %d", code)
	}
	// Only the copy marker --copy writes is there.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != copyMarkerFile {
		t.Fatalf("created copy state: %v", entries)
	}
}
