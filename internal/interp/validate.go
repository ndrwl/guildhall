package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// validationSchema versions the validation results validate records.
const validationSchema = "guildhall.validation-result/1"

// The verdicts of a validation result.
const (
	validationPassed = "passed"
	validationFailed = "failed"
)

// validationFailedOutcome is the outcome whose loop rule a validate step follows.
const validationFailedOutcome = "failed"

// validationDoc is a recorded validation result, <step>-<round>-result: the forge's finished checks of one
// published head. A failed result is also the feedback the failed loop gives its target.
type validationDoc struct {
	Schema string `json:"schema"`
	// Remote, Number and URL are the pull request the head was published to.
	Remote  string        `json:"remote"`
	Number  int           `json:"number"`
	URL     string        `json:"url"`
	Head    string        `json:"head"`
	Verdict string        `json:"verdict"`
	Checks  []forge.Check `json:"checks"`
	// Failing names the failed checks, in the order of Checks.
	Failing []string `json:"failing,omitempty"`
	// NoCI marks a passed result for a head the forge reports no checks for, in a repository configured no_ci.
	NoCI bool `json:"no_ci,omitempty"`
	// Rebase names the rebases validate made from the published head to Head, when it rebased a conflicting pull
	// request (docs/interpreter.md#validate); Escalation, in a failed result, is why a rebase could not stand, and
	// is the build's feedback.
	Rebase     []string       `json:"rebase,omitempty"`
	Escalation *escalationDoc `json:"rebase_escalation,omitempty"`
	// Logs holds, in a failed result, each failed check's log excerpt, or why it has none, in the order of Checks.
	Logs []logDoc `json:"logs,omitempty"`
}

// logDoc is one failed check's entry in a failed validation result's logs.
type logDoc struct {
	Check string `json:"check"`
	// Job is the check's GitHub Actions job id; empty for a check that is not an Actions job.
	Job string `json:"job,omitempty"`
	// Excerpt and Dropped are the job log's excerpt and the bytes cut from the head of the cached log, when it
	// was fetched; Unavailable is why it was not.
	Excerpt     *string `json:"excerpt,omitempty"`
	Dropped     *int64  `json:"dropped,omitempty"`
	Unavailable string  `json:"unavailable,omitempty"`
	// Command is the guildhall ci log command that serves the job's full log.
	Command string `json:"command,omitempty"`
}

// maxRoundLogs is how many failed job logs one validate round fetches.
const maxRoundLogs = 10

// failedLogs returns the logs entry of each failed check of checks, fetching at most maxRoundLogs job logs, in
// order, through the CI log cache. A log that cannot be had is an entry naming why; only a cancelled advance is
// an error.
func (r *run) failedLogs(c forge.Commit, checks []forge.Check) ([]logDoc, error) {
	reader, _ := r.e.Forge.(CheckLogReader)
	dir := cilog.Dir(r.e.StateDir, r.task.Repository, r.task.ID)
	var out []logDoc
	fetched := 0
	for _, ch := range checks {
		if ch.State != forge.CheckFailed {
			continue
		}
		l := logDoc{Check: ch.Name, Job: ch.Job}
		switch {
		case ch.Job == "":
			l.Unavailable = "not a GitHub Actions job"
		case r.e.CILogLimit <= 0:
			l.Unavailable = "log fetching is off"
		case reader == nil:
			l.Unavailable = "forge does not provide logs"
		case fetched >= maxRoundLogs:
			l.Unavailable = fmt.Sprintf("log limit of %d per round reached", maxRoundLogs)
		default:
			fetched++
			got, err := cilog.Get(r.ctx, reader, dir, c, ch.Job, r.e.CILogLimit)
			if err != nil {
				if r.ctx.Err() != nil {
					return nil, r.ctx.Err()
				}
				l.Unavailable = forge.LogReason(err)
				break
			}
			excerpt := cilog.Excerpt(got.Text)
			l.Excerpt, l.Dropped = &excerpt, &got.Dropped
		}
		if ch.Job != "" {
			l.Command = ciLogCommand(r.e.Copy, r.task.Repository, r.task.ID, ch.Job)
		}
		out = append(out, l)
	}
	return out, nil
}

// ciLogCommand is the guildhall ci log command that serves job's log for repo/task. With a copy it names the copy,
// single-quoted for a POSIX shell with any ' in it escaped, since an Artisan runs it under the copy's state/, where
// copy discovery finds no copy (docs/ci-logs.md).
func ciLogCommand(copyDir, repo, task, job string) string {
	if copyDir == "" {
		return fmt.Sprintf("guildhall ci log %s/%s %s", repo, task, job)
	}
	return fmt.Sprintf("guildhall ci log --copy '%s' %s/%s %s", strings.ReplaceAll(copyDir, "'", `'\''`), repo, task, job)
}

// parseValidation decodes a recorded validation result and refuses one of another schema or verdict.
func parseValidation(name string, data []byte) (*validationDoc, error) {
	var v validationDoc
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("document %s: %w", name, err)
	}
	if v.Schema != validationSchema || v.Verdict != validationPassed && v.Verdict != validationFailed || !digest.ValidHead(v.Head) {
		return nil, fmt.Errorf("document %s is not a %s result", name, validationSchema)
	}
	return &v, nil
}

// loopOutcome is the outcome whose loop rule step s follows: failed for validate, changes_requested otherwise.
func loopOutcome(s workflow.Step) string {
	if s.Type == "validate" {
		return validationFailedOutcome
	}
	return changesRequested
}

// validate reads the forge's checks of the published head until they finish. Pending checks, or none reported yet,
// wait; finished checks are recorded as the next round's result. A passed result for the head completes the step;
// a failed one follows the failed loop rule. While the step waits on the head's checks, an advance reads them only
// once ChecksRefresh has passed since the last read, and a failed read spends a round only when it holds a failed
// check run the latest result's failed runs do not (docs/interpreter.md#validate).
func (r *run) validate(s workflow.Step) (bool, Next, error) {
	in, err := r.input(s, "head")
	if err != nil {
		return false, Next{}, err
	}
	h, err := r.head(in)
	if err != nil {
		return false, Next{}, err
	}
	pr, err := r.published(s, h)
	switch {
	case err != nil:
		return false, Next{}, err
	case pr == nil:
		return false, Next{}, fmt.Errorf("%w: validating head %s, which no publish step pushed", ErrUnsupported, h)
	case r.e.Forge == nil:
		return false, Next{}, fmt.Errorf("%w: validating without a forge", ErrUnsupported)
	}
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	// The head validated is the published head, or the head validate rebased a conflicting pull request to.
	published := h
	links, err := r.chain(published, s.ID)
	if err != nil {
		return false, Next{}, err
	}
	h = chainHead(published, links)
	k := 1
	wait := Next{Wait: WaitChecks, Step: s.ID}
	var last *validationDoc
	if doc, source, err := r.output(s.ID, "result"); err == nil {
		data, err := r.read(doc)
		if err != nil {
			return false, Next{}, err
		}
		v, err := parseValidation(doc.Name, data)
		if err != nil {
			return false, Next{}, err
		}
		if v.Head == h {
			if v.Verdict == validationPassed {
				return true, Next{}, r.notifyValidation(s, source, *v)
			}
			done, next, again, err := r.validationFailed(s, source, *v)
			if !again || err != nil {
				return done, next, err
			}
			wait, last = next, v
		}
		p, _ := parseDecision(source)
		k = p.round + 1
	}
	if last != nil && last.Escalation != nil {
		// The rebase of this head failed and its loop is spent or served: the Director decides.
		return false, Next{Wait: WaitDirector, Step: s.ID}, nil
	}
	for {
		var stop bool
		var next Next
		if h, pr, stop, next, err = r.validateRebase(s, k, repo, published, pr); err != nil || stop {
			return false, next, err
		}
		if due, blocked, err := r.checksDue(s, h); err != nil || !due {
			if blocked {
				wait = Next{Wait: WaitDirector, Step: s.ID}
			}
			return false, wait, err
		}
		// A behind pull request reads its checks as usual: the merge queue rebases it at the front. A conflicting one
		// runs no checks, so it can never become ready: validate rebases it onto the base tip, its one rebase of a
		// pull request not at the front of the queue, and waits for the Director only when it cannot.
		m, err := r.observe(repo, pr, h)
		if err != nil {
			return false, Next{}, err
		}
		if m.State != "conflicting" {
			break
		}
		links, err := r.chain(published, s.ID)
		if err != nil {
			return false, Next{}, err
		}
		if len(links) < maxRebaseRounds {
			doc, err := r.replay(repo, h, m.Base)
			if err == nil {
				n := 1
				all, err := r.rebases(s.ID)
				if err != nil {
					return false, Next{}, err
				}
				for _, l := range all {
					if l.k == k && l.n >= n {
						n = l.n + 1
					}
				}
				if err := r.putJSONDoc(rebaseName(s.ID, k, n), decisionID(s.ID, k, false), fmt.Sprintf("rebase-%d.json", n), doc); err != nil {
					return false, Next{}, err
				}
				continue
			}
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, errRebaseRunning) {
				return false, Next{}, err
			}
		}
		// This copy cannot rebase, or validate rebased this pull request three times: the Director decides.
		if len(links) >= maxRebaseRounds {
			err = r.stuck(s, h, m, errRebaseStuck)
		} else {
			_, _, err = r.mergeBlocked(pr, h)
		}
		if err != nil {
			return false, Next{}, err
		}
		if err := r.checksRead(s, h, true); err != nil {
			return false, Next{}, err
		}
		return false, Next{Wait: WaitDirector, Step: s.ID}, nil
	}
	checks, err := r.e.Forge.Checks(r.ctx, forge.Commit{Repository: repo.Path, Remote: pr.Remote, Head: h})
	if err != nil {
		return false, Next{}, err
	}
	if err := r.checksRead(s, h, false); err != nil {
		return false, Next{}, err
	}
	v := validationDoc{Schema: validationSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: h, Verdict: validationPassed, Checks: checks}
	if h != published {
		links, err := r.chain(published, s.ID)
		if err != nil {
			return false, Next{}, err
		}
		v.Rebase = linkNames(links)
	}
	if v.Checks == nil {
		v.Checks = []forge.Check{}
	}
	finished := len(checks) > 0 || repo.NoCI
	v.NoCI = len(checks) == 0 && repo.NoCI
	for _, c := range checks {
		switch c.State {
		case forge.CheckFailed:
			v.Verdict, v.Failing = validationFailed, append(v.Failing, c.Name)
		case forge.CheckPending:
			finished = false
		}
	}
	if v.Verdict == validationPassed && !finished {
		// The text names no check, so a wait that sees other pending checks replays the same message.
		return false, Next{Wait: WaitChecks, Step: s.ID}, r.notify(fmt.Sprintf("checks-pending.%s.%s", s.ID, h[:12]), store.MessageChecksPending,
			fmt.Sprintf("Step %s waits for the checks of head %s on pull request %s to finish. Advancing the task again, such as with task start, reads them again.", s.ID, h, pr.URL))
	}
	if last != nil && v.Verdict == validationFailed && !newFailure(v.Checks, last.Checks) {
		return false, wait, nil
	}
	if v.Verdict == validationFailed {
		// Only a round about to be recorded fetches logs; the result, recorded once, is the only write.
		if v.Logs, err = r.failedLogs(forge.Commit{Repository: repo.Path, Remote: pr.Remote, Head: h}, checks); err != nil {
			return false, Next{}, err
		}
	}
	id := decisionID(s.ID, k, false)
	data, err := json.Marshal(v)
	if err != nil {
		return false, Next{}, err
	}
	if err := r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: id + "-result",
		Path: path.Join("tasks", r.task.Repository, r.task.ID, id, "result.json"), SHA256: digest.Of(data).Hex()}, data); err != nil {
		return false, Next{}, err
	}
	if v.Verdict == validationPassed {
		return true, Next{}, r.notifyValidation(s, id, v)
	}
	done, next, _, err := r.validationFailed(s, id, v)
	return done, next, err
}

// validateRebase brings the rebases validate step s made of the published head up to date and returns the head they
// end at, with its pull request: an unresolved minor_conflicts rebase is resolved by the rebaser and a narrowed
// review, and a rebased head is pushed to the same pull request. A major_conflicts rebase, a behaviour change, a
// resolution the narrowed review did not approve cleanly or a failed rebaser fails the round, recorded as a failed
// result that the failed loop gives the build as feedback; stop then reports that validate waits, with next.
func (r *run) validateRebase(s workflow.Step, k int, repo config.Repository, published string, pr *pullRequestDoc) (string, *pullRequestDoc, bool, Next, error) {
	for {
		links, err := r.chain(published, s.ID)
		if err != nil {
			return "", nil, false, Next{}, err
		}
		cur := chainHead(published, links)
		all, err := r.rebases(s.ID)
		if err != nil {
			return "", nil, false, Next{}, err
		}
		if i := slices.IndexFunc(all, func(l rebaseLink) bool { return l.doc.From == cur && l.doc.Kind == config.RebaseMajorConflicts }); i >= 0 {
			d := all[i].doc
			files := []string{}
			for _, c := range d.Conflicts {
				files = append(files, c.Path)
			}
			next, err := r.rebaseFailed(s, k, cur, pr, links, escalationDoc{From: cur, Onto: d.Onto, Kind: d.Kind, Files: files, Reason: d.Reason, Carry: true})
			return cur, pr, true, next, err
		}
		// At validate there is no approval to carry over, so a narrowed review's findings fail the round.
		if i := slices.IndexFunc(links, func(l rebaseLink) bool { return l.findings > 0 }); i >= 0 {
			l := links[i]
			next, err := r.rebaseFailed(s, k, l.doc.From, pr, links, escalationDoc{From: l.doc.From, Onto: l.doc.Onto, Kind: config.RebaseMajorConflicts,
				Reason: fmt.Sprintf("the narrowed review of the resolution approved it with %d finding(s) or suggestion(s)", l.findings)})
			return cur, pr, true, next, err
		}
		if n := len(links); n > 0 && links[n-1].head == "" {
			next, fail, err := r.resolveRebase(s, repo, links[n-1])
			switch {
			case err != nil:
				return "", nil, false, Next{}, err
			case fail != nil:
				next, err := r.rebaseFailed(s, k, fail.From, pr, links, *fail)
				return cur, pr, true, next, err
			case next != (Next{}):
				return cur, pr, true, next, nil
			}
			continue
		}
		if cur != published {
			if pr, err = r.publishLink(s, repo, links[len(links)-1], pr); err != nil {
				return "", nil, false, Next{}, err
			}
		}
		return cur, pr, false, Next{}, nil
	}
}

// rebaseFailed records round k of validate step s as a failed result for head cur, whose rebase e could not stand,
// tells the Director, and follows the failed loop, which gives the build the result, naming the base, the
// conflicting files and the reason, as feedback.
func (r *run) rebaseFailed(s workflow.Step, k int, cur string, pr *pullRequestDoc, links []rebaseLink, e escalationDoc) (Next, error) {
	e.Schema = escalationSchema
	if e.Files == nil {
		e.Files = []string{}
	}
	id := decisionID(s.ID, k, false)
	v := validationDoc{Schema: validationSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: cur, Verdict: validationFailed,
		Checks: []forge.Check{}, Failing: []string{"rebase"}, Rebase: linkNames(links), Escalation: &e}
	if r.document(id+"-result") == nil {
		if err := r.putJSONDoc(id+"-result", id, "result.json", v); err != nil {
			return Next{}, err
		}
	}
	text := fmt.Sprintf("Validate could not rebase conflicting pull request %s at head %s onto %s (%s: %s); conflicting files: %s. The failed loop gives the build this as feedback.",
		pr.URL, cur, e.Onto, e.Kind, e.Reason, strings.Join(e.Files, ", "))
	if err := r.notify("rebase-escalated."+id, store.MessageRebaseEscalated, text); err != nil {
		return Next{}, err
	}
	_, next, _, err := r.validationFailed(s, id, v)
	return next, err
}

// linkNames names the rebases of links, in order.
func linkNames(links []rebaseLink) []string {
	var out []string
	for _, l := range links {
		out = append(out, l.name)
	}
	return out
}

// newFailure reports whether checks hold a failed run, by check name and run id, that is not among the failed runs
// of last: a new failure or a failed re-run, the only failed evidence a further round is spent on.
func newFailure(checks, last []forge.Check) bool {
	return slices.ContainsFunc(checks, func(c forge.Check) bool {
		return c.State == forge.CheckFailed && !slices.ContainsFunc(last, func(l forge.Check) bool {
			return l.State == forge.CheckFailed && l.Name == c.Name && l.ID == c.ID
		})
	})
}

// checksRead is when validate step s last read the checks of Head, kept in <state>/<repo>/<task>/<step>.checks.json.
type checksRead struct {
	Head    string    `json:"head"`
	ReadAt  time.Time `json:"read_at"`
	Blocked bool      `json:"blocked,omitempty"`
}

func (r *run) checksReadPath(s workflow.Step) string {
	return filepath.Join(r.e.StateDir, r.task.Repository, r.task.ID, s.ID+".checks.json")
}

// checksDue reports whether validate step s may read the checks of head h: always without a ChecksRefresh, and
// otherwise unless it read them less than ChecksRefresh ago.
func (r *run) checksDue(s workflow.Step, h string) (bool, bool, error) {
	if r.e.ChecksRefresh <= 0 {
		return true, false, nil
	}
	data, err := atomicfile.Read(r.checksReadPath(s))
	if errors.Is(err, fs.ErrNotExist) {
		return true, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var c checksRead
	if err := json.Unmarshal(data, &c); err != nil {
		return false, false, fmt.Errorf("%s: %w", r.checksReadPath(s), err)
	}
	now := r.now()
	return c.Head != h || c.ReadAt.After(now) || now.Sub(c.ReadAt) >= r.e.ChecksRefresh, c.Head == h && c.Blocked, nil
}

// checksRead records that validate step s read the checks of head h now, when a ChecksRefresh paces the reads.
func (r *run) checksRead(s workflow.Step, h string, blocked bool) error {
	if r.e.ChecksRefresh <= 0 {
		return nil
	}
	data, err := json.Marshal(checksRead{Head: h, ReadAt: r.now().UTC(), Blocked: blocked})
	if err != nil {
		return err
	}
	p := r.checksReadPath(s)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(p, data)
}

// replayValidation resends the message of validate step s's latest result. A failed result sends the task back
// to its loop's target before the walk reaches s again, so a message lost to a crash after the result was recorded
// would otherwise never be sent.
func (r *run) replayValidation(s workflow.Step) error {
	doc, source, err := r.output(s.ID, "result")
	if err != nil {
		return nil
	}
	data, err := r.read(doc)
	if err != nil {
		return err
	}
	v, err := parseValidation(doc.Name, data)
	if err != nil {
		return err
	}
	return r.notifyValidation(s, source, *v)
}

// notifyValidation tells the Director the result recorded as round id of step s.
func (r *run) notifyValidation(s workflow.Step, id string, v validationDoc) error {
	if v.NoCI {
		return r.notify("checks-passed."+id, store.MessageChecksPassed, fmt.Sprintf("Head %s on pull request %s has no CI to wait for: repository %s is configured with no_ci (step %s, %s).", v.Head, v.URL, r.task.Repository, s.ID, id))
	}
	if v.Verdict == validationPassed {
		return r.notify("checks-passed."+id, store.MessageChecksPassed, fmt.Sprintf("The checks of head %s on pull request %s passed (step %s, %s).", v.Head, v.URL, s.ID, id))
	}
	then := "the Director decides what happens next."
	if rule, ok := s.On[validationFailedOutcome]; ok {
		then = fmt.Sprintf("the failed loop sends the task back to step %s with the result as feedback, at most %d times.", rule.Goto, rule.Max)
	}
	return r.notify("checks-failed."+id, store.MessageChecksFailed, fmt.Sprintf("The checks of head %s on pull request %s failed (step %s, %s): %s; %s",
		v.Head, v.URL, s.ID, id, strings.Join(v.Failing, ", "), then))
}

// validationFailed follows the failed result v, recorded as round id of step s: without a loop rule the Director
// decides; past the rule's max the loop-exit decision is asked; within it the loop's target runs with the result as
// feedback. again reports that the step then waits on the head's checks, so they are read again: without a rule,
// after a rejected loop exit, or once the target served the result and left the head unchanged; next is that wait.
func (r *run) validationFailed(s workflow.Step, id string, v validationDoc) (done bool, next Next, again bool, err error) {
	if err := r.notifyValidation(s, id, v); err != nil {
		return false, Next{}, false, err
	}
	rule, ok := s.On[validationFailedOutcome]
	if !ok {
		return false, Next{Wait: WaitDirector, Step: s.ID}, true, nil
	}
	fs, err := r.firings(s)
	if err != nil {
		return false, Next{}, false, err
	}
	n := len(fs)
	subject := &store.Subject{Kind: store.SubjectHead, Digest: v.Head}
	if n > r.allowed(s) {
		done, next, err := r.exit(s, n, subject, "failing checks: "+strings.Join(v.Failing, ", "))
		return done, next, next.Wait == WaitDirector, err
	}
	i := slices.IndexFunc(r.def.Steps, func(t workflow.Step) bool { return t.ID == rule.Goto })
	target := r.def.Steps[i]
	if actor := target.Actor(); actor != workflow.ArtisanActor {
		return false, Next{}, false, fmt.Errorf("%w: loops back to steps run by the %s", ErrUnsupported, actor)
	}
	served, err := r.served(target.ID, fs[n-1])
	switch {
	case err != nil:
		return false, Next{}, false, err
	case served:
		return false, Next{Wait: WaitChecks, Step: s.ID}, true, nil
	}
	next, err = r.start(target)
	return false, next, false, err
}

// validated checks, before deliver, that its validation input is a passed result for the delivered head.
func (r *run) validated(deliver workflow.Step, h string) error {
	if len(deliver.Inputs["validation"]) == 0 {
		return nil
	}
	in, err := r.input(deliver, "validation")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrValidationGate, err)
	}
	data, err := r.read(store.DocumentRef{Name: in.Name, Path: in.Name, SHA256: in.SHA256})
	if err != nil {
		return err
	}
	v, err := parseValidation(in.Name, data)
	if err != nil {
		return err
	}
	if v.Verdict != validationPassed || v.Head != h {
		return fmt.Errorf("%w: %s is a %s result for head %s, not a passed one for %s", ErrValidationGate, in.Name, v.Verdict, v.Head, h)
	}
	return nil
}
