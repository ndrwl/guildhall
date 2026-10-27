package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// waitWakeExit is the exit status that wakes the Director: Claude Code reads an asyncRewake hook's exit 2 as a
// rewake and its standard error as the new turn's input.
const waitWakeExit = 2

// waitWakeRetry is the pause before a waiter polls again after its supervisor could not be reached, such as while
// the supervisor restarts. It is a variable only so tests can shorten it.
var waitWakeRetry = time.Second

// directorWaitWake runs `guildhall director wait-wake [--copy <dir>] [--refresh-after <duration>]`, the Director
// runtime's async rewake hook (docs/decisions/director-wake-hook.md). It registers with the supervisor of its
// session, from GUILDHALL_SESSION or else the newest, over the session endpoint and blocks until the supervisor
// hands it a wake: it then prints the counts-only wake line on standard error and exits 2, which wakes the
// Director without touching its input box. A newer waiter retires it, exit 0 with nothing printed, as does its
// session ending. A dropped or missing endpoint is polled again, so a supervisor restart does not leave it unarmed.
// Shortly before its hook's timeout, --refresh-after (claude.WaitWakeRefresh unless set), a waiter still
// registered exits 2 with a counts-only refresh line, so the turn that starts arms a new waiter.
// Every other failure is exit 1.
func (a *App) directorWaitWake(args []string) int {
	const name = "director wait-wake"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	refreshAfter := fl.Duration("refresh-after", claude.WaitWakeRefresh, "refresh the wake this long after arming")
	var storePath string
	err := fl.Parse(args)
	if err == nil {
		err = cp.apply(a.Getenv, &storePath, nil, nil)
	}
	switch {
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case fl.NArg() > 0:
		return a.fail(fmt.Errorf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return a.fail(fmt.Errorf("%s: --copy is required, or GUILDHALL_COPY", name))
	case *refreshAfter <= 0:
		return a.fail(fmt.Errorf("%s: --refresh-after must be positive", name))
	}
	copyDir, err := filepath.Abs(cp.dir)
	if err != nil {
		return a.fail(err)
	}
	if _, err := os.Stat(storePath); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, noStore(storePath)))
	}
	n := 0
	if a.Getenv != nil {
		n, _ = strconv.Atoi(a.Getenv(session.NumberEnv))
	}
	if n < 1 {
		d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
		if err != nil {
			return a.fail(fmt.Errorf("%s: %w", name, err))
		}
		n = d.Number
	}
	dir := sessionDir(copyDir, n)
	a.hookInput()
	id := fmt.Sprintf("%d-%s", os.Getpid(), rand.Text()[:10])
	refreshAt := time.Now().Add(*refreshAfter)
	for {
		left := time.Until(refreshAt)
		if left <= 0 {
			return a.refreshWake(storePath, dir, n, id)
		}
		ctx, cancel := context.WithTimeout(context.Background(), min(left, session.PollHold+10*time.Second))
		line, err := session.WaitWake(ctx, dir, n, id)
		cancel()
		switch {
		case errors.Is(err, session.ErrRetired):
			return 0
		case err == nil && line != "":
			_, _ = fmt.Fprintln(a.Stderr, line)
			return waitWakeExit
		case err == nil:
			continue
		}
		if !sessionLive(storePath, n) {
			return 0
		}
		time.Sleep(min(waitWakeRetry, max(time.Until(refreshAt), 0)))
	}
}

// refreshWake is a waiter's refresh before its hook times out: it tells the supervisor, which counts the refresh
// as a wake, and exits 2 with the Director's unread counts, so the turn it starts arms a new waiter. A waiter a newer
// one has retired, or whose session has ended, exits 0 instead.
func (a *App) refreshWake(storePath, dir string, n int, id string) int {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	if err := session.Refresh(ctx, dir, n, id); errors.Is(err, session.ErrRetired) || err != nil && !sessionLive(storePath, n) {
		return 0
	}
	unread, err := withStore(storePath, true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
	if err != nil {
		unread = nil
	}
	_, _ = fmt.Fprintln(a.Stderr, messaging.WakeLine(unread))
	return waitWakeExit
}

// sessionLive reports whether session n is still the newest session and not stopped, so its waiter keeps waiting;
// a store that cannot be read counts as live, so a busy store does not disarm the waiter.
func sessionLive(storePath string, n int) bool {
	d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
	if err != nil {
		return !errors.Is(err, store.ErrNotFound)
	}
	return d.Number == n && d.State != store.DirectorStopped
}

// hookInput reads the hook's JSON input; the waiter keeps nothing of it yet.
func (a *App) hookInput() {
	if a.Stdin != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(a.Stdin, 1<<20))
	}
}
