package steward_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
)

type stubHarness struct{ f steward.StubFleet }

func (h stubHarness) Copier() steward.Copier { return h.f.Transport() }
func (h stubHarness) Write(_ *testing.T, file steward.Remote, data []byte) {
	h.f.Write(file, data)
}
func (h stubHarness) Read(_ *testing.T, file steward.Remote) ([]byte, bool) { return h.f.Read(file) }
func (h stubHarness) Refuse(_ *testing.T, alias string)                     { h.f.Refuse(alias) }
func (h stubHarness) Hang(_ *testing.T, alias string)                       { h.f.Hang(alias) }

// TestTransportCopierContract runs the shared contract against scp, played by the test binary in a separate process.
func TestTransportCopierContract(t *testing.T) {
	stewardtest.RunCopierContract(t, func(t *testing.T) stewardtest.Harness { return stubHarness{steward.NewStubFleet(t)} })
}

// TestTransportTimeoutBoundsEachCopy covers the scp Transport's own limit, which applies even when the caller sets none.
func TestTransportTimeoutBoundsEachCopy(t *testing.T) {
	f := steward.NewStubFleet(t)
	file := steward.Remote{Alias: "alpha", Path: "/home/guild/tools/managed.toml"}
	f.Write(file, []byte("remote\n"))
	f.Hang("alpha")
	tr := f.Transport()
	tr.Timeout = 200 * time.Millisecond // timing:allow intentional timeout of a tool that never answers; no startup-speed assertion.
	start := time.Now()
	err := tr.Fetch(context.Background(), file, t.TempDir()+"/fetched.toml")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "scp: ") {
		t.Errorf("Fetch error = %v, want an scp error wrapping context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Fetch returned after %v", elapsed)
	}
}
