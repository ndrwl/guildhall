package cli

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// conversation is repo/task's conversation in the store at path.
func conversation(t *testing.T, path, repo, task string) []store.ConversationEntry {
	t.Helper()
	got, err := withStore(path, true, func(s *store.Store) ([]store.ConversationEntry, error) { return s.Conversation(repo, task) })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func runTaskSay(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader(stdin)
	code := app.Run(append([]string{"task", "say"}, args...))
	return code, stdout.String(), stderr.String()
}

// TestTaskSay: the Director's words go on the task page's conversation under an attempt or a decision of the task,
// with --author guildmaster-chat quoting the Guildmaster's words in chat; a rerun prints the original receipt and
// writes nothing; a tag the task does not have, a closed task and a bad author are refused.
func TestTaskSay(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	code, out, errOut := runTaskSay(t, "Round 1 is on the **page**.\r\n", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--text-file", "-")
	rc := decodeJSON[store.Receipt](t, out)
	if code != 0 || rc.Operation != "add-conversation" || rc.Record != store.ConversationRecord(1) || rc.Repository != "app" || rc.TaskID != "a-approve" {
		t.Fatalf("task say = %d %+v %s", code, rc, errOut)
	}
	if code, again, _ := runTaskSay(t, "Round 1 is on the **page**.\n", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--text-file", "-"); code != 0 || again != out {
		t.Fatalf("rerun = %d %s, want the original receipt %s", code, again, out)
	}
	if code, _, errOut := runTaskSay(t, "yes, ship it", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "accept-plan", "--text-file", "-", "--author", "guildmaster-chat"); code != 0 {
		t.Fatalf("task say --author guildmaster-chat = %d %s", code, errOut)
	}
	got := conversation(t, path, "app", "a-approve")
	want := []store.Saying{
		{Author: store.ConversationDirector, Tag: "plan-1", Kind: store.ConversationSay, Text: "Round 1 is on the **page**.\n"},
		{Author: store.ConversationGuildmasterChat, Tag: "accept-plan", Kind: store.ConversationSay, Text: "yes, ship it"},
	}
	if len(got) != 2 || got[0].Saying != want[0] || got[1].Saying != want[1] || got[0].At != rc.CommittedAt {
		t.Fatalf("conversation = %+v", got)
	}

	for name, c := range map[string]struct {
		args []string
		code int
		says string
	}{
		"unknown tag":   {[]string{"--repo", "app", "--task", "a-approve", "--tag", "build-9"}, 1, `no attempt or decision "build-9"`},
		"unknown task":  {[]string{"--repo", "app", "--task", "zz-none", "--tag", "plan-1"}, 1, "not found"},
		"page author":   {[]string{"--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--author", "guildmaster-page"}, 2, "--author"},
		"no tag":        {[]string{"--repo", "app", "--task", "a-approve"}, 2, "requires"},
		"tag not an id": {[]string{"--repo", "app", "--task", "a-approve", "--tag", "Plan 1"}, 2, "must be ids"},
		"positional":    {[]string{"--repo", "app", "--task", "a-approve", "--tag", "plan-1", "extra"}, 2, "no arguments"},
		"blank text":    {[]string{"--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--blank"}, 1, "empty"},
	} {
		t.Run(name, func(t *testing.T) {
			stdin, args := "Words.", c.args
			if args[len(args)-1] == "--blank" {
				stdin, args = " \n", args[:len(args)-1]
			}
			code, _, errOut := runTaskSay(t, stdin, append([]string{"--store", path, "--text-file", "-"}, args...)...)
			if code != c.code || !strings.Contains(errOut, c.says) {
				t.Errorf("= %d %s; want %d saying %q", code, errOut, c.code, c.says)
			}
		})
	}
	if n := len(conversation(t, path, "app", "a-approve")); n != 2 {
		t.Errorf("refusals wrote: %d entries", n)
	}

	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.VoidDecision("void", "app", "a-approve", "accept-plan", "task cancelled"); err == nil {
		_, err = s.CancelTask("cancel", "app", "a-approve", "not needed")
	}
	_ = s.Close()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runTaskSay(t, "Too late.", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--text-file", "-"); code != 1 || !strings.Contains(errOut, "cancelled") {
		t.Errorf("task say on a cancelled task = %d %s", code, errOut)
	}
	if n := len(conversation(t, path, "app", "a-approve")); n != 2 {
		t.Errorf("a cancelled task's conversation has %d entries, want its 2 kept", n)
	}
}

// TestConversationMirrorsWhereTheWordsAre: decide answer records the Guildmaster's chat answer and decide open the
// Director's question on the page's conversation, each under its decision and written with it; a retry replays and
// writes no second entry.
func TestConversationMirrorsWhereTheWordsAre(t *testing.T) {
	path := decideStorePath(t)
	code, _, errOut := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t1", "--question", "Back off or *stop*?", "--option", "back-off", "--option", "stop")
	if code != 0 {
		t.Fatalf("open = %d %s", code, errOut)
	}
	if code, _, _ := runDecide(t, "", "open", "--store", path, "--repo", "app", "--task", "t1", "--question", "Back off or *stop*?", "--option", "back-off", "--option", "stop"); code != 0 {
		t.Fatal("reopen failed")
	}
	if code, _, errOut := runDecide(t, "back-off\r\n\r\nkeep it cheap\r\n", "answer", "--store", path, "--repo", "app", "--task", "t1", "--decision", "q1", "--answer-file", "-"); code != 0 {
		t.Fatalf("answer = %d %s", code, errOut)
	}
	if code, _, _ := runDecide(t, "back-off\n\nkeep it cheap\n", "answer", "--store", path, "--repo", "app", "--task", "t1", "--decision", "q1", "--answer-file", "-"); code != 0 {
		t.Fatal("re-answer failed")
	}
	got := conversation(t, path, "app", "t1")
	want := []store.Saying{
		{Author: store.ConversationDirector, Tag: "q1", Kind: store.ConversationSay, Text: "Back off or *stop*?"},
		{Author: store.ConversationGuildmasterChat, Tag: "q1", Kind: store.ConversationSay, Text: "back-off\n\nkeep it cheap\n"},
	}
	if len(got) != 2 || got[0].Saying != want[0] || got[1].Saying != want[1] {
		t.Fatalf("conversation = %+v", got)
	}
	r, err := withStore(path, true, func(s *store.Store) (store.Receipt, error) { return s.Receipt("decision-answer:app:t1:q1") })
	if err != nil || !got[1].At.Equal(r.CommittedAt) {
		t.Errorf("the answer's entry is at %v, its receipt %+v, %v", got[1].At, r, err)
	}
	// A refused answer writes no entry with it.
	if code, _, _ := runDecide(t, "stop", "answer", "--store", path, "--repo", "app", "--task", "t1", "--decision", "q1", "--answer-file", "-"); code != 1 {
		t.Fatal("a second answer was recorded")
	}
	if n := len(conversation(t, path, "app", "t1")); n != 2 {
		t.Errorf("a refused answer wrote an entry: %d", n)
	}
}

// TestPageNotesAreTheGuildmastersTalk: words only on the page record the Guildmaster's words, as they wrote them,
// under the ask, written with the note; a tile answer records none, and the page shows the talk inside the ask with
// the rail's Talk line.
func TestPageNotesAreTheGuildmastersTalk(t *testing.T) {
	h, path, _ := pageFixture(t)
	words := url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "send": {"words"}, "text": {"Hold on: does it cover **Windows**?"}}
	if w := postPage(t, h, "a-approve", words); w.Code != http.StatusSeeOther {
		t.Fatalf("words: %d\n%s", w.Code, w.Body)
	}
	if w := postPage(t, h, "a-approve", words); w.Code != http.StatusSeeOther {
		t.Fatalf("words again: %d\n%s", w.Code, w.Body)
	}
	got := conversation(t, path, "app", "a-approve")
	want := store.Saying{Author: store.ConversationGuildmasterPage, Tag: "accept-plan", Kind: store.ConversationSay, Text: "Hold on: does it cover **Windows**?"}
	if len(got) != 1 || got[0].Saying != want {
		t.Fatalf("conversation = %+v", got)
	}
	if code, _, errOut := runTaskSay(t, "Yes: the build runs on Windows too.", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "accept-plan", "--text-file", "-"); code != 0 {
		t.Fatalf("task say = %d %s", code, errOut)
	}

	get := httptest.NewRequest(http.MethodGet, "/app/a-approve", nil)
	get.Host, get.RemoteAddr = pageHost, pageNode
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, get)
	body := gw.Body.String()
	for _, want := range []string{
		`<div class="talk" id="accept-plan-talk"><p class="pick">Talk on #accept-plan</p><div class="msg"><div class="mh"><b>You</b><span class="src">page</span>`,
		`<div class="mb"><p>Hold on: does it cover <strong>Windows</strong>?</p>`,
		`<div class="msg dir"><div class="mh"><b>Director</b>`, `<p>Yes: the build runs on Windows too.</p>`,
		`<a class="tg" href="#accept-plan-talk">accept-plan</a>`,
		`<dt>Talk</dt><dd>2 messages · last `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}

	answer := url.Values{"decision": {"accept-plan"}, "subject": {planSubject}, "option": {"approve"}, "text": {"Go ahead."}}
	if w := postPage(t, h, "a-approve", answer); w.Code != http.StatusSeeOther {
		t.Fatalf("answer: %d\n%s", w.Code, w.Body)
	}
	if n := len(conversation(t, path, "app", "a-approve")); n != 2 {
		t.Errorf("a page answer wrote a conversation entry: %d entries", n)
	}
}

// TestWebSourceConversation: the Source gives the page the task's conversation with each tag's stage, the current
// stage's attempt as the task's tag, and each attempt and decision node its tag.
func TestWebSourceConversation(t *testing.T) {
	dir := clerkFixture(t)
	path := filepath.Join(dir, "director.store")
	if code, _, errOut := runTaskSay(t, "Planned.", "--store", path, "--repo", "app", "--task", "a-approve", "--tag", "plan-1", "--text-file", "-"); code != 0 {
		t.Fatalf("task say = %d %s", code, errOut)
	}
	task, err := webSourceFor(t, dir).Task("app", "a-approve")
	if err != nil {
		t.Fatal(err)
	}
	if len(task.Conversation) != 1 || task.Conversation[0].Tag != "plan-1" || task.Conversation[0].Stage != "plan" || task.Conversation[0].Text != "Planned." ||
		task.Conversation[0].Author != store.ConversationDirector || task.Conversation[0].At.IsZero() {
		t.Fatalf("conversation = %+v", task.Conversation)
	}
	if task.Tag != "plan-1" {
		t.Errorf("tag = %q, want the latest attempt plan-1", task.Tag)
	}
	tags := map[string]string{}
	for _, n := range task.Story {
		tags[n.Title] = n.Tag
	}
	if tags["The plan was written (round 1)"] != "plan-1" || tags["The plan is ready for your approval"] != "accept-plan" || tags["You added the task"] != "" {
		t.Errorf("node tags = %v", tags)
	}
	if len(task.Asks) != 1 || task.Asks[0].Stage != "" {
		t.Errorf("asks = %+v; accept-plan names no step", task.Asks)
	}
}
