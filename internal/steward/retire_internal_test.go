package steward

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	retireWS       = "age1workstation00000000000000000000000000000000000000000000"
	retireLaptop   = "age1laptop000000000000000000000000000000000000000000000000000"
	retireRecovery = "age1recovery000000000000000000000000000000000000000000000000"
)

const retireRoster = `schema_version = 1
recovery_recipient = "` + retireRecovery + `"

[[machine]]
label = "workstation"
ssh_alias = "guild-ws"
os = "linux"
roles = ["director"]
age_recipient = "` + retireWS + `" # from secrets init

[[machine]]
label = "laptop"
ssh_alias = "guild-laptop"
os = "linux"
roles = ["director"]
age_recipient = "` + retireLaptop + `"
`

// TestWithoutRecipient: retiring removes only the machine's age_recipient line, and refuses a roster that does not
// write it on a line of its own.
func TestWithoutRecipient(t *testing.T) {
	r, err := ParseRoster([]byte(retireRoster))
	if err != nil {
		t.Fatal(err)
	}
	got, err := withoutRecipient([]byte(retireRoster), r, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(retireRoster, "age_recipient = \""+retireWS+"\" # from secrets init\n", "", 1); string(got) != want {
		t.Fatalf("roster =\n%s\nwant\n%s", got, want)
	}
	inline := `schema_version = 1
machine = [{label = "workstation", ssh_alias = "guild-ws", os = "linux", roles = ["director"], age_recipient = "` + retireWS + `"}]
`
	r, err = ParseRoster([]byte(inline))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutRecipient([]byte(inline), r, "workstation"); !errors.Is(err, ErrRosterConflict) {
		t.Fatalf("inline roster: %v, want ErrRosterConflict", err)
	}
}

// TestRetireReceiptClose: a rotation closes by its name and machine, a retired machine closes all of its own, and
// the receipt closes with its last rotation.
func TestRetireReceiptClose(t *testing.T) {
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	r := &RetireReceipt{State: RetireOpen, Rotations: []Rotation{{Name: "A", Machine: "laptop"}, {Name: "B", Machine: "laptop"}, {Name: "A", Machine: "server"}}}
	if r.close("A", "other", ClosedBySet, at) || r.close("C", "laptop", ClosedBySet, at) {
		t.Fatal("closed a rotation the receipt does not wait for")
	}
	if !r.close("A", "laptop", ClosedBySet, at) || len(r.Pending()) != 2 || r.State != RetireOpen {
		t.Fatalf("after A on laptop: %+v", r)
	}
	if !r.close("", "laptop", ClosedByRetired, at) || len(r.Pending()) != 1 || r.Rotations[1].ClosedBy != ClosedByRetired {
		t.Fatalf("after laptop retired: %+v", r)
	}
	if !r.close("A", "server", ClosedBySet, at) || r.State != RetireClosed || r.ClosedAt == nil {
		t.Fatalf("after the last: %+v", r)
	}
}

// TestRetireReceiptUnheld: a name no other machine holds waits for its own mark, and a retired machine does not
// close it.
func TestRetireReceiptUnheld(t *testing.T) {
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	r := &RetireReceipt{State: RetireOpen, Rotations: []Rotation{{Name: "A", Machine: "laptop"}, {Name: "B"}}}
	if !r.waits("B", "") || r.waits("A", "") || r.waits("B", "laptop") {
		t.Fatalf("waits: %+v", r)
	}
	if !r.close("", "laptop", ClosedByRetired, at) || r.State != RetireOpen || len(r.Pending()) != 1 {
		t.Fatalf("after laptop retired: %+v", r)
	}
	if !r.close("B", "", ClosedByMark, at) || r.State != RetireClosed || r.Rotations[1].ClosedBy != ClosedByMark {
		t.Fatalf("after the mark: %+v", r)
	}
}
