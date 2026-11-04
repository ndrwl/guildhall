package interp

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestNoteAbsent: the first read with no checks of a head is kept; a later read of the same head keeps it, a
// different head or a lost record starts again, a record from a clock that went back starts again, and a read with
// checks removes it.
func TestNoteAbsent(t *testing.T) {
	now := time.Date(2026, 11, 3, 23, 15, 0, 0, time.UTC)
	r := &run{e: &Engine{StateDir: t.TempDir(), Now: func() time.Time { return now }}, task: store.Task{Repository: "app", ID: "t7"}}
	s := workflow.Step{ID: "validate"}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	first, err := r.noteAbsent(s, a)
	if err != nil || !first.Equal(now) {
		t.Fatalf("first read = %v, %v", first, err)
	}
	start := now
	now = now.Add(5 * time.Minute)
	if got, err := r.noteAbsent(s, a); err != nil || !got.Equal(start) {
		t.Errorf("same head later = %v, %v; want %v", got, err, start)
	}
	if got, err := r.noteAbsent(s, b); err != nil || !got.Equal(now) {
		t.Errorf("changed head = %v, %v; want now", got, err)
	}
	if got, _ := r.noteAbsent(s, a); !got.Equal(now) {
		t.Errorf("back to the first head = %v, want now: the record is per head", got)
	}
	if err := r.clearAbsent(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.checksAbsentPath(s)); !os.IsNotExist(err) {
		t.Errorf("cleared record: %v", err)
	}
	if err := r.clearAbsent(s); err != nil {
		t.Errorf("clearing no record = %v", err)
	}
	if _, err := r.noteAbsent(s, a); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour)
	if got, _ := r.noteAbsent(s, a); !got.Equal(now) {
		t.Errorf("a record ahead of the clock = %v, want now", got)
	}
	if err := os.WriteFile(r.checksAbsentPath(s), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := r.noteAbsent(s, a); err != nil || !got.Equal(now) {
		t.Errorf("an unreadable record = %v, %v; want now", got, err)
	}
}

// TestCheckWait: the four outcomes of a read, and what each sets on the result.
func TestCheckWait(t *testing.T) {
	passed, pending, failed := forge.Check{Name: "a", State: forge.CheckPassed}, forge.Check{Name: "b", State: forge.CheckPending}, forge.Check{Name: "c", State: forge.CheckFailed}
	for _, tc := range []struct {
		checks []forge.Check
		noCI   bool
		want   checkState
	}{
		{nil, false, checksAbsent},
		{nil, true, checksNoCI},
		{[]forge.Check{passed}, false, checksFinished},
		{[]forge.Check{passed}, true, checksFinished},
		{[]forge.Check{passed, pending}, false, checksPending},
		{[]forge.Check{pending, failed}, false, checksFinished},
	} {
		v := validationDoc{Verdict: validationPassed, Checks: tc.checks}
		if got := checkWait(&v, tc.noCI); got != tc.want {
			t.Errorf("checkWait(%v, no_ci %v) = %v, want %v", tc.checks, tc.noCI, got, tc.want)
		}
		if v.Checks == nil || v.NoCI != (tc.want == checksNoCI) {
			t.Errorf("%v: checks %v, no_ci %v", tc.checks, v.Checks, v.NoCI)
		}
		if failing := len(v.Failing) > 0; failing != (v.Verdict == validationFailed) || failing != (len(tc.checks) == 2 && tc.checks[1] == failed) {
			t.Errorf("%v: verdict %s, failing %v", tc.checks, v.Verdict, v.Failing)
		}
	}
}

// TestCIStartName: a start record's name and its outcome's fit a document id.
func TestCIStartName(t *testing.T) {
	h := strings.Repeat("a", 64)
	if got := ciStartName("validate", h); got != "validate-ci-start-aaaaaaaaaaaa" {
		t.Errorf("ciStartName = %q", got)
	}
	long := ciStartName("validate-implementation-of-the-feature-12", h)
	if !strings.HasPrefix(long, "ci-start-") || len(long+"-outcome") > 63 {
		t.Errorf("long ciStartName = %q", long)
	}
}
