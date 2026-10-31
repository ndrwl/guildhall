package store

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bucketTaskDependencies = []byte("task_dependencies")

// TaskDependencyAuthor is who records dependency changes: always the Director, on the Guildmaster's word or when it
// replaces a task that has dependents (docs/tasks.md#changing-dependencies).
const TaskDependencyAuthor = "director"

// TaskDependencyOp is what a dependency record does to its dependency: adds it to the task's effective dependencies or
// removes it.
type TaskDependencyOp string

const (
	TaskDependencyAdd    TaskDependencyOp = "add"
	TaskDependencyRemove TaskDependencyOp = "remove"
)

// TaskDependency is one record of a change to a task's dependencies after creation. The Task record's DependsOn never
// changes (R1.62); a task's effective dependencies are its creation list with these records applied in number order
// (EffectiveDependencies). Records are numbered per task from 1 and append-only, adds and removes sharing one
// sequence.
type TaskDependency struct {
	Repository string           `json:"repository"`
	TaskID     string           `json:"task_id"`
	Number     int              `json:"number"`
	Op         TaskDependencyOp `json:"op"`
	Dependency TaskRef          `json:"dependency"`
	// Text is the reason for the change.
	Text   string `json:"text"`
	Author string `json:"author"`
	// AddedAt is the commit time of the record's receipt.
	AddedAt time.Time `json:"added_at"`
}

// taskDependencyRequest is what AddTaskDependency is asked, which a replay must repeat exactly.
type taskDependencyRequest struct {
	Repository string           `json:"repository"`
	TaskID     string           `json:"task_id"`
	Op         TaskDependencyOp `json:"op"`
	Dependency TaskRef          `json:"dependency"`
	Prior      int              `json:"prior"`
	Text       string           `json:"text"`
}

// AddTaskDependency records the task's next dependency record, receipt record task-dependency/<n>, on an open task:
// op add makes dep one of the task's effective dependencies and op remove drops it, a creation-time dependency
// included. prior is how many records the task already has for dep, so a command that did not see the latest one
// cannot stack a record on top of it: another count is refused with ErrExists.
// Blank text or text over MaxTextBytes, a task that is not open, a dep that is the task itself, an add of a cancelled
// dep, an add that would close a cycle of effective dependencies (across repositories) and a remove of a dep that is
// not effective are refused with ErrInvalid; an add of a dep already effective is ErrExists; an unknown task or dep
// is ErrNotFound. A done dep may be added: it is satisfied at once.
func (s *Store) AddTaskDependency(mutationID, repository, taskID string, op TaskDependencyOp, dep TaskRef, prior int, text string) (Receipt, error) {
	if err := validateIDs(repository, taskID, dep.Repository, dep.ID); err != nil {
		return Receipt{}, err
	}
	if op != TaskDependencyAdd && op != TaskDependencyRemove {
		return Receipt{}, invalid("task dependency op %q must be add or remove", op)
	}
	if prior < 0 {
		return Receipt{}, invalid("task dependency prior count %d must not be negative", prior)
	}
	if err := validateText("reason", text, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	self := TaskRef{Repository: repository, ID: taskID}
	if dep == self {
		return Receipt{}, invalid("task %s cannot depend on itself", DependencyName(self))
	}
	req := taskDependencyRequest{Repository: repository, TaskID: taskID, Op: op, Dependency: dep, Prior: prior, Text: text}
	return s.mutate(mutationID, "add-task-dependency", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID = repository, taskID
		t, err := openTask(tx, repository, taskID)
		if err != nil {
			return err
		}
		d, err := getTask(tx, dep.Repository, dep.ID)
		if err != nil {
			return fmt.Errorf("store: dependency: %w", err)
		}
		b, err := createRepoBucket(tx, repository, bucketTaskDependencies)
		if err != nil {
			return err
		}
		var all []TaskDependency
		if err := scan(b, recordKey(taskID, ""), &all); err != nil {
			return err
		}
		mine := 0
		for _, r := range all {
			if r.Dependency == dep {
				mine++
			}
		}
		effective := slices.Contains(EffectiveDependencies(t, all), dep)
		switch {
		case mine != prior:
			return fmt.Errorf("store: %w: task %s has %d dependency records for %s, not %d", ErrExists, DependencyName(self), mine, DependencyName(dep), prior)
		case op == TaskDependencyAdd && effective:
			return fmt.Errorf("store: %w: task %s already depends on %s", ErrExists, DependencyName(self), DependencyName(dep))
		case op == TaskDependencyAdd && d.State == TaskCancelled:
			return invalid("task %s cannot depend on %s: it is cancelled and can never merge", DependencyName(self), DependencyName(dep))
		case op == TaskDependencyRemove && !effective:
			return invalid("task %s does not depend on %s", DependencyName(self), DependencyName(dep))
		}
		if op == TaskDependencyAdd {
			path, err := dependencyPath(tx, dep, self)
			if err != nil {
				return err
			}
			if path != nil {
				names := []string{DependencyName(self)}
				for _, p := range path {
					names = append(names, DependencyName(p))
				}
				return invalid("task %s cannot depend on %s: that would make a cycle %s", DependencyName(self), DependencyName(dep), strings.Join(names, " -> "))
			}
		}
		r := TaskDependency{Repository: repository, TaskID: taskID, Number: len(all) + 1, Op: op, Dependency: dep, Text: text, Author: TaskDependencyAuthor}
		// The record's time is its receipt's, which commit keeps when apply sets it.
		rc.CommittedAt = s.now().UTC()
		r.AddedAt, rc.Record = rc.CommittedAt, TaskDependencyRecord(r.Number)
		return putJSON(b, taskDependencyKey(taskID, r.Number), r)
	})
}

// dependencyPath walks effective dependencies from start, across repositories, and returns the path from start to
// target when the walk reaches it, or nil when it does not.
func dependencyPath(tx *bolt.Tx, start, target TaskRef) ([]TaskRef, error) {
	parent := map[TaskRef]TaskRef{start: start}
	queue := []TaskRef{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == target {
			var path []TaskRef
			for p := cur; ; p = parent[p] {
				path = append(path, p)
				if p == start {
					break
				}
			}
			slices.Reverse(path)
			return path, nil
		}
		deps, err := effectiveDependencies(tx, cur)
		if err != nil {
			return nil, err
		}
		for _, d := range deps {
			if _, seen := parent[d]; !seen {
				parent[d] = cur
				queue = append(queue, d)
			}
		}
	}
	return nil, nil
}

// effectiveDependencies reads a task's effective dependencies inside tx.
func effectiveDependencies(tx *bolt.Tx, ref TaskRef) ([]TaskRef, error) {
	t, err := getTask(tx, ref.Repository, ref.ID)
	if err != nil {
		return nil, err
	}
	var records []TaskDependency
	if err := scan(repoBucket(tx, ref.Repository, bucketTaskDependencies), recordKey(ref.ID, ""), &records); err != nil {
		return nil, err
	}
	return EffectiveDependencies(t, records), nil
}

// TaskDependencies lists a task's dependency records by number.
func (s *Store) TaskDependencies(repository, taskID string) ([]TaskDependency, error) {
	return list[TaskDependency](s, repository, bucketTaskDependencies, recordKey(taskID, ""))
}

// EffectiveDependencies is the one definition of a task's dependencies: its creation list, DependsOn, with each of
// its dependency records, given by number, applied in turn, so the latest record for a dependency wins. Creation-time
// dependencies come first, in creation order, then added ones in the order they were added; duplicates are dropped.
func EffectiveDependencies(task Task, records []TaskDependency) []TaskRef {
	var out []TaskRef
	for _, d := range task.DependsOn {
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	for _, r := range records {
		switch r.Op {
		case TaskDependencyAdd:
			if !slices.Contains(out, r.Dependency) {
				out = append(out, r.Dependency)
			}
		case TaskDependencyRemove:
			out = slices.DeleteFunc(out, func(d TaskRef) bool { return d == r.Dependency })
		}
	}
	return out
}

// DependencyName names a task as <repo>/<task>, the form guildhall task depends and --depends-on take.
func DependencyName(d TaskRef) string { return d.Repository + "/" + d.ID }

// TaskDependencyRecord is the receipt record name of dependency record number.
func TaskDependencyRecord(number int) string { return "task-dependency/" + strconv.Itoa(number) }

// taskDependencyKey zero-pads the number so records sort numerically.
func taskDependencyKey(taskID string, number int) []byte {
	return recordKey(taskID, fmt.Sprintf("%010d", number))
}
