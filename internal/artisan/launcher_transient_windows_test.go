package artisan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// TestResultHeldAtDecisionIsFinished reproduces main's Windows failure of L16
// deterministically: the result is published, then held without sharing, as
// a rename in progress holds it, while the launcher decides. The read waits
// for the holder instead of deciding the attempt failed.
func TestResultHeldAtDecisionIsFinished(t *testing.T) {
	dir := t.TempDir()
	files := artisantest.NewFiles(dir)
	if err := os.WriteFile(files.Result, []byte(artisantest.ResultBody), 0o600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(files.Result)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(100*time.Millisecond, func() { _ = windows.CloseHandle(h) })
	u := &exitedLauncherUnit{exited: make(chan struct{})}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: u, Files: files, Key: artisantest.StopKey})
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Run(&exec.Cmd{}, nil)
	if err != nil || out.State != artisan.Finished {
		t.Fatalf("Run = %+v, %v; want finished with no error", out, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, artisan.OutcomeFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"state": "finished"`) {
		t.Fatalf("outcome.json = %s, want finished", b)
	}
}
