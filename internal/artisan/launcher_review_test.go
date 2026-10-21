package artisan_test

import (
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

type exitedLauncherUnit struct {
	artisan.Unit
	start     func()
	members   func()
	interrupt func()
	alive     bool
	exited    chan struct{}
}

func (u *exitedLauncherUnit) Start(*exec.Cmd) (artisan.Identity, error) {
	if u.start != nil {
		u.start()
	}
	close(u.exited)
	return artisan.Identity{PID: 1, Token: "worker"}, nil
}
func (u *exitedLauncherUnit) Exited() <-chan struct{} { return u.exited }
func (u *exitedLauncherUnit) Members() ([]artisan.Identity, error) {
	if u.members != nil {
		u.members()
	}
	if u.alive {
		return []artisan.Identity{{PID: 2, Token: "child"}}, nil
	}
	return nil, nil
}
func (u *exitedLauncherUnit) Interrupt() error {
	if u.interrupt != nil {
		u.interrupt()
	}
	u.alive = false
	return nil
}

func TestLauncherCancelDuringCleanup(t *testing.T) {
	dir := t.TempDir()
	files := artisantest.NewFiles(dir)
	u := &exitedLauncherUnit{exited: make(chan struct{})}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: u, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	u.members = func() {
		u.members = nil
		if _, err := l.Cancel(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files.Result, []byte(artisantest.ResultBody), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := l.Run(&exec.Cmd{}, nil)
	if err != nil || out.State != artisan.Cancelled || !out.LateResultRejected {
		t.Fatalf("Run = %+v, %v", out, err)
	}
}

func TestLauncherCancelledDescendantsRequireStop(t *testing.T) {
	dir := t.TempDir()
	files := artisantest.NewFiles(dir)
	u := &exitedLauncherUnit{exited: make(chan struct{}), alive: true}
	u.start = func() {
		if err := files.Mark(); err != nil {
			t.Fatal(err)
		}
	}
	signals := make(chan os.Signal, 1)
	var requested atomic.Bool
	calls := 0
	u.members = func() {
		calls++
		if calls == 3 {
			requested.Store(true)
			signals <- syscall.SIGTERM
		}
	}
	u.interrupt = func() {
		if !requested.Load() {
			t.Error("interrupted descendants before explicit stop")
		}
	}
	l, err := artisan.NewLauncher(artisan.LauncherConfig{Dir: dir, Unit: u, Files: files,
		Ladder: artisan.Ladder{Cooperative: time.Millisecond, Interrupt: time.Millisecond, Terminate: time.Millisecond, Kill: time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.Run(&exec.Cmd{}, signals)
	if err != nil || out.State != artisan.Cancelled || out.Rung != 2 {
		t.Fatalf("Run = %+v, %v", out, err)
	}
	if !requested.Load() {
		t.Error("launcher ended before explicit stop")
	}
}
