package interp

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

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

// validate reads the forge's checks of the published head at each advance until they finish. Pending checks, or
// none reported yet, wait; finished checks are recorded as the next round's result. A passed result for the head
// completes the step; a failed one follows the failed loop rule (docs/interpreter.md#validate).
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
	k := 1
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
			// A failed result whose loop target already ran and left the head unchanged has its checks read again.
			if done, next, again, err := r.validationFailed(s, source, *v); !again || err != nil {
				return done, next, err
			}
		}
		p, _ := parseDecision(source)
		k = p.round + 1
	}
	checks, err := r.e.Forge.Checks(r.ctx, forge.Commit{Repository: repo.Path, Remote: pr.Remote, Head: h})
	if err != nil {
		return false, Next{}, err
	}
	v := validationDoc{Schema: validationSchema, Remote: pr.Remote, Number: pr.Number, URL: pr.URL, Head: h, Verdict: validationPassed, Checks: checks}
	if v.Checks == nil {
		v.Checks = []forge.Check{}
	}
	finished := len(checks) > 0
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
		return false, Next{Wait: WaitChecks, Step: s.ID}, r.notify(fmt.Sprintf("checks-pending.%s.%s", s.ID, h[:12]), "checks-pending",
			fmt.Sprintf("Step %s waits for the checks of head %s on pull request %s to finish. Advancing the task again, such as with task start, reads them again.", s.ID, h, pr.URL))
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
	if v.Verdict == validationPassed {
		return r.notify("checks-passed."+id, "checks-passed", fmt.Sprintf("The checks of head %s on pull request %s passed (step %s, %s).", v.Head, v.URL, s.ID, id))
	}
	then := "the Director decides what happens next."
	if rule, ok := s.On[validationFailedOutcome]; ok {
		then = fmt.Sprintf("the failed loop sends the task back to step %s with the result as feedback, at most %d times.", rule.Goto, rule.Max)
	}
	return r.notify("checks-failed."+id, "checks-failed", fmt.Sprintf("The checks of head %s on pull request %s failed (step %s, %s): %s; %s",
		v.Head, v.URL, s.ID, id, strings.Join(v.Failing, ", "), then))
}

// validationFailed follows the failed result v, recorded as round id of step s: without a loop rule the Director
// decides; past the rule's max the loop-exit decision is asked; within it the loop's target runs with the result as
// feedback. again reports a target that already served the result, so the head's checks are read again.
func (r *run) validationFailed(s workflow.Step, id string, v validationDoc) (done bool, next Next, again bool, err error) {
	if err := r.notifyValidation(s, id, v); err != nil {
		return false, Next{}, false, err
	}
	rule, ok := s.On[validationFailedOutcome]
	if !ok {
		return false, Next{Wait: WaitDirector, Step: s.ID}, false, nil
	}
	fs, err := r.firings(s)
	if err != nil {
		return false, Next{}, false, err
	}
	n := len(fs)
	subject := &store.Subject{Kind: store.SubjectHead, Digest: v.Head}
	if n > r.allowed(s) {
		done, next, err := r.exit(s, n, subject, "failing checks: "+strings.Join(v.Failing, ", "))
		return done, next, false, err
	}
	i := slices.IndexFunc(r.def.Steps, func(t workflow.Step) bool { return t.ID == rule.Goto })
	target := r.def.Steps[i]
	if actor := target.Actor(); actor != workflow.ArtisanActor {
		return false, Next{}, false, fmt.Errorf("%w: loops back to steps run by the %s", ErrUnsupported, actor)
	}
	served, err := r.served(target.ID, fs[n-1])
	if err != nil || served {
		return false, Next{}, served, err
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
