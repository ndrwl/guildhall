package lanterns

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	now := time.Now().UTC()
	if err := PublishLabels(dir, v, now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "director", "lanterns", "vitals-identities.json")
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
	v.Session.State = store.DirectorStopped
	v.Owners[0].Containment.Worker.Token += "reused"
	if err := PublishLabels(dir, v, now); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got.Entries) != 0 {
		t.Fatalf("stale entries retained: %+v", got)
	}
}
