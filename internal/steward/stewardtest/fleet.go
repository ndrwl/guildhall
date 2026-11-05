package stewardtest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/ndrwl/guildhall/internal/steward"
)

// Fleet is an in-memory steward.Copier that passes RunCopierContract, and steward.Runner that passes
// RunRunnerContract, for testing a rollout consumer without scp or ssh. Its machines are maps of files held only
// for its own lifetime; it has no directories, permissions or partial transfers. Its remote commands do nothing
// but answer as Answer, AnswerCommand and Handle set.
type Fleet struct {
	mu      sync.Mutex
	files   map[steward.Remote][]byte
	refused map[string]bool
	hung    map[string]bool
	// unreadable are files a fetch fails for with a diagnostic other than a missing file.
	unreadable map[steward.Remote]string
	answers    map[string]answer
	// handlers answer the commands of one machine, by alias, before answers.
	handlers map[string]Handler
	ran      map[string][][]string
	calls    []string
}

// Handler answers one remote command on a machine, argv as the Runner was given it: it returns what the command
// prints, or an error as its diagnostic when it exits non-zero, with handled true, or handled false to leave the
// command to the machine's Answer. It is called without the Fleet's lock held, so it may read and write the
// Fleet's files.
type Handler func(argv []string) (output []byte, handled bool, err error)

// answer is how a machine answers every remote command.
type answer struct {
	output  []byte
	failure string
}

var (
	_ steward.Copier = (*Fleet)(nil)
	_ steward.Runner = (*Fleet)(nil)
)

// NewFleet returns a Fleet with no files, every machine reachable and every remote command succeeding silently.
func NewFleet() *Fleet {
	return &Fleet{
		files: map[steward.Remote][]byte{}, refused: map[string]bool{}, hung: map[string]bool{}, unreadable: map[steward.Remote]string{},
		answers: map[string]answer{}, handlers: map[string]Handler{}, ran: map[string][][]string{},
	}
}

// Handle makes h answer each later remote command on the machine with alias first, a per-command answer; a command h
// leaves unhandled is answered as Answer set. A later Handle of the alias replaces it.
func (f *Fleet) Handle(alias string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[alias] = h
}

// AnswerCommand makes the remote command on the machine with alias whose arguments after the Guildhall program are
// exactly args print output and succeed, or, when failure is not empty, print failure as its diagnostic and fail;
// every other command is answered as before, by an earlier Handle of the alias or by Answer.
func (f *Fleet) AnswerCommand(alias string, args []string, output []byte, failure string) {
	f.mu.Lock()
	prev := f.handlers[alias]
	f.mu.Unlock()
	args, output = slices.Clone(args), slices.Clone(output)
	f.Handle(alias, func(argv []string) ([]byte, bool, error) {
		if len(argv) > 0 && slices.Equal(argv[1:], args) {
			if failure != "" {
				return nil, true, fmt.Errorf("fleet: %w: %s", errFailed, failure)
			}
			return slices.Clone(output), true, nil
		}
		if prev != nil {
			return prev(argv)
		}
		return nil, false, nil
	})
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

// FailFetch makes every later fetch of file fail with failure as its diagnostic, as a file the machine's user may
// not read or a directory at the path does, whether or not the file exists; the error does not wrap fs.ErrNotExist.
func (f *Fleet) FailFetch(file steward.Remote, failure string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unreadable[file] = failure
}

// Refuse makes every later copy or command to or from the machine with alias fail.
func (f *Fleet) Refuse(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused[alias] = true
}

// Hang makes every later copy or command to or from the machine with alias wait until its context ends.
func (f *Fleet) Hang(alias string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hung[alias] = true
}

// Answer makes every later remote command on the machine with alias print output and succeed, or, when failure is
// not empty, print failure as its diagnostic and fail, as a command exiting non-zero does.
func (f *Fleet) Answer(alias string, output []byte, failure string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[alias] = answer{slices.Clone(output), failure}
}

// Ran returns the remote commands the machine with alias has run, oldest first.
func (f *Fleet) Ran(alias string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.ran[alias])
}

// Calls returns the copies and commands attempted so far, as "fetch alias:path", "put alias:path" or
// "run alias <argv>", and forgets them.
func (f *Fleet) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// reach records an operation on the machine with alias and reports whether it can be reached.
func (f *Fleet) reach(ctx context.Context, call, alias string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	refused, hung := f.refused[alias], f.hung[alias]
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	if refused {
		return fmt.Errorf("fleet: %s: connection refused", alias)
	}
	if hung {
		<-ctx.Done()
		return fmt.Errorf("fleet: %w", ctx.Err())
	}
	return nil
}

// Fetch copies a machine's file to the local file dst.
func (f *Fleet) Fetch(ctx context.Context, src steward.Remote, dst string) error {
	if err := f.reach(ctx, "fetch "+src.String(), src.Alias); err != nil {
		return err
	}
	f.mu.Lock()
	failure, unreadable := f.unreadable[src]
	f.mu.Unlock()
	if unreadable {
		return fmt.Errorf("fleet: %s: %s", src, failure)
	}
	data, ok := f.Read(src)
	if !ok {
		return fmt.Errorf("fleet: %s: %w", src, fs.ErrNotExist)
	}
	return os.WriteFile(dst, data, 0o600)
}

// Put copies the local file src to a machine.
func (f *Fleet) Put(ctx context.Context, src string, dst steward.Remote) error {
	if err := f.reach(ctx, "put "+dst.String(), dst.Alias); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("fleet: %w", err)
	}
	f.Write(dst, data)
	return nil
}

// Run runs one fixed remote command on the machine with alias, refusing what steward.CheckRun refuses.
func (f *Fleet) Run(ctx context.Context, alias string, argv []string) ([]byte, error) {
	if err := steward.CheckRun(alias, argv); err != nil {
		return nil, err
	}
	if err := f.reach(ctx, "run "+alias+" "+strings.Join(argv, " "), alias); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.ran[alias] = append(f.ran[alias], slices.Clone(argv))
	h := f.handlers[alias]
	f.mu.Unlock()
	if h != nil {
		if out, handled, err := h(slices.Clone(argv)); handled {
			return out, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.answers[alias]
	if a.failure != "" {
		return nil, fmt.Errorf("fleet: %w: %s", errFailed, a.failure)
	}
	return slices.Clone(a.output), nil
}

var errFailed = errors.New("remote command failed")
