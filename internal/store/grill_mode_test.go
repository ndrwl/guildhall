package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestSetGrillMode: a task with no record is live; records append numbered from 1, the latest is in effect, a replay
// returns the original receipt and the task's history reads the records.
func TestSetGrillMode(t *testing.T) {
	s := overrideFixture(t)
	none, err := s.GrillModes("app", "t-1")
	if err != nil || len(none) != 0 {
		t.Fatalf("GrillModes before any = %+v, %v", none, err)
	}
	if g := CurrentGrillMode(none); g.Mode != workflow.GrillLive || g.Number != 0 || g.Line() != "grill: live" {
		t.Errorf("CurrentGrillMode(none) = %+v, line %q; want live", g, g.Line())
	}
	first, err := s.SetGrillMode("g-1", "app", "t-1", workflow.GrillUnattended, "A hallmark follow-up with its contracts written.", GrillModeByDirector, 0)
	if err != nil || first.Record != GrillModeRecord(1) || first.Operation != "set-grill-mode" || first.Repository != "app" || first.TaskID != "t-1" {
		t.Fatalf("SetGrillMode 1 = %+v, %v", first, err)
	}
	if replay, err := s.SetGrillMode("g-1", "app", "t-1", workflow.GrillUnattended, "A hallmark follow-up with its contracts written.", GrillModeByDirector, 0); err != nil || replay != first {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	second, err := s.SetGrillMode("g-2", "app", "t-1", workflow.GrillLive, "The Guildmaster: grill me on this.", GrillModeByGuildmaster, 1)
	if err != nil || second.Record != GrillModeRecord(2) {
		t.Fatalf("SetGrillMode 2 = %+v, %v", second, err)
	}
	got, err := s.GrillModes("app", "t-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("GrillModes = %+v, %v", got, err)
	}
	want := GrillMode{Repository: "app", TaskID: "t-1", Number: 2, Mode: workflow.GrillLive, Reason: "The Guildmaster: grill me on this.", By: GrillModeByGuildmaster, AddedAt: second.CommittedAt}
	if c := CurrentGrillMode(got); c != want {
		t.Errorf("CurrentGrillMode = %+v, want %+v", c, want)
	}
	if line := got[0].Line(); line != "grill: unattended (director): A hallmark follow-up with its contracts written." {
		t.Errorf("line = %q", line)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.GrillModes) != 2 || h.GrillModes[1] != want {
		t.Errorf("history grill modes = %+v, %v", h.GrillModes, err)
	}
	if rep, err := s.Doctor(); err != nil || len(rep.Problems) != 0 || rep.Repositories[0].GrillModes != 2 {
		t.Errorf("doctor = %+v, %v; want 2 grill modes and no problems", rep, err)
	}
}

// TestSetGrillModeRefusals: each refusal records nothing.
func TestSetGrillModeRefusals(t *testing.T) {
	s := overrideFixture(t)
	if _, err := s.SetGrillMode("g-1", "app", "t-1", workflow.GrillUnattended, "Delegated.", GrillModeByDirector, 0); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		task   string
		mode   workflow.GrillMode
		reason string
		by     string
		prior  int
		want   error
	}{
		"an unknown mode":           {"t-1", "skipped", "r", GrillModeByDirector, 1, ErrInvalid},
		"an unknown setter":         {"t-1", workflow.GrillLive, "r", "artisan", 1, ErrInvalid},
		"unattended with no reason": {"t-1", workflow.GrillUnattended, " ", GrillModeByDirector, 1, ErrInvalid},
		"a two-line reason":         {"t-1", workflow.GrillLive, "one\ntwo", GrillModeByDirector, 1, ErrInvalid},
		"the mode in effect":        {"t-1", workflow.GrillUnattended, "Again.", GrillModeByDirector, 1, ErrInvalid},
		"a stale prior count":       {"t-1", workflow.GrillLive, "r", GrillModeByDirector, 0, ErrExists},
		"a closed task":             {"t-done", workflow.GrillLive, "r", GrillModeByDirector, 0, ErrInvalid},
		"an unknown task":           {"t-none", workflow.GrillLive, "r", GrillModeByDirector, 0, ErrNotFound},
	} {
		if _, err := s.SetGrillMode("refused:"+strings.ReplaceAll(name, " ", "-"), "app", c.task, c.mode, c.reason, c.by, c.prior); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	if got, err := s.GrillModes("app", "t-1"); err != nil || len(got) != 1 {
		t.Errorf("GrillModes after refusals = %+v, %v", got, err)
	}
	// A first record may be live with no reason: the Director chose the default explicitly.
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	if _, err := s.SetGrillMode("g-live", "app", "t-2", workflow.GrillLive, "", GrillModeByDirector, 0); err != nil {
		t.Errorf("a first live record with no reason: %v", err)
	}
}
