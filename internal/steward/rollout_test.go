package steward

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	toolsPath = "/home/guild/directors/tools/config.toml"
	docsPath  = "/home/guild/directors/docs/config.toml"
	gamesPath = "C:/Users/guild/directors/games/config.toml"
)

// docsCopy already carries managedFixture's settings, so a rollout leaves it unchanged.
const docsCopy = `schema_version = 1
mode = "director"
name = "docs"

[managed.lint_profile]
value = "strict \"quoted\"\nsecond line"

[managed.review_instructions]
value = "Cite the requirement."
overridable = true

[managed.standards_revision]
value = "standards-2"

[[repository]]
id = "handbook"
path = '/srv/src/handbook'
remote = "git@github.com:example/handbook.git"
owner_machine = "workstation"
`

const gamesCopy = "schema_version = 1\r\nmode = \"director\"\r\nname = \"games\"\r\n\r\n[local]\r\nnotes_dir = 'D:/notes'\r\n\r\n[[repository]]\r\nid = \"engine\"\r\npath = 'C:/src/engine'\r\nremote = \"git@github.com:example/engine.git\"\r\nowner_machine = \"laptop\"\r\n"

var stamp = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// fleet is a private config directory and a fake fleet with three enrolled copies.
func fleet(t *testing.T) (*stub, *Rollout) {
	t.Helper()
	s := newStub(t)
	private := t.TempDir()
	for name, content := range map[string]string{"roster.toml": rosterFixture, "managed.toml": managedFixture} {
		if err := os.WriteFile(filepath.Join(private, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s.put("guild-ws", toolsPath, copyFixture)
	s.put("guild-ws", docsPath, docsCopy)
	s.put("guild-laptop", gamesPath, gamesCopy)
	r, err := LoadRollout(private, s.transport())
	if err != nil {
		t.Fatalf("load rollout: %v", err)
	}
	r.Now = func() time.Time { return stamp }
	return s, r
}

func dryRun(t *testing.T, r *Rollout, machines ...string) *Plan {
	t.Helper()
	p, err := r.Plan(context.Background(), machines, filepath.Join(t.TempDir(), "plan"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := WritePlan(p); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return p
}

func statuses[T any](items []T, status func(T) string) []string {
	var out []string
	for _, it := range items {
		out = append(out, status(it))
	}
	return out
}

func copyStatus(c CopyPlan) string       { return c.Machine + "/" + c.Director + "=" + c.Status }
func receiptStatus(c CopyReceipt) string { return c.Machine + "/" + c.Director + "=" + c.Status }

func TestDryRunPreviewsWithoutChangingCopies(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation", "laptop")

	want := []string{"workstation/tools=change", "workstation/docs=unchanged", "laptop/games=change"}
	if got := statuses(p.Copies, copyStatus); !slices.Equal(got, want) {
		t.Fatalf("copies = %v, want %v", got, want)
	}
	if !strings.HasPrefix(p.Digest, "sha256:") || !p.NeedsApproval() {
		t.Errorf("digest %q, needs approval %v", p.Digest, p.NeedsApproval())
	}
	// Planning only downloads: every scp call reads from a remote.
	for _, call := range s.calls() {
		if !strings.Contains(call, "-- guild-") {
			t.Errorf("dry run ran a non-download: %s", call)
		}
	}
	if s.get("guild-ws", toolsPath) != copyFixture || s.get("guild-laptop", gamesPath) != gamesCopy {
		t.Error("dry run changed a copy")
	}

	tools := p.Copies[0]
	staged := filepath.Join(p.StageDir, "files", "workstation", "tools")
	wantCommands := [][]string{
		{"scp", "-q", "-B", "-o", "BatchMode=yes", "--", filepath.Join(staged, "previous.toml"), "guild-ws:" + toolsPath + ".rev-" + tools.BeforeSHA256[:16]},
		{"scp", "-q", "-B", "-o", "BatchMode=yes", "--", filepath.Join(staged, "config.toml"), "guild-ws:" + toolsPath},
		{"scp", "-q", "-B", "-o", "BatchMode=yes", "--", "guild-ws:" + toolsPath, filepath.Join(staged, "verify.toml")},
	}
	if len(tools.Commands) != len(wantCommands) {
		t.Fatalf("commands = %q", tools.Commands)
	}
	for i := range wantCommands {
		if !slices.Equal(tools.Commands[i], wantCommands[i]) {
			t.Errorf("command %d = %q, want %q", i, tools.Commands[i], wantCommands[i])
		}
	}
	if len(p.Copies[1].Commands) != 0 {
		t.Errorf("unchanged copy has commands %q", p.Copies[1].Commands)
	}
	if !strings.Contains(tools.Diff, "-value = \"standards-1\"\n") || !strings.Contains(tools.Diff, "+[managed.standards_revision]\n+value = \"standards-2\"\n") {
		t.Errorf("diff does not show the change:\n%s", tools.Diff)
	}
	previous, _ := os.ReadFile(filepath.Join(staged, "previous.toml"))
	next, _ := os.ReadFile(filepath.Join(staged, "config.toml"))
	if string(previous) != copyFixture || !strings.Contains(string(next), `value = "standards-2"`) {
		t.Errorf("staged previews wrong:\n%s\n---\n%s", previous, next)
	}

	again, err := r.Plan(context.Background(), []string{"workstation", "laptop"}, p.StageDir)
	if err != nil || again.Digest != p.Digest {
		t.Errorf("replanning the same state changed the digest: %v %q != %q", err, again.Digest, p.Digest)
	}
	read, err := ReadPlan(p.StageDir)
	if err != nil || read.Digest != p.Digest {
		t.Errorf("read plan: %v %+v", err, read)
	}
}

func TestPlanRefusesUnknownOrRepeatedMachines(t *testing.T) {
	_, r := fleet(t)
	for name, machines := range map[string][]string{
		"unenrolled": {"stranger"},
		"repeated":   {"laptop", "laptop"},
		"none":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Plan(context.Background(), machines, t.TempDir()); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestApplyRollsOutAndIsIdempotent(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation", "laptop")
	s.calls()

	if _, err := r.Apply(context.Background(), p.StageDir, ""); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("policy change applied without approval: %v", err)
	}
	if _, err := r.Apply(context.Background(), p.StageDir, "sha256:0000"); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("approval of another plan accepted: %v", err)
	}
	for _, call := range s.calls() {
		if !strings.Contains(call, "-- guild-") {
			t.Fatalf("refused apply wrote: %s", call)
		}
	}

	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	want := []string{"workstation/tools=applied", "workstation/docs=unchanged", "laptop/games=applied"}
	if got := statuses(receipt.Copies, receiptStatus); !slices.Equal(got, want) {
		t.Fatalf("receipt = %v, want %v", got, want)
	}
	if !receipt.Approved || receipt.PlanDigest != p.Digest {
		t.Errorf("receipt approval %v digest %q", receipt.Approved, receipt.PlanDigest)
	}
	staged, _ := os.ReadFile(filepath.Join(p.StageDir, "files", "workstation", "tools", "config.toml"))
	if got := s.get("guild-ws", toolsPath); got != string(staged) {
		t.Errorf("remote config is not the staged preview:\n%s", got)
	}
	tools := receipt.Copies[0]
	if tools.VerifiedSHA256 != tools.AfterSHA256 || tools.Revision != toolsPath+".rev-"+tools.BeforeSHA256[:16] {
		t.Errorf("tools receipt %+v", tools)
	}
	if got := s.get("guild-ws", tools.Revision); got != copyFixture {
		t.Errorf("previous revision not preserved: %q", got)
	}
	games := s.get("guild-laptop", gamesPath)
	if !strings.Contains(games, "[local]\r\nnotes_dir = 'D:/notes'\r\n") || !strings.Contains(games, "[managed.standards_revision]\r\nvalue = \"standards-2\"\r\n") {
		t.Errorf("windows copy lost local settings or managed values:\n%q", games)
	}

	data, err := os.ReadFile(filepath.Join(p.StageDir, "receipt-20261020T120000Z.json"))
	if err != nil {
		t.Fatalf("receipt file: %v", err)
	}
	var onDisk Receipt
	if err := json.Unmarshal(data, &onDisk); err != nil || onDisk.Schema != ReceiptSchema || len(onDisk.Copies) != 3 {
		t.Errorf("receipt file %v: %s", err, data)
	}

	// Applying the same plan again is refused: the copies no longer match it.
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest); !errors.Is(err, ErrPlanStale) {
		t.Errorf("stale plan reapplied: %v", err)
	}
	// A fresh plan finds nothing to do, and applying it writes nothing.
	s.calls()
	fresh := dryRun(t, r, "workstation", "laptop")
	if got := statuses(fresh.Copies, copyStatus); !slices.Equal(got, []string{"workstation/tools=unchanged", "workstation/docs=unchanged", "laptop/games=unchanged"}) {
		t.Fatalf("second plan = %v", got)
	}
	again, err := r.Apply(context.Background(), fresh.StageDir, "")
	if err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if got := statuses(again.Copies, receiptStatus); !slices.Equal(got, []string{"workstation/tools=unchanged", "workstation/docs=unchanged", "laptop/games=unchanged"}) {
		t.Errorf("idempotent receipt = %v", got)
	}
	for _, call := range s.calls() {
		if !strings.Contains(call, "-- guild-") {
			t.Errorf("idempotent apply wrote: %s", call)
		}
	}
}

func TestApplyRefusesWhenACopyChangedSinceThePlan(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation")
	edited := strings.Replace(copyFixture, `notes_dir = "notes"`, `notes_dir = "journal"`, 1)
	s.put("guild-ws", toolsPath, edited)
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("stale plan applied: %v", err)
	}
	if s.get("guild-ws", toolsPath) != edited {
		t.Error("the Director's newer edit was overwritten")
	}
}

func TestApplyRefusesTamperedPreview(t *testing.T) {
	_, r := fleet(t)
	p := dryRun(t, r, "workstation")
	preview := filepath.Join(p.StageDir, "files", "workstation", "tools", "config.toml")
	if err := os.WriteFile(preview, []byte("schema_version = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("tampered preview applied: %v", err)
	}
}

func TestApplyRecordsPartialFailure(t *testing.T) {
	s, r := fleet(t)
	p := dryRun(t, r, "workstation", "laptop")
	t.Setenv(stubFailEnv, "config.toml guild-laptop:")
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("partial failure not reported: %v", err)
	}
	want := []string{"workstation/tools=applied", "workstation/docs=unchanged", "laptop/games=failed"}
	if got := statuses(receipt.Copies, receiptStatus); !slices.Equal(got, want) {
		t.Fatalf("receipt = %v, want %v", got, want)
	}
	games := receipt.Copies[2]
	if !strings.Contains(games.Error, "connection closed") || len(games.Steps) != 2 || games.Steps[1].Error == "" {
		t.Errorf("failed copy receipt %+v", games)
	}
	if s.get("guild-laptop", gamesPath) != gamesCopy {
		t.Error("failed copy was changed")
	}
	if _, err := os.Stat(filepath.Join(p.StageDir, "receipt-20261020T120000Z.json")); err != nil {
		t.Errorf("no receipt for a partial rollout: %v", err)
	}
}

func TestApplyDetectsVerificationFailure(t *testing.T) {
	_, r := fleet(t)
	p := dryRun(t, r, "laptop")
	t.Setenv(stubCorruptEnv, "verify.toml")
	receipt, err := r.Apply(context.Background(), p.StageDir, p.Digest)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("verification failure not reported: %v", err)
	}
	games := receipt.Copies[0]
	if games.Status != StatusVerifyFailed || games.VerifiedSHA256 == "" || games.VerifiedSHA256 == games.AfterSHA256 {
		t.Errorf("verify receipt %+v", games)
	}
}

func TestRefusedCopyBlocksApply(t *testing.T) {
	s, r := fleet(t)
	s.put("guild-ws", docsPath, strings.Replace(docsCopy, `owner_machine = "workstation"`, `owner_machine = "laptop"`, 1))
	t.Setenv(stubFailEnv, "guild-laptop:")
	p := dryRun(t, r, "workstation", "laptop")
	want := []string{"workstation/tools=change", "workstation/docs=refused", "laptop/games=refused"}
	if got := statuses(p.Copies, copyStatus); !slices.Equal(got, want) {
		t.Fatalf("copies = %v, want %v", got, want)
	}
	if !strings.Contains(p.Copies[1].Problem, `owner machine "laptop"`) || !strings.Contains(p.Copies[2].Problem, "connection closed") {
		t.Errorf("problems: %q, %q", p.Copies[1].Problem, p.Copies[2].Problem)
	}
	if _, err := r.Apply(context.Background(), p.StageDir, p.Digest); !errors.Is(err, ErrNotApplicable) {
		t.Fatalf("plan with refused copies applied: %v", err)
	}
}
