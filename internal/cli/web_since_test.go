package cli

import (
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// TestWebSourceTail: the poll's read gives the conversation's last number and the entries after the page's as page
// messages; it reads the store alone, with no config or state at all (that the web route never asks the Source for
// the page model is TestSinceReply's); it opens
// the store read-only, so it reads beside another reader; and it closes the store before it returns, so a Director
// write right after each poll takes the lock at once. An unknown task is ErrNotFound and a held store ErrBusy.
func TestWebSourceTail(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	// Only the store's path: the tail needs no config, state or document directory.
	src := webSource{a: webSourceFor(t, dir).a, c: clerkPaths{store: path}}
	if tail, err := src.Tail("app", "a-grill", 0); err != nil || tail.Last != 0 || len(tail.Newer) != 0 {
		t.Fatalf("no talk: %+v, %v", tail, err)
	}
	// The probe: after each poll returns, a writer whose lock wait is a deliberate refusal input takes the lock.
	for i := range 3 {
		if _, err := src.Tail("app", "a-grill", i); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		// timing:allow deliberate refusal input: the poll has returned, so a lock it still held would refuse this write.
		w, err := store.Open(path, store.Options{LockTimeout: time.Millisecond})
		if err != nil {
			t.Fatalf("a write after poll %d: %v; the poll held the store", i, err)
		}
		author := store.ConversationDirector
		if i == 1 {
			author = store.ConversationGuildmasterPage
		}
		_, err = w.AddConversation("probe-"+strconv.Itoa(i), "app", "a-grill", store.Saying{Author: author, Tag: "grill-1", Kind: store.ConversationSay, Text: "Reply " + strconv.Itoa(i)})
		if cerr := w.Close(); err != nil || cerr != nil {
			t.Fatalf("write %d: %v, %v", i, err, cerr)
		}
	}
	tail, err := src.Tail("app", "a-grill", 1)
	if err != nil || tail.Last != 3 || len(tail.Newer) != 2 {
		t.Fatalf("tail after 1 = %+v, %v", tail, err)
	}
	if m := tail.Newer[1]; m.Number != 3 || m.Author != web.AuthorDirector || m.Tag != "grill-1" || m.Stage != "grill" || m.Text != "Reply 2" || m.At.IsZero() {
		t.Errorf("newest = %+v", m)
	}

	reader, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Tail("app", "a-grill", 0); err != nil {
		t.Errorf("a poll beside another reader: %v; it is not a read-only open", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := src.Tail("app", "nope", 0); !errors.Is(err, web.ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	held, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	old := webLockWait
	// timing:allow deliberate refusal input: the writer holds the lock for the whole call, so any wait is refused.
	webLockWait = 10 * time.Millisecond
	defer func() { webLockWait = old }()
	if _, err := src.Tail("app", "a-grill", 0); !errors.Is(err, web.ErrBusy) {
		t.Errorf("held store: %v, want ErrBusy", err)
	}
}
