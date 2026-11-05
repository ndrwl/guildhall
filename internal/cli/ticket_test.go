package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

type ticketListing struct {
	Tickets []ticketView `json:"tickets"`
}

// TestTicketFromMessage: a steward-report filed as a ticket opens an accept/decline decision on the reserved task
// messages/ticket-1, which decide list shows; task new --ticket is refused until the Guildmaster answers accept, the
// Director cannot answer it in its session, and once accepted the task is created and recorded on the ticket, once.
func TestTicketFromMessage(t *testing.T) {
	dir := newCopy(t)
	if code, r, stderr := receive(t, dir, stewardEnvelope("rollout-failed.1", "Rollout abc ended failed", "scp failed."), nil); code != 0 || r.Status != messaging.ReceiveDelivered {
		t.Fatalf("receive: exit %d, %+v, %s", code, r, stderr)
	}
	file := []string{"ticket", "file", "--copy", dir, "--sender", "steward", "--from-message", "rollout-failed.1", "--repo", "app", "--title", "Fix the rollout"}
	code, out, stderr := queueCmd(t, "", file...)
	v := decodeJSON[ticketView](t, out)
	if code != 0 || v.ID != "ticket-1" || v.State != store.TicketProposed || v.Sender != "steward" || v.Message != "rollout-failed.1" ||
		v.Type != store.MessageStewardReport || v.Kind != store.ReportRolloutFailed || v.Decision != "messages/ticket-1/ticket-1" || v.Task != "" {
		t.Fatalf("ticket file: exit %d, %s, %s", code, out, stderr)
	}
	// Filing the same message again prints the same ticket; another proposal for it is refused.
	if code, out, _ := queueCmd(t, "", file...); code != 0 || decodeJSON[ticketView](t, out).ID != "ticket-1" {
		t.Errorf("filing again: exit %d, %s", code, out)
	}
	other := append(append([]string{}, file[:len(file)-1]...), "Something else")
	if code, _, stderr := queueCmd(t, "", other...); code != 1 || !strings.Contains(stderr, "already ticket-1") {
		t.Errorf("another proposal: exit %d, %s", code, stderr)
	}
	code, out, _ = queueCmd(t, "", "decide", "list", "--copy", dir)
	if code != 0 || !strings.Contains(out, `"repository":"messages","task_id":"ticket-1","id":"ticket-1"`) || !strings.Contains(out, `"options":["accept","decline"]`) {
		t.Fatalf("decide list: %s", out)
	}
	newTask := []string{"new", "--copy", dir, "--repo", "app", "--title", "Fix the rollout", "--ticket", "ticket-1"}
	if code, _, stderr := runTask(t, newTask...); code != 1 || !strings.Contains(stderr, "waits for the Guildmaster's answer") {
		t.Fatalf("task new before the answer: exit %d, %s", code, stderr)
	}
	// The Director, in its session, cannot accept the ticket it filed.
	inSession, _, errOut := newTestApp(nil, nil)
	inSession.Getenv = func(k string) string { return map[string]string{session.NumberEnv: "3"}[k] }
	inSession.Stdin = strings.NewReader("accept\n")
	if code := inSession.Run([]string{"decide", "answer", "--copy", dir, "--repo", "messages", "--task", "ticket-1", "--decision", "ticket-1", "--answer-file", "-"}); code != 1 ||
		!strings.Contains(errOut.String(), "a ticket's accept or decline, and only the Guildmaster answers it") {
		t.Fatalf("answer in a session: exit %d, %s", code, errOut)
	}
	if code, _, stderr := queueCmd(t, "accept\n", "decide", "answer", "--copy", dir, "--repo", "messages", "--task", "ticket-1", "--decision", "ticket-1", "--answer-file", "-"); code != 0 {
		t.Fatalf("answer: %s", stderr)
	}
	code, out, stderr = runTask(t, newTask...)
	created := decodeJSON[store.Receipt](t, out)
	if code != 0 || created.Repository != "app" || created.TaskID == "" {
		t.Fatalf("task new after accept: exit %d, %s, %s", code, out, stderr)
	}
	code, out, _ = queueCmd(t, "", "ticket", "list", "--copy", dir)
	list := decodeJSON[ticketListing](t, out)
	if code != 0 || len(list.Tickets) != 1 || list.Tickets[0].State != store.TicketAccepted || list.Tickets[0].Choice != "accept" || list.Tickets[0].Task == "" {
		t.Fatalf("ticket list: exit %d, %s", code, out)
	}
	if code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", "app", "--title", "Again", "--ticket", "ticket-1"); code != 1 || !strings.Contains(stderr, "already became the task app/"+list.Tickets[0].Task) {
		t.Errorf("a second task from the ticket: exit %d, %s", code, stderr)
	}
	// The reserved ticket task is not one of the copy's tasks.
	if _, out, _ := queueCmd(t, "", "task", "list", "--copy", dir, "--json"); strings.Contains(out, `"ticket-1"`) {
		t.Errorf("task list shows the reserved ticket task: %s", out)
	}
}

// TestTicketDeclined: decline closes the ticket, and no task is created from it.
func TestTicketDeclined(t *testing.T) {
	dir := newCopy(t)
	if code, _, stderr := receive(t, dir, stewardEnvelope("r-1", "s", "t"), nil); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := queueCmd(t, "", "ticket", "file", "--copy", dir, "--sender", "steward", "--from-message", "r-1", "--repo", "app", "--title", "T"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := queueCmd(t, "decline\n", "decide", "answer", "--copy", dir, "--repo", "messages", "--task", "ticket-1", "--decision", "ticket-1", "--answer-file", "-"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", "app", "--title", "T", "--ticket", "ticket-1"); code != 1 || !strings.Contains(stderr, "declined ticket-1") {
		t.Errorf("task new after decline: exit %d, %s", code, stderr)
	}
	if _, out, _ := queueCmd(t, "", "ticket", "list", "--copy", dir); decodeJSON[ticketListing](t, out).Tickets[0].State != store.TicketDeclined {
		t.Errorf("ticket list: %s", out)
	}
}

// TestTicketFileRefusals: ticket file needs both identity fields, a configured repository and a title, refuses a
// message that does not exist, and a question is never a ticket.
func TestTicketFileRefusals(t *testing.T) {
	dir := newCopy(t)
	if code, _, stderr := receive(t, dir, stewardEnvelope("r-1", "s", "t"), nil); code != 0 {
		t.Fatal(stderr)
	}
	base := []string{"ticket", "file", "--copy", dir}
	for name, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no sender":     {[]string{"--from-message", "r-1", "--repo", "app", "--title", "T"}, 2, "--sender and --from-message"},
		"no message":    {[]string{"--sender", "steward", "--repo", "app", "--title", "T"}, 2, "--sender and --from-message"},
		"bad sender":    {[]string{"--sender", "guildmaster", "--from-message", "r-1", "--repo", "app", "--title", "T"}, 2, "--sender must be"},
		"no title":      {[]string{"--sender", "steward", "--from-message", "r-1", "--repo", "app"}, 2, "--repo and --title"},
		"unknown repo":  {[]string{"--sender", "steward", "--from-message", "r-1", "--repo", "nope", "--title", "T"}, 1, "--repo"},
		"reserved repo": {[]string{"--sender", "steward", "--from-message", "r-1", "--repo", "messages", "--title", "T"}, 1, "--repo"},
		"no such":       {[]string{"--sender", "steward", "--from-message", "r-2", "--repo", "app", "--title", "T"}, 1, "not found"},
		"argument":      {[]string{"--sender", "steward", "--from-message", "r-1", "--repo", "app", "--title", "T", "extra"}, 2, "unexpected argument"},
	} {
		if code, _, stderr := queueCmd(t, "", append(append([]string{}, base...), c.args...)...); code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, %s; want %d naming %q", name, code, stderr, c.code, c.want)
		}
	}
	if code, out, _ := queueCmd(t, "", "ticket", "list", "--copy", dir); code != 0 || len(decodeJSON[ticketListing](t, out).Tickets) != 0 {
		t.Errorf("a refused filing left a ticket: %s", out)
	}
	if code, _, stderr := runTask(t, "new", "--copy", dir, "--repo", "app", "--title", "T", "--ticket", "ticket-9"); code != 1 || !strings.Contains(stderr, "ticket-9") {
		t.Errorf("task new from no ticket: exit %d, %s", code, stderr)
	}
}
