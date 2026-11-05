package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// resultHookCommand is the result subcommand an agent's stop hook runs, `guildhall result hook`, which nudges an
// Artisan about to stop without a valid result once to submit one (docs/contracts/cli.md).
const resultHookCommand = "hook"

// resultHookLockWait bounds how long the hook waits for the store's lock, below the 10 seconds Claude Code gives
// a hook (claude.SteeringHookTimeout), so a store held past it is a warning, not a hook Claude Code gives up on.
const resultHookLockWait = 5 * time.Second

// maxHookInput bounds the hook input the result hook reads from stdin.
const maxHookInput = 1 << 20

// resultHook is `guildhall result hook`: run by an agent's stop hook with GUILDHALL_ATTEMPT set, it sends the attempt
// Guildhall's one nudge to submit its result when it is about to stop without one, and asks the agent to go on.
//
// It does nothing, printing nothing, when the attempt is cancelled, when model/result.json is a valid result for the
// attempt (adapter.ReadyDraft, the check the Claude wrapper publishes by: an accepted submission, a direct write and
// the 3rd rejection all count), when the hook input on stdin says stop_hook_active, or when the nudge was already
// sent (store.SubmitNudgeID). Otherwise it sends store.MessageNudge through interp.GuildhallOutbox, waiting the
// copy's lock wait bounded by resultHookLockWait, and prints one Claude Code Stop hook decision, block, whose reason
// is the nudge in its steeringFrame.
//
// Any failure, such as no attempt, no copy, unreadable stdin or a store locked or refusing the send, is one warning
// line on stderr with nothing on stdout. It always exits 0: exit 2 would tell Claude Code to keep going, and a
// broken hook must change nothing, the attempt ending as it would with no hook at all.
func (a *App) resultHook(args []string) int {
	warn := func(format string, args ...any) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: result hook: %s\n", oneLine(stripControls(fmt.Sprintf(format, args...))))
		return 0
	}
	if len(args) > 0 {
		return warn("takes no arguments; got %q, so no nudge is sent", args)
	}
	env := a.getenv(artisan.AttemptEnv)
	if env == "" {
		return warn("%s is not set; it runs inside a Guildhall attempt", artisan.AttemptEnv)
	}
	key, err := artisan.ParseAttemptKey(env)
	if err != nil {
		return warn("%s: %v", artisan.AttemptEnv, err)
	}
	active, err := stopHookActive(a.Stdin)
	if err != nil {
		return warn("hook input: %v", err)
	}
	dir, err := attemptCopy(a.Getwd, key)
	if err != nil {
		return warn("%v", err)
	}
	if dir == "" {
		return warn("no Guildhall copy runs attempt %s from this directory", key)
	}
	attemptDir := key.Dir(filepath.Join(dir, "state"))
	id := adapter.Identity{TaskID: key.TaskID, AttemptID: key.AttemptID()}
	if cancelled, err := adapter.Cancelled(filepath.Join(attemptDir, artisan.ResultFile), id); err != nil {
		return warn("attempt %s: %v", key, err)
	} else if cancelled {
		return 0
	}
	if _, ok := adapter.ReadyDraft(filepath.Join(attemptDir, artisan.ModelDir, artisan.ResultFile), id); ok {
		return 0
	}
	// Claude Code is already going on because of a stop hook: this is the stop after the nudge.
	if active {
		return 0
	}
	records, err := a.attemptStore(key)
	if err != nil {
		return warn("%v", err)
	}
	records.wait = min(records.wait, resultHookLockWait)
	to := attemptRecipient(key)
	nudgeID := store.SubmitNudgeID(key.TaskID, key.AttemptID())
	e := messaging.Envelope{Schema: messaging.SchemaPrefix + strconv.Itoa(messaging.SchemaVersion), ID: nudgeID,
		Recipient: store.RecipientArtisan, Repository: key.Repository, Task: key.TaskID, Step: key.Step, Attempt: key.Attempt,
		Type: store.MessageNudge, Text: submitNudgeText(nudgeID, filepath.Join(attemptDir, artisan.ModelDir, artisan.DraftFile))}
	type sent struct {
		m   store.Message
		new bool
	}
	got, err := withRecords(records, false, nil, func(s *store.Store) (sent, error) {
		m, err := s.Message(nudgeID)
		if err == nil {
			if m.To() != to {
				return sent{}, fmt.Errorf("message id %s is taken by a message to %s", nudgeID, messaging.FormatRecipient(m.To()))
			}
			return sent{m: m}, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return sent{}, err
		}
		if _, err := (interp.GuildhallOutbox{Mailbox: &messaging.StoreMailbox{Store: s}}).Send(to, e); err != nil {
			return sent{}, err
		}
		m, err = s.Message(nudgeID)
		return sent{m: m, new: true}, err
	})
	if err != nil {
		return warn("nudge to attempt %s not sent: %v", key, err)
	}
	if !got.new {
		return 0
	}
	reason := "Guildhall: this attempt has no valid result yet, and it fails if the session ends without one. Do not stop yet.\n" + steeringFrame(got.m)
	doc := struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{"block", reason}
	if err := json.NewEncoder(a.Stdout).Encode(doc); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: result hook: %v\n", err)
	}
	return 0
}

// submitNudgeText is the text of the nudge with id to an attempt whose draft file is draft: acknowledge it, then
// submit, or submit failed with a reason.
func submitNudgeText(id, draft string) string {
	return "You are about to stop without a valid result for this attempt, and an attempt that ends without one fails.\n" +
		"1. Acknowledge this message first: guildhall messages ack " + id + " (a succeeded result is refused while it is unacknowledged).\n" +
		"2. Write your result to the draft file, " + draft + ", and run: guildhall result submit " + draft + "\n" +
		"   On a rejection, fix what it names and submit again.\n" +
		"3. If you cannot finish the work, submit a result with status failed and a summary saying why.\n" +
		"Guildhall sends this reminder once per attempt."
}

// stopHookActive reads the stop hook's input, a JSON object, from r and reports its stop_hook_active field: true
// when Claude Code is already going on because of a stop hook. No input, or a nil r, is false.
func stopHookActive(r io.Reader) (bool, error) {
	if r == nil {
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, maxHookInput+1))
	if err != nil {
		return false, err
	}
	if len(data) > maxHookInput {
		return false, fmt.Errorf("larger than %d bytes", maxHookInput)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return false, nil
	}
	var in struct {
		StopHookActive bool `json:"stop_hook_active"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return false, err
	}
	return in.StopHookActive, nil
}
