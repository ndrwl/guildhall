package artisan_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// fakeUnit is the in-memory double of artisan.Unit for the launcher's tests.
// It never runs cmd: it models the artisantest.Worker the command describes,
// with fake identities, writing the inventory a real worker tree would. It
// models a Unix process group with a refused breakaway: Terminate empties it,
// a setsid grandchild escapes and survives Kill, a breakaway request is
// refused and the grandchild contained.
type fakeUnit struct {
	procs *fakeProcs

	mu      sync.Mutex
	started bool
	closed  bool
	worker  artisan.Identity
	members map[artisan.Identity]bool
	ignore  bool
	release string
	exited  chan struct{}
}

// fakeProcs is the liveness of every process the fakes of one harness made.
type fakeProcs struct {
	mu    sync.Mutex
	next  int
	alive map[artisan.Identity]bool
}

func (p *fakeProcs) spawn() artisan.Identity {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := artisan.Identity{PID: 1_000_000 + p.next, Token: "fake:" + strconv.Itoa(p.next)}
	p.alive[id] = true
	return id
}

func (p *fakeProcs) end(ids ...artisan.Identity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		delete(p.alive, id)
	}
}

func (p *fakeProcs) probe(id artisan.Identity) (artisan.Liveness, error) {
	if !strings.HasPrefix(id.Token, "fake:") {
		return artisan.Probe(id)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.alive[id] {
		return artisan.Alive, nil
	}
	return artisan.Gone, nil
}

func (p *fakeProcs) kill(id artisan.Identity) error {
	if !strings.HasPrefix(id.Token, "fake:") {
		return artisantest.KillVerified(id)
	}
	p.end(id)
	return nil
}

func newFakeHarness() artisantest.Harness {
	procs := &fakeProcs{alive: map[artisan.Identity]bool{}}
	return artisantest.Harness{
		New:              func(*testing.T) artisan.Unit { return newFakeUnit(procs) },
		Probe:            procs.probe,
		Kill:             procs.kill,
		Terminate:        true,
		SetsidEscapes:    true,
		BreakawayRefused: true,
		Kind:             artisan.KindGroup,
	}
}

func newFakeUnit(procs *fakeProcs) *fakeUnit {
	return &fakeUnit{procs: procs, members: map[artisan.Identity]bool{}, exited: make(chan struct{})}
}

func (u *fakeUnit) Start(cmd *exec.Cmd) (artisan.Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.closed:
		return artisan.Identity{}, artisan.ErrClosed
	case u.started:
		return artisan.Identity{}, artisan.ErrStarted
	}
	w, ok := artisantest.WorkerOf(cmd)
	if !ok {
		return artisan.Identity{}, fmt.Errorf("fake unit: %s is not an artisantest worker: %w", cmd.Path, os.ErrNotExist)
	}
	u.started, u.ignore, u.release = true, w.IgnoreInterrupt, w.Release
	if u.release == "" {
		u.release = filepath.Join(w.Inventory, "release")
	}
	u.worker = u.procs.spawn()
	u.members[u.worker] = true
	record := func(e artisantest.Entry) error { return artisantest.Record(w.Inventory, e) }
	if err := record(artisantest.Entry{Identity: u.worker, Role: "worker"}); err != nil {
		return artisan.Identity{}, err
	}
	var escaped []artisan.Identity
	for range w.Grandchildren {
		if w.BreakawayGrandchild {
			if err := record(artisantest.Entry{Identity: u.worker, Role: "breakaway", Breakaway: "refused: breakaway not allowed"}); err != nil {
				return artisan.Identity{}, err
			}
		}
		g := u.procs.spawn()
		if w.SetsidGrandchild {
			escaped = append(escaped, g)
		} else {
			u.members[g] = true
		}
		if err := record(artisantest.Entry{Identity: g, Role: "grandchild", Setsid: w.SetsidGrandchild}); err != nil {
			return artisan.Identity{}, err
		}
	}
	go func() {
		w.Wait()
		u.mu.Lock()
		defer u.mu.Unlock()
		select {
		case <-u.exited:
			// Unit shutdown released this waiter, not the escaped processes.
			return
		default:
		}
		u.procs.end(escaped...)
		u.endLocked()
	}()
	return u.worker, nil
}

// endLocked ends every member and the worker, releasing the modelled
// worker's wait so it publishes nothing once ended.
func (u *fakeUnit) endLocked() {
	if u.release != "" {
		_ = os.WriteFile(u.release, nil, 0o600)
	}
	for id := range u.members {
		u.procs.end(id)
		delete(u.members, id)
	}
	select {
	case <-u.exited:
	default:
		close(u.exited)
	}
}

func (u *fakeUnit) rung(end bool) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.closed:
		return artisan.ErrClosed
	case !u.started:
		return artisan.ErrNotStarted
	}
	if end {
		u.endLocked()
	}
	return nil
}

func (u *fakeUnit) Members() ([]artisan.Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.closed:
		return nil, artisan.ErrClosed
	case !u.started:
		return nil, artisan.ErrNotStarted
	}
	var out []artisan.Identity
	for id := range u.members {
		out = append(out, id)
	}
	return out, nil
}

func (u *fakeUnit) Interrupt() error {
	u.mu.Lock()
	ignore := u.ignore
	u.mu.Unlock()
	return u.rung(!ignore)
}

func (u *fakeUnit) Terminate() error        { return u.rung(true) }
func (u *fakeUnit) Kill() error             { return u.rung(true) }
func (u *fakeUnit) Exited() <-chan struct{} { return u.exited }

func (u *fakeUnit) Containment() artisan.Containment {
	return artisan.Containment{Kind: artisan.KindGroup}
}

func (u *fakeUnit) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return nil
	}
	u.endLocked()
	u.closed = true
	return nil
}
