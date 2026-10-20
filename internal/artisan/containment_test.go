package artisan_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// TestRecordContainment: the attempt directory records the kind under the
// key "containment", replacing an earlier record.
func TestRecordContainment(t *testing.T) {
	dir := t.TempDir()
	if err := artisan.RecordContainment(dir, artisan.Containment{Kind: artisan.KindGroup}); err != nil {
		t.Fatal(err)
	}
	want := artisan.Containment{Kind: artisan.KindSubreaper, Reason: "cgroup v1"}
	if err := artisan.RecordContainment(dir, want); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, artisan.ContainmentFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["containment"] != "subreaper+pgroup" || raw["reason"] != "cgroup v1" {
		t.Errorf("record = %s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
