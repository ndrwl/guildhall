package steward_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/steward"
)

const (
	wsPub       = "age1workstation00000000000000000000000000000000000000000000"
	laptopPub   = "age1laptop000000000000000000000000000000000000000000000000000"
	recoveryPub = "age1recovery000000000000000000000000000000000000000000000000"
)

func secretsRoster(recovery, ws, laptop string) string {
	return `schema_version = 1
recovery_recipient = "` + recovery + `"

[[machine]]
label = "workstation"
ssh_alias = "guild-ws"
os = "linux"
roles = ["director"]
age_recipient = "` + ws + `"

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/directors/tools"
repositories = ["cli"]

[[machine]]
label = "laptop"
ssh_alias = "guild-laptop"
os = "linux"
roles = ["director"]
age_recipient = "` + laptop + `"
`
}

// TestRosterAgeRecipients: a machine's age_recipient and the roster's recovery_recipient must be age public keys, and
// no two of them the same.
func TestRosterAgeRecipients(t *testing.T) {
	if _, err := steward.ParseRoster([]byte(secretsRoster(recoveryPub, wsPub, laptopPub))); err != nil {
		t.Fatal(err)
	}
	for name, roster := range map[string]string{
		"bad recovery":        secretsRoster("AGE-SECRET-KEY-1X", wsPub, laptopPub),
		"bad machine":         secretsRoster(recoveryPub, "ssh-ed25519 AAAA", laptopPub),
		"shared machine key":  secretsRoster(recoveryPub, wsPub, wsPub),
		"recovery as machine": secretsRoster(recoveryPub, recoveryPub, laptopPub),
	} {
		if _, err := steward.ParseRoster([]byte(roster)); err == nil {
			t.Errorf("%s: roster accepted", name)
		}
	}
	// Both keys are optional: a roster without them is valid, as before.
	if _, err := steward.ParseRoster([]byte(strings.ReplaceAll(secretsRoster("", "", ""), "age_recipient = \"\"\n", ""))); err != nil {
		t.Fatalf("a roster with no keys: %v", err)
	}
}

// TestSopsConfig: the generated .sops.yaml has one rule per machine with a key, to its key and the recovery key, and
// needs a recovery key.
func TestSopsConfig(t *testing.T) {
	r, err := steward.ParseRoster([]byte(secretsRoster(recoveryPub, wsPub, laptopPub)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := steward.SopsConfig(r)
	if err != nil {
		t.Fatal(err)
	}
	want := "creation_rules:\n  - path_regex: ^secrets/workstation\\.sops\\.yaml$\n    age: " + wsPub + "," + recoveryPub + "\n  - path_regex: ^secrets/laptop\\.sops\\.yaml$\n    age: " + laptopPub + "," + recoveryPub + "\n"
	if !strings.HasSuffix(string(data), want) {
		t.Fatalf(".sops.yaml =\n%s\nwant it to end\n%s", data, want)
	}
	r.RecoveryRecipient = ""
	if _, err := steward.SopsConfig(r); err == nil {
		t.Fatal("SopsConfig without a recovery key succeeded")
	}
	if got := steward.SecretsFile("laptop"); got != "secrets/laptop.sops.yaml" {
		t.Fatalf("SecretsFile = %q", got)
	}
}

// TestSecretsCheckAllowed: secrets check --json is one of the fixed remote commands; other secrets verbs, and check
// without --json or a copy, are still refused.
func TestSecretsCheckAllowed(t *testing.T) {
	if err := steward.CheckCommand(steward.SecretsCheckCommand(steward.DefaultGuildhall, "/home/guild/directors/tools")); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"guildhall", "secrets", "check", "--copy", "/home/guild/directors/tools"},
		{"guildhall", "secrets", "init", "--copy", "/home/guild/directors/tools"},
		{"guildhall", "secrets", "set", "--json", "--copy", "/home/guild/directors/tools"},
		{"guildhall", "secrets", "check", "--json"},
	} {
		if err := steward.CheckCommand(argv); !errors.Is(err, steward.ErrNotAllowed) {
			t.Errorf("CheckCommand(%q) = %v, want ErrNotAllowed", argv, err)
		}
	}
}
