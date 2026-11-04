package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLastTurnRoundTrips: a written last turn is read back, a newer one replaces it, and a missing one is
// fs.ErrNotExist.
func TestLastTurnRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TurnFile)
	if _, err := ReadLastTurn(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no record = %v, want fs.ErrNotExist", err)
	}
	at := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	for _, lt := range []LastTurn{{Turn: TurnStarted, At: at}, {Turn: TurnEnded, At: at.Add(time.Minute)}} {
		if err := WriteLastTurn(dir, lt); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadLastTurn(path); err != nil || got != lt {
			t.Fatalf("read back %+v, %v; want %+v", got, err, lt)
		}
	}
}

// TestLastTurnRefusals: a relative directory or an unknown turn writes nothing, and a record of another schema or
// turn is refused.
func TestLastTurnRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := WriteLastTurn("relative", LastTurn{Turn: TurnEnded, At: time.Now()}); err == nil {
		t.Error("relative directory written")
	}
	if err := WriteLastTurn(dir, LastTurn{Turn: "wake", At: time.Now()}); err == nil {
		t.Error("unknown turn written")
	}
	path := filepath.Join(dir, TurnFile)
	for _, bad := range []string{
		`{"schema":"guildhall.session-turn/2","turn":"turn-ended","at":"2026-11-04T09:00:00Z"}`,
		`{"schema":"guildhall.session-turn/1","turn":"wake","at":"2026-11-04T09:00:00Z"}`,
		`{"schema":"guildhall.session-turn/1","turn":"turn-ended","at":"2026-11-04T09:00:00Z","extra":1}`,
	} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLastTurn(path); err == nil {
			t.Errorf("ReadLastTurn(%s) accepted", bad)
		}
	}
}
