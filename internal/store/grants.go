package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var bucketTaskGrants = []byte("task-grants")

// GrantAllSteps is a task grant's step when it covers every step of the task's workflow whose profile runs the
// claude adapter.
const GrantAllSteps = "all"

// GrantUntil is how long an approved task grant lasts.
type GrantUntil string

const (
	// GrantUntilAttempt lasts for the first attempt of its step bound after approval, until that attempt has an
	// outcome.
	GrantUntilAttempt GrantUntil = "attempt"
	// GrantUntilDone lasts until the task is merged or closed.
	GrantUntilDone GrantUntil = "done"
)

// GrantState is a task grant's state, derived from its record, its decision, its task and its bound attempts; it
// is never written (docs/decisions/task-grants.md).
type GrantState string

const (
	GrantPending  GrantState = "pending"
	GrantRejected GrantState = "rejected"
	GrantRevoked  GrantState = "revoked"
	GrantActive   GrantState = "active"
	GrantExpired  GrantState = "expired"
)

// GrantApprove is the option of a grant decision that activates the grant; GrantReject is the other.
const (
	GrantApprove = "approve"
	GrantReject  = "reject"
)

// TaskGrant is the Guildmaster's temporary grant of extra allowed_tools rules to one task's Artisans, for one step
// of its workflow or all of them (docs/decisions/task-grants.md). It is recorded with its decision,
// grant-<id>, and takes effect only once that decision is answered approve. It holds rules and nothing else: no
// forge rights, merge powers, deny-rule removal or settings.
type TaskGrant struct {
	Repository string `json:"repository"`
	TaskID     string `json:"task_id"`
	// ID is g<n>, numbered from 1 per task.
	ID string `json:"id"`
	// Step is one step id of the task's workflow, or GrantAllSteps.
	Step   string     `json:"step"`
	Rules  []string   `json:"rules"`
	Reason string     `json:"reason"`
	Until  GrantUntil `json:"until"`
	// Decision is the id of the decision that approves the grant, grant-<id>; the store sets it.
	Decision string `json:"decision"`
	// Revoked is set once, by RevokeTaskGrant.
	Revoked *GrantRevoke `json:"revoked,omitempty"`
	// Bound are the attempts whose binding recorded the grant, in the order they were bound (BindTaskGrant).
	Bound []GrantUse `json:"bound,omitempty"`
}

// GrantRevoke is when and why a task grant was revoked.
type GrantRevoke struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// GrantUse is one attempt that was bound with a task grant.
type GrantUse struct {
	Step    string `json:"step"`
	Attempt int    `json:"attempt"`
}

// BoundGrant is the task grant an attempt's binding records: the grant's id and the rules the attempt was given.
type BoundGrant struct {
	ID    string   `json:"id"`
	Rules []string `json:"rules"`
}

// TaskGrantStatus is a task grant with its derived state and the time its decision was answered approve, if it was.
type TaskGrantStatus struct {
	TaskGrant
	State      GrantState `json:"state"`
	ApprovedAt *time.Time `json:"approved_at,omitempty"`
}

var grantIDPattern = regexp.MustCompile(`^g[1-9][0-9]{0,8}$`)

// TaskGrantID is the id of a task's nth grant, g<n>.
func TaskGrantID(n int) string { return "g" + strconv.Itoa(n) }

// GrantDecision is the id of the decision that approves grant id, grant-<id>.
func GrantDecision(id string) string { return "grant-" + id }

// taskGrantRecord is the receipt record name of creating, binding and revoking grant id.
func taskGrantRecord(id string) string { return "task-grant/" + id }

// grantKey zero-pads the grant's number so a task's grants sort numerically.
func grantKey(taskID, id string) []byte {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "g"))
	return recordKey(taskID, fmt.Sprintf("%010d", n))
}

// refusedGrantCommands are the commands a task grant may never allow, whatever their arguments: each would let an
// Artisan push, merge or answer its own decisions. git is refused too without a subcommand or with push.
var refusedGrantCommands = []string{"guildhall", "gh", "glab"}

// grantWrappers are well-known commands that run the command after them, so a rule naming one is judged by the
// command it wraps: Bash(env gh:*) as Bash(gh:*). This is a speed bump, not a boundary, as a granted shell or
// interpreter can run anything (docs/decisions/task-grants.md).
var grantWrappers = []string{"env", "command", "builtin", "exec", "sudo", "doas", "nice", "nohup", "timeout", "time", "xargs", "stdbuf", "setsid"}

// grantCommand is the command a rule's field names: its base name, lowercase and without .exe.
func grantCommand(field string) string {
	command := path.Base(strings.ReplaceAll(field, `\`, "/"))
	return strings.TrimSuffix(strings.ToLower(command), ".exe")
}

// CheckTaskGrantRule reports why rule cannot be granted to a task, or nil if it can: config.CheckAllowedTool's
// rules, and never a Bash rule for guildhall, gh or glab, or for git without a subcommand or with push, whether
// named after a wrapper such as env or sudo or by a wildcard command, such as g*, that could complete to one.
func CheckTaskGrantRule(rule string) error {
	if err := config.CheckAllowedTool(rule); err != nil {
		return err
	}
	spec, ok := strings.CutPrefix(rule, "Bash(")
	if !ok {
		return nil
	}
	spec = strings.TrimSuffix(spec, ")")
	// A command prefix rule ends in :*, and a wildcard one in * after a space; either names the same command.
	fields := strings.FieldsFunc(spec, func(r rune) bool { return r == ' ' || r == '\t' || r == ':' })
	fields = slices.DeleteFunc(fields, func(f string) bool { return strings.Trim(f, "*") == "" })
	// Skip VAR=value assignments and wrappers, with a wrapper's options and numeric arguments (nice -n 10, timeout 5).
	wrapped := false
	for len(fields) > 0 {
		f := fields[0]
		wrapper := slices.Contains(grantWrappers, grantCommand(f))
		wrapperArg := wrapped && (strings.HasPrefix(f, "-") || f[0] >= '0' && f[0] <= '9')
		if !strings.Contains(f, "=") && !wrapper && !wrapperArg {
			break
		}
		wrapped = wrapped || wrapper
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return nil
	}
	command := grantCommand(fields[0])
	// A wildcard in the command could complete to a refused command, or to git and so to git push.
	if strings.Contains(command, "*") {
		for _, refused := range append(slices.Clone(refusedGrantCommands), "git") {
			if ok, _ := path.Match(command, refused); ok {
				return fmt.Errorf("%q could run %s, which can push, merge or answer decisions; a task grant never allows it", rule, refused)
			}
		}
		return nil
	}
	if slices.Contains(refusedGrantCommands, command) {
		return fmt.Errorf("%q would let an Artisan run %s, which can push, merge or answer decisions; a task grant never allows it", rule, command)
	}
	if command != "git" {
		return nil
	}
	if len(fields) == 1 {
		return fmt.Errorf("%q allows every git command, push among them; name the git subcommands, such as Bash(git status:*)", rule)
	}
	// A subcommand after an option, or one a wildcard could complete to push, could be push.
	sub := fields[1]
	stem, wild := strings.CutSuffix(sub, "*")
	if sub == "push" || strings.HasPrefix(sub, "-") || wild && strings.HasPrefix("push", stem) {
		return fmt.Errorf("%q could run git push; a task grant never allows it, as the publish stage pushes", rule)
	}
	return nil
}

// Covers reports whether the grant applies to attempts of step.
func (g TaskGrant) Covers(step string) bool { return g.Step == GrantAllSteps || g.Step == step }

// State derives the grant's state from its decision, nil when it cannot be found, its task and the task's attempts:
// revoked once revoked, pending until its decision is answered, rejected when the answer is not approve, expired
// once its task is closed or, for GrantUntilAttempt, once its bound attempt has an outcome, and otherwise active.
func (g TaskGrant) State(d *Decision, t Task, attempts []StageAttempt) GrantState {
	switch {
	case g.Revoked != nil:
		return GrantRevoked
	case d == nil || !d.Answered:
		return GrantPending
	case d.Choice != GrantApprove:
		return GrantRejected
	case t.State != TaskOpen:
		return GrantExpired
	case g.Until == GrantUntilAttempt && len(g.Bound) > 0:
		use := g.Bound[0]
		for _, a := range attempts {
			if a.Repository == g.Repository && a.TaskID == g.TaskID && a.Step == use.Step && a.Number == use.Attempt && a.Outcome != "" {
				return GrantExpired
			}
		}
	}
	return GrantActive
}

// open reports whether a grant in state counts against another for the same step: pending or active.
func (s GrantState) open() bool { return s == GrantPending || s == GrantActive }

// grantQuestion is the question of grant g's decision.
func grantQuestion(g TaskGrant) string {
	step := "step " + g.Step
	if g.Step == GrantAllSteps {
		step = "every claude step"
	}
	until := "the first attempt of the step bound after approval has an outcome"
	if g.Until == GrantUntilDone {
		until = "the task is merged or closed"
	}
	return fmt.Sprintf("Grant task %s/%s, %s, these extra allowed tools: %s, until %s? Reason: %s\nAnswer approve or reject.",
		g.Repository, g.TaskID, step, strings.Join(g.Rules, ", "), until, g.Reason)
}

func validateTaskGrant(g TaskGrant) error {
	if err := validateIDs(g.Repository, g.TaskID); err != nil {
		return err
	}
	switch {
	case !grantIDPattern.MatchString(g.ID):
		return invalid("grant id %q must be g<n>", g.ID)
	case g.Step != GrantAllSteps && !workflow.ValidID(g.Step):
		return invalid("grant step %q must be a step id or %q", g.Step, GrantAllSteps)
	case g.Until != GrantUntilAttempt && g.Until != GrantUntilDone:
		return invalid("grant until %q must be %q or %q", g.Until, GrantUntilAttempt, GrantUntilDone)
	case g.Step == GrantAllSteps && g.Until != GrantUntilDone:
		return invalid("a grant for every step lasts until the task is done; pass until %q", GrantUntilDone)
	case len(g.Rules) == 0:
		return invalid("a grant needs at least one rule")
	case g.Decision != "" || g.Revoked != nil || len(g.Bound) > 0:
		return invalid("a new grant has no decision, revoke or bound attempts")
	}
	for i, rule := range g.Rules {
		if err := CheckTaskGrantRule(rule); err != nil {
			return invalid("%v", err)
		}
		if slices.Contains(g.Rules[:i], rule) {
			return invalid("grant lists %q twice", rule)
		}
	}
	return validateText("reason", g.Reason, MaxTextBytes)
}

// CreateTaskGrant records grant g on an open task, receipt record task-grant/<id>, and in the same transaction opens
// its decision grant-<id>, of kind grant with the options approve and reject. steps are the task's workflow steps a
// grant may name, those whose profile runs the claude adapter; they are not part of the request. It refuses with
// ErrInvalid an invalid id, step, until or reason, a rule CheckTaskGrantRule refuses or a repeated one, a step
// outside steps, a grant for every step that does not last until done, and with ErrExists a grant id already used
// or a pending or active grant of the task for an overlapping step, which it names.
func (s *Store) CreateTaskGrant(mutationID string, g TaskGrant, steps []string) (Receipt, error) {
	if err := validateTaskGrant(g); err != nil {
		return Receipt{}, err
	}
	switch {
	case g.Step == GrantAllSteps && len(steps) == 0:
		return Receipt{}, invalid("task %s/%s has no step whose profile runs the claude adapter", g.Repository, g.TaskID)
	case g.Step != GrantAllSteps && !slices.Contains(steps, g.Step):
		return Receipt{}, invalid("step %q is not a step of the task's workflow whose profile runs the claude adapter; those are %q", g.Step, steps)
	}
	g.Decision = GrantDecision(g.ID)
	return s.mutate(mutationID, "create-task-grant", g, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = g.Repository, g.TaskID, taskGrantRecord(g.ID)
		t, err := openTask(tx, g.Repository, g.TaskID)
		if err != nil {
			return err
		}
		statuses, err := taskGrantStatuses(tx, t)
		if err != nil {
			return err
		}
		for _, other := range statuses {
			switch {
			case other.ID == g.ID:
				return fmt.Errorf("%w: grant %s of %s/%s", ErrExists, g.ID, g.Repository, g.TaskID)
			case other.State.open() && (other.Covers(g.Step) || g.Covers(other.Step)):
				return fmt.Errorf("%w: grant %s of %s/%s for step %s is %s; revoke it first", ErrExists, other.ID, g.Repository, g.TaskID, other.Step, other.State)
			}
		}
		grants, err := createRepoBucket(tx, g.Repository, bucketTaskGrants)
		if err != nil {
			return err
		}
		if err := putJSON(grants, grantKey(g.TaskID, g.ID), g); err != nil {
			return err
		}
		d := Decision{Repository: g.Repository, TaskID: g.TaskID, ID: g.Decision, Kind: DecisionGrant, Options: []string{GrantApprove, GrantReject}, Question: grantQuestion(g)}
		if err := validateDecision(d); err != nil {
			return err
		}
		decisions, err := createRepoBucket(tx, g.Repository, bucketDecisions)
		if err != nil {
			return err
		}
		key := recordKey(g.TaskID, d.ID)
		if decisions.Get(key) != nil {
			return fmt.Errorf("%w: decision %q of %s/%s", ErrExists, d.ID, g.Repository, g.TaskID)
		}
		return putJSON(decisions, key, d)
	})
}

// RevokeTaskGrant records the revoke of grant id, pending or active, with the Director's reason and the store's
// time, receipt record task-grant/<id>. Its decision stays as it is: approving a revoked grant activates nothing.
// An unknown grant is ErrNotFound and one already revoked ErrInvalid.
func (s *Store) RevokeTaskGrant(mutationID, repository, taskID, id, reason string) (Receipt, error) {
	if err := validateIDs(repository, taskID); err != nil {
		return Receipt{}, err
	}
	if !grantIDPattern.MatchString(id) {
		return Receipt{}, invalid("grant id %q must be g<n>", id)
	}
	if err := validateText("reason", reason, MaxTextBytes); err != nil {
		return Receipt{}, err
	}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		ID         string `json:"id"`
		Reason     string `json:"reason"`
	}{repository, taskID, id, reason}
	return s.mutate(mutationID, "revoke-task-grant", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Record = repository, taskID, taskGrantRecord(id)
		b := repoBucket(tx, repository, bucketTaskGrants)
		var g TaskGrant
		if err := getJSON(b, grantKey(taskID, id), &g); err != nil {
			return fmt.Errorf("grant %s of %s/%s: %w", id, repository, taskID, err)
		}
		if g.Revoked != nil {
			return invalid("grant %s of %s/%s is already revoked", id, repository, taskID)
		}
		g.Revoked = &GrantRevoke{At: s.now().UTC(), Reason: reason}
		return putJSON(b, grantKey(taskID, id), g)
	})
}

// BindTaskGrant records that attempt number of step was bound with grant id, receipt record task-grant/<id>. The
// grant must be active and cover step, and a GrantUntilAttempt grant already bound to another attempt is refused,
// all with ErrInvalid. Binding an attempt already recorded writes the grant unchanged.
func (s *Store) BindTaskGrant(mutationID, repository, taskID, id, step string, number int) (Receipt, error) {
	if err := validateIDs(repository, taskID, step); err != nil {
		return Receipt{}, err
	}
	if !grantIDPattern.MatchString(id) || number < 1 {
		return Receipt{}, invalid("grant id %q must be g<n> and attempt number %d positive", id, number)
	}
	use := GrantUse{Step: step, Attempt: number}
	req := struct {
		Repository string `json:"repository"`
		TaskID     string `json:"task_id"`
		ID         string `json:"id"`
		GrantUse
	}{repository, taskID, id, use}
	return s.mutate(mutationID, "bind-task-grant", req, func(tx *bolt.Tx, rc *Receipt) error {
		rc.Repository, rc.TaskID, rc.Attempt, rc.Record = repository, taskID, number, taskGrantRecord(id)
		t, err := attemptTask(tx, repository, taskID, step)
		if err != nil {
			return err
		}
		b := repoBucket(tx, repository, bucketTaskGrants)
		var g TaskGrant
		if err := getJSON(b, grantKey(taskID, id), &g); err != nil {
			return fmt.Errorf("grant %s of %s/%s: %w", id, repository, taskID, err)
		}
		st, err := taskGrantStatus(tx, t, g)
		if err != nil {
			return err
		}
		switch {
		case slices.Contains(g.Bound, use):
			return putJSON(b, grantKey(taskID, id), g)
		case !g.Covers(step):
			return invalid("grant %s of %s/%s is for step %s, not %s", id, repository, taskID, g.Step, step)
		case st.State != GrantActive:
			return invalid("grant %s of %s/%s is %s, not active", id, repository, taskID, st.State)
		case g.Until == GrantUntilAttempt && len(g.Bound) > 0:
			return invalid("grant %s of %s/%s lasts one attempt and was bound to attempt %d of step %s", id, repository, taskID, g.Bound[0].Attempt, g.Bound[0].Step)
		}
		g.Bound = append(g.Bound, use)
		return putJSON(b, grantKey(taskID, id), g)
	})
}

// TaskGrants lists a task's grants as recorded, in id order; with an empty taskID, every grant of the repository.
func (s *Store) TaskGrants(repository, taskID string) ([]TaskGrant, error) {
	var prefix []byte
	if taskID != "" {
		prefix = recordKey(taskID, "")
	}
	return list[TaskGrant](s, repository, bucketTaskGrants, prefix)
}

// TaskGrantStatuses lists grants with their derived states and approval times, read in one transaction without
// writing: one task's, one repository's when taskID is empty, or every repository's when repository is empty too.
func (s *Store) TaskGrantStatuses(repository, taskID string) ([]TaskGrantStatus, error) {
	out := []TaskGrantStatus{}
	err := s.view(func(tx *bolt.Tx) error {
		var tasks []Task
		switch {
		case taskID != "":
			t, err := getTask(tx, repository, taskID)
			if err != nil {
				return err
			}
			tasks = []Task{t}
		default:
			repos := []string{repository}
			if repository == "" {
				repos = nil
				_ = tx.Bucket(bucketRepos).ForEachBucket(func(k []byte) error { repos = append(repos, string(k)); return nil })
			}
			for _, repo := range repos {
				if err := scan(repoBucket(tx, repo, bucketTasks), nil, &tasks); err != nil {
					return err
				}
			}
		}
		var all []TaskGrantStatus
		for _, t := range tasks {
			statuses, err := taskGrantStatuses(tx, t)
			if err != nil {
				return err
			}
			all = append(all, statuses...)
		}
		if len(all) == 0 {
			return nil
		}
		approved, err := grantApprovals(tx)
		if err != nil {
			return err
		}
		for i := range all {
			if all[i].State != GrantPending && all[i].State != GrantRejected {
				if at, ok := approved[all[i].Repository+"/"+all[i].TaskID+"/"+all[i].Decision]; ok {
					all[i].ApprovedAt = &at
				}
			}
		}
		out = append(out, all...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: task grants: %w", err)
	}
	return out, nil
}

// taskGrantStatuses reads task t's grants and their states, without approval times.
func taskGrantStatuses(tx *bolt.Tx, t Task) ([]TaskGrantStatus, error) {
	var grants []TaskGrant
	if err := scan(repoBucket(tx, t.Repository, bucketTaskGrants), recordKey(t.ID, ""), &grants); err != nil {
		return nil, err
	}
	var out []TaskGrantStatus
	for _, g := range grants {
		st, err := taskGrantStatus(tx, t, g)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// taskGrantStatus derives grant g's state from its decision, its task t and t's attempts.
func taskGrantStatus(tx *bolt.Tx, t Task, g TaskGrant) (TaskGrantStatus, error) {
	var d Decision
	dp := &d
	if err := getJSON(repoBucket(tx, t.Repository, bucketDecisions), recordKey(t.ID, g.Decision), &d); errors.Is(err, ErrNotFound) {
		dp = nil
	} else if err != nil {
		return TaskGrantStatus{}, err
	}
	var attempts []StageAttempt
	if g.Until == GrantUntilAttempt && len(g.Bound) > 0 {
		if err := scan(repoBucket(tx, t.Repository, bucketAttempts), recordKey(t.ID, g.Bound[0].Step, ""), &attempts); err != nil {
			return TaskGrantStatus{}, err
		}
	}
	return TaskGrantStatus{TaskGrant: g, State: g.State(dp, t, attempts)}, nil
}

// grantApprovals maps <repository>/<task>/<decision> to the commit time of each grant decision's answer.
func grantApprovals(tx *bolt.Tx) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := tx.Bucket(bucketReceipts).ForEach(func(k, raw []byte) error {
		var r Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("receipt %q: %w", k, err)
		}
		if r.Operation == "answer-decision" && strings.HasPrefix(r.Record, "decision/grant-") {
			out[r.Repository+"/"+r.TaskID+"/"+strings.TrimPrefix(r.Record, "decision/")] = r.CommittedAt
		}
		return nil
	})
	return out, err
}
