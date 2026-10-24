package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestLanternsDisplayCannotOverwriteHandoffContext(t *testing.T) {
	copyDir := t.TempDir()
	dir := sessionDir(copyDir, 1)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	original := session.Context{UsedPercent: 80, ReportedAt: time.Now().UTC()}
	if err := session.WriteContext(dir, original); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GUILDHALL_COPY": copyDir, session.NumberEnv: "1"}
	code, out, stderr := contextReport(t, fixture(t, "statusline.json"), env, "--lanterns")
	if code != 0 || !strings.HasPrefix(out, "⚪ status unavailable") || !strings.Contains(out, "context 72%") {
		t.Fatalf("display = %d %s %s", code, out, stderr)
	}
	got, err := session.ReadContext(filepath.Join(dir, session.ContextFile))
	if err != nil || got != original {
		t.Fatalf("display overwrote hook context: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(copyDir, "director.store")); !os.IsNotExist(err) {
		t.Fatalf("display created a store: %v", err)
	}
}

// Real hook, transcript, store and supervisor endpoint; no status-line command or
// plugin is present. A missing transcript must not suppress the end-of-turn signal.
func TestTurnEndedContextTriggersHandoffWithoutStatusLine(t *testing.T) {
	copyDir, err := os.MkdirTemp("", "gl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(copyDir) }()
	dir := sessionDir(copyDir, 1)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyDir, "director.store")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	d := store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"}, Release: "dev", ConfigDigest: "sha256:" + strings.Repeat("c", 64), Handoff: store.HandoffPolicy{ContextPercent: 70, ContextFile: filepath.Join(dir, session.ContextFile)}}
	if _, err := s.StartDirectorSession(d); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ep, err := session.Listen(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ep.Close() }()
	transcript := filepath.Join(copyDir, "transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":1000,"cache_read_input_tokens":143000}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		input := transcript
		if missing {
			input += ".missing"
		}
		b, _ := json.Marshal(map[string]string{"transcript_path": input})
		app, _, stderr := newTestApp(nil, nil)
		app.Stdin = strings.NewReader(string(b))
		app.Getenv = func(k string) string {
			if k == "GUILDHALL_COPY" {
				return copyDir
			}
			return ""
		}
		if code := app.Run([]string{"director", "turn-ended"}); code != 0 {
			t.Fatalf("hook=%d %s", code, stderr.String())
		}
		select {
		case turn := <-ep.Turns():
			if turn != session.TurnEnded {
				t.Fatal(turn)
			}
		case <-time.After(time.Second):
			t.Fatal("hook did not signal supervisor")
		}
	}
	s, err = store.Open(path, store.Options{RequireStore: true, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	due, err := messaging.DueHandoffs(s, d)
	if err != nil || len(due) != 1 || due[0].Trigger != store.HandoffContext || due[0].Reason != "context 72%" {
		t.Fatalf("handoff without status line: %+v %v", due, err)
	}
	for _, tail := range []string{
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"assistant","message":{"model":"provider-specific","usage":{"input_tokens":144000}}}`,
	} {
		if err := session.WriteContext(dir, session.Context{UsedPercent: 72, ReportedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		data := `{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":144000}}}` + "\n" + tail + "\n"
		if err := os.WriteFile(transcript, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]string{"transcript_path": transcript})
		app, _, stderr := newTestApp(nil, nil)
		app.Stdin = strings.NewReader(string(b))
		app.Getenv = func(k string) string {
			if k == "GUILDHALL_COPY" {
				return copyDir
			}
			return ""
		}
		if code := app.Run([]string{"director", "turn-ended"}); code != 0 {
			t.Fatalf("hook=%d %s", code, stderr.String())
		}
		select {
		case <-ep.Turns():
		case <-time.After(time.Second):
			t.Fatal("invalidation suppressed turn signal")
		}
		if _, err := os.Stat(filepath.Join(dir, session.ContextFile)); !os.IsNotExist(err) {
			t.Fatalf("historical context retained: %v", err)
		}
		if due, err := messaging.DueHandoffs(s, d); err != nil || len(due) != 0 {
			t.Fatalf("historical context triggered handoff: %+v %v", due, err)
		}
	}
}
