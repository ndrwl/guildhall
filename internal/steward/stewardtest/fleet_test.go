package stewardtest

import (
	"context"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/steward"
)

type fleetHarness struct{ f *Fleet }

func (h fleetHarness) Copier() steward.Copier { return h.f }
func (h fleetHarness) Write(_ *testing.T, file steward.Remote, data []byte) {
	h.f.Write(file, data)
}
func (h fleetHarness) Read(_ *testing.T, file steward.Remote) ([]byte, bool) { return h.f.Read(file) }
func (h fleetHarness) Refuse(_ *testing.T, alias string)                     { h.f.Refuse(alias) }
func (h fleetHarness) Hang(_ *testing.T, alias string)                       { h.f.Hang(alias) }

func TestFleetCopierContract(t *testing.T) {
	RunCopierContract(t, func(*testing.T) Harness { return fleetHarness{NewFleet()} })
}

type fleetRunnerHarness struct{ f *Fleet }

func (h fleetRunnerHarness) Runner() steward.Runner { return h.f }
func (h fleetRunnerHarness) Answer(_ *testing.T, alias string, output []byte, failure string) {
	h.f.Answer(alias, output, failure)
}
func (h fleetRunnerHarness) Ran(_ *testing.T, alias string) [][]string { return h.f.Ran(alias) }
func (h fleetRunnerHarness) Refuse(_ *testing.T, alias string)         { h.f.Refuse(alias) }
func (h fleetRunnerHarness) Hang(_ *testing.T, alias string)           { h.f.Hang(alias) }

// TestFleetAnswerCommand: a per-command answer answers only its command on its machine; every other command keeps
// the machine's answer.
func TestFleetAnswerCommand(t *testing.T) {
	f := NewFleet()
	f.Answer(Alpha, []byte("default"), "")
	f.AnswerCommand(Alpha, []string{"lesson", "list", "--json", "--pending", "--copy", posixCopy}, []byte(`{"lessons":[]}`), "")
	f.AnswerCommand(Alpha, []string{"director", "collected", "--copy", posixCopy}, nil, "locked")
	ctx := context.Background()
	if out, err := f.Run(ctx, Alpha, steward.PendingLessonsCommand(steward.DefaultGuildhall, posixCopy)); err != nil || string(out) != `{"lessons":[]}` {
		t.Errorf("lesson list = %q, %v", out, err)
	}
	if _, err := f.Run(ctx, Alpha, steward.CollectedCommand(steward.DefaultGuildhall, posixCopy)); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("director collected = %v, want its failure", err)
	}
	if out, err := f.Run(ctx, Alpha, steward.ModeCommand(steward.DefaultGuildhall, posixCopy)); err != nil || string(out) != "default" {
		t.Errorf("director mode = %q, %v; want the machine's answer", out, err)
	}
	if out, err := f.Run(ctx, Bravo, steward.PendingLessonsCommand(steward.DefaultGuildhall, posixCopy)); err != nil || len(out) != 0 {
		t.Errorf("another machine's lesson list = %q, %v", out, err)
	}
}

func TestFleetRunnerContract(t *testing.T) {
	RunRunnerContract(t, func(*testing.T) RunnerHarness { return fleetRunnerHarness{NewFleet()} })
}
