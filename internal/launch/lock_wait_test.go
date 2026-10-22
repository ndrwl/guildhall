package launch

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

func TestOwnershipLockWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	holder, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	a := Attempt{Store: path, LockWait: 100 * time.Millisecond}
	start := time.Now()
	_, err = a.owner()
	if !errors.Is(err, store.ErrLocked) {
		t.Fatalf("owner: %v; want locked", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("owner waited %v; want refusal within 1s", elapsed)
	}
}
