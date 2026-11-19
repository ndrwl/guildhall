package steward

import "testing"

// TestHasEntryGuildhallPath: a roster whose machine has another guildhall_path than a new machine's planned entry does
// not hold that entry, and one with the same path does.
func TestHasEntryGuildhallPath(t *testing.T) {
	e := RosterEntry{Machine: "vds", NewMachine: true, SSHAlias: "guild-vds", OS: OSLinux, GuildhallPath: "/home/u/.local/bin/guildhall",
		Director: "ops", CopyDir: "/home/u/ops", Repositories: []string{"infra"}}
	roster := func(path string) *Roster {
		return &Roster{SchemaVersion: 1, Machines: []Machine{{Label: "vds", SSHAlias: "guild-vds", OS: OSLinux, Roles: []Role{RoleDirector},
			GuildhallPath: path, Directors: []Director{{Name: "ops", CopyDir: "/home/u/ops", Repositories: []string{"infra"}}}}}}
	}
	if !hasEntry(roster(e.GuildhallPath), e) {
		t.Error("the planned machine does not hold the entry")
	}
	if hasEntry(roster("/opt/guildhall/guildhall"), e) {
		t.Error("a machine with another guildhall_path holds the entry")
	}
	if hasEntry(roster(""), e) {
		t.Error("a machine with no guildhall_path holds the entry")
	}
}
