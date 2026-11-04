package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func draft(q int, mark, text string) Draft {
	return Draft{Tag: "grill-2", Round: 1, Question: q, Mark: mark, Text: text}
}

// TestSaveDrafts: drafts validate, append only what changed, the latest of each question wins, a retry replays, a
// closed task takes none, and closing a task drops its drafts.
func TestSaveDrafts(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, bad := range map[string][]Draft{
		"none":             nil,
		"bad tag":          {{Tag: "Grill 2", Round: 1, Question: 1}},
		"no round":         {{Tag: "grill-2", Question: 1}},
		"no question":      {{Tag: "grill-2", Round: 1}},
		"unknown mark":     {draft(1, "maybe", "x")},
		"words with none":  {draft(1, DraftWords, " \n")},
		"oversized words":  {draft(1, "", strings.Repeat("x", MaxTextBytes+1))},
		"invalid utf-8":    {draft(1, "", "\xff")},
		"one bad of three": {draft(1, "", "a"), draft(2, "", "b"), draft(3, "nope", "")},
	} {
		if _, err := s.SaveDrafts("bad-"+strings.ReplaceAll(name, " ", "-"), "app", "t-1", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.SaveDrafts("unknown", "app", "t-9", []Draft{draft(1, "", "x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: err = %v, want ErrNotFound", err)
	}
	if got, err := s.Drafts("app", "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("Drafts after refusals = %+v, %v", got, err)
	}

	// An empty box never saved is no draft; words and marks are.
	first := []Draft{draft(1, "", "Keep it to CSV."), draft(2, DraftAccept, ""), draft(3, "", "")}
	rc, err := s.SaveDrafts("save-1", "app", "t-1", first)
	if err != nil || rc.Record != "drafts/2" || rc.Operation != "save-drafts" {
		t.Fatalf("SaveDrafts = %+v, %v", rc, err)
	}
	if replay, err := s.SaveDrafts("save-1", "app", "t-1", first); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	// The same drafts again write nothing; a changed one is appended and wins.
	second := []Draft{draft(1, DraftWords, "Keep it to CSV."), draft(2, DraftAccept, ""), draft(3, "", "")}
	if rc, err := s.SaveDrafts("save-2", "app", "t-1", second); err != nil || rc.Record != "drafts/3" {
		t.Fatalf("second save = %+v, %v", rc, err)
	}
	if rc, err := s.SaveDrafts("save-3", "app", "t-1", second); err != nil || rc.Record != "drafts/0" {
		t.Fatalf("unchanged save = %+v, %v; want drafts/0", rc, err)
	}
	got, err := s.Drafts("app", "t-1")
	if err != nil || len(got) != 2 || got[0].Draft != second[0] || got[0].Number != 3 || got[1].Draft != second[1] || got[1].Number != 2 {
		t.Fatalf("Drafts = %+v, %v", got, err)
	}
	if got[0].At.IsZero() {
		t.Error("a draft has no time")
	}
	if h, err := s.TaskHistory("app", "t-1"); err != nil || len(h.Drafts) != 2 || h.Drafts[0] != got[0] {
		t.Fatalf("TaskHistory drafts = %+v, %v", h.Drafts, err)
	}
	// Another round's question 1 is a draft of its own.
	if _, err := s.SaveDrafts("save-4", "app", "t-1", []Draft{{Tag: "grill-2", Round: 5, Question: 1, Text: "Later."}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Drafts("app", "t-1"); len(got) != 3 {
		t.Fatalf("drafts of two rounds = %+v", got)
	}

	// Closing drops the drafts, and a closed task takes none.
	if _, err := s.SetTaskState("close-1", "app", "t-1", TaskDone); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Drafts("app", "t-1"); err != nil || len(got) != 0 {
		t.Fatalf("Drafts of a done task = %+v, %v; want none", got, err)
	}
	if _, err := s.SaveDrafts("save-5", "app", "t-1", first); !errors.Is(err, ErrInvalid) {
		t.Errorf("drafts on a done task: err = %v, want ErrInvalid", err)
	}
	mustCreate(t, s, "m-2", sampleTask("app", "t-2"))
	if _, err := s.SaveDrafts("save-t2", "app", "t-2", first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTask("cancel-2", "app", "t-2", "Not wanted."); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Drafts("app", "t-2"); len(got) != 0 {
		t.Errorf("Drafts of a cancelled task = %+v; want none", got)
	}
}

// TestDraftsDoctorAndExport: store doctor counts and decodes drafts, and an export and import keeps them.
func TestDraftsDoctorAndExport(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	if _, err := s.SaveDrafts("save-1", "app", "t-1", []Draft{draft(1, "", "a"), draft(2, DraftAccept, "")}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || rep.Repositories[0].Drafts != 2 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	dir, _ := export(t, s, ExportOptions{})
	restored := filepath.Join(t.TempDir(), "restored.store")
	if rep, err := Import(restored, dir); err != nil || len(rep.Problems) != 0 {
		t.Fatalf("Import = %+v, %v", rep, err)
	}
	want, _ := s.Drafts("app", "t-1")
	got, err := openTest(t, restored).Drafts("app", "t-1")
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("restored drafts = %+v, %v; want %+v", got, err, want)
	}
}

// TestRoundSayings: a round is the Director's, its answers are the Guildmaster's and name the round they answer, and
// no other kind names a round.
func TestRoundSayings(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	for name, bad := range map[string]Saying{
		"round by the Guildmaster":    {Author: ConversationGuildmasterPage, Tag: "grill-1", Kind: ConversationRound, Text: "x"},
		"answers by the Director":     {Author: ConversationDirector, Tag: "grill-1", Kind: ConversationRoundAnswers, Text: "x", Round: 1},
		"answers to no round":         {Author: ConversationGuildmasterPage, Tag: "grill-1", Kind: ConversationRoundAnswers, Text: "x"},
		"answers with an option":      {Author: ConversationGuildmasterPage, Tag: "grill-1", Kind: ConversationRoundAnswers, Text: "x", Round: 1, Option: "a"},
		"say naming a round":          {Author: ConversationDirector, Tag: "grill-1", Kind: ConversationSay, Text: "x", Round: 1},
		"round naming a round":        {Author: ConversationDirector, Tag: "grill-1", Kind: ConversationRound, Text: "x", Round: 1},
		"recommendation with a round": {Author: ConversationDirector, Tag: "grill-1", Kind: ConversationRecommendation, Text: "x", Option: "a", Round: 1},
	} {
		if _, err := s.AddConversation("bad-"+strings.ReplaceAll(name, " ", "-"), "app", "t-1", bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	round := Saying{Author: ConversationDirector, Tag: "grill-1", Kind: ConversationRound, Text: "**Q1. CSV?**\nRecommended: yes."}
	answers := Saying{Author: ConversationGuildmasterPage, Tag: "grill-1", Kind: ConversationRoundAnswers, Text: "**Q1. CSV?**\nAccepted: yes.", Round: 1}
	for i, sy := range []Saying{round, answers} {
		if _, err := s.AddConversation("ok-"+string(rune('1'+i)), "app", "t-1", sy); err != nil {
			t.Fatalf("%s: %v", sy.Kind, err)
		}
	}
	if got, _ := s.Conversation("app", "t-1"); len(got) != 2 || got[0].Saying != round || got[1].Saying != answers {
		t.Fatalf("Conversation = %+v", got)
	}
}
