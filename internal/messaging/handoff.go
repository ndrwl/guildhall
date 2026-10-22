package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// HandoffSuffix is what a wake line carries when a handoff is due: "; handoff due (<reasons>)".
const HandoffSuffix = "; handoff due ("

// DueHandoffs returns the crossings of session d's task and context triggers that hold and are not yet announced
// (docs/director.md#automatic-handoff), context first: the task trigger's crossing k once k × AfterTasks tasks
// have finished, reasoned "<n> tasks finished"; the context trigger's one crossing once the report at
// ContextFile says at least ContextPercent used, reasoned "context <n>%". A trigger whose threshold is 0 never
// holds, nor does the context trigger with no ContextFile, or one missing or unreadable, which is not an error.
func DueHandoffs(st WakeStore, d store.DirectorSession) ([]store.HandoffDue, error) {
	return nil, errors.New("messaging: DueHandoffs: not implemented")
}

// HandoffLine is line with the suffix naming dues' reasons, joined with ", ", or line itself with no dues.
func HandoffLine(line string, dues []store.HandoffDue) string {
	return line
}

// WakeIdle is the idle trigger (docs/director.md#automatic-handoff): the session supervisor calls it once the
// Director has been quiet for idle. With the newest session running or recovering and a bound pane, it types
// WakeLine of the unread counts with the suffix of the idle trigger's next crossing, reasoned "idle <idle>",
// and of DueHandoffs, records each crossing with no message id and returns them. Otherwise, or when typing
// fails, it types or records nothing and returns none and why, as a queued wake's reason says it.
func WakeIdle(ctx context.Context, st WakeStore, pane DirectorPane, idle time.Duration) ([]store.HandoffDue, string, error) {
	return nil, "", errors.New("messaging: WakeIdle: not implemented")
}
