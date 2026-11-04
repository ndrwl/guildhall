package steward_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
)

// TestParseDirectorStatus: the Steward reads the session and health of a copy's status feed, ignoring the rest; a
// feed with no health is ErrNoHealth, and output that is not JSON or is too large is refused.
func TestParseDirectorStatus(t *testing.T) {
	feed := `{"as_of":{"sequence":9},"session":{"state":"running","number":4,"started_at":"2026-11-04T08:00:00Z","guard_rails":{}},` +
		`"tasks":[],"health":{"supervisor":"alive","last_turn":{"turn":"turn-ended","at":"2026-11-04T09:30:00Z"},"waiting":2,` +
		`"waiting_since":"2026-11-04T09:00:00Z","release":"v0.4.0","pin":"v0.5.0","behind":true}}`
	h, err := steward.ParseDirectorStatus([]byte(feed))
	want := steward.DirectorHealth{State: "running", Number: 4, StartedAt: time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC), Supervisor: "alive",
		LastTurn: time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC), Waiting: 2, WaitingSince: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC),
		Release: "v0.4.0", Pin: "v0.5.0", Behind: true}
	if err != nil || h != want {
		t.Fatalf("ParseDirectorStatus = %+v, %v; want %+v", h, err, want)
	}
	if _, err := steward.ParseDirectorStatus([]byte(`{"session":{"state":"no session"}}`)); !errors.Is(err, steward.ErrNoHealth) {
		t.Errorf("no health: %v, want ErrNoHealth", err)
	}
	if _, err := steward.ParseDirectorStatus([]byte("not json")); err == nil {
		t.Error("not JSON was read")
	}
	big := `{"health":{"supervisor":"none"},"pad":"` + strings.Repeat("x", steward.MaxStatusBytes) + `"}`
	if _, err := steward.ParseDirectorStatus([]byte(big)); err == nil {
		t.Error("an oversized feed was read")
	}
}
