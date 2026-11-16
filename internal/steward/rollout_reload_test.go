package steward

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
)

// TestRolloutReloadsEachWrittenCopy covers apply running config reload, over ssh, on each copy it wrote and only
// those, with the planned command recorded as the receipt's last step and its output kept.
func TestRolloutReloadsEachWrittenCopy(t *testing.T) {
	s, r := fleet(t)
	const authorized = "managed revision authorized by reload"
	s.answer("guild-ws", authorized+"\n", "")
	p := dryRun(t, r, "workstation", "laptop")
	wantRun := map[string][][]string{
		"guild-ws":     {ReloadCommand(DefaultGuildhall, "/home/guild/directors/tools"), ReloadCommand(DefaultGuildhall, "/home/guild/directors/docs")},
		"guild-laptop": {ReloadCommand(DefaultGuildhall, "C:/Users/guild/directors/games")},
	}
	for _, c := range p.Copies {
		last := c.Commands[len(c.Commands)-1]
		want := sshCommand(c.SSHAlias, ReloadCommand(DefaultGuildhall, strings.TrimSuffix(c.Target, "/managed.toml")))
		if !slices.Equal(last, want) {
			t.Errorf("%s planned last command %q, want %q", c.Director, last, want)
		}
	}
	if got := s.ran("guild-ws"); len(got) != 0 {
		t.Fatalf("dry run ran %q", got)
	}
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range wantRun {
		if got := s.ran(alias); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("%s ran %q, want %q", alias, got, want)
		}
	}
	for i, c := range receipt.Copies {
		if c.Status != StatusApplied || len(c.Steps) != 4 {
			t.Fatalf("receipt copy %+v", c)
		}
		step := c.Steps[3]
		if !slices.Equal(step.Command, p.Copies[i].Commands[3]) || step.Error != "" {
			t.Errorf("%s reload step %+v, want the planned %q", c.Director, step, p.Copies[i].Commands[3])
		}
		want := ""
		if c.Machine == "workstation" {
			want = authorized
		}
		if step.Output != want {
			t.Errorf("%s reload output %q, want %q", c.Director, step.Output, want)
		}
	}
	fresh := dryRun(t, r, p.Machines...)
	again, err := r.Apply(context.Background(), fresh.StageDir, "", fresh.Machines...)
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range wantRun {
		if got := s.ran(alias); !slices.EqualFunc(got, append(slices.Clone(want), want...), slices.Equal) {
			t.Errorf("an unchanged apply on %s ran %q in all, want one new reload per copy", alias, got)
		}
	}
	for i, c := range again.Copies {
		if c.Status != StatusUnchanged || len(c.Steps) != 1 || !slices.Equal(c.Steps[0].Command, fresh.Copies[i].Commands[0]) {
			t.Fatalf("unchanged receipt copy %+v", c)
		}
		if c.Machine == "workstation" && c.Steps[0].Output != authorized {
			t.Errorf("%s unchanged reload output %q, want %q", c.Director, c.Steps[0].Output, authorized)
		}
	}
}

// TestRolloutReRunReloadsAnUnchangedCopy (H41) covers a re-run after reload_failed: the copy's managed file is
// current, so it is unchanged, but its reload runs again; while that reload still fails the copy stays
// reload_failed and the apply is incomplete, and once it succeeds the copy is unchanged and the apply complete.
func TestRolloutReRunReloadsAnUnchangedCopy(t *testing.T) {
	s, r := fleet(t)
	const pin = "guildhall: release v1.4.0 is not installed and the rollout left no copy of it to install"
	s.answer("guild-laptop", "", pin)
	p := dryRun(t, r, "laptop")
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("first Apply: %v, want ErrIncomplete", err)
	}

	again := dryRun(t, r, "laptop")
	c := again.Copies[0]
	want := sshCommand("guild-laptop", ReloadCommand(DefaultGuildhall, "C:/Users/guild/directors/games"))
	if c.Status != StatusUnchanged || len(c.Commands) != 1 || !slices.Equal(c.Commands[0], want) || c.Revision != "" || c.Diff != "" {
		t.Fatalf("re-run plan copy %+v, want unchanged with only the reload %q", c, want)
	}
	if again.NeedsApproval() {
		t.Error("an unchanged re-run needs approval")
	}
	receipt, err := r.Apply(context.Background(), again.StageDir, "", again.Machines...)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("re-run Apply: %v, want ErrIncomplete while the reload still fails", err)
	}
	games := receipt.Copies[0]
	if games.Status != StatusReloadFailed || len(games.Steps) != 1 || !strings.Contains(games.Steps[0].Error, pin) ||
		!strings.Contains(games.Error, "managed.toml is already current, but config reload failed: ") || !strings.Contains(games.Error, pin) {
		t.Fatalf("re-run receipt %+v", games)
	}
	if strings.Contains(games.Error, "not authorized") {
		t.Errorf("re-run error %q claims the copy is not authorized", games.Error)
	}

	const pinned = "copy C:/Users/guild/directors/games pins v1.4.0"
	s.answer("guild-laptop", pinned+"\n", "")
	last := dryRun(t, r, "laptop")
	receipt, err = r.Apply(context.Background(), last.StageDir, "", last.Machines...)
	if err != nil {
		t.Fatalf("Apply once the reload succeeds: %v", err)
	}
	if games := receipt.Copies[0]; games.Status != StatusUnchanged || games.Error != "" || len(games.Steps) != 1 || games.Steps[0].Output != pinned {
		t.Fatalf("receipt once the reload succeeds %+v", games)
	}
	if got := s.ran("guild-laptop"); len(got) != 3 {
		t.Errorf("laptop ran %q, want three reloads", got)
	}
}

// TestPlanDigestCoversAnUnchangedCopysReload pins that an unchanged copy's planned reload is in the plan digest.
func TestPlanDigestCoversAnUnchangedCopysReload(t *testing.T) {
	_, r := fleet(t)
	p := dryRun(t, r, "laptop")
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...); err != nil {
		t.Fatal(err)
	}
	q := dryRun(t, r, "laptop")
	if q.Copies[0].Status != StatusUnchanged || len(q.Copies[0].Commands) != 1 {
		t.Fatalf("re-run plan copy %+v", q.Copies[0])
	}
	tampered := *q
	tampered.Copies = slices.Clone(q.Copies)
	c := &tampered.Copies[0]
	c.Commands = [][]string{sshCommand(c.SSHAlias, ReloadCommand("C:/Users/guild/bin/guildhall.exe", "C:/Users/guild/directors/games"))}
	if tampered.digest() == q.Digest {
		t.Fatal("the plan digest does not cover an unchanged copy's reload command")
	}
}

// TestRolloutReportsAFailedReloadPerCopy covers an old guildhall that does not know config reload: that copy is
// reload_failed with the remote diagnostic in its receipt, its published file stays, and the other copies apply.
func TestRolloutReportsAFailedReloadPerCopy(t *testing.T) {
	s, r := fleet(t)
	const old = `guildhall: unknown command "config"`
	s.answer("guild-laptop", "", old)
	p := dryRun(t, r, "workstation", "laptop")
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest, p.Machines...)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Apply: %v, want ErrIncomplete", err)
	}
	for _, c := range receipt.Copies[:2] {
		if c.Status != StatusApplied {
			t.Errorf("%s: %+v", c.Director, c)
		}
	}
	games := receipt.Copies[2]
	if games.Status != StatusReloadFailed || !strings.Contains(games.Error, old) || games.VerifiedSHA256 != games.AfterSHA256 {
		t.Fatalf("games receipt %+v", games)
	}
	if last := games.Steps[len(games.Steps)-1]; !strings.Contains(last.Error, old) || last.Command[0] != "ssh" {
		t.Errorf("games reload step %+v", last)
	}
	data, err := os.ReadFile(receipt.File)
	if err != nil || !strings.Contains(string(data), `"status": "reload_failed"`) || !strings.Contains(string(data), `unknown command \"config\"`) {
		t.Errorf("receipt file does not read as reload_failed with the diagnostic: %v\n%s", err, data)
	}
	if got := digest.Of([]byte(s.get("guild-laptop", managedPath(gamesPath)))).Hex(); got != games.AfterSHA256 {
		t.Error("a failed reload undid the published managed file")
	}
}

// TestPlanDigestCoversTheReloadCommand pins that the planned reload, guildhall_path included, is part of what an
// approval of the plan's digest approves, and that apply runs the roster's guildhall_path.
func TestPlanDigestCoversTheReloadCommand(t *testing.T) {
	s, r := fleet(t)
	const program = "C:/Users/guild/bin/guildhall.exe"
	p := dryRun(t, r, "laptop")
	tampered := *p
	tampered.Copies = slices.Clone(p.Copies)
	c := &tampered.Copies[0]
	c.Commands = slices.Clone(c.Commands)
	c.Commands[len(c.Commands)-1] = sshCommand(c.SSHAlias, ReloadCommand(program, "C:/Users/guild/directors/games"))
	if tampered.digest() == p.Digest {
		t.Fatal("the plan digest does not cover the reload command")
	}

	roster := strings.Replace(rosterFixture, `os = "windows"`, "os = \"windows\"\nguildhall_path = \""+program+"\"", 1)
	if err := os.WriteFile(filepath.Join(r.privateDir, "roster.toml"), []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	withPath, err := LoadRollout(r.privateDir, s.transport(), s.runner())
	if err != nil {
		t.Fatal(err)
	}
	q := dryRun(t, withPath, "laptop")
	argv := ReloadCommand(program, "C:/Users/guild/directors/games")
	if got := q.Copies[0].Commands[len(q.Copies[0].Commands)-1]; !slices.Equal(got, sshCommand("guild-laptop", argv)) {
		t.Errorf("planned reload %q, want %q", got, sshCommand("guild-laptop", argv))
	}
	if _, err := withPath.Apply(context.Background(), q.StageDir, q.Digest, q.Machines...); err != nil {
		t.Fatal(err)
	}
	if got := s.ran("guild-laptop"); len(got) != 1 || !slices.Equal(got[0], argv) {
		t.Errorf("laptop ran %q, want %q", got, argv)
	}
}
