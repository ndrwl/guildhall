package interp

import (
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// maxAdvanceErrorBytes bounds the last error an advance-failed message quotes.
const maxAdvanceErrorBytes = 1 << 10

// AdvanceFailedMessage is the advance-failed Director message for attempt k, whose launcher's command, such as
// "step done", failed twice with lastErr the second time. Its id is one per attempt, so a repeated send is a replay.
func AdvanceFailedMessage(k artisan.AttemptKey, command, lastErr string) messaging.Envelope {
	lastErr = strings.Join(strings.Fields(lastErr), " ")
	if len(lastErr) > maxAdvanceErrorBytes {
		lastErr = strings.ToValidUTF8(lastErr[:maxAdvanceErrorBytes], "")
	}
	if lastErr == "" {
		lastErr = "no error output"
	}
	e := directorMessage(k.Repository, k.TaskID, fmt.Sprintf("advance-failed.%s-%d", k.Step, k.Attempt), store.MessageAdvanceFailed,
		fmt.Sprintf("The launcher's %s for attempt %d of step %s failed twice, so the task has not taken the attempt; its last error: %s", command, k.Attempt, k.Step, lastErr))
	e.Step, e.Attempt = k.Step, k.Attempt
	return e
}
