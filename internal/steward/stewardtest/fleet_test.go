package stewardtest

import (
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

func TestFleetRunnerContract(t *testing.T) {
	RunRunnerContract(t, func(*testing.T) RunnerHarness { return fleetRunnerHarness{NewFleet()} })
}
