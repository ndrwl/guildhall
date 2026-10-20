package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

const messagesUsage = `usage:
  guildhall messages send --store <path> <recipient> <envelope-file|->
  guildhall messages list --store <path> [--after <sequence>] [--limit <n>] <recipient>
  guildhall messages read --store <path> <message-id>
  guildhall messages ack --store <path> <recipient> <message-id>
a recipient is "director" or "artisan:<repository>/<task>/<step>/<attempt>"`

// messages runs a messages subcommand against an existing store and prints its result as one JSON document.
// It exits 0 on success, 1 when the store refuses or fails and 2 on a usage error.
func (a *App) messages(args []string) int {
	if len(args) == 0 {
		return a.messagesUsage("missing messages subcommand")
	}
	sub := args[0]
	flags := flag.NewFlagSet("messages "+sub, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("store", "", "store file")
	after := flags.Uint64("after", 0, "list messages after this sequence")
	limit := flags.Int("limit", 20, "list at most this many messages")
	var want int
	switch sub {
	case "send", "ack":
		want = 2
	case "list", "read":
		want = 1
	default:
		return a.messagesUsage(fmt.Sprintf("unknown messages subcommand %q", sub))
	}
	if err := flags.Parse(args[1:]); err != nil {
		return a.messagesUsage(err.Error())
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case *path == "":
		return a.messagesUsage("--store is required")
	case flags.NArg() != want:
		return a.messagesUsage(fmt.Sprintf("messages %s takes %d arguments", sub, want))
	case sub != "list" && (set["after"] || set["limit"]):
		return a.messagesUsage("--after and --limit apply only to messages list")
	}
	pos := flags.Args()
	var to store.Recipient
	if sub != "read" {
		var err error
		if to, err = messaging.ParseRecipient(pos[0]); err != nil {
			return a.messagesUsage(err.Error())
		}
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
	}
	// Opening writable would create a missing file, and messages never create a store.
	if _, err := os.Stat(*path); errors.Is(err, fs.ErrNotExist) {
		return fail(fmt.Errorf("no store at %s", *path))
	}
	s, err := store.Open(*path, store.Options{ReadOnly: sub == "list" || sub == "read", RequireStore: true})
	if err != nil {
		return fail(err)
	}
	var box messaging.Mailbox = &messaging.StoreMailbox{Store: s}
	var out any
	switch sub {
	case "send":
		out, err = box.Send(to, env)
	case "list":
		out, err = box.List(to, *after, *limit)
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
	return 0
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
