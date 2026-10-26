package interp

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// sentBack records the Director's immutable feedback under the succeeded attempt's
// identity before any new launch. Replays use that record, including after a hold
// or crash; they never manufacture another request from the task's new position.
func (r *run) sentBack(ev Event) error {
	i := slices.IndexFunc(r.def.Steps, func(s workflow.Step) bool { return s.ID == ev.Step })
	a := r.attempt(ev.Step, ev.Attempt)
	if i < 0 || r.def.Steps[i].Actor() != workflow.ArtisanActor || a == nil || a.Outcome != store.AttemptSucceeded || strings.TrimSpace(ev.Reason) == "" {
		return fmt.Errorf("%w: send-back requires a succeeded Artisan attempt and nonempty feedback", ErrEvent)
	}
	name := sendBackName(ev.Step, ev.Attempt)
	if d := r.document(name); d != nil {
		if d.SHA256 != digest.Of([]byte(ev.Reason)).Hex() {
			return fmt.Errorf("%w: send-back feedback for %s attempt %d is immutable", ErrEvent, ev.Step, ev.Attempt)
		}
		return nil
	}
	if r.latestAttempt(ev.Step).Number != ev.Attempt {
		return fmt.Errorf("%w: send-back must name the latest attempt", ErrEvent)
	}
	for _, a := range r.attempts {
		if a.Outcome == "" {
			return fmt.Errorf("%w: step %s attempt %d is still running", ErrEvent, a.Step, a.Number)
		}
	}
	prepared := r.document(bindingName(ev.Step, ev.Attempt+1)) != nil
	if !prepared {
		p := filepath.Join(r.e.DocumentDir, "tasks", r.task.Repository, r.task.ID, fmt.Sprintf("%s-%d.inputs.json", ev.Step, ev.Attempt+1))
		_, err := os.Stat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		prepared = err == nil
	}
	if prepared {
		return fmt.Errorf("%w: step %s attempt %d already has prepared inputs", ErrEvent, ev.Step, ev.Attempt+1)
	}
	data := []byte(ev.Reason)
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: path.Join("tasks", r.task.Repository, r.task.ID, name+".md"), SHA256: digest.Of(data).Hex()}, data)
}

func sendBackName(step string, n int) string { return "send-back-" + step + "-" + strconv.Itoa(n) }

// sentBackFeedback returns unserved Director feedback in attempt order. Binding
// the feedback durably makes its consumption recoverable, as with loop feedback.
func (r *run) sentBackFeedback(s workflow.Step) ([]render.Input, error) {
	var out []render.Input
	for _, a := range r.attempts {
		if a.Step != s.ID {
			continue
		}
		d := r.document(sendBackName(s.ID, a.Number))
		if d == nil {
			continue
		}
		served := false
		for _, next := range r.attempts {
			if next.Step != s.ID || next.Number <= a.Number || next.Outcome != store.AttemptSucceeded {
				continue
			}
			b, err := r.binding(s.ID, next.Number)
			if err != nil {
				return nil, err
			}
			if slices.ContainsFunc(b.Inputs, func(in boundInput) bool { return in.Port == FeedbackPort && in.Path == d.Path && in.SHA256 == d.SHA256 }) {
				served = true
				break
			}
		}
		if !served {
			out = append(out, render.Input{Port: FeedbackPort, Name: d.Path, SHA256: d.SHA256, Path: filepath.Join(r.e.DocumentDir, filepath.FromSlash(d.Path))})
		}
	}
	return out, nil
}
