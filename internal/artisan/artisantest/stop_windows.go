package artisantest

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// runPlatformStopCases runs the Windows stop-contract cases.
//
// Every AttemptFiles adapter reads the result through the bounded
// publication-race retry (internal/atomicfile): a result published and then
// held without sharing while the launcher decides, as a rename in progress
// holds it, is waited for, and the attempt is finished, not failed.
func runPlatformStopCases(t *testing.T, h Harness) {
	t.Run("a result held without sharing at the decision is finished", func(t *testing.T) {
		dir := t.TempDir()
		files := h.StopFiles(dir)
		if err := os.WriteFile(files.Result, []byte(files.Body), 0o600); err != nil {
			t.Fatal(err)
		}
		name, err := windows.UTF16PtrFromString(files.Result)
		if err != nil {
			t.Fatal(err)
		}
		held, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(100*time.Millisecond, func() { _ = windows.CloseHandle(held) })
		l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: &exitedUnit{exited: make(chan struct{})}, Files: files.Reader, Key: StopKey})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := l.Run(&exec.Cmd{}, nil); err != nil || out.State != artisan.Finished {
			t.Fatalf("Run() = %+v, %v; want finished with no error", out, err)
		}
	})
}

// exitedUnit is a unit whose worker has exited as soon as it starts and
// leaves no member, so the launcher decides at once.
type exitedUnit struct {
	artisan.Unit
	exited chan struct{}
}

func (u *exitedUnit) Start(*exec.Cmd) (artisan.Identity, error) {
	close(u.exited)
	return artisan.Identity{PID: 1, Token: "worker"}, nil
}
func (u *exitedUnit) Exited() <-chan struct{}              { return u.exited }
func (u *exitedUnit) Members() ([]artisan.Identity, error) { return nil, nil }
