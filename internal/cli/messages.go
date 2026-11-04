package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

const messagesUsage = `usage:
  guildhall messages send --store <path> [--config <path>] <recipient> <envelope-file|->
  guildhall messages list --store <path> [--after <sequence>] [--limit <n>] <recipient>
  guildhall messages read --store <path> <message-id>
  guildhall messages ack --store <path> <recipient> <message-id>
  guildhall messages list --store <path> --from <sender> [--after <sequence>] [--limit <n>]
  guildhall messages read --store <path> --sender <sender> <message-id>
  guildhall messages ack --store <path> --sender <sender> director <message-id>
  guildhall messages summary [--copy <dir>]
  guildhall messages receive --copy <dir>
  guildhall messages receive --from <peer> --to <director> [--copy <dir>] <envelope-file|->
  guildhall messages send --to <peer> [--copy <dir>] <envelope-file|->
  guildhall messages peer setup <peer> [--copy <dir>]
  guildhall messages inbox close --sender <sender> --reason <text> [--copy <dir>]
  guildhall messages inbox open --sender <sender> [--copy <dir>]
  guildhall messages outbox [--all] [--copy <dir>]
a recipient is "director", "steward" (a Steward's store only, from the Guildmaster only) or "artisan:<repository>/<task>/<step>/<attempt>"
an external sender is "steward" or a peer Director's name: --from and --sender select external messages, which messages receive records
a peer is a [[peer]] of the copy's config.toml
--copy <dir>, or the copy found from the current directory, may stand in for --store and --config: the copy's director.store, or a Steward copy's steward.store, and config.toml`

// messages runs a messages subcommand against an existing store and prints its result as one JSON document.
// It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) messages(args []string) int {
	// The Artisan's after-tool-use hook never fails, inside an attempt or not (docs/messaging.md#steering-an-attempt).
	if len(args) > 0 && args[0] == steeringHookCommand {
		return a.steeringHook(args[1:])
	}
	// Inside an attempt, only the attempt's own inbox is reachable (docs/messaging.md#steering-an-attempt).
	if env := a.getenv(artisan.AttemptEnv); env != "" {
		return a.artisanMessages(env, args)
	}
	if len(args) == 0 {
		return a.messagesUsage("missing messages subcommand")
	}
	sub := args[0]
	switch sub {
	case "receive":
		return a.messagesReceive(args[1:])
	case "summary":
		return a.messagesSummaryCmd(args[1:])
	case "outbox":
		return a.messagesOutboxCmd(args[1:])
	case "inbox":
		return a.messagesInboxCmd(args[1:])
	case "peer":
		return a.messagesPeerCmd(args[1:])
	case "send":
		// --to names a peer Director: the Director message form of send, beside the positional-recipient one.
		if slices.ContainsFunc(args[1:], func(s string) bool {
			return s == "--to" || s == "-to" || strings.HasPrefix(s, "--to=") || strings.HasPrefix(s, "-to=")
		}) {
			return a.messagesSendPeer(args[1:])
		}
	}
	flags := flag.NewFlagSet("messages "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	cfgPath := flags.String("config", "", "Director config file")
	var cp copyFlags
	cp.register(flags)
	after := flags.Uint64("after", 0, "list messages after this sequence")
	limit := flags.Int("limit", 20, "list at most this many messages")
	from := flags.String("from", "", "list the external messages from this sender")
	sender := flags.String("sender", "", "the external sender of the message to read or acknowledge")
	var want int
	switch sub {
	case "send", "ack":
		want = 2
	case "list", "read":
		want = 1
	default:
		return a.messagesUsage(fmt.Sprintf("unknown messages subcommand %q", sub))
	}
	if err := parseInterspersed(flags, args[1:]); err != nil {
		return a.messagesUsage(err.Error())
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	// An external message is selected by its exact sender, never guessed: list --from, read and ack --sender.
	external := set["from"] || set["sender"]
	if sub == "list" && set["from"] {
		want = 0
	}
	cerr := cp.apply(a, path, cfgPath, nil)
	switch {
	case cerr != nil:
		return a.messagesUsage(cerr.Error())
	case cp.searched:
		return a.messagesUsage(cp.missing(a).Error())
	case *path == "":
		return a.messagesUsage("--store is required, or --copy")
	case flags.NArg() != want:
		return a.messagesUsage(fmt.Sprintf("messages %s takes %d arguments", sub, want))
	case sub != "list" && (set["after"] || set["limit"]):
		return a.messagesUsage("--after and --limit apply only to messages list")
	case sub != "send" && set["config"]:
		return a.messagesUsage("--config applies only to messages send")
	case set["from"] && sub != "list":
		return a.messagesUsage("--from applies only to messages list; read and ack take --sender")
	case set["sender"] && sub != "read" && sub != "ack":
		return a.messagesUsage("--sender applies only to messages read and ack; list takes --from")
	case external && store.ValidateExternalSender(*from+*sender) != nil:
		return a.messagesUsage(fmt.Sprintf("an external sender is %s or a peer Director's name, not %q", store.SenderSteward, *from+*sender))
	case sub == "ack" && set["sender"] && flags.Arg(0) != string(store.RecipientDirector):
		return a.messagesUsage(fmt.Sprintf("an external message is acknowledged by %q, not %q", store.RecipientDirector, flags.Arg(0)))
	case external && filepath.Base(*path) == stewardStoreFile:
		return a.messagesUsage(fmt.Sprintf("%s is the Steward's store, which has no external inbox", *path))
	}
	if external {
		return a.messagesExternal(sub, *path, *from+*sender, *after, *limit, flags.Args())
	}
	pos := flags.Args()
	var to store.Recipient
	if sub != "read" {
		var err error
		if to, err = messaging.ParseRecipient(pos[0]); err != nil {
			return a.messagesUsage(err.Error())
		}
	}
	// A store's session inbox is its session's: the Steward's in a Steward store, the Director's in any other.
	steward := filepath.Base(*path) == stewardStoreFile
	switch {
	case sub == "send" && steward && to.Role == store.RecipientDirector:
		return a.messagesUsage(fmt.Sprintf("%s is the Steward's store, whose session inbox is %q", *path, store.RecipientSteward))
	case sub == "send" && !steward && to.Role == store.RecipientSteward:
		return a.messagesUsage(fmt.Sprintf("only a Steward's store, %s, has the %q inbox; %s is not one", stewardStoreFile, store.RecipientSteward, *path))
	}

	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: messages %s: %v\n", sub, err)
		return 1
	}
	var env messaging.Envelope
	if sub == "send" {
		var err error
		if env, err = a.readEnvelope(pos[1]); err != nil {
			return fail(err)
		}
		// The Steward's inbox is about the Steward store's own reserved repository, which no config lists.
		if to.Role != store.RecipientSteward {
			if err := listedEnvelope(*cfgPath, set["config"], env); err != nil {
				return fail(err)
			}
		}
	}
	// Preserve the CLI's missing-store diagnostic before the existing-store-only open.
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(*path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub == "list" || sub == "read", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var box messaging.Mailbox = &messaging.StoreMailbox{Store: s}
	var out any
	switch sub {
	case "send":
		sender := messageSender(a.Getenv, a.Getwd, *path)
		// The director tag on the Steward's inbox is its collection round's own (store.StewardSenders); from messages
		// send, only the Guildmaster reaches it.
		if to.Role == store.RecipientSteward && sender != store.SenderGuildmaster {
			err = fmt.Errorf("%w: the steward's inbox takes messages send from %s only, not from %s", store.ErrInvalid, store.SenderGuildmaster, sender)
			break
		}
		// An Artisan's inbox takes steering from its Director (and from Guildhall in process, store.ArtisanSenders);
		// the Guildmaster steers an attempt through the Director.
		if to.Role == store.RecipientArtisan && env.Recipient == store.RecipientArtisan && sender != store.SenderDirector {
			err = fmt.Errorf("%w: an artisan's inbox takes messages send from %s only, run in its copy's Director session, not from %s; the Guildmaster steers an attempt through the Director", store.ErrInvalid, store.SenderDirector, sender)
			break
		}
		out, err = box.Send(to, sender, env)
	case "list":
		var page messaging.Page
		if page, err = box.List(to, *after, *limit); err == nil {
			out, err = withWakes(s, to, page)
		}
	case "read":
		out, err = box.Read(pos[0])
	case "ack":
		out, err = box.Ack(to, pos[1])
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
		return 1
	}
	// The message is stored whatever the surfacing and wake do; a failure of either is reported, not an error.
	if sub == "send" && to.Role.SessionInbox() {
		if err := a.surface(*path, env.ID); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: messages send: surface: %v\n", err)
		}
		if _, err := a.wake(*path, env.ID, messaging.SenderLocal); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "guildhall: messages send: wake: %v\n", err)
		}
	}
	return 0
}

// messagesExternal runs list --from, read --sender and ack --sender on the external messages of the store at path,
// from sender (docs/messaging.md#external-messages).
func (a *App) messagesExternal(sub, path, sender string, after uint64, limit int, pos []string) int {
	fail := func(err error) int {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: messages %s: %v\n", sub, err)
		return 1
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return fail(noStore(path))
	}
	out, err := withStoreClock(path, sub != "ack", a.Now, func(s *store.Store) (any, error) {
		switch sub {
		case "list":
			return externalList(s, sender, after, limit)
		case "read":
			return s.External(sender, pos[0])
		}
		return s.AckExternal(sender, pos[1])
	})
	if err != nil {
		return fail(err)
	}
	return a.printJSON(out)
}

// listedMessage is a listed message with the wake recorded for it, if any (docs/messaging.md#commands).
type listedMessage struct {
	messaging.Stored
	Wake *store.Wake `json:"wake,omitempty"`
}

// withWakes adds to a page of the Director's inbox each message's recorded wake; a page of an Artisan's inbox,
// which nothing wakes, is returned as it is.
func withWakes(s *store.Store, to store.Recipient, page messaging.Page) (any, error) {
	if !to.Role.SessionInbox() {
		return page, nil
	}
	out := struct {
		Messages []listedMessage `json:"messages"`
		More     bool            `json:"more"`
	}{Messages: make([]listedMessage, 0, len(page.Messages)), More: page.More}
	for _, m := range page.Messages {
		l := listedMessage{Stored: m}
		switch w, err := s.WakeFor(m.ID); {
		case err == nil:
			l.Wake = &w
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
		out.Messages = append(out.Messages, l)
	}
	return out, nil
}

// listedEnvelope refuses a message about a repository the config at path does not list: the repositories guard
// rail (docs/director.md#guard-rails). A copy's config that does not exist checks nothing; one named by --config
// must load.
func listedEnvelope(path string, named bool, env messaging.Envelope) error {
	if path == "" || env.Repository == "" {
		return nil
	}
	if _, err := os.Stat(path); !named && errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	cfg, err := reportedConfig(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if _, err := cfg.ListedRepository(env.Repository); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// readEnvelope reads an envelope from a file, or from standard input for "-".
func (a *App) readEnvelope(name string) (messaging.Envelope, error) {
	if name == "-" {
		return messaging.Decode(a.Stdin)
	}
	f, err := os.Open(name)
	if err != nil {
		return messaging.Envelope{}, fmt.Errorf("message body: %w", err)
	}
	defer func() { _ = f.Close() }()
	return messaging.Decode(f)
}

func (a *App) messagesUsage(msg string) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s\n%s\n", msg, messagesUsage)
	return 2
}
