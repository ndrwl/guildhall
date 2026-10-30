package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// HandoffSuffix starts what a wake line carries when a handoff is due: "; handoff due (<reasons>)".
const HandoffSuffix = session.DueSuffix

// DueHandoffs returns the crossings of session d's task and context triggers that hold and are not yet announced
// (docs/director.md#automatic-handoff), context first: the task trigger's crossing k once k × AfterTasks tasks
// have finished, reasoned "<n> tasks finished"; the context trigger's one crossing once the report at
// ContextFile says at least ContextPercent used, reasoned "context <n>%". A trigger whose threshold is 0 never
// holds, nor does the context trigger with no ContextFile, or one missing or unreadable, which is not an error.
func DueHandoffs(st WakeStore, d store.DirectorSession) ([]store.HandoffDue, error) {
	p := d.Handoff
	if p.AfterTasks <= 0 && (p.ContextPercent <= 0 || p.ContextFile == "") {
		return nil, nil
	}
	announced, err := st.HandoffDues(d.Number)
	if err != nil {
		return nil, err
	}
	seen := func(t store.HandoffTrigger, crossing int) bool {
		for _, a := range announced {
			if a.Trigger == t && a.Crossing == crossing {
				return true
			}
		}
		return false
	}
	var out []store.HandoffDue
	if p.ContextPercent > 0 && p.ContextFile != "" && !seen(store.HandoffContext, 1) {
		if c, err := session.ReadContext(p.ContextFile); err == nil && c.UsedPercent >= p.ContextPercent {
			out = append(out, store.HandoffDue{Session: d.Number, Trigger: store.HandoffContext, Crossing: 1,
				Reason: fmt.Sprintf("context %d%%", c.UsedPercent)})
		}
	}
	if p.AfterTasks > 0 {
		n, err := st.FinishedTasks(d.Number)
		if err != nil {
			return nil, err
		}
		if k := n / p.AfterTasks; k > 0 && !seen(store.HandoffTasks, k) {
			out = append(out, store.HandoffDue{Session: d.Number, Trigger: store.HandoffTasks, Crossing: k, Reason: tasksFinished(n)})
		}
	}
	return out, nil
}

func tasksFinished(n int) string {
	if n == 1 {
		return "1 task finished"
	}
	return fmt.Sprintf("%d tasks finished", n)
}

// appendIdle appends the idle trigger's next crossing in session n to dues.
func appendIdle(st WakeStore, n int, dues []store.HandoffDue, idle time.Duration) ([]store.HandoffDue, error) {
	announced, err := st.HandoffDues(n)
	if err != nil {
		return nil, err
	}
	crossing := 1
	for _, a := range announced {
		if a.Trigger == store.HandoffIdle {
			crossing = max(crossing, a.Crossing+1)
		}
	}
	return append(dues, store.HandoffDue{Session: n, Trigger: store.HandoffIdle, Crossing: crossing, Reason: "idle " + idle.String()}), nil
}

// recordDues records each typed crossing with the message whose wake carried it, if any. A conflict is a
// concurrent wake that recorded the crossing first, which stands.
func recordDues(st WakeStore, dues []store.HandoffDue, messageID string) error {
	for _, d := range dues {
		d.MessageID = messageID
		if _, err := st.RecordHandoffDue(d); err != nil && !errors.Is(err, store.ErrMutationConflict) {
			return err
		}
	}
	return nil
}

// HandoffLine is line with the suffix naming dues' reasons, joined with ", ", or line itself with no dues.
func HandoffLine(line string, dues []store.HandoffDue) string {
	if len(dues) == 0 {
		return line
	}
	reasons := make([]string, len(dues))
	for i, d := range dues {
		reasons[i] = d.Reason
	}
	return session.DueLine(line, reasons)
}

// WakeIdle is the idle trigger (docs/director.md#automatic-handoff): the session supervisor calls it once the
// Director has been quiet for idle. With the newest session running or recovering and a bound pane, it types
// WakeLine of the unread counts with the suffix of DueHandoffs and of the idle trigger's next crossing, reasoned
// "idle <idle>", records each crossing with no message id and returns them. Otherwise, or when typing fails, it
// types or records nothing and returns none and why, as a queued wake's reason says it.
func WakeIdle(ctx context.Context, st WakeStore, pane DirectorPane, idle time.Duration) ([]store.HandoffDue, string, error) {
	if idle <= 0 {
		return nil, "", errors.New("messaging: idle wake: the idle period must be positive")
	}
	reason, dues, err := typeWake(ctx, st, pane, idle)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	return dues, "", recordDues(st, dues, "")
}
