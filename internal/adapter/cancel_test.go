package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The cancellation sidecar is an attempt-scoped persisted protocol, not a
// task store. A late result must not override it, even after client restart.
func TestCancelledAttemptRejectsLateSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	id := Identity{TaskID: "task", AttemptID: "attempt"}
	r := Result{Identity: id, Status: StatusSucceeded, Summary: "late", Outputs: []Output{}}
	if err := WriteResult(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".cancelled", data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResult(path, id); err == nil {
		t.Fatal("accepted success for a cancelled attempt")
	}
	if err := WriteResult(filepath.Join(filepath.Dir(path), "other.json"), r); err != nil {
		t.Fatalf("cancellation affected another result path: %v", err)
	}
}
