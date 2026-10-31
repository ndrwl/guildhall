package release

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPin: a copy's release.pin is one line holding a release version, written by rename; a missing one is
// fs.ErrNotExist and one that is not a version is refused.
func TestPin(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadPin(dir); !errors.Is(err, fs.ErrNotExist) || HasPin(dir) {
		t.Fatalf("pin of a copy with none: %v", err)
	}
	if err := WritePin(dir, "dev"); err == nil {
		t.Fatal("WritePin wrote dev")
	}
	if err := WritePin(dir, "v1.4.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(PinPath(dir)); string(b) != "v1.4.0\n" {
		t.Fatalf("release.pin holds %q", b)
	}
	if v, err := ReadPin(dir); err != nil || v != "v1.4.0" || !HasPin(dir) {
		t.Fatalf("ReadPin = %q, %v", v, err)
	}
	if err := os.WriteFile(PinPath(dir), []byte("../etc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPin(dir); err == nil || errors.Is(err, fs.ErrNotExist) || !HasPin(dir) {
		t.Fatalf("ReadPin of a bad pin: %v", err)
	}
}

// TestNewestAndRunnable: Newest is the newest intact install, skipping damaged ones; Runnable names a missing or
// damaged release and its path.
func TestNewestAndRunnable(t *testing.T) {
	h := newHome(t)
	if _, ok, err := h.Newest(); ok || err != nil {
		t.Fatalf("Newest of an empty home: %v %v", ok, err)
	}
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	installed(t, h, "v1.0.0", "one", at)
	installed(t, h, "v2.0.0", "two", at.Add(time.Minute))
	if r, ok, err := h.Newest(); !ok || err != nil || r.Version != "v2.0.0" {
		t.Fatalf("Newest = %+v %v %v", r, ok, err)
	}
	if err := os.WriteFile(h.Binary("v2.0.0"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r, ok, err := h.Newest(); !ok || err != nil || r.Version != "v1.0.0" {
		t.Fatalf("Newest past a damaged release = %+v %v %v", r, ok, err)
	}
	if _, err := h.Runnable("v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Runnable("v2.0.0"); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("Runnable of a damaged release: %v", err)
	}
	if _, err := h.Runnable("v3.0.0"); err == nil || !strings.Contains(err.Error(), h.Binary("v3.0.0")) {
		t.Fatalf("Runnable of a missing release: %v", err)
	}
}
