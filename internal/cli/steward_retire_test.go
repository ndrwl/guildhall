//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/steward"
)

const laptopPushPub = "age1laptop000000000000000000000000000000000000000000000000000"

// laptopSecret is laptop's own API_KEY before the retirement, and rotatedSecret its new value after it; both are
// allowed in the private config's ciphertext only.
const (
	laptopSecret  = "sk-test-LAPTOP-VALUE"
	rotatedSecret = "sk-test-ROTATED-VALUE"
)

// TestStewardMachineRetire: the retire plan lists every name in the machine's file, which other machine holds each,
// and the credentials to revoke, and changes nothing; apply needs the Guildmaster's approval of its digest, then
// removes the recipient and the file in one pushed commit, leaving other machines' files untouched; the retirement
// stays open until secrets set gives each held name a new value, refusing as a rotation both the value the machine
// held and the one the retired machine held, and until the Guildmaster, outside a session, marks rotated each name
// no other machine holds; its record names each rotation and its date, and the machine cannot be retired again. No
// commit, plan, record or output holds a value.
func TestStewardMachineRetire(t *testing.T) {
	ps := newPushSetup(t)
	roster := filepath.Join(ps.private, "roster.toml")
	data, err := os.ReadFile(roster)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, roster, string(data)+"\n[[machine]]\nlabel = \"laptop\"\nssh_alias = \"guild-laptop\"\nos = \"linux\"\nroles = [\"director\"]\nage_recipient = \""+laptopPushPub+"\"\n")
	collectGit(t, ps.private, "commit", "-q", "-am", "enrol laptop")
	collectGit(t, ps.private, "push", "-q")
	for _, set := range [][3]string{{"workstation", "API_KEY", pushedSecret}, {"workstation", "DB_PASS", pushedPassword}, {"laptop", "API_KEY", laptopSecret}} {
		if code, out := ps.run(t, set[2], "steward", "secrets", "set", "--config", ps.cfg, set[0], set[1]); code != 0 {
			t.Fatalf("set %s %s: exit %d:\n%s", set[0], set[1], code, out)
		}
	}
	laptopFile := collectGit(t, ps.private, "rev-parse", "HEAD:secrets/laptop.sops.yaml")
	before := collectGit(t, ps.remote, "rev-parse", "main")

	if code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--plan", t.TempDir()); code != 2 {
		t.Fatalf("no machine: exit %d, want 2:\n%s", code, out)
	}
	planDir := filepath.Join(ps.steward, "rollouts", "retire-ws")
	code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--plan", planDir, "workstation")
	for _, want := range []string{
		"remove its age_recipient " + pushMachinePub,
		"remove secrets/workstation.sops.yaml",
		"holding API_KEY, DB_PASS",
		"API_KEY: rotate it, then set the new value with guildhall steward secrets set on laptop (waiting)",
		"DB_PASS: no other machine holds it; rotate or revoke it, then the Guildmaster marks it with guildhall steward machine retire --rotated DB_PASS workstation (waiting)",
		"GitHub:", "SSH:", "Host guild-ws", "Herdr:", "Claude:", "Codex:",
	} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("plan: exit %d, want %q:\n%s", code, want, out)
		}
	}
	if got := collectGit(t, ps.remote, "rev-parse", "main"); got != before {
		t.Fatal("the plan changed the private config")
	}
	plan, err := steward.ReadRetirePlan(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Rotations) != 2 || plan.Rotations[0] != (steward.Rotation{Name: "API_KEY", Machine: "laptop"}) || plan.Rotations[1] != (steward.Rotation{Name: "DB_PASS"}) {
		t.Fatalf("plan rotations %+v", plan.Rotations)
	}

	apply := []string{"steward", "machine", "retire", "--config", ps.cfg, "--plan", planDir, "--approve", plan.Digest, "workstation"}
	if code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--plan", planDir, "--approve", "sha256:0000", "workstation"); code != 1 || !strings.Contains(out, plan.Digest) {
		t.Fatalf("apply with another digest: exit %d, want 1 naming the plan's digest:\n%s", code, out)
	}
	if code, out := ps.runIn(t, true, "", apply...); code != 1 || !strings.Contains(out, "steward/secrets/"+retireDecisionID(plan.Digest)) {
		t.Fatalf("apply in a session before the answer: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, "", "steward", "approval", "request", "--plan", planDir, "--copy", ps.steward); code != 0 || !strings.Contains(out, retireDecisionID(plan.Digest)) {
		t.Fatalf("approval request: exit %d:\n%s", code, out)
	}
	answer := []string{"decide", "answer", "--copy", ps.steward, "--repo", stewardRepository, "--task", stewardSecretsTask, "--decision", retireDecisionID(plan.Digest), "--answer-file", "-"}
	if code, out := ps.run(t, "approve\n", answer...); code != 0 {
		t.Fatalf("answer: exit %d:\n%s", code, out)
	}
	if code, out := ps.runIn(t, true, "", apply...); code != 0 || !strings.Contains(out, "retired workstation") || !strings.Contains(out, ": open, 2 of 2 rotations waiting") {
		t.Fatalf("apply: exit %d:\n%s", code, out)
	}
	if got := collectGit(t, ps.remote, "log", "-1", "--format=%s", "--name-status", "main"); got != "Retire machine workstation\n\nM\t.sops.yaml\nA\tretired/workstation.json\nM\troster.toml\nD\tsecrets/workstation.sops.yaml" {
		t.Fatalf("the retirement commit:\n%s", got)
	}
	if got := collectGit(t, ps.private, "rev-parse", "HEAD:secrets/laptop.sops.yaml"); got != laptopFile {
		t.Fatal("the retirement changed laptop's secrets file")
	}
	for _, file := range []string{"roster.toml", ".sops.yaml"} {
		if got := collectGit(t, ps.private, "show", "HEAD:"+file); strings.Contains(got, pushMachinePub) || !strings.Contains(got, laptopPushPub) {
			t.Fatalf("%s after the retirement:\n%s", file, got)
		}
	}
	if code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--plan", filepath.Join(ps.steward, "rollouts", "again"), "workstation"); code != 1 || !strings.Contains(out, "open retirement") {
		t.Fatalf("a second retirement while open: exit %d:\n%s", code, out)
	}

	// A value the retired machine could read is not a rotation: neither the one laptop held nor the retired one.
	for _, old := range []string{laptopSecret, pushedSecret} {
		if code, out := ps.run(t, old, "steward", "secrets", "set", "--config", ps.cfg, "laptop", "API_KEY"); code != 0 || !strings.Contains(out, "not rotated") {
			t.Fatalf("set an old value: exit %d:\n%s", code, out)
		}
	}
	status := []string{"steward", "machine", "retire", "--config", ps.cfg, "--status", "workstation"}
	if code, out := ps.run(t, "", status...); code != 0 || !strings.Contains(out, ": open, 2 of 2 rotations waiting") {
		t.Fatalf("status before the rotation: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, rotatedSecret, "steward", "secrets", "set", "--config", ps.cfg, "laptop", "API_KEY"); code != 0 || !strings.Contains(out, "rotated API_KEY on laptop for the retirement of workstation") {
		t.Fatalf("set the rotated value: exit %d:\n%s", code, out)
	}
	// The name only the retired machine held keeps the retirement open until the Guildmaster marks it rotated.
	if code, out := ps.run(t, "", status...); code != 0 || !strings.Contains(out, ": open, 1 of 2 rotations waiting") || !strings.Contains(out, "laptop (secrets set 20") || !strings.Contains(out, "--rotated DB_PASS workstation (waiting)") {
		t.Fatalf("status after the set: exit %d:\n%s", code, out)
	}
	mark := []string{"steward", "machine", "retire", "--config", ps.cfg, "--rotated", "DB_PASS", "workstation"}
	if code, out := ps.runIn(t, true, "", mark...); code != 1 || !strings.Contains(out, "refused inside a Guildhall session") {
		t.Fatalf("mark in a session: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--rotated", "API_KEY", "workstation"); code != 1 || !strings.Contains(out, "waits for no rotation of API_KEY") {
		t.Fatalf("mark a name set anew: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, "", status...); code != 0 || !strings.Contains(out, ": open, 1 of 2 rotations waiting") {
		t.Fatalf("status after the refused marks: exit %d:\n%s", code, out)
	}
	if code, out := ps.run(t, "", mark...); code != 0 || !strings.Contains(out, "marked DB_PASS rotated for the retirement of workstation") || !strings.Contains(out, ": closed\n") || !strings.Contains(out, "(marked rotated 20") {
		t.Fatalf("mark: exit %d:\n%s", code, out)
	}
	r, err := steward.ReadRetireReceipt([]byte(collectGit(t, ps.remote, "show", "main:retired/workstation.json")))
	if err != nil {
		t.Fatal(err)
	}
	if set, marked := r.Rotations[0], r.Rotations[1]; r.State != steward.RetireClosed || r.ClosedAt == nil || set.ClosedAt == nil || set.ClosedBy != steward.ClosedBySet ||
		marked.Name != "DB_PASS" || marked.Machine != "" || marked.ClosedAt == nil || marked.ClosedBy != steward.ClosedByMark || strings.Join(r.Names, ",") != "API_KEY,DB_PASS" {
		t.Fatalf("record %+v", r)
	}
	if code, out := ps.run(t, "", "steward", "machine", "retire", "--config", ps.cfg, "--plan", filepath.Join(ps.steward, "rollouts", "after"), "workstation"); code != 1 || !strings.Contains(out, "already retired") {
		t.Fatalf("a second retirement once closed: exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(planDir, "receipt.json")); err != nil {
		t.Fatal(err)
	}
	noValueIn(t, ps.private)
	noValueIn(t, ps.remote)
	noValueIn(t, planDir)
}
