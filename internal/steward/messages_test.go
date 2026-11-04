package steward_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestDeliver: a Steward message is copied to the copy's fixed drop file, then received there by the one fixed
// command; the copy's printed result is returned, a refusal as a result and an unreachable machine as an error.
func TestDeliver(t *testing.T) {
	m := steward.Machine{Label: "laptop", SSHAlias: "guild-laptop", OS: steward.OSWindows, GuildhallPath: "C:/Users/guild/bin/guildhall.exe"}
	d := steward.Director{Name: "tools", CopyDir: "C:/Users/guild/directors/tools"}
	r := steward.Report{Machine: m.Label, Director: d.Name, Kind: store.ReportRolloutFailed, Key: "plan-1", Subject: "Rollout failed", Text: "It failed."}
	env, err := r.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	if e, err := messaging.DecodeDirector(strings.NewReader(string(env))); err != nil || e.ID != steward.ReportID(r) || e.From != "steward" || e.To != "tools" {
		t.Fatalf("envelope %s: %+v, %v", env, e, err)
	}
	f := stewardtest.NewFleet()
	f.Answer(m.SSHAlias, []byte(`{"schema":"guildhall.receive-result/1","status":"delivered","receipt":{"mutation_id":"dmessage:steward:x","operation":"receive-external-message","request_digest":"sha256:1","repository":"","task_id":"","sequence":7,"committed_at":"2026-11-04T10:00:00Z"}}`+"\n"), "")
	res, err := steward.Deliver(context.Background(), f, f, m, d, env, t.TempDir())
	if err != nil || res.Status != messaging.ReceiveDelivered || res.Receipt.Sequence != 7 {
		t.Fatalf("Deliver: %+v, %v", res, err)
	}
	want := []string{"put guild-laptop:C:/Users/guild/directors/tools/" + steward.MessageDropPath,
		"run guild-laptop C:/Users/guild/bin/guildhall.exe messages receive --copy C:/Users/guild/directors/tools"}
	if got := f.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}
	if got, _ := f.Read(steward.Remote{Alias: m.SSHAlias, Path: d.CopyDir + "/" + steward.MessageDropPath}); string(got) != string(env) {
		t.Errorf("drop file %q, want the envelope", got)
	}
	f.Answer(m.SSHAlias, []byte(`{"schema":"guildhall.receive-result/1","status":"refused","reason":"closed: the inbox is closed to steward: busy"}`), "")
	if res, err := steward.Deliver(context.Background(), f, f, m, d, env, t.TempDir()); err != nil || res.Status != messaging.ReceiveRefused {
		t.Errorf("refused: %+v, %v", res, err)
	}
	f.Answer(m.SSHAlias, nil, "guildhall: unknown command messages receive")
	if _, err := steward.Deliver(context.Background(), f, f, m, d, env, t.TempDir()); err == nil {
		t.Error("an older guildhall's failure was not an error")
	}
	f.Answer(m.SSHAlias, []byte(`{"status":"delivered"}`), "")
	if _, err := steward.Deliver(context.Background(), f, f, m, d, env, t.TempDir()); err == nil {
		t.Error("an unreadable result was not an error")
	}
	f.Refuse(m.SSHAlias)
	if _, err := steward.Deliver(context.Background(), f, f, m, d, env, t.TempDir()); err == nil {
		t.Error("an unreachable machine was not an error")
	}
}

// TestReportID: one issue on one copy has one message id, and another copy or issue another.
func TestReportID(t *testing.T) {
	r := steward.Report{Machine: "laptop", Director: "tools", Kind: store.ReportRolloutFailed, Key: "plan-1"}
	id := steward.ReportID(r)
	if id != steward.ReportID(r) || !strings.HasPrefix(id, "rollout-failed.") || len(id) != len("rollout-failed.")+16 {
		t.Fatalf("id %q", id)
	}
	for _, other := range []steward.Report{{Machine: "vds", Director: "tools", Kind: r.Kind, Key: r.Key}, {Machine: r.Machine, Director: r.Director, Kind: r.Kind, Key: "plan-2"}} {
		if steward.ReportID(other) == id {
			t.Errorf("%+v has the same id as %+v", other, r)
		}
	}
}
