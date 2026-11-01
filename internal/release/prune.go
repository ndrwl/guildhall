package release

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Keep is how many installed releases, newest install first, prune always keeps; retention is fixed, not a setting
// (docs/releases.md#pruning).
const Keep = 3

// IncomingStaleAfter is how long a copy under .incoming/ is left before prune removes it as stale: an install in
// flight, or a binary a rollout copied there for config reload to install, is far younger.
const IncomingStaleAfter = 24 * time.Hour

// prunePrefix names the directory a release is moved to under .incoming/ while prune removes it, so a release
// directory vanishes in one rename rather than lingering half removed, looking damaged.
const prunePrefix = ".prune-"

// PruneItem is one installed release in a prune plan.
type PruneItem struct {
	Release
	// Remove is set for a release prune removes, with Reason why it is eligible.
	Remove bool `json:"remove"`
	// Reason is why prune removes a release, or, with Left set, why it leaves an entry alone.
	Reason string `json:"reason,omitempty"`
	// Left is set for an entry under releases/ whose name is not a release version: it is never a release, so
	// prune neither counts it among the newest Keep nor removes it (docs/releases.md#pruning).
	Left bool `json:"left,omitempty"`
	// Held are what keep a release past the newest Keep: the copies pinning it or rolling back to it and the active
	// tasks recording it. Empty for a release within the newest Keep or one prune removes.
	Held []string `json:"held"`
}

// NotARelease is the Reason prune gives for leaving an entry under releases/ whose name is not a release version.
const NotARelease = "not a release directory; prune leaves it"

// PlanPrune decides, for releases as List orders them, newest install first, which prune keeps and which it
// removes: the newest Keep are kept, and an older one is kept only while held names what holds it, by version.
// An entry whose name is not a release version is left, neither counted among the newest Keep nor removed, as
// prune removes only releases/<version>/ directories.
func PlanPrune(releases []Release, held map[string][]string) []PruneItem {
	out := make([]PruneItem, 0, len(releases))
	n := 0
	for _, r := range releases {
		it := PruneItem{Release: r, Held: []string{}}
		if ValidVersion(r.Version) != nil {
			it.Left, it.Reason = true, NotARelease
			out = append(out, it)
			continue
		}
		n++
		switch {
		case n <= Keep:
		case len(held[r.Version]) > 0:
			it.Held = append(it.Held, held[r.Version]...)
		default:
			it.Remove = true
			it.Reason = fmt.Sprintf("older than the newest %d installed releases, and no inventoried copy pins it, rolls back to it or has an active task on it", Keep)
			if r.Damage != "" {
				it.Reason = "damaged (" + r.Damage + "), " + it.Reason
			}
		}
		out = append(out, it)
	}
	return out
}

// RemoveLocked removes installed release version, for a caller holding the home lock: its directory is first
// renamed under .incoming/, so it leaves releases/ in one step, and then removed.
func (h Home) RemoveLocked(version string) error {
	if err := ValidVersion(version); err != nil {
		return err
	}
	if err := os.MkdirAll(h.IncomingDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(h.IncomingDir(), prunePrefix+version+"-")
	if err != nil {
		return err
	}
	gone := filepath.Join(tmp, version)
	if err := os.Rename(h.ReleaseDir(version), gone); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.RemoveAll(tmp)
}

// PruneIncomingLocked removes, for a caller holding the home lock, each entry under .incoming/ last changed before
// now less IncomingStaleAfter, and any a previous prune left half removed, returning their names. A home with no
// .incoming/ has none.
func (h Home) PruneIncomingLocked(now time.Time) ([]string, error) {
	entries, err := os.ReadDir(h.IncomingDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, e := range entries {
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if !strings.HasPrefix(e.Name(), prunePrefix) && !info.ModTime().Before(now.Add(-IncomingStaleAfter)) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(h.IncomingDir(), e.Name())); err != nil {
			return removed, err
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}
