package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/admit"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// releaseHolds is what every copy in a Guildhall home's inventory holds of its installed releases, by version
// (docs/releases.md#pruning), and Problems, each inventoried copy whose pin or store cannot be read or disagrees,
// which make prune refuse.
type releaseHolds struct {
	Held     map[string][]string
	Problems []string
}

// copyHolds is what one copy's store holds: its newest release-pin event, nil for none, and its active tasks'
// recorded releases.
type copyHolds struct {
	newest *store.ReleasePin
	tasks  []activeTaskRelease
}

// readReleaseHolds reads, read-only, every copy in h's inventory: its pin, the rollback target its newest
// release-pin event names, and the releases its tasks neither done nor cancelled record, each a hold on that
// release. A copy that cannot be read, or whose pin file and newest event disagree, is a problem, not an error,
// so release status can show it; only an inventory that cannot be read is an error.
func readReleaseHolds(h release.Home) (releaseHolds, error) {
	copies, err := h.Copies()
	if err != nil {
		return releaseHolds{}, fmt.Errorf("inventory %s: %w", h.CopiesFile(), err)
	}
	out := releaseHolds{Held: map[string][]string{}, Problems: []string{}}
	hold := func(version, why string) { out.Held[version] = append(out.Held[version], why) }
	for _, c := range copies {
		if info, err := os.Stat(c); err != nil || !info.IsDir() {
			why := "is not a directory"
			if err != nil {
				why = err.Error()
			}
			out.Problems = append(out.Problems, fmt.Sprintf("copy %s cannot be read (%s); if the copy is gone for good, remove its line from %s", c, why, h.CopiesFile()))
			continue
		}
		storePath := copyStore(c)
		var got copyHolds
		if _, err := os.Stat(storePath); err == nil {
			got, err = withStore(storePath, true, func(s *store.Store) (copyHolds, error) {
				var ch copyHolds
				switch p, err := s.LatestReleasePin(); {
				case err == nil:
					ch.newest = &p
				case !errors.Is(err, store.ErrNotFound):
					return ch, err
				}
				var err error
				ch.tasks, _, err = activeTaskReleases(s)
				return ch, err
			})
			if err != nil {
				out.Problems = append(out.Problems, fmt.Sprintf("copy %s: store %s cannot be read: %v", c, storePath, err))
				continue
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			out.Problems = append(out.Problems, fmt.Sprintf("copy %s: store %s cannot be read: %v", c, storePath, err))
			continue
		}
		if why := admit.PinRefusal(c, got.newest); why != "" {
			out.Problems = append(out.Problems, fmt.Sprintf("copy %s: %s", c, why))
			continue
		}
		if p := got.newest; p != nil {
			hold(p.Version, "pinned by copy "+c)
			if p.PreviousVersion != "" {
				hold(p.PreviousVersion, "rollback target of copy "+c)
			}
		}
		for _, t := range got.tasks {
			hold(t.Version, fmt.Sprintf("recorded by active task %s/%s of copy %s", t.Repository, t.TaskID, c))
		}
	}
	return out, nil
}

// pruneOutcome is what a prune did, or with dry would do: the plan, newest install first, and the stale .incoming/
// entries removed.
type pruneOutcome struct {
	Items    []release.PruneItem
	Incoming []string
	Dry      bool
}

// pruneReleases prunes h, under the home lock, so no pin change, task creation or copy registration can add a
// reference between the check and the removal: the newest three installed releases and every release a copy holds
// stay; every other is removed and recorded as a release-pruned event in the store at storePath, the store of the
// copy that ran it; stale .incoming/ entries are removed. Any inventoried copy that cannot be read or disagrees
// refuses the prune with nothing removed. dry removes and records nothing.
func (a *App) pruneReleases(h release.Home, storePath string, dry bool) (pruneOutcome, error) {
	unlock, err := h.Lock(releaseLockWait)
	if err != nil {
		return pruneOutcome{}, err
	}
	defer func() { _ = unlock() }()
	holds, err := readReleaseHolds(h)
	if err != nil {
		return pruneOutcome{}, err
	}
	if len(holds.Problems) > 0 {
		return pruneOutcome{}, fmt.Errorf("refused, removing nothing, as an inventoried copy's references cannot be trusted: %s", strings.Join(holds.Problems, "; "))
	}
	releases, err := h.List()
	if err != nil {
		return pruneOutcome{}, err
	}
	out := pruneOutcome{Items: release.PlanPrune(releases, holds.Held), Dry: dry}
	if dry {
		return out, nil
	}
	if err := a.removeAndRecord(h, storePath, out.Items); err != nil {
		return out, err
	}
	if out.Incoming, err = h.PruneIncomingLocked(a.now()); err != nil {
		return out, fmt.Errorf("stale %s: %w", h.IncomingDir(), err)
	}
	return out, nil
}

// removeAndRecord removes each release items marks Remove and records it as a release-pruned event in the store at
// storePath, opening that store before the first removal, so a store that cannot be opened removes nothing, and
// recording each removal as soon as it succeeds.
func (a *App) removeAndRecord(h release.Home, storePath string, items []release.PruneItem) error {
	if !slices.ContainsFunc(items, func(it release.PruneItem) bool { return it.Remove }) {
		return nil
	}
	_, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (struct{}, error) {
		for _, it := range items {
			if !it.Remove {
				continue
			}
			if err := h.RemoveLocked(it.Version); err != nil {
				return struct{}{}, fmt.Errorf("remove %s: %w", h.ReleaseDir(it.Version), err)
			}
			if _, err := s.RecordReleasePruned(store.ReleasePruned{Version: it.Version, SHA256: it.SHA256, Reason: it.Reason}); err != nil {
				return struct{}{}, fmt.Errorf("record release-pruned %s: %w", it.Version, err)
			}
		}
		return struct{}{}, nil
	})
	return err
}

// lines is the prune's report, one line per release removed or kept past the limit and per stale entry.
func (o pruneOutcome) lines(h release.Home) []string {
	var out []string
	removed, would := "removed", "removed"
	if o.Dry {
		would = "would remove"
	}
	for _, it := range o.Items {
		switch {
		case it.Left:
			out = append(out, fmt.Sprintf("left %s: %s", h.ReleaseDir(it.Version), it.Reason))
		case it.Remove:
			out = append(out, fmt.Sprintf("%s %s (%s): %s", would, it.Version, shortSHA(it.SHA256), it.Reason))
		case len(it.Held) > 0:
			out = append(out, fmt.Sprintf("kept %s past the newest %d: %s", it.Version, release.Keep, strings.Join(it.Held, "; ")))
		}
	}
	for _, name := range o.Incoming {
		out = append(out, fmt.Sprintf("%s stale %s", removed, filepath.Join(h.IncomingDir(), name)))
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("nothing to prune: %d installed releases, within the newest %d or held", len(o.Items), release.Keep))
	}
	return out
}

// releasePrune runs `guildhall release prune [--dry-run] [--copy <dir>]` (docs/contracts/cli.md#release).
func (a *App) releasePrune(args []string) int {
	fl := flag.NewFlagSet("release prune", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	dry := fl.Bool("dry-run", false, "list what would be removed, removing nothing")
	var cp copyFlags
	cp.register(fl)
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release prune: " + err.Error())
	}
	if fl.NArg() != 0 {
		return a.usageError(fmt.Sprintf("release prune: unexpected argument %q\n%s", fl.Arg(0), releaseUsage))
	}
	_, storePath, err := a.releaseCopy(&cp)
	if err != nil {
		return a.fail(fmt.Errorf("release prune: %w", err))
	}
	h, err := a.releaseHome()
	if err != nil {
		return a.fail(fmt.Errorf("release prune: %w", err))
	}
	out, err := a.pruneReleases(h, storePath, *dry)
	if err != nil {
		return a.fail(fmt.Errorf("release prune: %w", err))
	}
	if _, err := io.WriteString(a.Stdout, strings.Join(out.lines(h), "\n")+"\n"); err != nil {
		return 1
	}
	return 0
}

// pruneAfterPin prunes h after a pin change of the copy whose store is at storePath, printing what it did; a
// refused prune is a warning on stderr, since the pin change stands.
func (a *App) pruneAfterPin(h release.Home, storePath string) {
	out, err := a.pruneReleases(h, storePath, false)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "warning: release prune after the pin change: %v; the pin change stands, and guildhall release prune retries\n", err)
		return
	}
	var lines []string
	for _, l := range out.lines(h) {
		if !strings.HasPrefix(l, "nothing to prune") {
			lines = append(lines, "prune: "+l)
		}
	}
	if len(lines) > 0 {
		_, _ = io.WriteString(a.Stdout, strings.Join(lines, "\n")+"\n")
	}
}

// releaseRollback runs `guildhall release rollback [--copy <dir>]`: a standalone copy's pin moves back to the
// previous version its newest release-pin event names, with source rollback, then prunes
// (docs/releases.md#rollback). A second rollback undoes the first, since the release it left is then the previous.
func (a *App) releaseRollback(args []string) int {
	fl := flag.NewFlagSet("release rollback", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release rollback: " + err.Error())
	}
	if fl.NArg() != 0 {
		return a.usageError(fmt.Sprintf("release rollback: unexpected argument %q\n%s", fl.Arg(0), releaseUsage))
	}
	fail := func(err error) int { return a.fail(fmt.Errorf("release rollback: %w", err)) }
	dir, storePath, err := a.releaseCopy(&cp)
	if err != nil {
		return fail(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, managedFile)); err == nil {
		return fail(fmt.Errorf("copy %s is managed: its release is a managed key, so the Steward rolls back by rollout of the previous %s revision, then guildhall config reload --copy and guildhall director reload --copy", dir, managedFile))
	}
	h, err := a.releaseHome()
	if err != nil {
		return fail(err)
	}
	latest, err := withStore(storePath, true, func(s *store.Store) (store.ReleasePin, error) { return s.LatestReleasePin() })
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fail(fmt.Errorf("copy %s has no release pin to roll back; pin one with guildhall release adopt or guildhall release use <version>", dir))
	case err != nil:
		return fail(err)
	case latest.PreviousVersion == "":
		return fail(fmt.Errorf("copy %s pins %s, its first pin, so there is no previous release to roll back to", dir, latest.Version))
	}
	r, err := h.Runnable(latest.PreviousVersion)
	if err != nil {
		return fail(fmt.Errorf("%w; reinstall it with guildhall release install %s --from <file> --sha256 %s", err, latest.PreviousVersion, latest.PreviousSHA256))
	}
	if r.SHA256 != latest.PreviousSHA256 {
		return fail(fmt.Errorf("release %s is installed with sha256 %s, not the %s copy %s was pinned to; a release directory is never changed, so remove it and reinstall the pinned binary", r.Version, r.SHA256, latest.PreviousSHA256, dir))
	}
	ch, err := a.setPin(h, dir, storePath, r, store.PinRollback)
	if err != nil {
		return fail(err)
	}
	code := a.printPin(dir, ch)
	if ch.Changed {
		a.pruneAfterPin(h, storePath)
	}
	return code
}
