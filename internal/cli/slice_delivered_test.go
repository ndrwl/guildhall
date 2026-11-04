package cli

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
)

func (c *cancelDirector) sliceDelivered(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return c.run(t, append([]string{"slice", "delivered"}, c.flags(args...)...)...)
}

// slicedMerged records t1 as a multi-PR task whose accepted spec lists slices, slice 1 merged on the forge as its
// pull request and recorded as deliver-1-merge, and nothing recorded for slice 2 yet.
func (c *cancelDirector) slicedMerged(t *testing.T, spec string) forge.PullRequest {
	t.Helper()
	p := path.Join("tasks", "app", "t1", "plan-1", "spec.md")
	file := filepath.Join(c.state, "documents", filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	c.write(t, func(s *store.Store) (store.Receipt, error) {
		return s.PutDocument("doc-accept", store.DocumentRef{Repository: "app", TaskID: "t1", Name: "accept-1-spec", Path: p, SHA256: digest.Of([]byte(spec)).Hex()})
	})
	first := c.publish(t)
	merged, err := c.forge.Merge(context.Background(), forge.Merge{Repository: appRepo, Remote: appRemote, Number: first.Number, Head: first.Head, Title: "Parser"})
	if err != nil {
		t.Fatal(err)
	}
	c.putDocument(t, "deliver-1-merge", map[string]any{"schema": "guildhall.pull-request/1", "remote": appRemote, "number": merged.Number,
		"url": merged.URL, "branch": merged.Branch, "head": merged.Head, "state": merged.State, "merge_commit": merged.MergeCommit})
	return merged
}

// withStore3 is withStore for a read returning two values.
func withStore3[A, B any](path string, fn func(*store.Store) (A, B, error)) (A, B, error) {
	var b B
	a, err := withStore(path, true, func(s *store.Store) (A, error) {
		var a A
		var err error
		a, b, err = fn(s)
		return a, err
	})
	return a, b, err
}

const twoSlices = "# Two\n\n## Goal\n\nTwo pull requests.\n\n## Slices\n\n1. **Parser**: one.\n2. **Engine**: two.\n"

// TestSliceDeliveredHelpAndValidation: the verb's help needs no store, and missing or malformed flags are usage errors.
func TestSliceDeliveredHelpAndValidation(t *testing.T) {
	d := newRunDirector(t)
	code, out, stderr := d.run(t, "slice", "delivered", "--help")
	if code != 0 || !strings.Contains(out, "--by-slice") || !strings.Contains(out, "--by-pr") || stderr != "" {
		t.Fatalf("help = %d %q %q", code, out, stderr)
	}
	for _, args := range [][]string{
		{"--unknown", "x"},
		{"--task", "t1", "--slice", "2", "--by-slice", "1"},
		{"--task", "t1", "--slice", "0", "--by-slice", "1", "--reason", "r"},
		{"--task", "t1", "--slice", "2", "--reason", "r"},
		{"--task", "t1", "--slice", "2", "--by-slice", "1", "--by-pr", "https://forge.invalid/1", "--reason", "r"},
		{"--task", "t1", "--slice", "2", "--by-slice", "x", "--reason", "r"},
		{"--task", "t1", "--slice", "2", "--by-slice", "1", "--reason", "r", "extra"},
	} {
		if code, _, _ := d.run(t, append([]string{"slice", "delivered"}, d.flags(args...)...)...); code != 2 {
			t.Errorf("%v exit %d, want 2", args, code)
		}
	}
	if code, _, _ := d.run(t, "slice", "merged"); code != 2 {
		t.Errorf("slice merged exit %d, want 2", code)
	}
	if _, out, _ := d.run(t, "help"); !strings.Contains(out, "slice delivered") {
		t.Fatalf("help omits slice delivered: %s", out)
	}
}

// TestSliceDeliveredClosesTheLastSlice: d160's case. A two-slice task whose slice 1 merged and delivered slice 2's
// work too is closed with slice delivered --by-slice 1: the task is done, not cancelled, the output is the done wait
// as task start prints it, and the Director is sent a slice-delivered message naming slice 1. Running it again prints
// the same wait and writes nothing; a refusal exits 1 and writes nothing.
func TestSliceDeliveredClosesTheLastSlice(t *testing.T) {
	c := newCancelDirectorWith(t, newRunDirectorWith(t, "sliced.toml"))
	c.slicedMerged(t, twoSlices)
	before := c.receipts(t)
	if code, _, stderr := c.sliceDelivered(t, "--task", "t1", "--slice", "1", "--by-slice", "1", "--reason", "r"); code != 1 || !strings.Contains(stderr, "not app/t1's current slice") {
		t.Errorf("a delivery of merged slice 1: exit %d, %s", code, stderr)
	}
	if c.receipts(t) != before {
		t.Error("a refused delivery wrote records")
	}
	args := []string{"--task", "t1", "--slice", "2", "--by-slice", "1", "--reason", "Slice 1 (#1) shipped every slice 2 item"}
	code, out, stderr := c.sliceDelivered(t, args...)
	if code != 0 || out != `{"wait":"done","step":"deliver--s2"}`+"\n" {
		t.Fatalf("slice delivered: exit %d, %q %s", code, out, stderr)
	}
	if got := c.task(t, "t1"); got.State != store.TaskDone {
		t.Errorf("task state = %s, want done", got.State)
	}
	m, err := withStore(c.store, true, func(s *store.Store) (store.Message, error) {
		return s.Message("app.t1.slice-delivered.delivered--s2-1")
	})
	if err != nil || m.Type != store.MessageSliceDelivered || !strings.Contains(m.Text, "slice 2 of 2 (Engine) as already delivered by slice 1") {
		t.Errorf("slice-delivered message = %+v, %v", m, err)
	}
	msgs, _, err := withStore3(c.store, func(s *store.Store) ([]store.Message, bool, error) {
		return s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Type == store.MessageMerged && strings.Contains(m.Text, "slice 2 of 2 (Engine) merged") {
			t.Errorf("merged message %q: nothing merged for slice 2", m.Text)
		}
	}
	before = c.receipts(t)
	code, again, stderr := c.sliceDelivered(t, args...)
	if code != 0 || again != out || c.receipts(t) != before {
		t.Errorf("slice delivered again: exit %d, %q %s, %d new receipts", code, again, stderr, c.receipts(t)-before)
	}
	if code, _, stderr := c.cancel(t, "--reason", "Too late.", "t1"); code != 1 || !strings.Contains(stderr, "is done") {
		t.Errorf("task cancel of the done task: exit %d, %s", code, stderr)
	}
}

// TestSliceDeliveredByAPullRequest: --by-pr names a merged pull request of the task's repository; one the forge reports
// open is refused, and the merged one closes the slice.
func TestSliceDeliveredByAPullRequest(t *testing.T) {
	c := newCancelDirectorWith(t, newRunDirectorWith(t, "sliced.toml"))
	merged := c.slicedMerged(t, twoSlices)
	head := strings.Repeat("e", 40)
	c.forge.AddCommit(appRepo, head)
	open, err := c.forge.Publish(context.Background(), forge.Change{Repository: appRepo, Remote: appRemote, Branch: "elsewhere", Head: head, Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	before := c.receipts(t)
	if code, _, stderr := c.sliceDelivered(t, "--task", "t1", "--slice", "2", "--by-pr", open.URL, "--reason", "r"); code != 1 || !strings.Contains(stderr, "not merged") {
		t.Errorf("a delivery by an open pull request: exit %d, %s", code, stderr)
	}
	if c.receipts(t) != before {
		t.Error("a refused delivery wrote records")
	}
	if code, out, stderr := c.sliceDelivered(t, "--task", "t1", "--slice", "2", "--by-pr", merged.URL, "--reason", "#1 shipped it"); code != 0 || !strings.Contains(out, `"wait":"done"`) {
		t.Fatalf("slice delivered --by-pr: exit %d, %q %s", code, out, stderr)
	}
	if got := c.task(t, "t1"); got.State != store.TaskDone {
		t.Errorf("task state = %s, want done", got.State)
	}
}

// TestTaskCancelAfterADeliveredSlice: a three-slice task whose slice 2 closed as delivered by slice 1 is cancelled at
// slice 3: the delivered slice reads as closed, not merging, and the output names it.
func TestTaskCancelAfterADeliveredSlice(t *testing.T) {
	c := newCancelDirectorWith(t, newRunDirectorWith(t, "sliced.toml"))
	c.slicedMerged(t, "# Three\n\n## Goal\n\nThree pull requests.\n\n## Slices\n\n1. **Parser**: one.\n2. **Engine**: two.\n3. **Views**: three.\n")
	c.putDocument(t, "delivered--s2-1-delivery", map[string]any{"schema": "guildhall.slice-delivery/1", "slice": 2, "by_slice": 1,
		"reason": "slice 1 shipped it", "at": "2026-11-01T00:00:00Z"})
	code, out, stderr := c.cancel(t, "--reason", "Stop after the engine.", "t1")
	if code != 0 {
		t.Fatalf("task cancel: exit %d, %s", code, stderr)
	}
	if !strings.Contains(out, "slice 2 of 3 (Engine) was closed before the cancel as already delivered by slice 1") {
		t.Errorf("output does not name the delivered slice:\n%s", out)
	}
	if got := c.task(t, "t1"); got.State != store.TaskCancelled {
		t.Errorf("task state = %s, want cancelled", got.State)
	}
}
