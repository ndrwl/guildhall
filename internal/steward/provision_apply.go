package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/setup"
)

// managedFile is the plan's managed.toml.
func (p *SetupPlan) managedFile() PlannedFile {
	return p.plannedFile(setupManagedDir + "/managed.toml")
}

func (p *SetupPlan) plannedFile(name string) PlannedFile {
	for _, f := range p.Files {
		if f.Name == name {
			return f
		}
	}
	return PlannedFile{Name: name}
}

// putFile copies the planned file f to its target on the machine and reads it back.
func (s *Setup) putFile(ctx context.Context, p *SetupPlan, f PlannedFile) stepResult {
	target := Remote{p.SSHAlias, f.Target}
	inputs, err := os.MkdirTemp("", "guildhall-setup-")
	if err != nil {
		return failed("%v", err)
	}
	defer func() { _ = os.RemoveAll(inputs) }()
	local := filepath.Join(inputs, filepath.Base(filepath.FromSlash(f.Name)))
	if err := os.WriteFile(local, p.files[f.Name], 0o600); err != nil {
		return failed("%v", err)
	}
	base := filepath.Base(filepath.FromSlash(f.Name))
	if err := s.Transport.Put(ctx, local, target); err != nil {
		return failed("copying %s to %s: %v", base, target, err)
	}
	back, err := s.fetch(ctx, target)
	if err != nil {
		return failed("reading %s back from %s: %v", base, target, err)
	}
	if !bytes.Equal(back, p.files[f.Name]) {
		return failed("%s read back from %s does not match the plan; apply again to copy it again", base, target)
	}
	return stepResult{note: "copied and read back with sha256 " + f.SHA256}
}

// putNew is a provisioning copy step: the planned file is copied only when the machine reports that the target does
// not exist, and is left alone when the machine already has it byte for byte. A different file at the target is
// refused with ErrCopyExists, and any other failure to read the target, such as a file the user may not read, a
// directory at the path or a lost connection, fails the step; in both cases the target is left as it is.
func (s *Setup) putNew(ctx context.Context, p *SetupPlan, f PlannedFile) stepResult {
	back, err := s.fetch(ctx, Remote{p.SSHAlias, f.Target})
	switch {
	case err == nil && bytes.Equal(back, p.files[f.Name]):
		return stepResult{note: "already in place with sha256 " + f.SHA256}
	case err == nil:
		return failed("%v: %s:%s exists and is not the plan's file; it is left as it is", ErrCopyExists, p.Machine, f.Target)
	case !errors.Is(err, fs.ErrNotExist):
		return failed("reading %s:%s before copying, to be sure nothing is there: %v; nothing was copied, so apply again once it can be read", p.Machine, f.Target, err)
	}
	return s.putFile(ctx, p, f)
}

// runProvisionStep checks, acts and verifies one step of a provisioning plan; ok is false for the takeover's
// enrol, reload and roster steps, which runStep runs as they are.
func (s *Setup) runProvisionStep(ctx context.Context, p *SetupPlan, st SetupStep, log *StepLog) (stepResult, bool) {
	switch {
	case st.Kind == StepKindFix:
		return s.runFix(ctx, p, st), true
	case st.Kind == StepKindWizard:
		return s.runWizard(ctx, p, st), true
	case st.Kind == StepKindSummary:
		return s.runSummary(ctx, p), true
	case st.ID == StepConfig:
		return s.putNew(ctx, p, p.plannedFile(setupConfigFile)), true
	case st.ID == StepManaged:
		return s.putNew(ctx, p, p.managedFile()), true
	case strings.HasPrefix(st.ID, stepFilesPrefix):
		sf, _ := p.stepFile(strings.TrimPrefix(st.ID, stepFilesPrefix))
		return s.putNew(ctx, p, p.plannedFile(sf.File)), true
	case st.ID == StepStore:
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			// The dry run saw no store, so one that exists now was made by an earlier run of this step that
			// stopped before its record was written.
			if again := log.Record(st.ID); again != nil && again.Runs > 1 && strings.Contains(err.Error(), "already exists") {
				return stepResult{note: "the store was made by an earlier run of this step"}, true
			}
			return failed("store init: %v", err), true
		}
		return stepResult{output: output}, true
	case strings.HasPrefix(st.ID, stepClonePrefix):
		return s.runClone(ctx, p, st, log), true
	case st.ID == StepAdopt:
		output, err := s.run(ctx, p, st.Argv)
		if err != nil {
			return failed("release adopt: %v", err), true
		}
		return stepResult{output: output}, true
	case st.ID == StepStart:
		return s.runStart(ctx, p, st), true
	}
	return stepResult{}, false
}

// runClone clones one repository into its path, first running the new-copy doctor for that path alone, so a path
// that is not empty is refused with ErrCopyExists before git runs, whatever language git speaks. On a re-run of a
// step that stopped after git made the clone, a repository at the path whose HEAD the machine gives back is taken
// as that clone, and the step's note says so.
func (s *Setup) runClone(ctx context.Context, p *SetupPlan, st SetupStep, log *StepLog) stepResult {
	path := st.Argv[len(st.Argv)-1]
	pre, err := preflightFile("", []string{path})
	if err != nil {
		return failed("%v", err)
	}
	r, err := s.doctor(ctx, p, pre)
	if err != nil {
		return failed("%v", err)
	}
	if exists := copyExists(p.Machine, r); exists != nil {
		if again := log.Record(st.ID); again != nil && again.Runs > 1 {
			if _, err := s.fetch(ctx, Remote{p.SSHAlias, path + "/.git/HEAD"}); err == nil {
				return stepResult{note: "a repository is at " + path + ", taken as the clone an earlier run of this step made from " + st.Argv[len(st.Argv)-2]}
			}
		}
		return failed("%v", exists)
	}
	output, err := s.run(ctx, p, st.Argv)
	if err != nil {
		return failed("git clone: %v", err)
	}
	return stepResult{output: output}
}

// runFix closes a fixable gap with setup doctor --fix --only, which checks first and acts only on a failing check,
// and verifies that the fresh report no longer has the gap.
func (s *Setup) runFix(ctx context.Context, p *SetupPlan, st SetupStep) stepResult {
	sf, ok := p.stepFile(st.StepFile)
	if !ok {
		return failed("the plan has no step file %q", st.StepFile)
	}
	r, err := s.doctorRun(ctx, p, sf, st.Argv)
	if err != nil {
		return failed("%v; the step's undo note is in %s, and the setup journal in %s", err, sf.Ref, p.SetupDir)
	}
	if g, open := gapOf(r, st.ID); open {
		return failed("after setup doctor --fix the doctor still reports %s: %s", st.ID, oneLine(g.Detail))
	}
	return stepResult{note: "the doctor reports " + st.ID + " closed"}
}

func gapOf(r setup.Report, id string) (setup.Gap, bool) {
	i := slices.IndexFunc(r.Gaps, func(g setup.Gap) bool { return g.ID == id })
	if i < 0 {
		return setup.Gap{}, false
	}
	return r.Gaps[i], true
}

// runWizard is a human gap, which only the Guildmaster closes with the wizard on the machine. The doctor decides:
// a gap it no longer reports is done. While it still reports one, the step waits, printing the wizard command, and
// says what the wizard's completion record, read back by scp, shows: missing, malformed, of another step file, not
// naming this step, or claiming done while the gap is still open.
func (s *Setup) runWizard(ctx context.Context, p *SetupPlan, st SetupStep) stepResult {
	sf, ok := p.stepFile(st.StepFile)
	if !ok {
		return failed("the plan has no step file %q", st.StepFile)
	}
	r, err := s.doctor(ctx, p, sf)
	if err != nil {
		return failed("%v", err)
	}
	gap, open := gapOf(r, st.ID)
	path := p.recordPath(sf)
	rec, recErr := s.wizardRecord(ctx, p, sf, path)
	if !open {
		if recErr != nil {
			return stepResult{note: "the doctor reports " + st.ID + " closed, with no wizard record of it (" + recErr.Error() + ")"}
		}
		return stepResult{note: "the doctor reports " + st.ID + " closed; the wizard recorded it " + stepStatus(rec, st.ID)}
	}
	run := fmt.Sprintf("run on %s: %s", p.Machine, shellJoin(st.Argv))
	switch {
	case recErr != nil:
		return stepResult{waiting: true, diagnosis: fmt.Sprintf("%v; %s", recErr, run)}
	case stepStatus(rec, st.ID) == "":
		return stepResult{waiting: true, diagnosis: fmt.Sprintf("%v: the record at %s is of wizard steps %s, not %s; %s", ErrWizardRecord, path, strings.Join(rec.StepIDs, ","), st.ID, run)}
	}
	return stepResult{waiting: true, diagnosis: fmt.Sprintf("the wizard recorded %s %s, but the doctor still reports it (%s); %s", st.ID, stepStatus(rec, st.ID), oneLine(gap.Detail), run)}
}

// wizardRecord reads the completion record of sf back from the machine, refusing with ErrWizardRecord one that is
// missing, malformed or of another step file.
func (s *Setup) wizardRecord(ctx context.Context, p *SetupPlan, sf StepFile, path string) (setup.Record, error) {
	var rec setup.Record
	data, err := s.fetch(ctx, Remote{p.SSHAlias, path})
	if err != nil {
		return rec, fmt.Errorf("%w: no completion record at %s", ErrWizardRecord, path)
	}
	if err := json.Unmarshal(data, &rec); err != nil || rec.Schema != setup.RecordSchema {
		return rec, fmt.Errorf("%w: %s is not a %s record", ErrWizardRecord, path, setup.RecordSchema)
	}
	if rec.Digest != sf.SHA256 || rec.Steps != sf.Name {
		return rec, fmt.Errorf("%w: %s is of step file %s %s, not the plan's %s %s", ErrWizardRecord, path, rec.Steps, rec.Digest, sf.Name, sf.SHA256)
	}
	return rec, nil
}

// stepStatus is the status the record gives step id, or "" when it names none.
func stepStatus(rec setup.Record, id string) string {
	for _, r := range rec.Results {
		if r.ID == id {
			return r.Status
		}
	}
	return ""
}

// shellJoin is argv as one line to type, every argument being in the restricted character set.
func shellJoin(argv []string) string { return strings.Join(argv, " ") }

// startedStates are the session states in which a started session has its supervisor running.
var startedStates = []string{"recovering", "running", "paused"}

// runStart starts the Director's session in its Herdr tab, unless director status already reports one started
// with its tab, then waits up to StartWait for status to report the session started and its tab recorded.
func (s *Setup) runStart(ctx context.Context, p *SetupPlan, st SetupStep) stepResult {
	status, err := s.status(ctx, p)
	if err != nil {
		return failed("%v", err)
	}
	if slices.Contains(startedStates, status.Session.State) && status.Session.Pane.Tab != "" {
		return stepResult{note: fmt.Sprintf("the session is already %s in tab %s", status.Session.State, status.Session.Pane.Tab)}
	}
	output, err := s.run(ctx, p, st.Argv)
	if err != nil {
		return failed("director start --tab: %v", err)
	}
	wait, every := s.StartWait, s.PollEvery
	if wait <= 0 {
		wait = 2 * time.Minute
	}
	if every <= 0 {
		every = 2 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		if status, err = s.status(ctx, p); err != nil {
			return failed("%v", err)
		}
		if slices.Contains(startedStates, status.Session.State) && status.Session.Pane.Tab != "" {
			return stepResult{output: output, note: fmt.Sprintf("the session is %s in tab %s", status.Session.State, status.Session.Pane.Tab)}
		}
		tick := time.NewTimer(every)
		select {
		case <-ctx.Done():
			tick.Stop()
			return failed("waiting for the session to start: %v", ctx.Err())
		case <-deadline.C:
			tick.Stop()
			return failed("director start --tab returned, but %s later director status reports the session %s with tab %q; look at the Director's Herdr tab on %s, then apply again",
				wait, status.Session.State, status.Session.Pane.Tab, p.Machine)
		case <-tick.C:
		}
	}
}

// SetupSummary is summary.json, the verified summary a provisioning plan ends with.
type SetupSummary struct {
	Machine  string `json:"machine"`
	Director string `json:"director"`
	CopyDir  string `json:"copy_dir"`
	Mode     string `json:"mode"`
	Steward  string `json:"steward"`
	Session  string `json:"session"`
	// Ready reports whether the session has said it is ready; a slow first turn does not fail the plan.
	Ready bool       `json:"ready"`
	Pane  remotePane `json:"pane"`
	// Versions are what the machine runs beside what the plan pins.
	Versions []SummaryVersion `json:"versions"`
	// Outstanding are the gaps the doctor still reports, each with the action line that closes it.
	Outstanding []setup.Gap `json:"outstanding"`
}

// SummaryVersion is one program's version on the machine against its pin.
type SummaryVersion struct {
	Name   string `json:"name"`
	Found  string `json:"found"`
	Pinned string `json:"pinned,omitempty"`
	Match  bool   `json:"match"`
}

// runSummary verifies the provisioned copy and writes summary.json: it is managed, its session's tab is recorded,
// and the doctor, run again for every step file, gives the outstanding manual items.
func (s *Setup) runSummary(ctx context.Context, p *SetupPlan) stepResult {
	if diag := s.managedAuthorized(ctx, p); diag != "" {
		return failed("%s", diag)
	}
	status, err := s.status(ctx, p)
	if err != nil {
		return failed("%v", err)
	}
	if status.Session.Pane.Tab == "" {
		return failed("director status reports the session %s with no Herdr tab recorded", status.Session.State)
	}
	sum := SetupSummary{Machine: p.Machine, Director: p.Director, CopyDir: p.CopyDir, Mode: "managed", Steward: p.Steward,
		Session: status.Session.State, Ready: status.Session.State == "running" || !status.Session.ReadyAt.IsZero(),
		Pane: status.Session.Pane, Outstanding: []setup.Gap{}}
	pins := p.Request.Pins
	found := status.Health.Pin
	if found == "" {
		found = status.Health.Release
	}
	sum.Versions = append(sum.Versions, SummaryVersion{Name: "guildhall", Found: found, Pinned: pins.Guildhall, Match: pins.Guildhall == "" || pins.Guildhall == found})
	for _, sf := range p.StepFiles {
		r, err := s.doctor(ctx, p, sf)
		if err != nil {
			return failed("%v", err)
		}
		sum.Outstanding = append(sum.Outstanding, r.Gaps...)
		if sf.Name != "herdr" {
			continue
		}
		for _, c := range r.Checks {
			if c.ID == "herdr" {
				v := SummaryVersion{Name: "herdr", Found: oneLine(c.Detail), Pinned: pins.HerdrVersion}
				v.Match = c.OK && (v.Pinned == "" || strings.Contains(c.Detail, v.Pinned))
				sum.Versions = append(sum.Versions, v)
			}
		}
	}
	data, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return failed("%v", err)
	}
	if err := os.WriteFile(filepath.Join(p.PlanDir, setupSummary), append(data, '\n'), 0o600); err != nil {
		return failed("writing %s: %v", setupSummary, err)
	}
	return stepResult{output: strings.TrimSpace(string(data)), note: summaryLine(sum)}
}

// summaryLine is the summary as one line.
func summaryLine(sum SetupSummary) string {
	ready := "not ready yet"
	if sum.Ready {
		ready = "ready"
	}
	parts := []string{fmt.Sprintf("managed by %s; session %s (%s) in Herdr session %s tab %s", sum.Steward, sum.Session, ready, sum.Pane.Session, sum.Pane.Tab)}
	for _, v := range sum.Versions {
		line := v.Name + " " + v.Found
		if v.Pinned != "" {
			if v.Match {
				line += " (pinned " + v.Pinned + ")"
			} else {
				line += " (differs from the pin " + v.Pinned + ")"
			}
		}
		parts = append(parts, line)
	}
	if len(sum.Outstanding) == 0 {
		parts = append(parts, "no outstanding manual item")
	} else {
		var ids []string
		for _, g := range sum.Outstanding {
			ids = append(ids, g.ID)
		}
		parts = append(parts, "outstanding: "+strings.Join(ids, ", ")+" (see summary.json for each action line)")
	}
	return strings.Join(parts, "; ")
}

// ReadSetupSummary reads the summary.json a finished provisioning wrote in planDir.
func ReadSetupSummary(planDir string) (*SetupSummary, error) {
	data, err := os.ReadFile(filepath.Join(planDir, setupSummary))
	if err != nil {
		return nil, err
	}
	var sum SetupSummary
	if err := json.Unmarshal(data, &sum); err != nil {
		return nil, fmt.Errorf("%s: %w", setupSummary, err)
	}
	return &sum, nil
}
