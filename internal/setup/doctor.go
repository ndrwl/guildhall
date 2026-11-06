package setup

// The doctor is the Go form of Firstmate's bin/fm-remote-doctor.sh at ndrwl/firstmate
// 2add138ffe92309787dbbedf375beb4f05704f7b (MIT, see NOTICE): each gap tagged fixable or human with
// an exact action line, --fix closing fixable gaps only, safe to re-run, every check re-derived after fixing. New
// here: a JSON report whose gap ids are step ids, --fix --only for the gaps an approved plan names, and the
// ownership manifest.

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ReportSchema is the format of the doctor's JSON report.
const ReportSchema = "guildhall.doctor-report/1"

// Check is one step the doctor checked.
type Check struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Tag    Tag    `json:"tag"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Gap is a failing check: its step id, tag and the exact command line that closes it.
type Gap struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Tag    Tag    `json:"tag"`
	Action string `json:"action"`
	Detail string `json:"detail,omitempty"`
}

// Report is the doctor's finding for one step file.
type Report struct {
	Schema string  `json:"schema"`
	Steps  string  `json:"steps"`
	Ref    string  `json:"ref"`
	Digest string  `json:"digest"`
	OS     string  `json:"os"`
	Checks []Check `json:"checks"`
	Gaps   []Gap   `json:"gaps"`
	// Skipped are the steps whose os list leaves this operating system out.
	Skipped []string `json:"skipped"`
}

// Doctor runs the check of every step for this operating system and reports each failing one as a gap. It changes
// nothing, so it is safe to re-run.
func Doctor(ctx context.Context, s Steps, f Facts) (Report, error) {
	e, err := newEnv(s, f)
	if err != nil {
		return Report{}, err
	}
	return e.doctor(ctx), nil
}

func (e *env) doctor(ctx context.Context) Report {
	r := Report{Schema: ReportSchema, Steps: e.s.Name, Ref: e.s.Ref, Digest: e.s.Digest, OS: e.f.GOOS,
		Checks: []Check{}, Gaps: []Gap{}, Skipped: []string{}}
	for _, st := range e.s.Steps {
		if !st.ForOS(e.f.GOOS) {
			// A step with another form for this operating system is checked in that form, not skipped.
			if form, _ := e.s.Step(st.ID, e.f.GOOS); !form.ForOS(e.f.GOOS) && !slices.Contains(r.Skipped, st.ID) {
				r.Skipped = append(r.Skipped, st.ID)
			}
			continue
		}
		ok, detail := e.check(ctx, st, st.Check)
		r.Checks = append(r.Checks, Check{ID: st.ID, Title: st.Title, Tag: st.Tag, OK: ok, Detail: detail})
		if !ok {
			r.Gaps = append(r.Gaps, Gap{ID: st.ID, Title: st.Title, Tag: st.Tag, Action: e.actionLine(st), Detail: detail})
		}
	}
	return r
}

// actionLine is the exact command that closes st's gap: the doctor's fix for a fixable step, the wizard for a
// human one.
func (e *env) actionLine(st Step) string {
	ref := quoteArg(e.s.Ref)
	var line string
	if st.Tag == Human {
		line = fmt.Sprintf("guildhall setup wizard --steps %s --digest %s --only %s", ref, e.s.Digest, st.ID)
	} else {
		line = fmt.Sprintf("guildhall setup doctor --fix --only %s --steps %s --digest %s", st.ID, ref, e.s.Digest)
	}
	for _, k := range sortedStrings(keys(e.f.Vars)) {
		line += " --set " + quoteArg(k+"="+e.f.Vars[k])
	}
	if e.f.SetupDir != "" {
		line += " --setup-dir " + quoteArg(e.f.SetupDir)
	}
	return line
}

// quoteArg single-quotes an argument that a POSIX shell would split or expand.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`*?[]{}()<>|&;#~!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedStrings(s []string) []string {
	slices.Sort(s)
	return s
}

// selectSteps returns the steps ids names, in file order, refusing an unknown id with ErrUnknownStep and one whose
// os list leaves this operating system out with ErrUnsupportedOS. No ids means every step for this operating system.
func (e *env) selectSteps(ids []string) ([]Step, error) {
	for _, id := range ids {
		st, ok := e.s.Step(id, e.f.GOOS)
		if !ok {
			return nil, fmt.Errorf("%w: %s has no step %q", ErrUnknownStep, e.s.Name, id)
		}
		if !st.ForOS(e.f.GOOS) {
			return nil, fmt.Errorf("%w: step %s runs on %s, not %s", ErrUnsupportedOS, id, strings.Join(st.OS, ", "), e.f.GOOS)
		}
	}
	var out []Step
	for _, st := range e.s.Steps {
		if st.ForOS(e.f.GOOS) && (len(ids) == 0 || slices.Contains(ids, st.ID)) {
			out = append(out, st)
		}
	}
	return out, nil
}

// Fix closes the fixable gaps ids names, or every fixable gap when ids is empty, then re-derives every check and
// returns the fresh report. A human step named in ids is refused with ErrHumanStep before anything runs; a step
// whose act or verify fails stops the fix with a *StepError, and the report is still the fresh one.
func Fix(ctx context.Context, s Steps, f Facts, ids []string, j Journal) (Report, error) {
	e, err := newEnv(s, f)
	if err != nil {
		return Report{}, err
	}
	steps, err := e.selectSteps(ids)
	if err != nil {
		return Report{}, err
	}
	if err := e.requireSetupDir(); err != nil {
		return Report{}, err
	}
	for _, st := range steps {
		if st.Tag == Human && len(ids) > 0 {
			return Report{}, fmt.Errorf("%w: %s is closed by a person through `%s`, not by --fix", ErrHumanStep, st.ID, e.actionLine(st))
		}
	}
	var failed error
	for _, st := range steps {
		if st.Tag != Fixable {
			continue
		}
		if _, err := e.act(ctx, st, j); err != nil {
			failed = err
			break
		}
	}
	return e.doctor(ctx), failed
}

// Outcome is the end of a Run: done, or waiting at a human step whose gap a person must close.
type Outcome struct {
	Waiting string `json:"waiting,omitempty"`
	Action  string `json:"action,omitempty"`
}

// Run takes the steps ids names (every step for this operating system when empty) in order: a fixable step is
// checked, acted on and verified, and a human step whose check fails stops the run waiting for it, with the wizard
// command that closes it. A step that fails stops the run with a *StepError.
func Run(ctx context.Context, s Steps, f Facts, ids []string, j Journal) (Outcome, error) {
	e, err := newEnv(s, f)
	if err != nil {
		return Outcome{}, err
	}
	steps, err := e.selectSteps(ids)
	if err != nil {
		return Outcome{}, err
	}
	if err := e.requireSetupDir(); err != nil {
		return Outcome{}, err
	}
	for _, st := range steps {
		if st.Tag == Human {
			if ok, _ := e.check(ctx, st, st.Check); ok {
				e.journal(j, st.ID, "already-done", "")
				continue
			}
			e.journal(j, st.ID, "waiting", "")
			return Outcome{Waiting: st.ID, Action: e.actionLine(st)}, nil
		}
		if _, err := e.act(ctx, st, j); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{}, nil
}
