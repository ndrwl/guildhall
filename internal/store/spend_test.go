package store

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

var claudeSpend = Spend{Tokens: SpendMeasured, InputTokens: 1200, OutputTokens: 340, CacheCreationInputTokens: 5000, CacheReadInputTokens: 18000, Cost: SpendEstimated, CostUSD: 0.0421}

func TestRecordAttemptSpendFirstRecordStands(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	mustCreate(t, s, "task", sampleTask("app", "t1"))
	if _, err := s.StartAttempt("start", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	first, err := s.RecordAttemptSpend("spend-1", "app", "t1", "build", 1, claudeSpend)
	if err != nil || first.Operation != "record-attempt-spend" || first.Record != "attempt/build/1" || first.Attempt != 1 {
		t.Fatalf("RecordAttemptSpend = %+v, %v", first, err)
	}
	if a, _ := s.Attempts("app", "t1"); len(a) != 1 || a[0].Spend == nil || *a[0].Spend != claudeSpend || a[0].Outcome != "" {
		t.Fatalf("attempts = %+v; want the running attempt with its spend", a)
	}
	// A second writer, such as artisan resume, recording the same spend gets the first receipt and writes nothing.
	again, err := s.RecordAttemptSpend("spend-2", "app", "t1", "build", 1, claudeSpend)
	if err != nil || again.MutationID != "spend-1" {
		t.Fatalf("equal spend again = %+v, %v; want the first receipt", again, err)
	}
	if _, err := s.Receipt("spend-2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the standing record left a receipt: %v", err)
	}
	if _, err := s.RecordAttemptSpend("spend-3", "app", "t1", "build", 1, UnknownSpend); !errors.Is(err, ErrInvalid) {
		t.Errorf("a different spend = %v, want ErrInvalid", err)
	}
	// Spend is recorded on a finished attempt too, and a finish keeps it.
	if _, err := s.FinishAttempt("finish", "app", "t1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Attempts("app", "t1"); a[0].Spend == nil || *a[0].Spend != claudeSpend {
		t.Errorf("finish dropped the spend: %+v", a[0])
	}
	if _, err := s.StartAttempt("start-2", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("finish-2", "app", "t1", "build", 2, AttemptStopped); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordAttemptSpend("spend-4", "app", "t1", "build", 2, UnknownSpend); err != nil {
		t.Errorf("spend of a finished attempt = %v", err)
	}
}

func TestRecordAttemptSpendRefuses(t *testing.T) {
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	mustCreate(t, s, "task", sampleTask("app", "t1"))
	if _, err := s.StartAttempt("start", "app", "t1", "build"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordAttemptSpend("no-attempt", "app", "t1", "build", 2, claudeSpend); err == nil {
		t.Error("spend of a missing attempt recorded")
	}
	if _, err := s.RecordAttemptSpend("no-task", "app", "t9", "build", 1, claudeSpend); err == nil {
		t.Error("spend of a missing task recorded")
	}
	n := 0
	for name, sp := range map[string]Spend{
		"no measures":     {},
		"guessed tokens":  {Tokens: SpendUnknown, InputTokens: 5, Cost: SpendUnknown},
		"guessed cost":    {Tokens: SpendUnknown, Cost: SpendUnknown, CostUSD: 1},
		"negative tokens": {Tokens: SpendMeasured, OutputTokens: -1, Cost: SpendUnknown},
		"negative cost":   {Tokens: SpendUnknown, Cost: SpendEstimated, CostUSD: -1},
		"not a number":    {Tokens: SpendUnknown, Cost: SpendEstimated, CostUSD: math.NaN()},
		"other measure":   {Tokens: "guessed", Cost: SpendUnknown},
	} {
		n++
		if _, err := s.RecordAttemptSpend(fmt.Sprintf("bad-%d", n), "app", "t1", "build", 1, sp); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: RecordAttemptSpend = %v, want ErrInvalid", name, err)
		}
	}
	if a, _ := s.Attempts("app", "t1"); a[0].Spend != nil {
		t.Errorf("a refused record wrote %+v", a[0].Spend)
	}
}

func TestSpendTotal(t *testing.T) {
	total := NoSpend
	if total.Tokens != SpendUnknown || total.Cost != SpendUnknown || total.Attempts != 0 {
		t.Fatalf("NoSpend = %+v; want unknown measures and no attempts", total)
	}
	total.AddAttempt(StageAttempt{Number: 1, Outcome: AttemptSucceeded, Spend: &claudeSpend})
	measured := Spend{Tokens: SpendMeasured, InputTokens: 1, OutputTokens: 2, CacheCreationInputTokens: 3, CacheReadInputTokens: 4, Cost: SpendUnknown}
	total.AddAttempt(StageAttempt{Number: 2, Outcome: AttemptFailed, Spend: &measured})
	total.AddAttempt(StageAttempt{Number: 3, Outcome: AttemptStopped})                                                                // finished without a record: unknown
	total.AddAttempt(StageAttempt{Number: 4})                                                                                         // running without a record: not counted
	total.AddAttempt(StageAttempt{Number: 5, Spend: &Spend{Tokens: SpendMeasured, InputTokens: 10, Cost: SpendMeasured, CostUSD: 1}}) // recorded before its finish
	want := SpendTotal{
		Attempts: 4, TokensUnknown: 1, CostUnknown: 2,
		Tokens: SpendMeasured, InputTokens: 1211, OutputTokens: 342, CacheCreationInputTokens: 5003, CacheReadInputTokens: 18004,
		Cost: SpendEstimated, CostUSD: 1.0421,
	}
	if math.Abs(total.CostUSD-want.CostUSD) > 1e-9 {
		t.Errorf("cost = %v, want %v", total.CostUSD, want.CostUSD)
	}
	total.CostUSD = want.CostUSD
	if total != want {
		t.Errorf("total = %+v\nwant    %+v", total, want)
	}
	unknown := NoSpend
	unknown.AddAttempt(StageAttempt{Number: 1, Outcome: AttemptFailed, Spend: &UnknownSpend})
	if want := (SpendTotal{Attempts: 1, TokensUnknown: 1, CostUnknown: 1, Tokens: SpendUnknown, Cost: SpendUnknown}); unknown != want {
		t.Errorf("unknown total = %+v, want %+v", unknown, want)
	}
}

func TestAttemptUsageSumsTodaysSpend(t *testing.T) {
	now := time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC)
	s := capStore(t, &now)
	mustCreate(t, s, "task-1", sampleTask("app", "t1"))
	mustCreate(t, s, "task-2", sampleTask("site", "t2"))
	// Yesterday's attempt is not today's spend.
	if _, err := s.StartAttempt("start-y", "app", "t1", "plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordAttemptSpend("spend-y", "app", "t1", "plan", 1, claudeSpend); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("finish-y", "app", "t1", "plan", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	for i, repo := range []string{"app", "site"} {
		task := []string{"t1", "t2"}[i]
		if _, err := s.StartAttempt("start-"+repo, repo, task, "build"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RecordAttemptSpend("spend-app", "app", "t1", "build", 1, claudeSpend); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("finish-app", "app", "t1", "build", 1, AttemptSucceeded); err != nil {
		t.Fatal(err)
	}
	u, err := s.AttemptUsage()
	if err != nil {
		t.Fatal(err)
	}
	want := SpendTotal{Attempts: 1, Tokens: SpendMeasured, InputTokens: 1200, OutputTokens: 340, CacheCreationInputTokens: 5000, CacheReadInputTokens: 18000, Cost: SpendEstimated, CostUSD: 0.0421}
	if u.Today != 2 || u.Running != 1 || u.Spend != want {
		t.Fatalf("AttemptUsage = %+v; want 2 today, 1 running and spend %+v", u, want)
	}
	v, err := s.DirectorView()
	if err != nil || v.Usage != u {
		t.Errorf("DirectorView.Usage = %+v, %v; want %+v", v.Usage, err, u)
	}
}
