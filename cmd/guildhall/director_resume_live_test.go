package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// Exercise resume from a separate CLI process while the owned runtime waits for input.
// Herdr is a disposable terminal transport; the Director and CLI are the built product.
func TestExternalResumeDeliversQueuedWork(t *testing.T) {
	c := newSafetyCopy(t, "", "")
	c.approve()
	c.guildhall("", "task", "new", "--repo", "app", "--title", "Queued work", "t-1")
	c.script("guildhall director ready")
	s := c.start("fake", herdrBinding...)
	c.waitFor("ready", func() bool { return c.latestSession().State == store.DirectorRunning })
	t.Log("pause:", c.guildhall("", "director", "pause"))
	note := `{"schema":"guildhall.message/1","id":"resume-note","recipient":"director","repository":"app","task":"t-1","type":"note","text":"Please handle queued work."}`
	t.Log("send while paused:", c.guildhall(note, "messages", "send", "director", "-"))
	// A persisted paused wait is the engine's public continuation contract.
	dir := filepath.Join(c.dir, "state", "app", "t-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "next.json")
	if err := os.WriteFile(next, []byte(`{"wait":"paused","step":"build"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	wakes := func() []store.Wake {
		return read(c, func(s *store.Store) ([]store.Wake, error) {
			w, _, err := s.Wakes(0, store.MaxInboxLimit)
			return w, err
		})
	}
	before := wakes()
	if len(before) != 1 || before[0].Decision != store.WakeQueued || len(c.typed()) != 0 || len(c.turnLines()) != 1 {
		t.Fatalf("paused: wakes=%+v typed=%q turns=%q", before, c.typed(), c.turnLines())
	}
	out := c.guildhall("", "director", "resume")
	t.Log("external resume:", out)
	var doc struct {
		Wake struct {
			Decision string `json:"decision"`
		} `json:"wake"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Wake.Decision != "woke" {
		t.Fatalf("resume: %s (%v)", out, err)
	}
	typed := c.typed()
	if len(typed) != 1 || !strings.Contains(typed[0], "messages 1, paused 1") {
		t.Fatalf("typed %q", typed)
	}
	// Deliver the terminal transport's captured line to the waiting runtime.
	if _, err := io.WriteString(s.stdin, typed[0]+"\n"); err != nil {
		t.Fatal(err)
	}
	c.waitFor("runtime input turn", func() bool { return len(c.turnLines()) == 2 })
	t.Logf("terminal delivery: %q; runtime turns: %q", typed, c.turnLines())
	t.Log("repeated resume:", c.guildhall("", "director", "resume"))
	t.Log("replayed message:", c.guildhall(note, "messages", "send", "director", "-"))
	// Both CLI operations have acknowledged their synchronous wake decisions.
	if !reflect.DeepEqual(wakes(), before) || len(c.typed()) != 1 || len(c.turnLines()) != 2 {
		t.Fatalf("replay altered wakes or delivered again: wakes=%+v typed=%q turns=%q", wakes(), c.typed(), c.turnLines())
	}
	t.Logf("immutable message wake after resume/replay: %+v", wakes())

	c.guildhall("", "messages", "ack", "director", "resume-note")
	if err := os.Remove(next); err != nil {
		t.Fatal(err)
	}
	c.guildhall("", "director", "pause")
	out = c.guildhall("", "director", "resume")
	t.Log("resume with no actionable work:", out)
	if !strings.Contains(out, `"reason":"nothing waiting"`) || len(c.typed()) != 1 || len(c.turnLines()) != 2 {
		t.Fatalf("empty resume delivered input: %s; typed=%q turns=%q", out, c.typed(), c.turnLines())
	}
	if code := s.end(); code != 0 {
		t.Fatalf("session exit %d", code)
	}
	c.finish(2)
}
