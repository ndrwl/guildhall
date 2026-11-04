package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// checkState is what one read of a head's checks says about waiting on them (docs/interpreter.md#absent-checks).
type checkState int

const (
	// checksFinished: every check reported has finished, or one failed, which settles the read however many are
	// still pending.
	checksFinished checkState = iota
	// checksPending: at least one check is reported, none failed, and some are still pending.
	checksPending
	// checksAbsent: no check is reported and the repository is not no_ci; it is never success (audit A01).
	checksAbsent
	// checksNoCI: no check is reported and the repository is no_ci, which passes.
	checksNoCI
)

// checkWait sorts the read in v.Checks, for a repository with no_ci = noCI, and sets on v what the read records:
// an empty list for none, NoCI for a no_ci pass, and the failed verdict with the failing checks. Validate and the
// rebased merge-queue front share it, so the two cannot tell absent and pending checks apart differently.
func checkWait(v *validationDoc, noCI bool) checkState {
	if v.Checks == nil {
		v.Checks = []forge.Check{}
	}
	v.NoCI = len(v.Checks) == 0 && noCI
	pending := false
	for _, c := range v.Checks {
		switch c.State {
		case forge.CheckFailed:
			v.Verdict, v.Failing = validationFailed, append(v.Failing, c.Name)
		case forge.CheckPending:
			pending = true
		}
	}
	switch {
	case v.Verdict == validationFailed:
		return checksFinished
	case pending:
		return checksPending
	case len(v.Checks) > 0:
		return checksFinished
	case noCI:
		return checksNoCI
	}
	return checksAbsent
}

// The schemas of a CI start record and its outcome.
const (
	ciStartSchema        = "guildhall.ci-start/1"
	ciStartOutcomeSchema = "guildhall.ci-start-outcome/1"
)

// ciStartDoc is a CI start record: validate's or deliver's one request that the forge re-trigger the pull request CI
// of a head that reported no check for ci_start_grace, by closing and reopening its pull request, written before the
// forge is asked, so a replay never asks again.
type ciStartDoc struct {
	Schema      string    `json:"schema"`
	Step        string    `json:"step"`
	Head        string    `json:"head"`
	PullRequest int       `json:"pull_request"`
	URL         string    `json:"url"`
	At          time.Time `json:"at"`
}

// ciStartOutcomeDoc is what the forge answered the start of its record: an outcome, or the error that refused it.
// A record with no outcome is a start whose outcome is unknown, its advance having stopped after the record.
type ciStartOutcomeDoc struct {
	Schema  string             `json:"schema"`
	Record  string             `json:"record"`
	Outcome forge.StartOutcome `json:"outcome,omitempty"`
	Error   string             `json:"error,omitempty"`
}

// ciStartName names the CI start record of head h in step id, whatever the round: <id>-ci-start-<first 12 of h>, since a
// document id holds at most 63 characters and the record holds the whole head. A name too long for its outcome's id
// is hashed whole.
func ciStartName(id, h string) string {
	name := id + "-ci-start-" + h[:12]
	if len(name+"-outcome") > 63 {
		name = "ci-start-" + digest.Of([]byte(id + "\n" + h)).Hex()[:24]
	}
	return name
}

// absentRecord is when a step first read Head's checks and the forge reported none, kept in
// <state>/<repo>/<task>/<step>.checks-absent.json beside its checks.json. Losing it only restarts the grace period.
type absentRecord struct {
	Head      string    `json:"head"`
	FirstSeen time.Time `json:"first_seen"`
}

func (r *run) checksAbsentPath(s workflow.Step) string {
	return filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, s.ID+".checks-absent.json")
}

// noteAbsent records that step s read no checks for head h, and returns when it first did: the recorded time for the
// same head, or now for a new head, a lost record or one from a clock that went back.
func (r *run) noteAbsent(s workflow.Step, h string) (time.Time, error) {
	p, now := r.checksAbsentPath(s), r.now().UTC()
	data, err := atomicfile.Read(p)
	switch {
	case err == nil:
		var a absentRecord
		if json.Unmarshal(data, &a) == nil && a.Head == h && !a.FirstSeen.IsZero() && !a.FirstSeen.After(now) {
			return a.FirstSeen, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return time.Time{}, err
	}
	data, err = json.Marshal(absentRecord{Head: h, FirstSeen: now})
	if err != nil {
		return time.Time{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return time.Time{}, err
	}
	return now, atomicfile.Write(p, data)
}

// clearAbsent removes step s's record of a head with no checks, once a read reports some.
func (r *run) clearAbsent(s workflow.Step) error {
	if err := os.Remove(r.checksAbsentPath(s)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ciStartGrace is how long a head may report no check before it is reported missing.
func (r *run) ciStartGrace() time.Duration {
	switch g := r.e.CIStartGrace; {
	case g == 0:
		return config.DefaultCIStartGrace
	case g < 0:
		return 0
	default:
		return g
	}
}

// awaitChecks is the wait of round k of step s on head h of pull request pr, whose read is in state: a pending or
// absent read waits, telling the Director once per head with the checks-pending pendingText; an absent one past
// ci_start_grace since the first read that saw none also re-triggers the pull request's CI once and sends
// the checks-missing messages. wait reports that the step waits, with next; a read with checks forgets the absence.
func (r *run) awaitChecks(s workflow.Step, repo config.Repository, pr *pullRequestDoc, h string, state checkState, pendingText string) (wait bool, next Next, err error) {
	if state != checksAbsent {
		if err := r.clearAbsent(s); err != nil {
			return false, Next{}, err
		}
	}
	if state != checksPending && state != checksAbsent {
		return false, Next{}, nil
	}
	next = Next{Wait: WaitChecks, Step: s.ID}
	// The text names no check, so a wait that sees other pending checks replays the same message.
	if err := r.notify(fmt.Sprintf("checks-pending.%s.%s", s.ID, h[:12]), store.MessageChecksPending, pendingText); err != nil {
		return false, Next{}, err
	}
	if state == checksPending {
		return true, next, nil
	}
	first, err := r.noteAbsent(s, h)
	if err != nil {
		return false, Next{}, err
	}
	grace, now := r.ciStartGrace(), r.now().UTC()
	if now.Sub(first) < grace {
		return true, next, nil
	}
	return true, next, r.checksMissing(s, repo, pr, h, now.Sub(first), grace)
}

// checksMissing tells the Director that head h of round k of step s has reported no check of pull request pr for
// waited, first re-triggering the pull request's CI by closing and reopening it: once per head, recording the start
// before the forge is asked and its outcome after. A start the forge may have made whose pull request still reports
// no check ci_start_grace after it is reported once more. Only the pull request's own checks count, so a run the
// forge does not link to it, such as a GitHub workflow_dispatch run on the head, neither stops the start nor
// satisfies it.
func (r *run) checksMissing(s workflow.Step, repo config.Repository, pr *pullRequestDoc, h string, waited, grace time.Duration) error {
	id := fmt.Sprintf("checks-missing.%s.%s", s.ID, h[:12])
	head := fmt.Sprintf("Step %s has seen no check reported for head %s on pull request %s for %s: the pull request has no CI run for it.", s.ID, h, pr.URL, waited.Round(time.Second))
	if repo.CIRetriggerPolicy() != config.CIRetriggerReopen {
		return r.notify(id, store.MessageChecksMissing, head+fmt.Sprintf(` No run was started: repository %s does not set ci_retrigger = "reopen", so Guildhall does not close and reopen its pull requests. Close and reopen the pull request, or push the head again; the step keeps waiting.`, repo.ID))
	}
	starter, can := r.e.Forge.(forge.CIStarter)
	if !can {
		return r.notify(id, store.MessageChecksMissing, head+" No run was started: this forge cannot start a CI run. Close and reopen the pull request, or push the head again; the step keeps waiting.")
	}
	// The record is keyed on the step and head, not the round, so a later round of the step on the same head never
	// closes and reopens the pull request again.
	name := ciStartName(s.ID, h)
	fresh := false
	var d ciStartDoc
	ok, err := r.readJSONDoc(name, &d)
	if err != nil {
		return err
	}
	var out ciStartOutcomeDoc
	known := false
	if !ok {
		// The record goes first: once it exists, no replay asks the forge again for this head.
		d = ciStartDoc{Schema: ciStartSchema, Step: s.ID, Head: h, PullRequest: pr.Number, URL: pr.URL, At: r.now().UTC()}
		if err := r.putOnce(name, name, "start.json", d); err != nil {
			return err
		}
		if _, err := r.readJSONDoc(name, &d); err != nil {
			return err
		}
		outcome, err := starter.StartCI(r.ctx, forge.Retrigger{Repository: repo.Path, Remote: pr.Remote, Number: d.PullRequest, Head: h,
			Comment: fmt.Sprintf("Guildhall closes and reopens this pull request to start its CI: GitHub reported no check for head %s for %s (%s).", h, waited.Round(time.Second), name)})
		if err != nil && r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		out = ciStartOutcomeDoc{Schema: ciStartOutcomeSchema, Record: name, Outcome: outcome}
		if err != nil {
			out = ciStartOutcomeDoc{Schema: ciStartOutcomeSchema, Record: name, Error: err.Error()}
		}
		if err := r.putOnce(name+"-outcome", name, "outcome.json", out); err != nil {
			return err
		}
		known, fresh = true, true
	} else if d.Schema != ciStartSchema || d.Head != h {
		return fmt.Errorf("document %s is not a %s record of %s", name, ciStartSchema, h)
	}
	if !known {
		if known, err = r.readJSONDoc(name+"-outcome", &out); err != nil {
			return err
		}
	}
	started := true
	var text string
	switch {
	case !known:
		text = fmt.Sprintf(" Guildhall asked the forge at %s to close and reopen the pull request to start its CI; whether it did is not recorded, so it is not asked again. Check that the pull request is open; the step keeps waiting.", d.At.Format(time.RFC3339))
	case out.Error != "":
		started = false
		text = fmt.Sprintf(" Closing and reopening the pull request to start its CI failed: %s. Nothing is tried again; check that the pull request is open, and close and reopen it or push the head again yourself; the step keeps waiting.", out.Error)
	case out.Outcome == forge.Started:
		text = " Guildhall closed and reopened the pull request, whose reopened event runs its CI on this same head; the step keeps waiting for the pull request's checks."
	case out.Outcome == forge.HeadMoved:
		started = false
		text = " The pull request is no longer at the head, so nothing was started; the step keeps waiting."
	default:
		return fmt.Errorf("document %s-outcome: unknown outcome %q", name, out.Outcome)
	}
	if err := r.notify(id, store.MessageChecksMissing, head+text); err != nil {
		return err
	}
	// The start must be older than this advance, so even a zero grace leaves the reopened run one read to appear.
	if !started || fresh || !r.now().After(d.At) || r.now().Sub(d.At) < grace {
		return nil
	}
	return r.notify(id+".after-start", store.MessageChecksMissing, fmt.Sprintf(
		"Head %s on pull request %s still reports no check of the pull request %s after Guildhall closed and reopened it to start its CI (%s): the run did not appear. Guildhall does not try again; step %s keeps waiting for you.",
		h, pr.URL, grace, name, s.ID))
}
