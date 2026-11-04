package steward_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
)

// holdLock takes .rollout.lock in dir with a live privateLock, released when the test ends.
func holdLock(t *testing.T, dir string) {
	t.Helper()
	release, err := steward.PrivateLock(dir, time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

// checkBusyOwner checks that err is busy on a lock this process holds, and names its pid and command.
func checkBusyOwner(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, steward.ErrBusy) || !errors.Is(err, fs.ErrExist) {
		t.Fatalf("error = %v, want one matching ErrBusy and fs.ErrExist", err)
	}
	for _, want := range []string{fmt.Sprintf("pid %d", os.Getpid()), os.Args[0]} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestCollectBusyNamesOwner (red-first): a collection round refused by a held lock names its owner, and still
// returns the copies it read.
func TestCollectBusyNamesOwner(t *testing.T) {
	fx := newCollectFixture(t)
	fx.tools.lesson("l-one", "One")
	holdLock(t, fx.checkout)
	c, _ := fx.collector()
	res, err := c.Collect(context.Background(), nil, nil)
	checkBusyOwner(t, err)
	if res == nil || len(res.Copies) != 3 {
		t.Errorf("a busy round returned %+v, want the three copies it read", res)
	}
	if _, err := os.Stat(filepath.Join(fx.checkout, ".rollout.lock")); err != nil {
		t.Errorf("a busy round removed the lock: %v", err)
	}
}

// TestTakeoverBusyNamesOwner (red-first): a takeover apply refused by a held lock names its owner.
func TestTakeoverBusyNamesOwner(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	planDir := filepath.Join(t.TempDir(), "plan")
	p := fx.dryRun(t, planDir)
	holdLock(t, fx.checkout)
	_, err := fx.setup().ApplySetup(context.Background(), planDir, p.Digest)
	checkBusyOwner(t, err)
	if len(fx.ops.acted) != 0 {
		t.Errorf("acted %v", fx.ops.acted)
	}
}
