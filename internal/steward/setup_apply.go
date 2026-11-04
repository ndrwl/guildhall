package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// Outcome states of a setup apply.
const (
	OutcomeDone    = "done"
	OutcomeWaiting = "waiting"
	OutcomeFailed  = "failed"
)

// Step record states in the step log.
const (
	StepStarted = "started"
	StepDone    = "done"
	StepWaiting = "waiting"
	StepFailed  = "failed"
)

// Outcome is what one apply of a setup plan came to: done, waiting on a step that the next apply checks again, or
// failed at a step with the diagnosis the Steward gives the Guildmaster.
type Outcome struct {
	State     string   `json:"state"`
	Step      string   `json:"step,omitempty"`
	Diagnosis string   `json:"diagnosis,omitempty"`
	Log       *StepLog `json:"log"`
}

// String is the outcome as one line: done, waiting <step>: <why> or failed <step>: <diagnosis>.
func (o *Outcome) String() string {
	if o.State == OutcomeDone {
		return OutcomeDone
	}
	return fmt.Sprintf("%s %s: %s", o.State, o.Step, o.Diagnosis)
}

// StepLog is steps.json in the plan directory: what each step of one plan did, so a later apply resumes at the first
// step not done and never acts again for a step done.
type StepLog struct {
	Schema     string `json:"schema"`
	PlanDigest string `json:"plan_digest"`
	// PausedByTakeover is set before the pause step acts on a running session, so the resume step resumes only a
	// session the takeover paused.
	PausedByTakeover bool         `json:"paused_by_takeover"`
	Abandoned        bool         `json:"abandoned"`
	Steps            []StepRecord `json:"steps"`
}

// StepRecord is one step's newest run.
type StepRecord struct {
	ID    string    `json:"id"`
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Runs  int       `json:"runs"`
	// Output is what the step's command printed, trimmed; Note what the step found; Error why it failed or waits.
	Output string `json:"output,omitempty"`
	Note   string `json:"note,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Record returns the record of step id, nil when it has not run.
func (l *StepLog) Record(id string) *StepRecord {
	for i := range l.Steps {
		if l.Steps[i].ID == id {
			return &l.Steps[i]
		}
	}
	return nil
}

func (l *StepLog) record(id string) *StepRecord {
	if r := l.Record(id); r != nil {
		return r
	}
	l.Steps = append(l.Steps, StepRecord{ID: id})
	return &l.Steps[len(l.Steps)-1]
}

// started reports whether any step has started.
func (l *StepLog) started() bool { return len(l.Steps) > 0 }

// ReadStepLog reads the step log in planDir; a plan never applied has an empty one.
func ReadStepLog(planDir string) (*StepLog, error) {
	data, err := os.ReadFile(filepath.Join(planDir, setupStepLog))
	if errors.Is(err, os.ErrNotExist) {
		return &StepLog{Schema: StepLogSchema, Steps: []StepRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var l StepLog
	if err := strictjson.Decode(data, &l, strictjson.Options{Schema: stepLogSchemaPrefix, Version: stepLogSchemaVersion}); err != nil {
		return nil, fmt.Errorf("step log: %w", err)
	}
	return &l, nil
}

// writeStepLog replaces steps.json in planDir with l atomically.
func writeStepLog(planDir string, l *StepLog) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(planDir, ".steps-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(planDir, setupStepLog))
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// lock takes .rollout.lock in the private config, which a rollout and a collection take too, and returns its release.
func (s *Setup) lock() (func(), error) {
	release, err := privateLock(s.Private.Dir, s.Now())
	if err != nil && !errors.Is(err, ErrBusy) {
		return nil, fmt.Errorf("setup %w", err)
	}
	return release, err
}

// open reads the plan in planDir and checks that it is the one written there and that its planned files are intact.
func openSetupPlan(planDir string) (*SetupPlan, error) {
	p, err := ReadSetupPlan(planDir)
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(planDir); err != nil || abs != p.PlanDir {
		return nil, fmt.Errorf("%w: the plan was written for %s", ErrPlanChanged, p.PlanDir)
	}
	if p.digest() != p.Digest {
		return nil, fmt.Errorf("%w: plan.json does not have its digest %s", ErrPlanChanged, p.Digest)
	}
	p.files = map[string][]byte{}
	for _, f := range p.Files {
		data, err := os.ReadFile(filepath.Join(planDir, filepath.FromSlash(f.Name)))
		if err != nil || digest.Of(data).Hex() != f.SHA256 {
			return nil, fmt.Errorf("%w: planned file %s changed", ErrPlanChanged, f.Name)
		}
		p.files[f.Name] = data
	}
	return p, nil
}

// ApplySetup carries out the approved setup plan in planDir, where approve must be its exact digest, holding
// .rollout.lock throughout. On the first apply it plans again and refuses with ErrPlanChanged, running no step,
// unless the result has the same digest; a later apply of the same plan checks that the copy's config.toml is
// unchanged and resumes at the first step not done. Each step checks before it acts and verifies after, and its
// record in the step log is written before it acts and after it ends, so a step done never acts again. Re-applying
// a finished plan checks the copy is still managed and the roster holds the entry, and changes nothing.
func (s *Setup) ApplySetup(ctx context.Context, planDir, approve string) (*Outcome, error) {
	p, err := openSetupPlan(planDir)
	if err != nil {
		return nil, err
	}
	if approve != p.Digest {
		return nil, ErrNotApproved
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	log, err := ReadStepLog(p.PlanDir)
	if err != nil {
		return nil, err
	}
	switch {
	case log.PlanDigest != "" && log.PlanDigest != p.Digest:
		return nil, fmt.Errorf("%w: the step log belongs to plan %s", ErrPlanChanged, log.PlanDigest)
	case log.Abandoned:
		return nil, ErrAbandoned
	}
	if !log.started() {
		again, err := s.PlanTakeover(ctx, p.input)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrPlanChanged, err)
		}
		if again.Digest != p.Digest {
			return nil, ErrPlanChanged
		}
	} else {
		local, err := s.fetch(ctx, Remote{p.SSHAlias, p.CopyDir + "/config.toml"})
		if err != nil {
			return nil, fmt.Errorf("reading the copy's config.toml: %w", err)
		}
		if digest.Of(local).Hex() != p.Probe.ConfigSHA256 {
			return nil, fmt.Errorf("%w: the copy's config.toml changed", ErrPlanChanged)
		}
	}
	log.PlanDigest = p.Digest
	out := &Outcome{Log: log}
	for _, st := range p.Steps {
		rec := log.record(st.ID)
		if rec.State == StepDone {
			if diag := s.recheck(ctx, p, st); diag != "" {
				out.State, out.Step, out.Diagnosis = OutcomeFailed, st.ID, diag
				return out, &ErrStepFailed{Step: st.ID, Diagnosis: diag}
			}
			continue
		}
		rec.State, rec.At, rec.Runs, rec.Output, rec.Note, rec.Error = StepStarted, s.Now().UTC(), rec.Runs+1, "", "", ""
		if err := writeStepLog(p.PlanDir, log); err != nil {
			return out, err
		}
		r := s.runStep(ctx, p, st, log)
		rec = log.record(st.ID)
		rec.At, rec.Output, rec.Note, rec.Error = s.Now().UTC(), r.output, r.note, r.diagnosis
		switch {
		case r.waiting:
			rec.State = StepWaiting
			out.State, out.Step, out.Diagnosis = OutcomeWaiting, st.ID, r.diagnosis
		case r.diagnosis != "":
			rec.State = StepFailed
			out.State, out.Step, out.Diagnosis = OutcomeFailed, st.ID, r.diagnosis
		default:
			rec.State = StepDone
		}
		if err := writeStepLog(p.PlanDir, log); err != nil {
			return out, err
		}
		switch out.State {
		case OutcomeWaiting:
			return out, nil
		case OutcomeFailed:
			return out, &ErrStepFailed{Step: st.ID, Diagnosis: r.diagnosis}
		}
	}
	out.State = OutcomeDone
	return out, nil
}

// stepResult is what one run of a step came to.
type stepResult struct {
	output, note, diagnosis string
	waiting                 bool
}

func failed(format string, args ...any) stepResult {
	return stepResult{diagnosis: fmt.Sprintf(format, args...)}
}

// run runs a step's remote command and returns what it printed, trimmed.
func (s *Setup) run(ctx context.Context, p *SetupPlan, argv []string) (string, error) {
	out, err := s.Runner.Run(ctx, p.SSHAlias, argv)
	return strings.TrimSpace(string(out)), err
}

// runStep checks, acts and verifies one step.
func (s *Setup) runStep(ctx context.Context, p *SetupPlan, st SetupStep, log *StepLog) stepResult {
	switch st.ID {
	case StepPause:
		status, err := s.status(ctx, p)
		if err != nil {
			return failed("%v", err)
		}
		switch status.Session.State {
		case "paused":
			if log.PausedByTakeover {
				return stepResult{note: "paused by this takeover"}
			}
			return stepResult{note: "the session was already paused, and the takeover leaves it paused"}
		case "no session", "stopped":
			return stepResult{note: "no live session (" + status.Session.State + "), so there is nothing to pause or resume"}
		case "running":
		default:
			return failed("the session is %s; wait until it is running, paused or stopped, then apply again", status.Session.State)
		}
		log.PausedByTakeover = true
		if err := writeStepLog(p.PlanDir, log); err != nil {
			return failed("%v", err)
		}
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			return failed("director pause: %v", err)
		}
		if status, err = s.status(ctx, p); err != nil {
			return failed("%v", err)
		} else if status.Session.State != "paused" {
			return failed("after director pause the session is %s, not paused", status.Session.State)
		}
		return stepResult{output: output, note: "paused by this takeover"}
	case StepDrain:
		status, err := s.status(ctx, p)
		if err != nil {
			return failed("%v", err)
		}
		if status.Session.State == "running" {
			return failed("the session is running again, so new work may start; pause it or abandon this plan")
		}
		if n := len(status.Running); n > 0 {
			var names []string
			for _, r := range status.Running {
				names = append(names, fmt.Sprintf("%s/%s %s #%d", r.Repository, r.TaskID, r.Step, r.Attempt))
			}
			return stepResult{waiting: true, diagnosis: fmt.Sprintf("%s still running (%s); apply again once they finish", plural(n, "attempt"), strings.Join(names, ", "))}
		}
		return stepResult{note: "no attempt is running"}
	case StepBackup:
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			diag := fmt.Sprintf("%s: %v", strings.Join(st.Argv[1:3], " "), err)
			if !p.Probe.Backup {
				diag += "; if an earlier run left a partial export at " + st.Argv[4] + ", remove it on the machine and apply again"
			}
			return failed("%s", diag)
		}
		return stepResult{output: output}
	case StepManaged:
		want := string(digest.Of(p.files[p.Files[0].Name]))
		mode, err := s.mode(ctx, p)
		if err != nil {
			return failed("%v", err)
		}
		switch mode.ManagedDigest {
		case want:
			return stepResult{note: "managed.toml is already published"}
		case "":
		default:
			return failed("%v: the copy has a managed.toml with digest %s that this plan did not publish; it is left as it is", ErrPlanChanged, mode.ManagedDigest)
		}
		target := Remote{p.SSHAlias, p.Files[0].Target}
		inputs, err := os.MkdirTemp("", "guildhall-setup-")
		if err != nil {
			return failed("%v", err)
		}
		defer func() { _ = os.RemoveAll(inputs) }()
		local := filepath.Join(inputs, "managed.toml")
		if err := os.WriteFile(local, p.files[p.Files[0].Name], 0o600); err != nil {
			return failed("%v", err)
		}
		if err := s.Transport.Put(ctx, local, target); err != nil {
			return failed("copying managed.toml to %s: %v", target, err)
		}
		back, err := s.fetch(ctx, target)
		if err != nil {
			return failed("reading managed.toml back from %s: %v", target, err)
		}
		if !bytes.Equal(back, p.files[p.Files[0].Name]) {
			return failed("managed.toml read back from %s does not match the plan; apply again to copy it again", target)
		}
		return stepResult{note: "published and read back with digest " + want}
	case StepEnrol:
		mode, err := s.mode(ctx, p)
		if err != nil {
			return failed("%v", err)
		}
		if mode.Steward == p.Steward {
			return stepResult{note: "already enrolled with Steward " + p.Steward}
		}
		if mode.Steward != "" {
			return failed("%v: the copy is enrolled with Steward %q, not %q", ErrPlanChanged, mode.Steward, p.Steward)
		}
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			return failed("director enrol: %v", err)
		}
		if mode, err = s.mode(ctx, p); err != nil {
			return failed("%v", err)
		} else if mode.Steward != p.Steward {
			return failed("after director enrol the copy names Steward %q, not %q", mode.Steward, p.Steward)
		}
		return stepResult{output: output}
	case StepReload:
		if diag := s.managedAuthorized(ctx, p); diag == "" {
			return stepResult{note: "the copy is already managed with its managed.toml authorized"}
		}
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			return failed("config reload: %v; the copy is enrolled but refuses its settings until it is reloaded, so apply again to run the reload again", err)
		}
		if diag := s.managedAuthorized(ctx, p); diag != "" {
			return failed("after config reload %s", diag)
		}
		return stepResult{output: output}
	case StepResume:
		if !log.PausedByTakeover {
			return stepResult{note: "the takeover paused no session, so it resumes none"}
		}
		status, err := s.status(ctx, p)
		if err != nil {
			return failed("%v", err)
		}
		switch status.Session.State {
		case "running":
			return stepResult{note: "the session is running"}
		case "paused":
		default:
			return stepResult{note: "the session is " + status.Session.State + ", so there is nothing to resume"}
		}
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			return failed("director resume: %v", err)
		}
		return stepResult{output: output, note: "resumed"}
	case StepRoster:
		return s.commitRoster(ctx, p)
	}
	return failed("unknown step %q", st.ID)
}

// recheck verifies a step done earlier whose result can be read again: the copy is managed and authorized, and the
// roster at HEAD holds the entry; it returns why not, or "".
func (s *Setup) recheck(ctx context.Context, p *SetupPlan, st SetupStep) string {
	switch st.ID {
	case StepReload:
		return s.managedAuthorized(ctx, p)
	case StepRoster:
		_, r, err := s.headRoster(ctx)
		if err != nil {
			return err.Error()
		}
		if !hasEntry(r, p.Roster) {
			return "roster.toml at the private config's HEAD no longer holds the entry"
		}
	}
	return ""
}

// managedAuthorized returns why the copy is not managed with the plan's managed.toml authorized, or "".
func (s *Setup) managedAuthorized(ctx context.Context, p *SetupPlan) string {
	mode, err := s.mode(ctx, p)
	if err != nil {
		return err.Error()
	}
	want := string(digest.Of(p.files[p.Files[0].Name]))
	if mode.Mode != "managed" || !mode.Authorized || mode.ManagedDigest != want || mode.Steward != p.Steward {
		return fmt.Sprintf("director mode reports %s, Steward %q, managed.toml %s, authorized %v; want managed by %q with %s authorized: %s",
			mode.Mode, mode.Steward, mode.ManagedDigest, mode.Authorized, p.Steward, want, mode.Refusal)
	}
	return ""
}

// commitRoster adds the plan's entry to roster.toml at the private config's HEAD, commits that file alone through a
// private index and pushes it; an entry already at HEAD is only pushed.
func (s *Setup) commitRoster(ctx context.Context, p *SetupPlan) stepResult {
	data, r, err := s.headRoster(ctx)
	if err != nil {
		return failed("%v", err)
	}
	note := "the roster already holds the entry"
	if !hasEntry(r, p.Roster) {
		next, err := proposeRoster(data, r, p.Roster)
		if err != nil {
			return failed("%v", err)
		}
		msg := fmt.Sprintf("Take over Director %s on %s", p.Director, p.Machine)
		if _, err := s.Private.CommitFiles(ctx, map[string][]byte{"roster.toml": next}, msg, s.signature()); err != nil {
			return failed("committing roster.toml: %v", err)
		}
		note = "added the entry to roster.toml"
	}
	pushed, err := s.Private.Publish(ctx, s.signature())
	if err != nil {
		return failed("pushing the private config: %v; the commit is kept, so apply again to push it", err)
	}
	return stepResult{output: pushed, note: note}
}

// Abandon gives up the setup plan in planDir before its copy is changed: it resumes a session the takeover paused and
// marks the plan abandoned, so it never runs again. Once the managed step has started it refuses, since the copy
// may already hold the plan's managed.toml; the plan is then finished with an apply.
func (s *Setup) Abandon(ctx context.Context, planDir string) (*Outcome, error) {
	p, err := openSetupPlan(planDir)
	if err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	log, err := ReadStepLog(p.PlanDir)
	if err != nil {
		return nil, err
	}
	if log.PlanDigest != "" && log.PlanDigest != p.Digest {
		return nil, fmt.Errorf("%w: the step log belongs to plan %s", ErrPlanChanged, log.PlanDigest)
	}
	log.PlanDigest = p.Digest
	for _, id := range []string{StepManaged, StepEnrol, StepReload} {
		if log.Record(id) != nil {
			return nil, fmt.Errorf("the %s step has run, so the copy may hold the plan's managed.toml or its enrolment; finish the takeover with --apply instead", id)
		}
	}
	out := &Outcome{Log: log, State: OutcomeDone}
	if log.PausedByTakeover {
		var resume SetupStep
		for _, st := range p.Steps {
			if st.ID == StepResume {
				resume = st
			}
		}
		r := s.runStep(ctx, p, resume, log)
		rec := log.record(StepResume)
		rec.State, rec.At, rec.Runs, rec.Output, rec.Note, rec.Error = StepDone, s.Now().UTC(), rec.Runs+1, r.output, r.note, r.diagnosis
		if r.diagnosis != "" {
			rec.State = StepFailed
			out.State, out.Step, out.Diagnosis = OutcomeFailed, StepResume, r.diagnosis
			if err := writeStepLog(p.PlanDir, log); err != nil {
				return out, err
			}
			return out, &ErrStepFailed{Step: StepResume, Diagnosis: r.diagnosis}
		}
	}
	log.Abandoned = true
	if err := writeStepLog(p.PlanDir, log); err != nil {
		return out, err
	}
	return out, nil
}
