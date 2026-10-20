package artisan

import (
	"errors"
	"os"
	"testing"
)

func TestMemberRejectsProcessOutsideJob(t *testing.T) {
	unit, err := newUnit("membership-" + t.Name())
	if err != nil {
		t.Fatal(err)
	}
	u := unit.(*jobUnit)
	t.Cleanup(func() { _ = u.Close() })
	if id, err := u.member(os.Getpid()); !errors.Is(err, errNoProcess) {
		t.Fatalf("outside process returned as member: %v, %v", id, err)
	}
}
