package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// peerCopy is a Director copy named name, in a directory of that name whose path a [[peer]] copy_dir can hold.
func peerCopy(t *testing.T, name string) string {
	t.Helper()
	// The long form of the temporary directory: Windows runners name it with a short "RUNNER~1", whose '~' no
	// copy_dir may hold.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, name)
	if err := os.Rename(newCopy(t), dir); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(b), `name = "alpha"`, `name = "`+name+`"`, 1))
	return dir
}

// addPeer adds a [[peer]] to the config of the copy in dir: a peer on this machine and account when alias is empty.
func addPeer(t *testing.T, dir, name, alias, peerDir string) {
	t.Helper()
	cfg := filepath.Join(dir, "config.toml")
	b, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf("\n[[peer]]\nname = %q\nmachine = \"m-%s\"\ncopy_dir = %q\n", name, name, filepath.ToSlash(peerDir))
	if alias != "" {
		entry += fmt.Sprintf("ssh_alias = %q\n", alias)
	}
	writeFile(t, cfg, string(b)+entry)
}

// peerKey is the authorized_keys line a peer account holds for one sender's key: its forced receive command.
type peerKey struct{ copyDir, from, to string }

// peerAccounts is a fake ssh to the peers' accounts: each alias reaches an account whose authorized_keys forces the
// key's receive command, which runs in process against the receiving copy, whatever command the sender asked for.
type peerAccounts struct {
	keys map[string]peerKey
	down map[string]bool
	ran  []string
}

func (p *peerAccounts) Run(_ context.Context, alias string, argv []string, stdin []byte) ([]byte, error) {
	p.ran = append(p.ran, alias+" "+strings.Join(argv, " "))
	if p.down[alias] {
		return nil, errors.New("ssh: connect to host " + alias + " port 22: Connection timed out")
	}
	k, ok := p.keys[alias]
	if !ok {
		return nil, errors.New("ssh: Permission denied (publickey)")
	}
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = bytes.NewReader(stdin)
	if code := app.Run([]string{"messages", "receive", "--copy", k.copyDir, "--from", k.from, "--to", k.to, "-"}); code != 0 {
		return nil, fmt.Errorf("ssh: exit %d: %s", code, stderr)
	}
	return stdout.Bytes(), nil
}

// peerEnvelope is a Director message envelope.
func peerEnvelope(id, from, to, typ, inReplyTo, text string) string {
	b, err := json.Marshal(messaging.DirectorEnvelope{Schema: "guildhall.director-message/1", ID: id, From: from, To: to, Type: typ,
		Subject: "About cli --json", Text: text, InReplyTo: inReplyTo})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// sendPeer runs messages send --to in the copy dir with the envelope on standard input, through accounts.
func sendPeer(t *testing.T, accounts *peerAccounts, dir, to, envelope string) (int, store.OutboxEntry, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.PeerRunner = accounts
	app.Stdin = strings.NewReader(envelope)
	code := app.Run([]string{"messages", "send", "--to", to, "--copy", dir, "-"})
	var e store.OutboxEntry
	if stdout.Len() > 0 {
		e = decodeJSON[store.OutboxEntry](t, stdout.String())
	}
	return code, e, stderr.String()
}

// TestPeerMessages: a Director sends a peer a Director message with messages send --to, over SSH to a peer on another
// machine and in process to one on this machine and account. The message is recorded in the sender's outbox first and
// stays pending while the peer is unreachable; the scheduled retry delivers it once. The receiver records the sender
// fixed by the key's forced command, refuses an envelope from anyone else, and never retries a refusal; an answer
// must answer a question the receiver sent.
func TestPeerMessages(t *testing.T) {
	alpha, web, tools := peerCopy(t, "alpha"), peerCopy(t, "web"), peerCopy(t, "tools")
	addPeer(t, alpha, "web", "guild-web", web)
	addPeer(t, alpha, "tools", "", tools)
	addPeer(t, web, "alpha", "guild-alpha", alpha)
	addPeer(t, web, "tools", "guild-tools", tools)
	addPeer(t, tools, "alpha", "", alpha)
	accounts := &peerAccounts{
		keys: map[string]peerKey{"guild-web": {web, "alpha", "web"}, "guild-alpha": {alpha, "web", "alpha"}},
		down: map[string]bool{"guild-web": true},
	}

	// The VDS is asleep: the message is kept pending with the error.
	question := peerEnvelope("q-1", "alpha", "web", store.MessageQuestion, "", "Does cli list take --json on release 3?")
	code, e, stderr := sendPeer(t, accounts, alpha, "web", question)
	if code != 0 || e.State != store.OutboxPending || e.Attempts != 1 || !strings.Contains(e.LastError, "Connection timed out") || !strings.Contains(stderr, "kept pending") {
		t.Fatalf("send while down: exit %d, %+v, %s", code, e, stderr)
	}
	if want := "guild-web guildhall messages receive --copy " + filepath.ToSlash(web) + " --from alpha --to web -"; len(accounts.ran) != 1 || accounts.ran[0] != want {
		t.Fatalf("ran %q, want %q", accounts.ran, want)
	}
	if code, out, _ := runMessages(t, "", "outbox", "--copy", alpha); code != 0 || !strings.Contains(out, `"id":"q-1"`) || !strings.Contains(out, `"state":"pending"`) {
		t.Fatalf("outbox: exit %d, %s", code, out)
	}

	// It wakes: the session supervisor's retry delivers it, once.
	accounts.down["guild-web"] = false
	cfg, err := config.LoadUnadmitted(filepath.Join(alpha, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	app, _, _ := newTestApp(nil, nil)
	app.PeerRunner = accounts
	alphaStore := filepath.Join(alpha, directorStoreFile)
	deliveries := app.sessionPeerDeliveries(cfg, alphaStore)
	if deliveries == nil {
		t.Fatal("a copy with peers has no retry")
	}
	deliveries.round()
	if problems := app.deliverPeerOutbox(context.Background(), cfg, alphaStore); len(problems) != 0 || len(accounts.ran) != 2 {
		t.Fatalf("second retry: %q, ran %q", problems, accounts.ran)
	}
	code, out, _ := runMessages(t, "", "outbox", "--copy", alpha, "--all")
	entries := decodeJSON[struct{ Entries []store.OutboxEntry }](t, out)
	if code != 0 || len(entries.Entries) != 1 || entries.Entries[0].State != store.OutboxDelivered || !strings.Contains(string(entries.Entries[0].Receipt), "dmessage:alpha:q-1") {
		t.Fatalf("outbox --all: exit %d, %s", code, out)
	}

	// The receiver has it from alpha, woken for as a director message, and the summary counts it.
	code, out, _ = runMessages(t, "", "list", "--copy", web, "--from", "alpha")
	list := decodeJSON[struct{ Messages []listedExternal }](t, out)
	if code != 0 || len(list.Messages) != 1 || list.Messages[0].Sender != "alpha" || list.Messages[0].Type != store.MessageQuestion ||
		list.Messages[0].Wake == nil || list.Messages[0].Wake.SenderType != messaging.SenderPeer || list.Messages[0].Wake.ExternalSender != "alpha" {
		t.Fatalf("list --from alpha: exit %d, %s", code, out)
	}
	code, out, _ = runMessages(t, "", "summary", "--copy", web)
	sum := decodeJSON[messagesSummary](t, out)
	if code != 0 || sum.Unread["alpha"] != 1 || len(sum.Inbox) != 3 || sum.Inbox[1].Sender != "alpha" || strings.Contains(out, "release 3?") {
		t.Fatalf("summary: exit %d, %s", code, out)
	}

	// Sending it again prints its entry and runs nothing; another envelope under its id is refused.
	if code, e, _ := sendPeer(t, accounts, alpha, "web", question); code != 0 || e.State != store.OutboxDelivered || len(accounts.ran) != 2 {
		t.Fatalf("resend: exit %d, %+v, ran %d", code, e, len(accounts.ran))
	}
	if code, _, stderr := sendPeer(t, accounts, alpha, "web", peerEnvelope("q-1", "alpha", "web", store.MessageQuestion, "", "Other.")); code != 1 || !strings.Contains(stderr, "choose a new id") {
		t.Fatalf("conflicting resend: exit %d, %s", code, stderr)
	}

	// The VDS answers alpha's question; an answer to anything else is refused before it is sent.
	if code, e, stderr := sendPeer(t, accounts, web, "alpha", peerEnvelope("a-1", "web", "alpha", store.MessageAnswer, "q-1", "Yes, from release 3.")); code != 0 || e.State != store.OutboxDelivered {
		t.Fatalf("answer: exit %d, %+v, %s", code, e, stderr)
	}
	if code, _, stderr := sendPeer(t, accounts, web, "alpha", peerEnvelope("a-2", "web", "alpha", store.MessageAnswer, "q-7", "Yes.")); code != 1 || !strings.Contains(stderr, "in_reply_to") {
		t.Fatalf("answer to no question: exit %d, %s", code, stderr)
	}
	// The receiver checks it too, against its own outbox.
	code, out, _ = runMessages(t, peerEnvelope("a-3", "web", "alpha", store.MessageAnswer, "q-7", "Yes."), "receive", "--copy", alpha, "--from", "web", "--to", "alpha", "-")
	if code != 0 || !strings.Contains(out, `"status":"refused"`) || !strings.Contains(out, "never sent") {
		t.Fatalf("receive an answer to no question: exit %d, %s", code, out)
	}

	// The key on the VDS is tools': its forced command records tools, so alpha's envelope is refused, finally.
	accounts.keys["guild-web"] = peerKey{web, "tools", "web"}
	code, e, stderr = sendPeer(t, accounts, alpha, "web", peerEnvelope("q-2", "alpha", "web", store.MessageQuestion, "", "Still there?"))
	if code != 1 || e.State != store.OutboxRefused || !strings.Contains(e.Reason, `is from "alpha", but was received from tools`) || !strings.Contains(stderr, "web refused message q-2") {
		t.Fatalf("send under another key: exit %d, %+v, %s", code, e, stderr)
	}
	ran := len(accounts.ran)
	if problems := app.deliverPeerOutbox(context.Background(), cfg, alphaStore); len(problems) != 0 || len(accounts.ran) != ran {
		t.Fatalf("a refusal was retried: %q, ran %q", problems, accounts.ran[ran:])
	}
	accounts.keys["guild-web"] = peerKey{web, "alpha", "web"}

	// The receiver's own refusals: a sender that is not its peer, another Director's name, a type no peer sends.
	for name, c := range map[string]struct{ from, to, body, reason string }{
		"not a peer":     {"mallory", "web", peerEnvelope("m-1", "mallory", "web", store.MessageQuestion, "", "Hi."), "unknown sender"},
		"reserved name":  {"steward", "web", peerEnvelope("m-2", "steward", "web", store.MessageQuestion, "", "Hi."), "unknown sender"},
		"another to":     {"alpha", "tools", peerEnvelope("m-3", "alpha", "tools", store.MessageQuestion, "", "Hi."), "this copy is the Director"},
		"envelope to":    {"alpha", "web", peerEnvelope("m-4", "alpha", "tools", store.MessageQuestion, "", "Hi."), "is addressed to"},
		"steward-report": {"alpha", "web", peerEnvelope("m-5", "alpha", "web", store.MessageStewardReport, "", "Hi."), "type must be one of"},
		"note":           {"alpha", "web", peerEnvelope("m-6", "alpha", "web", store.MessageNote, "", "Hi."), "type must be one of"},
	} {
		code, out, stderr := runMessages(t, c.body, "receive", "--copy", web, "--from", c.from, "--to", c.to, "-")
		if code != 0 || !strings.Contains(out, `"status":"refused"`) || !strings.Contains(out, c.reason) {
			t.Errorf("%s: exit %d, %s, %s; want refused naming %q", name, code, out, stderr, c.reason)
		}
	}
	for name, args := range map[string][]string{
		"--from alone":     {"receive", "--copy", web, "--from", "alpha", "-"},
		"no envelope":      {"receive", "--copy", web, "--from", "alpha", "--to", "web"},
		"send no envelope": {"send", "--to", "web", "--copy", alpha},
	} {
		if code, _, _ := runMessages(t, "", args...); code != 2 {
			t.Errorf("%s: exit %d, want a usage error", name, code)
		}
	}

	// The VDS closes its inbox to alpha: alpha's next message is refused closed, and tools' still arrive.
	if code, _, stderr := runMessages(t, "", "inbox", "close", "--copy", web, "--sender", "alpha", "--reason", "too chatty"); code != 0 {
		t.Fatalf("close: exit %d, %s", code, stderr)
	}
	if code, e, _ := sendPeer(t, accounts, alpha, "web", peerEnvelope("q-3", "alpha", "web", store.MessageQuestion, "", "Hello?")); code != 1 || !strings.HasPrefix(e.Reason, "closed") {
		t.Fatalf("send to a closed inbox: exit %d, %+v", code, e)
	}
	if code, out, _ := runMessages(t, peerEnvelope("t-1", "tools", "web", store.MessageQuestion, "", "Hi."), "receive", "--copy", web, "--from", "tools", "--to", "web", "-"); code != 0 || !strings.Contains(out, `"status":"delivered"`) {
		t.Fatalf("tools while closed to alpha: exit %d, %s", code, out)
	}

	// A peer on this machine and account receives in process, with no SSH.
	ran = len(accounts.ran)
	notice := peerEnvelope("d-1", "alpha", "tools", store.MessageDependencyNotice, "", "web now needs cli --json.")
	if code, e, stderr := sendPeer(t, accounts, alpha, "tools", notice); code != 0 || e.State != store.OutboxDelivered || len(accounts.ran) != ran {
		t.Fatalf("local peer: exit %d, %+v, %s, ran %q", code, e, stderr, accounts.ran[ran:])
	}
	if code, out, _ := runMessages(t, "", "read", "--copy", tools, "--sender", "alpha", "d-1"); code != 0 || !strings.Contains(out, "web now needs cli --json.") {
		t.Fatalf("read on tools: exit %d, %s", code, out)
	}

	// What send refuses before recording anything.
	for name, c := range map[string]struct{ to, body, want string }{
		"not a peer": {"nobody", notice, "not a [[peer]]"},
		"from":       {"tools", peerEnvelope("d-2", "web", "tools", store.MessageDependencyNotice, "", "x"), "received from alpha"},
		"to":         {"tools", peerEnvelope("d-3", "alpha", "web", store.MessageDependencyNotice, "", "x"), `addressed to "web"`},
		"type":       {"tools", peerEnvelope("d-4", "alpha", "tools", store.MessageNote, "", "x"), "type must be one of"},
	} {
		if code, _, stderr := sendPeer(t, accounts, alpha, c.to, c.body); code != 1 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %s; want %q", name, code, stderr, c.want)
		}
	}
	if code, out, _ := runMessages(t, "", "outbox", "--copy", alpha, "--all"); code != 0 || strings.Contains(out, `"d-2"`) || strings.Contains(out, `"d-4"`) {
		t.Errorf("a refused send was recorded: %s", out)
	}

	// No peers, no retry.
	if plain, err := config.LoadUnadmitted(filepath.Join(newCopy(t), "config.toml")); err != nil || app.sessionPeerDeliveries(plain, alphaStore) != nil {
		t.Errorf("a copy with no peers has a retry: %v", err)
	}
}

// TestPeerSetup: messages peer setup prints the commands that make this Director's key reach the peer's receive
// command only, for Linux and Windows, with the public key once it exists, and none for a peer on this account.
func TestPeerSetup(t *testing.T) {
	alpha, web, tools := peerCopy(t, "alpha"), peerCopy(t, "web"), peerCopy(t, "tools")
	addPeer(t, alpha, "web", "guild-web", web)
	addPeer(t, alpha, "tools", "", tools)
	home := t.TempDir()
	setup := func(peer string) peerSetup {
		t.Helper()
		app, stdout, stderr := newTestApp(nil, nil)
		app.Getenv = func(k string) string {
			if k == "HOME" {
				return home
			}
			return ""
		}
		if code := app.Run([]string{"messages", "peer", "setup", peer, "--copy", alpha}); code != 0 {
			t.Fatalf("peer setup %s: exit %d, %s", peer, code, stderr)
		}
		return decodeJSON[peerSetup](t, stdout.String())
	}
	s := setup("web")
	receive := "messages receive --copy " + filepath.ToSlash(web) + " --from alpha --to web -"
	if s.Local || s.Key != "guildhall-alpha-to-web" || !strings.HasSuffix(s.ForcedCommand, receive) || len(s.ThisMachine) != 3 {
		t.Fatalf("setup: %+v", s)
	}
	if !strings.Contains(s.ThisMachine[1], "guild-web m-web guildhall-alpha-to-web") && !strings.Contains(s.ThisMachine[1], "Host guild-web") {
		t.Errorf("ssh alias command: %q", s.ThisMachine[1])
	}
	if !strings.Contains(s.PeerLinux[1], `command=\"$(command -v guildhall) `+receive+`\",restrict <the one line of`) ||
		!strings.Contains(s.PeerWindows[1], `(Get-Command guildhall).Source + ' `+receive+`",restrict <the one line of`) {
		t.Errorf("peer lines: %q, %q", s.PeerLinux, s.PeerWindows)
	}
	if !strings.Contains(s.PeerConfig, `name = "alpha"`) || !strings.Contains(strings.Join(s.Notes, " "), "run guildhall messages peer setup web again") {
		t.Errorf("peer config and notes: %q, %q", s.PeerConfig, s.Notes)
	}
	// With the key made, the lines carry it; a key line with anything else in it is never printed.
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGuildhallTestKey+/= guildhall-alpha-to-web"
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".ssh", "guildhall-alpha-to-web.pub"), pub+"\n")
	if s := setup("web"); !strings.HasSuffix(s.PeerLinux[1], `restrict `+pub+`" >> ~/.ssh/authorized_keys`) || !strings.Contains(s.PeerWindows[1], "restrict "+pub+"')") {
		t.Errorf("with the key: %q, %q", s.PeerLinux[1], s.PeerWindows[1])
	}
	writeFile(t, filepath.Join(home, ".ssh", "guildhall-alpha-to-web.pub"), `ssh-ed25519 AAAA "$(rm -rf ~)"`+"\n")
	if s := setup("web"); strings.Contains(s.PeerLinux[1], "rm -rf") {
		t.Errorf("an unsafe key line was printed: %q", s.PeerLinux[1])
	}
	if s := setup("tools"); !s.Local || len(s.ThisMachine)+len(s.PeerLinux)+len(s.PeerWindows) != 0 || s.Key != "" {
		t.Errorf("local peer setup: %+v", s)
	}
	for name, args := range map[string][]string{
		"no peer":   {"messages", "peer", "setup", "--copy", alpha},
		"other sub": {"messages", "peer", "list", "--copy", alpha},
	} {
		app, _, _ := newTestApp(nil, nil)
		if code := app.Run(args); code != 2 {
			t.Errorf("%s: exit %d, want 2", name, code)
		}
	}
	app, _, stderr := newTestApp(nil, nil)
	if code := app.Run([]string{"messages", "peer", "setup", "nobody", "--copy", alpha}); code != 1 || !strings.Contains(stderr.String(), "not a [[peer]]") {
		t.Errorf("unknown peer: exit %d, %s", code, stderr)
	}
}
