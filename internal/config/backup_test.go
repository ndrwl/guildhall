package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBackupTableParses: [backup] parses its repo, branch and every, and is never encoded, so adding it leaves the
// config digest of the same config without it unchanged.
func TestBackupTableParses(t *testing.T) {
	base := director(t)
	plain, err := Parse([]byte(base))
	if err != nil || plain.Backup != nil {
		t.Fatalf("no table: Backup = %+v, %v; want nil", plain, err)
	}
	cfg, err := Parse([]byte(base + "\n[backup]\nrepo = \"git@guildhall-backup-alpha:ndrwl/guildhall-backups.git\"\nbranch = \"andrew-lap-wsl/alpha\"\nevery = \"30m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := Backup{Repo: "git@guildhall-backup-alpha:ndrwl/guildhall-backups.git", Branch: "andrew-lap-wsl/alpha", Every: "30m"}
	if cfg.Backup == nil || *cfg.Backup != want {
		t.Fatalf("Backup = %+v, want %+v", cfg.Backup, want)
	}
	if d, err := cfg.Backup.EveryDuration(); err != nil || d != 30*time.Minute {
		t.Fatalf("every = %v, %v", d, err)
	}
	if d, err := (Backup{}).EveryDuration(); err != nil || d != DefaultBackupEvery {
		t.Fatalf("default every = %v, %v", d, err)
	}
	a, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("adding [backup] changed the config's JSON, and so its digest:\n%s\n%s", a, b)
	}
}

func TestInvalidBackupTableRefused(t *testing.T) {
	const repo = "repo = \"git@host:o/r.git\"\n"
	cases := map[string]string{
		"branch = \"m/alpha\"\n": "backup: repo is required",
		"repo = \"https://u:p@github.com/o/r.git\"\nbranch = \"m/alpha\"\n": "backup: repo must not contain credentials",
		"repo = \"https://u@github.com/o/r.git\"\nbranch = \"m/alpha\"\n":   "backup: repo must not contain credentials",
		repo:                                              "backup: branch is required",
		repo + "branch = \"alpha\"\n":                     `must be "<machine>/<director>"`,
		repo + "branch = \"m/n/alpha\"\n":                 `must be "<machine>/<director>"`,
		repo + "branch = \"-m/alpha\"\n":                  `must be "<machine>/<director>"`,
		repo + "branch = \"m/beta\"\n":                    `must end with this Director's name, as "m/alpha"`,
		repo + "branch = \"m/alpha\"\nevery = \"soon\"\n": `backup: every "soon" must be a duration`,
		repo + "branch = \"m/alpha\"\nevery = \"1s\"\n":   `at least a minute`,
		repo + "branch = \"m/alpha\"\nkeep = 3\n":         "unknown keys: backup.keep",
	}
	for body, want := range cases {
		_, err := Parse([]byte(director(t) + "\n[backup]\n" + body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	steward := "schema_version = 1\nmode = \"steward\"\n\n[private_config]\nlocation = \"git@github.com:example/c.git\"\n\n[backup]\n" + repo + "branch = \"m/s\"\n"
	if _, err := Parse([]byte(steward)); err == nil || !strings.Contains(err.Error(), "the backup table belongs to a director, not the steward") {
		t.Errorf("steward backup table: err = %v", err)
	}
}
