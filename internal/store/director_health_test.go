package store

import (
	"errors"
	"testing"
	"time"
)

// TestDirectorWaiting: the Director's unread local and external messages are counted, since is the oldest one's
// arrival, and with every one acknowledged nothing waits.
func TestDirectorWaiting(t *testing.T) {
	s, _ := messagingStore(t)
	if n, since, err := s.DirectorWaiting(); err != nil || n != 0 || !since.IsZero() {
		t.Fatalf("empty: %d, %v, %v", n, since, err)
	}
	mustSend(t, s, toDirector("q-1"))
	mustSend(t, s, toArtisan("n-1"))
	r, err := s.Receipt("message:q-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveExternal(stewardReport("r-1"), DefaultMailboxMax); err != nil {
		t.Fatal(err)
	}
	n, since, err := s.DirectorWaiting()
	if err != nil || n != 2 || !since.Equal(r.CommittedAt) {
		t.Fatalf("waiting: %d, %v, %v; want 2 since %v", n, since, err, r.CommittedAt)
	}
	if _, err := s.AckMessage(Recipient{Role: RecipientDirector}, "q-1"); err != nil {
		t.Fatal(err)
	}
	m, _ := s.External(SenderSteward, "r-1")
	if n, since, err := s.DirectorWaiting(); err != nil || n != 1 || !since.Equal(m.ReceivedAt) {
		t.Fatalf("after ack: %d, %v, %v; want 1 since %v", n, since, err, m.ReceivedAt)
	}
	if _, err := s.AckExternal(SenderSteward, "r-1"); err != nil {
		t.Fatal(err)
	}
	if n, since, err := s.DirectorWaiting(); err != nil || n != 0 || !since.IsZero() {
		t.Fatalf("all acknowledged: %d, %v, %v", n, since, err)
	}
}

// TestMarkCopyCondition: a condition keeps the time it was first found with its mark until it clears or its mark
// changes, either of which starts it again; another kind is its own condition, and a kind that is not a report kind
// is refused.
func TestMarkCopyCondition(t *testing.T) {
	s, _ := newStore(t)
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		holds bool
		mark  string
		at    time.Time
		want  time.Time
	}{
		{false, "", t0, time.Time{}},
		{true, "v0.4.0 v0.5.0", t0.Add(time.Minute), t0.Add(time.Minute)},
		{true, "v0.4.0 v0.5.0", t0.Add(time.Hour), t0.Add(time.Minute)},
		{true, "v0.4.0 v0.6.0", t0.Add(2 * time.Hour), t0.Add(2 * time.Hour)},
		{false, "", t0.Add(3 * time.Hour), time.Time{}},
		{true, "v0.4.0 v0.6.0", t0.Add(4 * time.Hour), t0.Add(4 * time.Hour)},
	} {
		got, err := s.MarkCopyCondition("laptop/tools", ReportReleaseBehind, c.mark, c.holds, c.at)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("step %d: %v, %v; want %v", i, got, err, c.want)
		}
	}
	at := t0.Add(5 * time.Hour)
	if got, err := s.MarkCopyCondition("laptop/tools", ReportDirectorUnhealthy, "session 3 supervisor gone", true, at); err != nil || !got.Equal(at) {
		t.Errorf("other kind: %v, %v", got, err)
	}
	// The same call again is a replay of its mutation, which still returns the condition's Since.
	if got, err := s.MarkCopyCondition("laptop/tools", ReportDirectorUnhealthy, "session 3 supervisor gone", true, at); err != nil || !got.Equal(at) {
		t.Errorf("replay: %v, %v", got, err)
	}
	if got, _ := s.MarkCopyCondition("laptop/tools", ReportReleaseBehind, "v0.4.0 v0.6.0", true, t0.Add(6*time.Hour)); !got.Equal(t0.Add(4 * time.Hour)) {
		t.Errorf("release-behind after another kind: %v", got)
	}
	if _, err := s.MarkCopyCondition("laptop/tools", "bored", "x", true, t0); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown kind: %v, want ErrInvalid", err)
	}
}
