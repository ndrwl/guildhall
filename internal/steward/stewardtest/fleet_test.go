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
