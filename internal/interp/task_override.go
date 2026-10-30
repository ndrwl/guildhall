package interp

import (
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/store"
)

// TaskOverrides are one task's overrides as an advance applied them (docs/tasks.md#task-overrides): Config is the
// task's effective config, the base config with the overrides in effect applied, Active the records in effect, by
// key, and Skipped those that did not apply, with the reason. The task's merge-requires overrides are not in Config:
// run.repository applies them to the task's repository alone.
type TaskOverrides struct {
	Repository, TaskID string
	Config             *config.Config
	Active             []store.TaskOverride
	Skipped            []config.Skipped
}

// ApplyTaskOverrides reads the task's override records and applies those in effect to base
// (config.WithTaskOverrides). An override that does not apply is skipped, never an error: only reading the records
// or an invalid base fails. A task with no overrides gets a copy of base that reads the same.
func ApplyTaskOverrides(records Records, base *config.Config, repository, taskID string) (TaskOverrides, error) {
	all, err := records.TaskOverrides(repository, taskID)
	if err != nil {
		return TaskOverrides{}, err
	}
	active := store.ActiveTaskOverrides(all)
	cfg, skipped, _, err := EffectiveOverrides(base, repository, active)
	if err != nil {
		return TaskOverrides{}, err
	}
	return TaskOverrides{Repository: repository, TaskID: taskID, Config: cfg, Active: active, Skipped: skipped}, nil
}

// EffectiveOverrides applies a task's overrides in effect to base: it returns the task's effective config, the
// overrides that do not apply, with why, in key order, and the merge requirements the overrides add to those of
// repository (config.MergeRequires.WithTaskOverrides), in documented order.
func EffectiveOverrides(base *config.Config, repository string, active []store.TaskOverride) (*config.Config, []config.Skipped, []string, error) {
	values := overrideValues(active)
	cfg, skipped, err := base.WithTaskOverrides(values)
	if err != nil {
		return nil, nil, nil, err
	}
	var added []string
	if i := slices.IndexFunc(base.Repositories, func(c config.Repository) bool { return c.ID == repository }); i >= 0 {
		var merge []config.Skipped
		_, added, merge = base.Repositories[i].Merge.Requires.WithTaskOverrides(values)
		skipped = append(skipped, merge...)
		slices.SortFunc(skipped, func(a, b config.Skipped) int { return strings.Compare(a.Key, b.Key) })
	}
	return cfg, skipped, added, nil
}

func overrideValues(active []store.TaskOverride) map[string]string {
	values := map[string]string{}
	for _, o := range active {
		values[o.Key] = o.Value
	}
	return values
}

// taskOverride is why a merge requirement the repository's merge_requires does not ask for is required when the
// task's override adds it (conditions).
const taskOverride = "this task's override"

// mergeRequires returns base, the requirements of the task's repository, with the task's merge-requires overrides
// applied, and the requirements they added. It builds a fresh MergeRequires, so the shared config, which other tasks
// advanced in the same process read, is never written through. An engine given another task's overrides, or none,
// leaves base as it is.
func (r *run) mergeRequires(base config.MergeRequires) (config.MergeRequires, []string) {
	o := r.e.Overrides
	if o == nil || o.Repository != r.task.Repository || o.TaskID != r.task.ID {
		return base, nil
	}
	requires, added, _ := base.WithTaskOverrides(overrideValues(o.Active))
	return requires, added
}

// addedRequires lists the merge requirements the task's overrides add to its repository's merge_requires.
func (r *run) addedRequires() []string {
	i := slices.IndexFunc(r.e.Config.Repositories, func(c config.Repository) bool { return c.ID == r.task.Repository })
	if i < 0 {
		return nil
	}
	_, added := r.mergeRequires(r.e.Config.Repositories[i].Merge.Requires)
	return added
}

// boundOverride is a task override in effect as an attempt was bound: its key, value and record number.
type boundOverride struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Number int    `json:"number"`
}

// bindOverrides records in b the task's overrides the engine was given, when they are this task's: those applied
// and those skipped. A task with none leaves b as it was, so its binding reads as it did before overrides existed.
func (r *run) bindOverrides(b *binding) {
	o := r.e.Overrides
	if o == nil || o.Repository != r.task.Repository || o.TaskID != r.task.ID {
		return
	}
	for _, a := range o.Active {
		if slices.ContainsFunc(o.Skipped, func(s config.Skipped) bool { return s.Key == a.Key }) {
			continue
		}
		b.Overrides = append(b.Overrides, boundOverride{Key: a.Key, Value: a.Value, Number: a.Number})
	}
	b.SkippedOverrides = slices.Clone(o.Skipped)
}
