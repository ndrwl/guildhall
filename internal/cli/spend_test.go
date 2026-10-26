package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"time"
)

func TestSpendReportRecordedUsage(t *testing.T) {
	d := newRunDirector(t)
	s, err := store.Open(d.store, store.Options{RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range []string{"one", "two", "three"} {
		if _, err := s.StartAttempt(step, "app", "t-1", step); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := s.RecordAttemptSpend("spend", "app", "t-1", step, 1, store.Spend{Tokens: store.SpendMeasured, InputTokens: 10, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 4, Cost: store.SpendEstimated, CostUSD: 0.1}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"one", "two"} {
		dir := (artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: step, Attempt: 1}).Dir(d.state)
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
	if len(got.Rows) != 3 || got.Rows[0].Step != "one" || got.Rows[0].Model != "unavailable" {
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
	started, err := s.StartAttempt("start", "app", "t-1", "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishAttempt("finish", "app", "t-1", "build", 1, store.AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := (artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}).Dir(d.state)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := render.Manifest{Schema: render.ManifestSchema, Identity: render.Identity{Repository: "app", Task: "t-1", Step: "build", Attempt: 1, Profile: "builder"}, Role: render.File{Path: "roles/builder.md"}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, render.ManifestFile)
	if err = os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(d.store, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := readSpend(s, d.state, "app", "t-1", started.CommittedAt)
	if err != nil || len(got.Rows) != 1 || got.Rows[0].Profile != "builder" || got.Rows[0].Role != "roles/builder.md" || got.Total.TokensUnknown != 1 {
		t.Fatalf("report=%+v err=%v", got, err)
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
		r, err := s.StartAttempt(id, "app", "t-1", "build")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.RecordAttemptSpend(id+"-spend", "app", "t-1", "build", r.Attempt, store.Spend{Tokens: store.SpendMeasured, InputTokens: 9, Cost: store.SpendMeasured, CostUSD: 0.25}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.FinishAttempt(id+"-finish", "app", "t-1", "build", r.Attempt, store.AttemptFailed); err != nil {
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
	if err != nil || len(got.Rows) != 1 || got.Total.Attempts != 2 || got.Total.Partial || got.Rows[0].Measured.InputTokens != 18 || got.Rows[0].Measured.CostUSD != 0.5 {
		t.Fatalf("report=%+v err=%v", got, err)
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
	app.Getenv = func(key string) string {
		if key == copyEnvVar {
			return dir
		}
		return ""
	}
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
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("created copy state")
	}
}
