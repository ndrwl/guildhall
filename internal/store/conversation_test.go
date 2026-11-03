package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func say(author, tag, kind, text string) Saying {
	return Saying{Author: author, Tag: tag, Kind: kind, Text: text}
}

func TestAddConversation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	good := say(ConversationDirector, "grill-2", ConversationSay, "Round 1 is on the page.")
	for name, tc := range map[string]struct {
		task string
		say  Saying
		want error
	}{
		"free-text author": {"t-1", say("andrew", "grill-2", ConversationSay, "Hi."), ErrInvalid},
		"unknown kind":     {"t-1", say(ConversationDirector, "grill-2", "shout", "Hi."), ErrInvalid},
		"bad tag":          {"t-1", say(ConversationDirector, "Grill 2", ConversationSay, "Hi."), ErrInvalid},
		"no tag":           {"t-1", say(ConversationDirector, "", ConversationSay, "Hi."), ErrInvalid},
		"blank text":       {"t-1", say(ConversationDirector, "grill-2", ConversationSay, " \n"), ErrInvalid},
		"oversized text":   {"t-1", say(ConversationDirector, "grill-2", ConversationSay, strings.Repeat("x", MaxTextBytes+1)), ErrInvalid},
		"invalid utf-8":    {"t-1", say(ConversationDirector, "grill-2", ConversationSay, "\xff"), ErrInvalid},
		"unknown task":     {"t-9", good, ErrNotFound},
	} {
		if _, err := s.AddConversation("bad-"+strings.ReplaceAll(name, " ", "-"), "app", tc.task, tc.say); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	if got, err := s.Conversation("app", "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("Conversation after refusals = %+v, %v", got, err)
	}

	says := []Saying{good, say(ConversationGuildmasterChat, "accept-1", ConversationSay, "Looks *good*."),
		say(ConversationGuildmasterPage, "grill-2", ConversationSay, strings.Repeat("y", MaxTextBytes))}
	var receipts []Receipt
	for i, sy := range says {
		rc, err := s.AddConversation("say-"+string(rune('1'+i)), "app", "t-1", sy)
		if err != nil || rc.Record != ConversationRecord(i+1) || rc.Operation != "add-conversation" || rc.Repository != "app" || rc.TaskID != "t-1" {
			t.Fatalf("AddConversation %d = %+v, %v", i+1, rc, err)
		}
		receipts = append(receipts, rc)
	}
	if replay, err := s.AddConversation("say-1", "app", "t-1", good); err != nil || replay != receipts[0] {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, receipts[0])
	}
	if _, err := s.AddConversation("say-1", "app", "t-1", say(ConversationDirector, "grill-2", ConversationSay, "Other words.")); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("reused mutation id: err = %v, want ErrMutationConflict", err)
	}
	got, err := s.Conversation("app", "t-1")
	if err != nil || len(got) != 3 {
		t.Fatalf("Conversation = %+v, %v", got, err)
	}
	for i, e := range got {
		w := ConversationEntry{Repository: "app", TaskID: "t-1", Number: i + 1, Saying: says[i], At: receipts[i].CommittedAt}
		if e != w {
			t.Errorf("entry %d = %+v, want %+v", i+1, e, w)
		}
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.Conversation) != 3 || h.Conversation[2] != got[2] {
		t.Fatalf("TaskHistory conversation = %+v, %v", h.Conversation, err)
	}
	// Another task's entries are numbered on their own.
	mustCreate(t, s, "m-10", sampleTask("app", "t-10"))
	if rc, err := s.AddConversation("say-t10", "app", "t-10", good); err != nil || rc.Record != ConversationRecord(1) {
		t.Fatalf("first entry of t-10 = %+v, %v", rc, err)
	}
	if got, _ := s.Conversation("app", "t-1"); len(got) != 3 {
		t.Errorf("t-1 has %d entries after t-10's, want 3", len(got))
	}

	for _, state := range []TaskState{TaskDone, TaskCancelled} {
		id := "t-" + string(state)
		mustCreate(t, s, "m-"+id, sampleTask("app", id))
		if _, err := s.AddConversation("before-"+id, "app", id, good); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetTaskState("close-"+id, "app", id, state); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddConversation("after-"+id, "app", id, good); !errors.Is(err, ErrInvalid) {
			t.Errorf("an entry on a %s task: err = %v, want ErrInvalid", state, err)
		}
		if got, err := s.Conversation("app", id); err != nil || len(got) != 1 {
			t.Errorf("a %s task's conversation = %+v, %v; want it readable", state, got, err)
		}
	}
}

func TestMirroredConversation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	q := Decision{Repository: "app", TaskID: "t-1", ID: "q1", Kind: DecisionQuestion, Question: "Back off or stop?", Options: []string{"back-off", "stop"}}
	asked := say(ConversationDirector, "q1", ConversationSay, q.Question)
	bad := say(ConversationDirector, "q1", "shout", "x")
	if _, err := s.OpenDecisionSaying("open-q1", q, &bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an invalid saying: err = %v, want ErrInvalid", err)
	}
	if all, err := s.Decisions("app", "t-1"); err != nil || len(all) != 0 {
		t.Fatalf("an invalid saying wrote the decision: %+v", all)
	}
	open, err := s.OpenDecisionSaying("open-q1", q, &asked)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.OpenDecisionSaying("open-q1", q, &asked); err != nil || replay != open {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	answered := say(ConversationGuildmasterChat, "q1", ConversationSay, "back-off")
	if _, err := s.AnswerDecisionSaying("answer-q1", "app", "t-1", "q1", "back-off", "", "", &answered); err != nil {
		t.Fatal(err)
	}
	// A refused answer writes no entry.
	if _, err := s.AnswerDecisionSaying("answer-q1-again", "app", "t-1", "q1", "stop", "", "", &answered); err == nil {
		t.Fatal("a second answer was recorded")
	}
	m := Message{ID: "page-note.1", Recipient: RecipientDirector, Repository: "app", TaskID: "t-1", Type: "note", Text: "t-1 #q1, words only: hi"}
	noted := say(ConversationGuildmasterPage, "q1", ConversationSay, "hi")
	if _, err := s.SendMessageSaying(m, &noted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessageSaying(m, &noted); err != nil {
		t.Fatalf("resend: %v", err)
	}
	got, err := s.Conversation("app", "t-1")
	if err != nil || len(got) != 3 || got[0].Saying != asked || got[1].Saying != answered || got[2].Saying != noted {
		t.Fatalf("Conversation = %+v, %v", got, err)
	}
	if got[0].At != open.CommittedAt {
		t.Errorf("the question's entry is at %v, its receipt at %v", got[0].At, open.CommittedAt)
	}

	// A done task's audit is answered with no entry: its conversation is closed.
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	audit := Decision{Repository: "app", TaskID: "t-2", ID: "q1", Kind: DecisionQuestion, Question: "Audit?"}
	if _, err := s.OpenDecision("open-t2", audit); err != nil {
		t.Fatal(err)
	}
	// The engine opens a done task's audit itself; here the task is marked done under the open decision directly.
	if err := s.db.Update(func(tx *bolt.Tx) error {
		task, err := getTask(tx, "app", "t-2")
		if err != nil {
			return err
		}
		task.State = TaskDone
		return putJSON(repoBucket(tx, "app", bucketTasks), []byte("t-2"), task)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerDecisionSaying("answer-t2", "app", "t-2", "q1", "fine", "", "", &answered); err != nil {
		t.Fatalf("answering a done task's decision: %v", err)
	}
	if got, err := s.Conversation("app", "t-2"); err != nil || len(got) != 0 {
		t.Errorf("a done task's conversation = %+v, %v; want none", got, err)
	}
}

func TestConversationDoctorAndExport(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for _, m := range []string{"say-1", "say-2"} {
		if _, err := s.AddConversation(m, "app", "t-1", say(ConversationDirector, "grill-1", ConversationSay, "Words for "+m+".")); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || len(rep.Repositories) != 1 || rep.Repositories[0].Conversation != 2 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	dir, _ := export(t, s, ExportOptions{})
	restored := filepath.Join(t.TempDir(), "restored.store")
	if rep, err := Import(restored, dir); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("Import = %+v, %v", rep, err)
	}
	r := openTest(t, restored)
	want, _ := s.Conversation("app", "t-1")
	got, err := r.Conversation("app", "t-1")
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("restored conversation = %+v, %v; want %+v", got, err, want)
	}
	if rc, err := r.AddConversation("say-3", "app", "t-1", say(ConversationDirector, "grill-1", ConversationSay, "After.")); err != nil || rc.Record != ConversationRecord(3) {
		t.Fatalf("entry after the restore = %+v, %v", rc, err)
	}

	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketConversation).Put([]byte("t-1/9999999999"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || rep.Repositories[0].Conversation != 3 {
		t.Fatalf("doctor after a bad entry = %+v, %v", rep, err)
	}
}

// TestRecommendationAndConfirmation: a recommendation is the Director's and names an option, with words or none; a
// confirmation is the Guildmaster's and names its option and the recommendation it confirms; no other kind carries
// an option, words or a confirmed number. Each round-trips through the store.
func TestRecommendationAndConfirmation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	rec := Saying{Author: ConversationDirector, Tag: "accept-1", Kind: ConversationRecommendation, Text: "I recommend **approve**.", Option: "approve", Words: "approve, it covers Windows"}
	conf := Saying{Author: ConversationGuildmasterPage, Tag: "accept-1", Kind: ConversationConfirmed, Text: "approve\n\nit covers Windows", Option: "approve", Confirms: 1}
	for name, bad := range map[string]Saying{
		"recommendation by the Guildmaster": {Author: ConversationGuildmasterPage, Tag: "accept-1", Kind: ConversationRecommendation, Text: "x", Option: "approve"},
		"recommendation of nothing":         {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationRecommendation, Text: "x"},
		"recommendation of two lines":       {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationRecommendation, Text: "x", Option: "a\nb"},
		"recommendation that confirms":      {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationRecommendation, Text: "x", Option: "a", Confirms: 1},
		"oversized words":                   {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationRecommendation, Text: "x", Option: "a", Words: strings.Repeat("w", MaxTextBytes+1)},
		"confirmation by the Director":      {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationConfirmed, Text: "x", Option: "a", Confirms: 1},
		"confirmation of nothing":           {Author: ConversationGuildmasterPage, Tag: "accept-1", Kind: ConversationConfirmed, Text: "x", Option: "a"},
		"confirmation with words":           {Author: ConversationGuildmasterPage, Tag: "accept-1", Kind: ConversationConfirmed, Text: "x", Option: "a", Confirms: 1, Words: "w"},
		"say with an option":                {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationSay, Text: "x", Option: "a"},
		"say with words":                    {Author: ConversationDirector, Tag: "accept-1", Kind: ConversationSay, Text: "x", Words: "w"},
	} {
		if _, err := s.AddConversation("bad-"+strings.ReplaceAll(name, " ", "-"), "app", "t-1", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	for i, say := range []Saying{rec, conf} {
		if _, err := s.AddConversation("ok-"+string(rune('1'+i)), "app", "t-1", say); err != nil {
			t.Fatalf("%+v: %v", say, err)
		}
	}
	got, err := s.Conversation("app", "t-1")
	if err != nil || len(got) != 2 || got[0].Saying != rec || got[1].Saying != conf {
		t.Fatalf("Conversation = %+v, %v", got, err)
	}
}

// TestDecisionRecommended: a question may carry the option the Director recommends, one of its options; no other
// kind may.
func TestDecisionRecommended(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	q := Decision{Repository: "app", TaskID: "t-1", ID: "q1", Kind: DecisionQuestion, Question: "Back off?", Options: []string{"back-off", "stop"}, Recommended: "pause"}
	if _, err := s.OpenDecision("m-2", q); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a recommendation not offered: err = %v", err)
	}
	a := sampleDecision("accept-1")
	a.Recommended = "approve"
	if _, err := s.OpenDecision("m-3", a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a recommendation on an approval: err = %v", err)
	}
	q.Recommended = "stop"
	if _, err := s.OpenDecision("m-4", q); err != nil {
		t.Fatal(err)
	}
	all, err := s.Decisions("app", "t-1")
	if err != nil || len(all) != 1 || all[0].Recommended != "stop" {
		t.Fatalf("Decisions = %+v, %v", all, err)
	}
}
