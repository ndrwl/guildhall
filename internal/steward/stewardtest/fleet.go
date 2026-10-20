package stewardtest

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/ndrwl/guildhall/internal/steward"
)

// Fleet is an in-memory steward.Copier that passes RunCopierContract, for
// testing a rollout consumer without scp. Its machines are maps of files held
// only for its own lifetime; it has no directories, permissions or partial transfers.
type Fleet struct {
	mu      sync.Mutex
	files   map[steward.Remote][]byte
	refused map[string]bool
	hung    map[string]bool
	calls   []string
}

var _ steward.Copier = (*Fleet)(nil)

// NewFleet returns a Fleet with no files and every machine reachable.
func NewFleet() *Fleet {
	return &Fleet{files: map[steward.Remote][]byte{}, refused: map[string]bool{}, hung: map[string]bool{}}
}

// Write sets a file on a machine.
func (f *Fleet) Write(file steward.Remote, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[file] = slices.Clone(data)
}

// Read returns a file on a machine and whether it exists.
func (f *Fleet) Read(file steward.Remote) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[file]
	return slices.Clone(data), ok
}

// Refuse makes every later copy to or from the machine with alias fail.
func (f *Fleet) Refuse(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused[alias] = true
}

// Hang makes every later copy to or from the machine with alias wait until its context ends.
func (f *Fleet) Hang(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung[alias] = true
}

// Calls returns the copies attempted so far, as "fetch alias:path" or "put alias:path", and forgets them.
func (f *Fleet) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// reach records a copy and reports whether the machine can be reached.
func (f *Fleet) reach(ctx context.Context, op string, file steward.Remote) error {
	f.mu.Lock()
	f.calls = append(f.calls, op+" "+file.String())
	refused, hung := f.refused[file.Alias], f.hung[file.Alias]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	if refused {
		return fmt.Errorf("fleet: %s: connection refused", file.Alias)
	}
	if hung {
		<-ctx.Done()
		return fmt.Errorf("fleet: %w", ctx.Err())
	}
	return nil
}

// Fetch copies a machine's file to the local file dst.
func (f *Fleet) Fetch(ctx context.Context, src steward.Remote, dst string) error {
	if err := f.reach(ctx, "fetch", src); err != nil {
		return err
	}
	data, ok := f.Read(src)
	if !ok {
		return fmt.Errorf("fleet: %s: no such file", src)
	}
	return os.WriteFile(dst, data, 0o600)
}

// Put copies the local file src to a machine.
func (f *Fleet) Put(ctx context.Context, src string, dst steward.Remote) error {
	if err := f.reach(ctx, "put", dst); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	f.Write(dst, data)
	return nil
}
