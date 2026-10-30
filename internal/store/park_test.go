package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// parkFixture is app/t-1 with build attempt 1 failed and 2 stopped, plan attempt 1 succeeded and review attempt 1
// running, app/t-2 cancelled with a failed build attempt, and app/t-3 done with a failed judge attempt.
func parkFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	mustCreate(t, s, "m-3", sampleTask("app", "t-3"))
	for _, step := range []func() (Receipt, error){
		func() (Receipt, error) { return s.StartAttempt("a-1", "app", "t-1", "build") },
		func() (Receipt, error) { return s.FinishAttempt("a-2", "app", "t-1", "build", 1, AttemptFailed) },
		func() (Receipt, error) { return s.StartAttempt("a-3", "app", "t-1", "build") },
		func() (Receipt, error) { return s.FinishAttempt("a-4", "app", "t-1", "build", 2, AttemptStopped) },
		func() (Receipt, error) { return s.StartAttempt("a-5", "app", "t-1", "plan") },
		func() (Receipt, error) { return s.FinishAttempt("a-6", "app", "t-1", "plan", 1, AttemptSucceeded) },
		func() (Receipt, error) { return s.StartAttempt("a-7", "app", "t-1", "review") },
		func() (Receipt, error) { return s.StartAttempt("a-8", "app", "t-2", "build") },
		func() (Receipt, error) { return s.FinishAttempt("a-9", "app", "t-2", "build", 1, AttemptFailed) },
		func() (Receipt, error) { return s.SetTaskState("a-10", "app", "t-2", TaskCancelled) },
		func() (Receipt, error) { return s.SetTaskState("a-11", "app", "t-3", TaskDone) },
		func() (Receipt, error) { return s.StartAttempt("a-12", "app", "t-3", "quality-judge") },
		func() (Receipt, error) {
			return s.FinishAttempt("a-13", "app", "t-3", "quality-judge", 1, AttemptFailed)
		},
	} {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// TestParkAttemptRefusals: every refusal writes nothing, with ErrInvalid for what can never be parked and
// ErrNotFound for what does not exist.
func TestParkAttemptRefusals(t *testing.T) {
	s := parkFixture(t)
	for name, tc := range map[string]struct {
		task, step string
		n          int
		reason     string
		want       error
	}{
		"blank reason":       {"t-1", "build", 2, " ", ErrInvalid},
		"empty reason":       {"t-1", "build", 2, "", ErrInvalid},
		"oversized reason":   {"t-1", "build", 2, strings.Repeat("r", MaxTextBytes+1), ErrInvalid},
		"closed task":        {"t-2", "build", 1, "Hold.", ErrInvalid},
		"judge step":         {"t-3", "quality-judge", 1, "Hold.", ErrInvalid},
		"not the latest":     {"t-1", "build", 1, "Hold.", ErrInvalid},
		"succeeded attempt":  {"t-1", "plan", 1, "Hold.", ErrInvalid},
		"running attempt":    {"t-1", "review", 1, "Hold.", ErrInvalid},
		"zero attempt":       {"t-1", "build", 0, "Hold.", ErrInvalid},
		"invalid step id":    {"t-1", "Build", 2, "Hold.", ErrInvalid},
		"unknown task":       {"t-9", "build", 1, "Hold.", ErrNotFound},
		"unknown attempt":    {"t-1", "build", 3, "Hold.", ErrNotFound},
		"step with no tries": {"t-1", "deliver", 1, "Hold.", ErrNotFound},
	} {
		if _, err := s.ParkAttempt("bad-"+strings.ReplaceAll(name, " ", "-"), "app", tc.task, tc.step, tc.n, tc.reason, 0, nil); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Parks) != 0 {
		t.Errorf("parks after refusals = %+v", v.Parks)
	}
}

// TestParkAndUnparkAttempt: a park replays under its id, conflicts with another reason, is refused under another id
// or a stale cycle, and an unpark keeps the record so the next park, under the next cycle's id, records anew.
func TestParkAndUnparkAttempt(t *testing.T) {
	s := parkFixture(t)
	if _, err := s.AttemptPark("app", "t-1", "build", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AttemptPark before any = %v, want ErrNotFound", err)
	}
	if _, err := s.UnparkAttempt("unpark-0", "app", "t-1", "build", 2, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpark of an attempt never parked = %v, want ErrInvalid", err)
	}
	rc, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Hold until t-9 merges.", 0, nil)
	if err != nil || rc.Operation != "park-attempt" || rc.Record != "attempt-park/build/2" || rc.TaskID != "t-1" || rc.Attempt != 2 {
		t.Fatalf("ParkAttempt = %+v, %v", rc, err)
	}
	if replay, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Hold until t-9 merges.", 0, nil); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	if _, err := s.ParkAttempt("park.0", "app", "t-1", "build", 2, "Another reason.", 0, nil); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another reason under the same id = %v, want ErrMutationConflict", err)
	}
	if _, err := s.ParkAttempt("other", "app", "t-1", "build", 2, "Hold until t-9 merges.", 0, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("a second park under another id = %v, want ErrExists", err)
	}
	want := AttemptPark{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 2, Reason: "Hold until t-9 merges.", Parked: true}
	if got, err := s.AttemptPark("app", "t-1", "build", 2); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPark =%+v, %v; want %+v", got, err, want)
	}

	if _, err := s.UnparkAttempt("unpark-stale", "app", "t-1", "build", 2, 1); !errors.Is(err, ErrExists) {
		t.Fatalf("unpark at a stale cycle = %v, want ErrExists", err)
	}
	un, err := s.UnparkAttempt("unpark.0", "app", "t-1", "build", 2, 0)
	if err != nil || un.Operation != "unpark-attempt" || un.Record != "attempt-unpark/build/2" || un.Attempt != 2 {
		t.Fatalf("UnparkAttempt = %+v, %v", un, err)
	}
	if replay, err := s.UnparkAttempt("unpark.0", "app", "t-1", "build", 2, 0); err != nil || replay != un {
		t.Fatalf("unpark replay = %+v, %v; want %+v", replay, err, un)
	}
	want.Parked, want.Cycle = false, 1
	if got, err := s.AttemptPark("app", "t-1", "build", 2); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPark after unpark = %+v, %v; want %+v", got, err, want)
	}
	if _, err := s.UnparkAttempt("unpark.1", "app", "t-1", "build", 2, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unpark of an unparked attempt = %v, want ErrInvalid", err)
	}
	if _, err := s.ParkAttempt("park.stale", "app", "t-1", "build", 2, "Hold until t-9 merges.", 0, nil); !errors.Is(err, ErrExists) {
		t.Fatalf("park at a stale cycle = %v, want ErrExists", err)
	}
	again, err := s.ParkAttempt("park.1", "app", "t-1", "build", 2, "Hold until t-9 merges.", 1, nil)
	if err != nil || again.Sequence <= un.Sequence {
		t.Fatalf("park at cycle 1 = %+v, %v; want a new receipt after %d", again, err, un.Sequence)
	}
	want.Parked = true
	if got, err := s.AttemptPark("app", "t-1", "build", 2); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("AttemptPark after the second park = %+v, %v; want %+v", got, err, want)
	}

	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Parks) != 1 || !reflect.DeepEqual(v.Parks[0], want) {
		t.Errorf("DirectorView.Parks = %+v, want [%+v]", v.Parks, want)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil {
		t.Fatal(err)
	}
	var records []string
	for _, r := range h.Receipts {
		if strings.HasPrefix(r.Record, "attempt-park/") || strings.HasPrefix(r.Record, "attempt-unpark/") {
			records = append(records, r.Record)
		}
	}
	if strings.Join(records, " ") != "attempt-park/build/2 attempt-unpark/build/2 attempt-park/build/2" {
		t.Errorf("TaskHistory park receipts = %v", records)
	}
}

// TestUnparkAStalePark: unpark reads only the park record, so a park whose task closed can still be cleared.
func TestUnparkAStalePark(t *testing.T) {
	s := parkFixture(t)
	if _, err := s.ParkAttempt("park", "app", "t-1", "build", 2, "Hold.", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTaskState("close", "app", "t-1", TaskCancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnparkAttempt("unpark", "app", "t-1", "build", 2, 0); err != nil {
		t.Fatalf("unpark of a closed task's park = %v", err)
	}
}
