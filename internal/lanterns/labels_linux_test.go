package lanterns

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestLabelsVerifyGenerationAndReplace(t *testing.T) {
	id, err := artisan.Lookup(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity := store.ProcessIdentity{PID: id.PID, Token: id.Token}
	v := store.DirectorView{
		Session:  &store.DirectorSession{Number: 1, State: store.DirectorRunning, Identity: identity},
		Attempts: []store.StageAttempt{{Repository: "repo", TaskID: "task", Step: "build", Number: 1}},
		Owners:   []store.Ownership{{Repository: "repo", TaskID: "task", Step: "build", Attempt: 1, Containment: store.Containment{Worker: identity}}},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "shared", "vitals-identities.json")
	t.Setenv("VITALS_AXI_LABELS", path)
	now := time.Now().UTC()
	if err := PublishLabels(dir, v, now); err != nil {
		t.Fatal(err)
	}
	read := func() Labels {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var labels Labels
		if err := json.Unmarshal(b, &labels); err != nil {
			t.Fatal(err)
		}
		return labels
	}
	labels := read()
	if labels.SchemaVersion != 1 || labels.BootID == "" || labels.PIDNamespace == "" || len(labels.Entries) != 2 {
		t.Fatalf("invalid labels: %+v", labels)
	}
	if !labels.ExpiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("expiry: %s", labels.ExpiresAt)
	}
	for _, e := range labels.Entries {
		if e.Selector.PID != id.PID || e.Selector.StartTicks == "" || !e.Selector.Descendants {
			t.Fatalf("selector: %+v", e)
		}
	}
	var fields map[string]json.RawMessage
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	foreign := json.RawMessage(`{"id":"firstmate-worker","source":"bridge-lights","label":"Firstmate","extra":"preserved","selector":{"kind":"process","pid":123,"startTicks":"456","descendants":true}}`)
	var entries []json.RawMessage
	if err := json.Unmarshal(fields["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	fields["entries"], _ = json.Marshal(append(entries, foreign))
	fields["publisherMetadata"] = json.RawMessage(`{"keep":true}`)
	b, _ = json.Marshal(fields)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	otherCopy := filepath.Join(dir, "other-copy")
	if err := PublishLabels(otherCopy, v, now); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got.Entries) != 5 || got.Entries[0].ID == got.Entries[3].ID {
		t.Fatalf("copies collided: %+v", got)
	}
	v.Session.State = store.DirectorStopped
	v.Owners[0].Containment.Worker.Token += "reused"
	if err := PublishLabels(dir, v, now); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got.Entries) != 3 || got.Entries[0].ID != "firstmate-worker" {
		t.Fatalf("incorrect ownership merge: %+v", got)
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["publisherMetadata"]) != `{"keep":true}` {
		t.Fatalf("lost publisher metadata: %s", b)
	}
	if err := json.Unmarshal(fields["entries"], &entries); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(entries[0], &entry); err != nil || entry["extra"] != "preserved" {
		t.Fatalf("lost foreign entry fields: %s %v", entries[0], err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	if err := PublishLabels(dir, v, now); err == nil {
		t.Fatal("publication bypassed another writer's lock")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(b) {
		t.Fatalf("busy publication changed labels: %s %v", after, err)
	}
}

func TestLabelsDefaultDestination(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VITALS_AXI_LABELS", "")
	t.Setenv("XDG_STATE_HOME", dir)
	if err := PublishLabels(filepath.Join(dir, "copy"), store.DirectorView{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bridge-lights", "vitals-identities.json")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", dir)
	if err := PublishLabels(filepath.Join(dir, "copy"), store.DirectorView{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".local", "state", "bridge-lights", "vitals-identities.json")); err != nil {
		t.Fatal(err)
	}
}
