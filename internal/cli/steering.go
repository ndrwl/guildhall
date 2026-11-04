package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

const artisanMessagesUsage = `usage, inside an attempt (GUILDHALL_ATTEMPT set):
  guildhall messages [list] [--after <sequence>] [--limit <n>]
  guildhall messages ack <message-id>
lists or acknowledges the steering messages to this attempt's own inbox, in the copy that runs it; --store, --copy,
a recipient, read and send are refused`

// steeringCommands are the commands that, run inside an attempt, first show its unacknowledged steering messages on
// stderr (docs/messaging.md#steering-an-attempt): the commands an Artisan is granted.
var steeringCommands = map[string]bool{"ci": true, "result": true, "messages": true}

// steers reports whether command cmd with args runs inside an attempt and so first shows its steering messages:
// one of steeringCommands, with GUILDHALL_ATTEMPT set, and not asking only for help.
func (a *App) steers(cmd string, args []string) bool {
	if !steeringCommands[cmd] || a.getenv(artisan.AttemptEnv) == "" {
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
// and the copy's lock wait: its config's lock_wait, or storeWait when that does not load.
func (a *App) attemptStore(key artisan.AttemptKey) (storeRecords, error) {
	dir, err := attemptCopy(a.Getwd, key)
	if err != nil {
		return storeRecords{}, err
	}
	if dir == "" {
		return storeRecords{}, fmt.Errorf("no Guildhall copy runs attempt %s from this directory", key)
	}
	// The lock wait is a local setting, read without admission, which itself opens the store.
	wait := storeWait
	if cfg, err := config.Load(filepath.Join(dir, "config.toml")); err == nil {
		if w, err := cfg.LockWait(); err == nil && w > 0 {
			wait = w
		}
	}
	return storeRecords{path: copyStore(dir), wait: wait}, nil
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
