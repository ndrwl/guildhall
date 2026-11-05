package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

const artisanMessagesUsage = `usage, inside an attempt (GUILDHALL_ATTEMPT set):
  guildhall messages [list] [--after <sequence>] [--limit <n>]
  guildhall messages ack <message-id>
  guildhall messages hook
lists or acknowledges the steering messages to this attempt's own inbox, in the copy that runs it; --store, --copy,
a recipient, read and send are refused; hook is the Claude Artisan's PostToolUse hook, which always exits 0`

// steeringCommands are the commands that, run inside an attempt, first show its unacknowledged steering messages on
// stderr (docs/messaging.md#steering-an-attempt): the commands an Artisan is granted.
var steeringCommands = map[string]bool{"ci": true, "result": true, "messages": true}

// steers reports whether command cmd with args runs inside an attempt and so first shows its steering messages:
// one of steeringCommands, with GUILDHALL_ATTEMPT set, and not asking only for help.
func (a *App) steers(cmd string, args []string) bool {
	if !steeringCommands[cmd] || a.getenv(artisan.AttemptEnv) == "" {
		return false
	}
	// The steering hook shows the messages to the model itself, on stdout; the result hook's stderr is for warnings.
	if cmd == "messages" && len(args) > 0 && args[0] == steeringHookCommand ||
		cmd == "result" && len(args) > 0 && args[0] == resultHookCommand {
		return false
	}
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "-help" {
			return false
		}
	}
	return true
}

// attemptRecipient is the inbox of the Artisan running the attempt key.
func attemptRecipient(key artisan.AttemptKey) store.Recipient {
	return store.Recipient{Role: store.RecipientArtisan, Repository: key.Repository, TaskID: key.TaskID, Step: key.Step, Attempt: key.Attempt}
}

// attemptStore is the store of the copy running the attempt key, found from the working directory (attemptCopy),
// and the copy's lock wait (attemptWait). It never fails for the lock wait.
func (a *App) attemptStore(key artisan.AttemptKey) (storeRecords, error) {
	dir, err := attemptCopy(a.Getwd, key)
	if err != nil {
		return storeRecords{}, err
	}
	if dir == "" {
		return storeRecords{}, fmt.Errorf("no Guildhall copy runs attempt %s from this directory", key)
	}
	return storeRecords{path: copyStore(dir), wait: a.attemptWait(dir)}, nil
}

// attemptWait is the lock wait of the copy in dir: its admitted config's lock_wait (appliedConfig), so a managed.toml
// the copy has not authorized is not applied and config.toml's is used, with the refusal as one warning line. A
// config that does not read or load waits storeWait, and an admission that cannot be read waits config.toml's lock
// wait, each with a warning line; neither fails the command.
//
// Admission is skipped when there is no managed setting to admit, no managed.toml and no inline [managed], since
// appliedConfig is then config.toml alone whatever the verdict. Otherwise the store is first opened at config.toml's
// lock wait, since admission waits a fixed storeWait for it, which would hold every in-attempt command past the
// copy's own lock wait while the store is locked; a store that does not open then waits config.toml's lock wait with
// no warning of its own, as the command's read of the same store reports it. The probe narrows but does not close
// the window: a writer that takes the lock between the probe and admission's own open can still hold the command up
// to admission's fixed storeWait.
func (a *App) attemptWait(dir string) time.Duration {
	cfgPath := filepath.Join(dir, "config.toml")
	f, err := readCopyFiles(cfgPath)
	var local *config.Config
	if err == nil {
		local, err = config.Parse(f.Local)
	}
	if err != nil {
		a.warnAttemptWait(fmt.Sprintf("the copy's lock_wait not read, waiting %s: %s", storeWait, err))
		return storeWait
	}
	inline, err := config.InlineManaged(f.Local)
	local.Managed = nil
	wait := lockWaitOr(local, storeWait)
	if f.Managed == nil && err == nil && !inline {
		return wait
	}
	if path := filepath.Join(dir, directorStoreFile); pathExists(path) {
		s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true, LockTimeout: wait})
		if err != nil {
			// The command's own read of this store reports it, in one line.
			return wait
		}
		_ = s.Close()
	}
	cfg, refusal, err := appliedConfig(cfgPath)
	switch {
	case err != nil:
		a.warnAttemptWait("managed settings not checked, using config.toml alone: " + err.Error())
		return wait
	case refusal != "":
		a.warnAttemptWait("managed settings not applied, using config.toml alone: " + refusal)
	}
	return lockWaitOr(cfg, wait)
}

// pathExists reports whether path names a file that can be stat'ed.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// lockWaitOr is cfg's lock_wait, or fallback when it is unset, invalid or not positive.
func lockWaitOr(cfg *config.Config, fallback time.Duration) time.Duration {
	if w, err := cfg.LockWait(); err == nil && w > 0 {
		return w
	}
	return fallback
}

// warnAttemptWait prints why attemptStore's lock wait is not the copy's managed one as one guildhall: warning line,
// once per command.
func (a *App) warnAttemptWait(why string) {
	if a.attemptWaitWarned {
		return
	}
	a.attemptWaitWarned = true
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: %s\n", oneLine(stripControls(why)))
}

// showSteering prints, when the command runs inside an attempt, the attempt's unacknowledged steering messages to
// stderr, each in a frame. Stdout is untouched. A failure to read them, such as a store locked past the copy's lock
// wait, is a one-line warning: it never fails the command it precedes. A malformed GUILDHALL_ATTEMPT prints nothing,
// since the command itself reports it.
func (a *App) showSteering() {
	key, err := artisan.ParseAttemptKey(a.getenv(artisan.AttemptEnv))
	if err != nil {
		return
	}
	msgs, more, err := a.attemptInbox(key, 0, store.MaxInboxLimit)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: steering messages to attempt %s not read: %s\n", key, oneLine(stripControls(err.Error())))
		return
	}
	for _, m := range msgs {
		_, _ = io.WriteString(a.Stderr, steeringFrame(m))
	}
	if more {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: more than %d steering messages are unacknowledged; acknowledge these, then run guildhall messages\n", store.MaxInboxLimit)
	}
}

// attemptInbox reads the unacknowledged messages of the attempt key's own inbox, waiting the copy's lock wait.
func (a *App) attemptInbox(key artisan.AttemptKey, after uint64, limit int) ([]store.Message, bool, error) {
	records, err := a.attemptStore(key)
	if err != nil {
		return nil, false, err
	}
	return records.Inbox(attemptRecipient(key), after, limit)
}

// steeringFrame is one steering message as an Artisan sees it on stderr: a header naming its id, sender and type,
// its text with every line prefixed "| " so no text can close the frame, and the command that acknowledges it.
// The text is already bounded by store.MaxMessageTextBytes; terminal control characters are stripped from it.
func steeringFrame(m store.Message) string {
	var b strings.Builder
	fmt.Fprintf(&b, "==== guildhall: steering message %s from %s (%s) ====\n", m.ID, store.MessageSender(m), m.Type)
	for line := range strings.SplitSeq(strings.TrimRight(stripControls(m.Text), "\n"), "\n") {
		b.WriteString("| " + line + "\n")
	}
	fmt.Fprintf(&b, "==== act on it, then acknowledge it: guildhall messages ack %s ====\n", m.ID)
	return b.String()
}

// stripControls drops every control character from text but newline and tab, so a message cannot move the cursor,
// recolour or retitle the terminal it is printed to; a carriage return is dropped too.
func stripControls(text string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
}

// artisanMessages is `guildhall messages` inside an attempt: list or ack on the attempt's own inbox, which
// GUILDHALL_ATTEMPT names, in the store of the copy running it. Every other form is refused, so an Artisan reads and
// acknowledges only its own messages and sends none. It prints one JSON document and exits as messages does.
func (a *App) artisanMessages(env string, args []string) int {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	usage := func(msg string) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, artisanMessagesUsage)
		return 2
	}
	var want int
	switch sub {
	case "list":
	case "ack":
		want = 1
	default:
		return usage(fmt.Sprintf("messages %s is not available inside an attempt (%s is set)", sub, artisan.AttemptEnv))
	}
	flags := flag.NewFlagSet("messages "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	after := flags.Uint64("after", 0, "list messages after this sequence")
	limit := flags.Int("limit", 20, "list at most this many messages")
	if err := parseInterspersed(flags, args); err != nil {
		return usage(fmt.Sprintf("%v; inside an attempt, messages takes no --store, --copy or --config", err))
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case flags.NArg() != want && sub == "list":
		return usage("inside an attempt, messages lists the attempt's own inbox and takes no recipient")
	case flags.NArg() != want:
		return usage("inside an attempt, messages ack takes only the message id; the recipient is the attempt")
	case sub != "list" && (set["after"] || set["limit"]):
		return usage("--after and --limit apply only to messages list")
	}
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: messages %s: %v\n", sub, err)
		return 1
	}
	key, err := artisan.ParseAttemptKey(env)
	if err != nil {
		return fail(fmt.Errorf("%s: %w", artisan.AttemptEnv, err))
	}
	records, err := a.attemptStore(key)
	if err != nil {
		return fail(err)
	}
	to := attemptRecipient(key)
	var out any
	switch sub {
	case "list":
		out, err = withRecords(records, true, nil, func(s *store.Store) (messaging.Page, error) {
			return (&messaging.StoreMailbox{Store: s}).List(to, *after, *limit)
		})
	case "ack":
		// The store returns the original receipt when this attempt acknowledges a message again.
		out, err = withRecords(records, false, nil, func(s *store.Store) (store.Receipt, error) {
			return (&messaging.StoreMailbox{Store: s}).Ack(to, flags.Arg(0))
		})
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && sub == "ack" {
			return fail(fmt.Errorf("no message %q; guildhall messages lists this attempt's", flags.Arg(0)))
		}
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
		return 1
	}
	return 0
}

// steeringHookCommand is the messages subcommand the Claude Artisan's PostToolUse hook runs, `guildhall messages
// hook` (claude.SteeringHookCommand), which shows the model the attempt's unacknowledged steering messages right
// after each tool call.
const steeringHookCommand = "hook"

// steeringHook is `guildhall messages hook`: with GUILDHALL_ATTEMPT set and unacknowledged steering messages to the
// attempt, it prints one Claude Code PostToolUse hook document on stdout whose additionalContext is each message in
// its steeringFrame, so the model sees them after its next tool call without running a guildhall command. It reads
// the store read-only and writes nothing. With no attempt, no message or any failure it prints nothing on stdout,
// a failure being one warning line on stderr, and it always exits 0, so a hook that cannot read the store changes
// nothing: delivery in command output and the check at intake stand without it. It reads no standard input.
func (a *App) steeringHook(args []string) int {
	if len(args) > 0 {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: messages hook takes no arguments; ignored %q\n", args)
	}
	env := a.getenv(artisan.AttemptEnv)
	if env == "" {
		return 0
	}
	key, err := artisan.ParseAttemptKey(env)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: %s: %s\n", artisan.AttemptEnv, oneLine(stripControls(err.Error())))
		return 0
	}
	msgs, more, err := a.attemptInbox(key, 0, store.MaxInboxLimit)
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: steering messages to attempt %s not read: %s\n", key, oneLine(stripControls(err.Error())))
		return 0
	}
	if len(msgs) == 0 {
		return 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Guildhall: %d unacknowledged steering message(s) to this attempt. Act on each, then acknowledge it with guildhall messages ack <id>; a succeeded result is refused until every one is acknowledged.\n", len(msgs))
	for _, m := range msgs {
		b.WriteString(steeringFrame(m))
	}
	if more {
		fmt.Fprintf(&b, "More than %d are unacknowledged; acknowledge these, then run guildhall messages.\n", store.MaxInboxLimit)
	}
	type output struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}
	doc := struct {
		HookSpecificOutput output `json:"hookSpecificOutput"`
	}{output{HookEventName: "PostToolUse", AdditionalContext: b.String()}}
	if err := json.NewEncoder(a.Stdout).Encode(doc); err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: warning: messages hook: %v\n", err)
	}
	return 0
}
