package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// releaseKind is the needs_me kind of an open task whose recorded release is missing or damaged, which refuses to
// advance until that release is installed (docs/releases.md#tasks-keep-their-release).
const releaseKind = "release"

// taskStoreCopy is the copy whose store is at storePath when copyDir, the copy the command selected, is "": the
// store's own directory when it is a copy's director.store, as the PATH launcher takes it; else "".
func taskStoreCopy(copyDir, storePath string) string {
	if copyDir != "" {
		abs, err := filepath.Abs(copyDir)
		if err != nil {
			return copyDir
		}
		return abs
	}
	abs, err := filepath.Abs(storePath)
	if err != nil || filepath.Base(abs) != directorRole.storeFile || !copydir.IsCopy(filepath.Dir(abs)) {
		return ""
	}
	return filepath.Dir(abs)
}

// newTaskRelease is the release a task created now in the store at storePath records: on a copy that pins one,
// the pin's version and sha256, read under the Guildhall home's lock, which it returns held, with the copy
// registered in the home's inventory, so no pin change or prune runs before the task is recorded; on a copy with
// no pin, nil, and the task records none, as before releases. A pin that cannot be read, or whose release is
// missing or damaged, refuses the task.
func (a *App) newTaskRelease(copyDir, storePath string) (*store.TaskRelease, func() error, error) {
	noop := func() error { return nil }
	dir := taskStoreCopy(copyDir, storePath)
	if dir == "" || !release.HasPin(dir) {
		return nil, noop, nil
	}
	h, err := a.releaseHome()
	if err != nil {
		return nil, noop, err
	}
	unlock, err := h.Lock(releaseLockWait)
	if err != nil {
		return nil, noop, err
	}
	refuse := func(err error) (*store.TaskRelease, func() error, error) {
		_ = unlock()
		return nil, noop, err
	}
	if err := h.RegisterLocked(dir); err != nil {
		return refuse(fmt.Errorf("register copy %s in %s: %w", dir, h.CopiesFile(), err))
	}
	pin, err := release.ReadPin(dir)
	if err != nil {
		return refuse(fmt.Errorf("copy %s: %w; a new task records its copy's pinned release; repair the pin with guildhall release use <version>", dir, err))
	}
	r, err := h.Runnable(pin)
	if err != nil {
		return refuse(fmt.Errorf("copy %s pins %s, and a new task records and runs on its copy's pin: %w; install it with guildhall release install %s --from <file> --sha256 <hex>", dir, pin, err, pin))
	}
	return &store.TaskRelease{Version: r.Version, SHA256: r.SHA256}, unlock, nil
}

// activeTaskRelease is an open task's recorded release.
type activeTaskRelease struct {
	store.TaskRelease
	Task store.Task
}

// activeTaskReleases is every recorded release of a task in s that is neither done nor cancelled, and the open
// tasks that recorded none, by repository and id.
func activeTaskReleases(s *store.Store) (recorded []activeTaskRelease, unrecorded []store.Task, err error) {
	rels, err := s.TaskReleases()
	if err != nil {
		return nil, nil, err
	}
	byTask := map[[2]string]store.TaskRelease{}
	for _, r := range rels {
		byTask[[2]string{r.Repository, r.TaskID}] = r
	}
	tasks, err := listTasks(s, "")
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Repository != tasks[j].Repository {
			return tasks[i].Repository < tasks[j].Repository
		}
		return tasks[i].ID < tasks[j].ID
	})
	for _, t := range tasks {
		if t.State == store.TaskDone || t.State == store.TaskCancelled {
			continue
		}
		if r, ok := byTask[[2]string{t.Repository, t.ID}]; ok {
			recorded = append(recorded, activeTaskRelease{TaskRelease: r, Task: t})
		} else {
			unrecorded = append(unrecorded, t)
		}
	}
	return recorded, unrecorded, nil
}

// checkStoreSchema refuses moving the pin of the copy whose store is s to target while an active task records a
// release with another store schema, or one whose schema cannot be read, naming those tasks: no release migrates
// the store under a task still running an older one, so the Guildmaster lets them finish first
// (docs/releases.md#tasks-keep-their-release).
func checkStoreSchema(h release.Home, s *store.Store, target release.Release) error {
	recorded, _, err := activeTaskReleases(s)
	if err != nil {
		return err
	}
	var blocking []string
	for _, r := range recorded {
		if r.Version == target.Version {
			continue
		}
		rel, err := h.Check(r.Version)
		switch {
		case err != nil || rel.Damage != "":
			blocking = append(blocking, fmt.Sprintf("%s/%s on %s, whose store schema cannot be read", r.Repository, r.TaskID, r.Version))
		case rel.StoreSchema != target.StoreSchema:
			blocking = append(blocking, fmt.Sprintf("%s/%s on %s (store schema %d)", r.Repository, r.TaskID, r.Version, rel.StoreSchema))
		}
	}
	if len(blocking) == 0 {
		return nil
	}
	return fmt.Errorf("release %s has store schema %d, and active tasks run on releases with another: %s; no release migrates the store under a task still running an older one, so let those tasks finish, or cancel them, first",
		target.Version, target.StoreSchema, strings.Join(blocking, ", "))
}

// taskReleaseItems is a release needs_me item for each active task in the store at storePath whose recorded
// release is missing, damaged or another binary in the Guildhall home: such a task refuses to advance, never moving
// to another release, until that release is installed. A store or home that cannot be read gives none, as the
// launcher's refusal still names the version.
func (a *App) taskReleaseItems(storePath string) []needsMeItem {
	if _, err := os.Stat(storePath); err != nil {
		return nil
	}
	recorded, err := withStore(storePath, true, func(s *store.Store) ([]activeTaskRelease, error) {
		r, _, err := activeTaskReleases(s)
		return r, err
	})
	if err != nil || len(recorded) == 0 {
		return nil
	}
	h, err := a.releaseHome()
	if err != nil {
		return nil
	}
	var items []needsMeItem
	for _, r := range recorded {
		if why := taskReleaseProblem(h, r.TaskRelease); why != "" {
			items = append(items, needsMeItem{Kind: releaseKind, Repository: r.Repository, TaskID: r.TaskID,
				Reason:  fmt.Sprintf("task %s/%s records release %s, which %s, so it refuses to advance and never moves to another release; ask the Guildmaster to install it", r.Repository, r.TaskID, r.Version, why),
				Command: fmt.Sprintf("guildhall release install %s --from <file> --sha256 %s", r.Version, r.SHA256)})
		}
	}
	return items
}

// otherTaskRelease is the release task t recorded in the store at storePath, with other true when it is not this
// binary's own release, so the task must advance on it rather than in this process.
func otherTaskRelease(storePath string, wait time.Duration, t taskRef) (store.TaskRelease, bool, error) {
	rec, err := withRecords(storeRecords{path: storePath, wait: wait}, true, nil, func(s *store.Store) (store.TaskRelease, error) {
		return s.TaskRelease(t.repo, t.task)
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.TaskRelease{}, false, nil
	case err != nil:
		return store.TaskRelease{}, false, err
	}
	return rec, rec.Version != Version(), nil
}

// advanceThroughLauncher advances task t as `guildhall task start` does, on the release rec it recorded, by running
// the Guildhall home's PATH launcher with f's copy selection, which runs it there and refuses a missing or damaged
// one; the launcher's standard error goes to stderr, and the next.json it prints is returned.
func (a *App) advanceThroughLauncher(f advanceFlags, t taskRef, rec store.TaskRelease, stderr io.Writer) (interp.Next, error) {
	h, err := a.releaseHome()
	if err != nil {
		return interp.Next{}, err
	}
	launcher := h.Launcher()
	if _, err := os.Stat(launcher); err != nil {
		return interp.Next{}, fmt.Errorf("task %s/%s records release %s, but the Guildhall home has no launcher to run it: %w", t.repo, t.task, rec.Version, err)
	}
	args := []string{"task", "start"}
	if f.copy.dir != "" {
		args = append(args, "--copy", f.copy.dir)
	} else {
		args = append(args, "--store", f.store, "--config", f.config, "--state", f.state)
	}
	args = append(args, "--repo", t.repo, t.task)
	cmd := exec.Command(launcher, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return interp.Next{}, fmt.Errorf("advance on release %s: %s %s: %w", rec.Version, launcher, strings.Join(args, " "), err)
	}
	var n next
	if err := json.Unmarshal(out.Bytes(), &n); err != nil {
		return interp.Next{}, fmt.Errorf("advance on release %s printed %q, not its next.json: %w", rec.Version, out.String(), err)
	}
	return interp.Next{Wait: n.Wait, Step: n.Step, Attempt: n.Attempt, Decision: n.Decision, Position: n.Position}, nil
}

// taskReleaseProblem says why the task release r cannot run from h, "" when it can.
func taskReleaseProblem(h release.Home, r store.TaskRelease) string {
	rel, err := h.Check(r.Version)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "is not installed at " + h.ReleaseDir(r.Version)
	case err != nil:
		return "cannot be read: " + err.Error()
	case rel.Damage != "":
		return "is damaged: " + rel.Damage
	case rel.SHA256 != r.SHA256:
		return fmt.Sprintf("is installed with sha256 %s, not the %s it recorded", rel.SHA256, r.SHA256)
	}
	return ""
}
