package messagingtest

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/messaging"
)

type fakeHarness struct{ f *Fake }

func (h fakeHarness) Mailbox() messaging.Mailbox                       { return h.f }
func (h fakeHarness) CreateTask(_ *testing.T, repository, task string) { h.f.AddTask(repository, task) }
func (h fakeHarness) StartAttempt(_ *testing.T, repository, task, step string) int {
	return h.f.StartAttempt(repository, task, step)
}
func (h fakeHarness) FinishAttempt(_ *testing.T, repository, task, step string, number int) {
	h.f.FinishAttempt(repository, task, step, number)
}

// Restart keeps everything: the fake models one process whose state is all committed.
func (h fakeHarness) Restart(*testing.T) {}

func TestFakeMailboxContract(t *testing.T) {
	RunMailboxContract(t, func(*testing.T) Harness { return fakeHarness{NewFake()} })
}
