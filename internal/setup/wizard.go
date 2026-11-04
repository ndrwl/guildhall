package setup

// The wizard is the Go form of the wizard template Firstmate uses, bin/wizard-template.sh and bin/wizard-template.ps1
// in ndrwl/dev-setup at 8a095ccfeff5563f776f743d088b44c5afc57747 (itself adapted from mattpocock/skills at 49dd158;
// MIT, see NOTICE): numbered step
// cards, confirm, ask and masked ask_secret prompts, recorded actions, the undo note shown when a step fails,
// --dry-run, a journal and a JSON completion record. New here: one implementation for every operating system, the
// step file's digest checked by the caller and carried in the record, and no secret value in any file.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// RecordSchema is the format of a wizard's completion record.
const RecordSchema = "guildhall.wizard-record/1"

// journalFile is the journal's name in the setup directory, and recordsDir the directory of completion records.
const (
	journalFile = "journal.jsonl"
	recordsDir  = "records"
)

// ErrDeclined is a confirm prompt the person answered no.
var ErrDeclined = errors.New("not confirmed")

// Entry is one journal line: what the engine did to a step, and when.
type Entry struct {
	Time   time.Time `json:"time"`
	Steps  string    `json:"steps"`
	Digest string    `json:"digest"`
	Step   string    `json:"step"`
	Event  string    `json:"event"`
	Detail string    `json:"detail,omitempty"`
}

// Journal keeps the engine's entries.
type Journal interface {
	Append(Entry) error
}

// FileJournal appends entries as JSON lines to journal.jsonl in a setup directory.
type FileJournal struct {
	Dir string
	mu  sync.Mutex
}

// Append writes e as one line, creating the setup directory and journal when missing.
func (j *FileJournal) Append(e Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(j.Dir, journalFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Prompter is the person at the wizard: it shows cards and asks questions, so tests answer without a terminal.
type Prompter interface {
	Show(text string) error
	Confirm(question string) (bool, error)
	Ask(question string) (string, error)
	// AskSecret reads an answer without echoing it.
	AskSecret(question string) (string, error)
}

// StepResult is one step's line in a completion record. Answers holds ask answers and confirmations; Secrets only
// names the ask_secret prompts answered, never their values.
type StepResult struct {
	ID      string            `json:"id"`
	Status  string            `json:"status"`
	Answers map[string]string `json:"answers,omitempty"`
	Secrets []string          `json:"secrets,omitempty"`
	Detail  string            `json:"detail,omitempty"`
}

// Record is a wizard's completion record, the receipt a Steward reads back; the doctor, not the record, decides
// whether a gap is closed.
type Record struct {
	Schema   string       `json:"schema"`
	Steps    string       `json:"steps"`
	Ref      string       `json:"ref"`
	Digest   string       `json:"digest"`
	StepIDs  []string     `json:"step_ids"`
	Status   string       `json:"status"`
	Started  time.Time    `json:"started"`
	Finished time.Time    `json:"finished"`
	Results  []StepResult `json:"results"`
}

// RecordPath is where the wizard writes the completion record of the step file with the given name and digest.
func RecordPath(setupDir, name, digest string) string {
	return filepath.Join(setupDir, recordsDir, name+"-"+digest[:min(12, len(digest))]+".json")
}

// WizardOptions choose the steps (all for this operating system when IDs is empty) and whether to only show them.
type WizardOptions struct {
	IDs    []string
	DryRun bool
}

// Wizard walks a person through the chosen steps, one numbered card each: a step whose check passes is already
// done; a fixable step is acted on as Fix does; a human step shows its instructions, asks its prompts, runs its run
// command with the terminal attached and is verified. A failed step shows its undo note and stops the wizard with a
// *StepError. The completion record and journal are written in the setup directory; with DryRun nothing runs and
// nothing is written.
func Wizard(ctx context.Context, s Steps, f Facts, opts WizardOptions, p Prompter, j Journal) (Record, error) {
	e, err := newEnv(s, f)
	if err != nil {
		return Record{}, err
	}
	steps, err := e.selectSteps(opts.IDs)
	if err != nil {
		return Record{}, err
	}
	if opts.DryRun {
		j = nil
	} else if err := e.requireSetupDir(); err != nil {
		return Record{}, err
	}
	ids := make([]string, len(steps))
	for i, st := range steps {
		ids[i] = st.ID
	}
	rec := Record{Schema: RecordSchema, Steps: s.Name, Ref: s.Ref, Digest: s.Digest, StepIDs: ids, Status: "done",
		Started: e.f.Now().UTC(), Results: []StepResult{}}
	head := fmt.Sprintf("%s: %d step(s)", s.Title, len(steps))
	if opts.DryRun {
		head += " (dry run: nothing is run or written)"
	}
	if err := p.Show(head); err != nil {
		return Record{}, err
	}
	for i, st := range steps {
		if err := p.Show(card(i+1, len(steps), st)); err != nil {
			return Record{}, err
		}
		if opts.DryRun {
			if err := p.Show(dryRunLines(st)); err != nil {
				return Record{}, err
			}
			continue
		}
		res, err := e.wizardStep(ctx, st, p, j)
		rec.Results = append(rec.Results, res)
		if err != nil {
			rec.Status = "failed"
			_ = p.Show(fmt.Sprintf("Step %d failed: %v\nUndo: %s", i+1, err, st.Undo))
			if werr := e.writeRecord(&rec); werr != nil {
				return rec, errors.Join(err, werr)
			}
			return rec, err
		}
	}
	if opts.DryRun {
		return rec, nil
	}
	if err := e.writeRecord(&rec); err != nil {
		return rec, err
	}
	return rec, p.Show(fmt.Sprintf("All %d step(s) done. Record: %s", len(steps), RecordPath(e.f.SetupDir, s.Name, s.Digest)))
}

// card is a step's numbered card.
func card(n, total int, st Step) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n── Step %d of %d · %s [%s] ──", n, total, st.Title, st.Tag)
	if st.Instructions != "" {
		b.WriteString("\n" + strings.TrimSpace(st.Instructions))
	}
	return b.String()
}

func dryRunLines(st Step) string {
	lines := []string{"would check: " + st.Check.String()}
	if st.Tag == Fixable {
		lines = append(lines, "would run: "+st.actDisplay())
	}
	for _, pr := range st.Prompts {
		lines = append(lines, "would ask ("+pr.Kind+"): "+pr.Text)
	}
	if !st.Run.IsZero() {
		lines = append(lines, "would run: "+st.Run.String())
	}
	lines = append(lines, "undo: "+st.Undo)
	return strings.Join(lines, "\n")
}

// wizardStep carries out one step with the person.
func (e *env) wizardStep(ctx context.Context, st Step, p Prompter, j Journal) (StepResult, error) {
	res := StepResult{ID: st.ID}
	if st.Tag == Fixable {
		ran, err := e.act(ctx, st, j)
		switch {
		case err != nil:
			res.Status, res.Detail = "failed", err.Error()
			return res, err
		case !ran:
			res.Status = "already-done"
			return res, p.Show("Already done.")
		}
		res.Status = "done"
		return res, p.Show("Done.")
	}
	if ok, _ := e.check(ctx, st, st.Check); ok {
		e.journal(j, st.ID, "already-done", "")
		res.Status = "already-done"
		return res, p.Show("Already done.")
	}
	e.journal(j, st.ID, "started", "")
	secrets := map[string]string{}
	values := map[string]string{}
	fail := func(err error) (StepResult, error) {
		msg := redact(err.Error(), secrets)
		e.journal(j, st.ID, "failed", msg)
		res.Status, res.Detail = "failed", msg
		return res, &StepError{Step: st.ID, Err: redactedError{msg: msg, err: err}}
	}
	for _, pr := range st.Prompts {
		switch pr.Kind {
		case "confirm":
			yes, err := p.Confirm(pr.Text)
			if err != nil {
				return fail(err)
			}
			if !yes {
				return fail(fmt.Errorf("%w: %s", ErrDeclined, pr.Text))
			}
			if pr.Name != "" {
				res.set(pr.Name, "yes")
			}
			e.journal(j, st.ID, "confirmed", pr.Text)
		case "ask":
			v, err := p.Ask(pr.Text)
			if err != nil {
				return fail(err)
			}
			values[pr.Name] = v
			res.set(pr.Name, v)
			e.journal(j, st.ID, "answered", pr.Name+"="+v)
		case "ask_secret":
			v, err := p.AskSecret(pr.Text)
			if err != nil {
				return fail(err)
			}
			secrets[pr.Name] = v
			res.Secrets = append(res.Secrets, pr.Name)
			e.journal(j, st.ID, "answered", pr.Name+" (secret, not recorded)")
		}
	}
	if !st.Run.IsZero() {
		env := map[string]string{}
		for k, v := range values {
			env[k] = v
		}
		for k, v := range secrets {
			env[k] = v
		}
		e.journal(j, st.ID, "run", st.Run.String())
		if out, err := e.exec(ctx, st.Run, actionTimeout, env, true); err != nil {
			return fail(fmt.Errorf("run %s: %v: %s", st.Run, err, tail(out)))
		}
	}
	if ok, detail := e.check(ctx, st, verifyCommand(st)); !ok {
		return fail(fmt.Errorf("verify failed: %s", detail))
	}
	e.journal(j, st.ID, "done", "")
	res.Status = "done"
	return res, p.Show("Done.")
}

func (r *StepResult) set(name, v string) {
	if r.Answers == nil {
		r.Answers = map[string]string{}
	}
	r.Answers[name] = v
}

// redactedError is err with its message's secret values replaced; errors.Is still sees err.
type redactedError struct {
	msg string
	err error
}

func (r redactedError) Error() string { return r.msg }
func (r redactedError) Unwrap() error { return r.err }

// redact replaces every secret value in s.
func redact(s string, secrets map[string]string) string {
	for _, v := range secrets {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[secret]")
		}
	}
	return s
}

func (e *env) writeRecord(r *Record) error {
	r.Finished = e.f.Now().UTC()
	path := RecordPath(e.f.SetupDir, r.Steps, r.Digest)
	if !filepath.IsAbs(path) {
		return fmt.Errorf("setup directory %q is not an absolute path", e.f.SetupDir)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'))
}

// ReadRecord reads and checks a completion record, refusing one of another schema.
func ReadRecord(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil || r.Schema != RecordSchema {
		return Record{}, fmt.Errorf("%s: not a %s record", path, RecordSchema)
	}
	return r, nil
}
