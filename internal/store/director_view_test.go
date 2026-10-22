package store

import (
	"slices"
	"testing"
	"time"
)

func TestDirectorViewOfAnEmptyStore(t *testing.T) {
	s, _ := newStore(t)
	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	if v.AsOf != (Snapshot{}) || v.Tasks == nil || v.Attempts == nil || v.Owners == nil || v.OpenDecisions == nil || v.Unread == nil || v.Receipts == nil ||
		len(v.Tasks)+len(v.Attempts)+len(v.Owners)+len(v.OpenDecisions)+len(v.Unread)+len(v.Receipts) != 0 {
		t.Fatalf("empty store view %+v", v)
	}
}

// TestDirectorView checks every list across two repositories: an answered decision, a released owner,
// an acknowledged message and an Artisan message are left out, and receipts outside a task are too.
func TestDirectorView(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("lib", "t-2"))
	mustCreate(t, s, "m-2", sampleTask("app", "t-1"))
	if _, err := s.StartOwnedAttempt("m-3", sampleOwnership(launcherA)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAttempt("m-4", "app", "t-1", "build", 1, AttemptFailed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartOwnedAttempt("m-5", sampleOwnership(launcherB)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("m-6", "lib", "t-2", "plan"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []Decision{
		{Repository: "app", TaskID: "t-1", ID: "b-open", Kind: DecisionQuestion, Question: "Which branch?"},
		{Repository: "app", TaskID: "t-1", ID: "a-answered", Kind: DecisionQuestion, Question: "Rebase?"},
		{Repository: "lib", TaskID: "t-2", ID: "c-open", Kind: DecisionQuestion, Question: "Ship it?"},
	} {
		if _, err := s.OpenDecision("open-"+d.ID, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AnswerDecision("answer", "app", "t-1", "a-answered", "yes"); err != nil {
		t.Fatal(err)
	}
	lib := Message{ID: "q-lib", Recipient: RecipientDirector, Repository: "lib", TaskID: "t-2", Type: "question", Text: "Which parser?"}
	mustSend(t, s, lib)
	mustSend(t, s, toDirector("q-app"))
	mustSend(t, s, Message{ID: "q-acked", Recipient: RecipientDirector, Repository: "app", TaskID: "t-1", Type: "note", Text: "Done."})
	artisan := toArtisan("n-artisan")
	artisan.Attempt = 2
	mustSend(t, s, artisan)
	if _, err := s.AckMessage(Recipient{Role: RecipientDirector}, "q-acked"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveWorkflow(WorkflowApproval{Name: "default", Digest: sampleDigest('a'), Words: "fine"}); err != nil {
		t.Fatal(err)
	}

	v, err := s.DirectorView()
	if err != nil {
		t.Fatal(err)
	}
	var tasks, attempts, owners, decisions, unread []string
	for _, x := range v.Tasks {
		tasks = append(tasks, x.Repository+"/"+x.ID)
	}
	for _, x := range v.Attempts {
		attempts = append(attempts, x.Repository+"/"+x.TaskID+"/"+x.Step+"/"+string(rune('0'+x.Number))+":"+string(x.Outcome))
	}
	for _, x := range v.Owners {
		owners = append(owners, x.TaskID+"/"+x.Step+"/"+string(rune('0'+x.Attempt))+":"+string(x.State))
	}
	for _, x := range v.OpenDecisions {
		decisions = append(decisions, x.Repository+"/"+x.ID)
	}
	for _, x := range v.Unread {
		unread = append(unread, x.ID)
	}
	for _, c := range []struct {
		name      string
		got, want []string
	}{
		{"tasks", tasks, []string{"app/t-1", "lib/t-2"}},
		{"attempts", attempts, []string{"app/t-1/build/1:failed", "app/t-1/build/2:", "lib/t-2/plan/1:"}},
		{"owners", owners, []string{"t-1/build/2:starting"}},
		{"open decisions", decisions, []string{"app/b-open", "lib/c-open"}},
		{"unread", unread, []string{"q-lib", "q-app"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	last, err := s.Receipt("message-ack:q-acked")
	if err != nil {
		t.Fatal(err)
	}
	if v.AsOf.Sequence <= last.Sequence {
		t.Errorf("as of %d, before the newest mutation %d", v.AsOf.Sequence, last.Sequence)
	}
	for i, r := range v.Receipts {
		if r.TaskID == "" || (i > 0 && r.Sequence <= v.Receipts[i-1].Sequence) {
			t.Fatalf("receipt %d %+v: every receipt names a task, in sequence order", i, r)
		}
	}
	if n := len(v.Receipts); n == 0 || v.Receipts[n-1].MutationID != "message-ack:q-acked" {
		t.Fatalf("receipts end %+v, want the acknowledgement last", v.Receipts)
	}
	day := time.Date(testTime.Year(), testTime.Month(), testTime.Day(), 0, 0, 0, 0, time.UTC)
	if want := (AttemptUsage{Running: 2, Today: 3, Day: day}); v.Usage != want {
		t.Errorf("Usage = %+v, want %+v: two attempts running, three started today", v.Usage, want)
	}
}
