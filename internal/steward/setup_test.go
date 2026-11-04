package steward_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
)

// The copy the takeover tests adopt: Director "ops" on the roster's workstation, beside its two enrolled copies.
const (
	opsDir    = "/home/guild/directors/ops"
	opsConfig = `schema_version = 1
mode = "director"
name = "ops"

[[repository]]
id = "infra"
path = "/home/guild/src/infra"
remote = "https://example.com/infra.git"
owner_machine = "workstation"
`
)

// takeoverCopy is a standalone Director copy as the takeover's remote commands see it: its session, running attempts,
// enrolment and authorized managed digest, with its files on the Fleet.
type takeoverCopy struct {
	mu         sync.Mutex
	f          *stewardtest.Fleet
	alias, dir string
	state      string
	running    []string
	steward    string
	authorized string
	backups    int
	exports    []string
	// acted counts each command that changes the copy, by its words; fail makes the next such command fail once.
	acted map[string]int
	fail  map[string]bool
}

func newTakeoverCopy(f *stewardtest.Fleet, alias, dir, config, state string) *takeoverCopy {
	c := &takeoverCopy{f: f, alias: alias, dir: dir, state: state, acted: map[string]int{}, fail: map[string]bool{}}
	f.Write(steward.Remote{Alias: alias, Path: dir + "/config.toml"}, []byte(config))
	return c
}

func (c *takeoverCopy) managed() (string, bool) {
	data, ok := c.f.Read(steward.Remote{Alias: c.alias, Path: c.dir + "/managed.toml"})
	if !ok {
		return "", false
	}
	return string(digest.Of(data)), true
}

func (c *takeoverCopy) handle(argv []string) ([]byte, bool, error) {
	if argv[len(argv)-1] != c.dir {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	words := strings.Join(argv[1:3], " ")
	switch words {
	case "director mode":
		m := map[string]any{"mode": "standalone", "authorized": false}
		if d, ok := c.managed(); ok {
			m["managed_digest"], m["mode"] = d, "refused"
			if c.steward != "" && c.authorized == d {
				m["mode"], m["authorized"] = "managed", true
			}
		}
		if c.steward != "" {
			m["steward"] = c.steward
		}
		out, _ := json.Marshal(m)
		return out, true, nil
	case "director status":
		running := []map[string]any{}
		for _, r := range c.running {
			running = append(running, map[string]any{"repository": "infra", "task_id": r, "step": "build", "attempt": 1})
		}
		out, _ := json.Marshal(map[string]any{"session": map[string]any{"state": c.state}, "running": running})
		return out, true, nil
	}
	if c.fail[words] {
		delete(c.fail, words)
		return nil, true, fmt.Errorf("%s: injected failure", words)
	}
	c.acted[words]++
	switch words {
	case "director pause":
		if c.state != "running" {
			return nil, true, fmt.Errorf("session is %s", c.state)
		}
		c.state = "paused"
	case "director resume":
		if c.state != "paused" {
			return nil, true, fmt.Errorf("session is %s", c.state)
		}
		c.state = "running"
	case "backup now":
		c.backups++
	case "store export":
		if slices.Contains(c.exports, argv[4]) {
			return nil, true, fmt.Errorf("store: export: %s is not empty", argv[4])
		}
		c.exports = append(c.exports, argv[4])
	case "director enrol":
		if _, ok := c.managed(); !ok {
			return nil, true, errors.New("enrolment needs a valid managed.toml")
		}
		c.steward = argv[4]
	case "config reload":
		d, ok := c.managed()
		if !ok || c.steward == "" {
			return nil, true, errors.New("this copy is standalone")
		}
		c.authorized = d
	default:
		return nil, true, fmt.Errorf("unexpected command %q", argv)
	}
	return []byte("ok\n"), true, nil
}

// takeoverFixture is the collection fixture's private config with the ops copy on the workstation.
type takeoverFixture struct {
	*collectFixture
	f   *stewardtest.Fleet
	ops *takeoverCopy
}

func newTakeoverFixture(t *testing.T, state string) *takeoverFixture {
	t.Helper()
	fx := &takeoverFixture{collectFixture: newCollectFixture(t), f: stewardtest.NewFleet()}
	fx.ops = newTakeoverCopy(fx.f, "guild-ws", opsDir, opsConfig, state)
	fx.f.Handle("guild-ws", fx.ops.handle)
	return fx
}

func (fx *takeoverFixture) setup() *steward.Setup {
	return &steward.Setup{Private: git.WorkTree{Dir: fx.checkout}, Transport: fx.f, Runner: fx.f, Steward: "steward",
		Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }}
}

func (fx *takeoverFixture) dryRun(t *testing.T, planDir string) *steward.SetupPlan {
	t.Helper()
	p, err := fx.setup().PlanTakeover(context.Background(), steward.TakeoverInput{Machine: "workstation", Director: "ops", CopyDir: opsDir, PlanDir: planDir})
	if err != nil {
		t.Fatalf("PlanTakeover: %v", err)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatalf("WriteSetupPlan: %v", err)
	}
	return p
}

// mutating returns the Fleet's calls that change a machine: puts and every remote command but the reads.
func mutating(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "fetch ") || strings.Contains(c, " director mode ") || strings.Contains(c, " director status ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

// TestTakeoverDryRun: the dry run writes a setup plan with the first managed.toml, which has no managed key, and the
// roster entry under the copy's machine, changes nothing on the machine, and gives the same digest when run again.
func TestTakeoverDryRun(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	planDir := filepath.Join(t.TempDir(), "takeover-ops")
	p := fx.dryRun(t, planDir)
	if calls := mutating(fx.f.Calls()); len(calls) != 0 {
		t.Errorf("dry run changed the machine: %q", calls)
	}
	if p.Schema != steward.SetupPlanSchema || p.Kind != steward.SetupKindTakeover || p.Session() != "running" || p.Probe.Mode != "standalone" || p.Probe.Backup {
		t.Errorf("plan = %+v", p)
	}
	var ids []string
	for _, st := range p.Steps {
		ids = append(ids, st.ID)
	}
	if want := []string{"pause", "drain", "backup", "managed", "enrol", "reload", "resume", "roster"}; !slices.Equal(ids, want) {
		t.Errorf("steps %q, want %q", ids, want)
	}
	if want := steward.ExportCommand("guildhall", opsDir+"/state/steward/takeover/takeover-ops", opsDir); !slices.Equal(p.Steps[2].Argv, want) {
		t.Errorf("backup step %q, want %q", p.Steps[2].Argv, want)
	}
	managed, err := os.ReadFile(filepath.Join(planDir, "files", "managed.toml"))
	if err != nil || !strings.Contains(string(managed), "\nschema_version = 1\n") || strings.Contains(string(managed), "[managed.") {
		t.Errorf("planned managed.toml = %q, %v; want one with no managed key", managed, err)
	}
	if !strings.Contains(p.RosterDiff, `+name = "ops"`) || p.Roster.NewMachine || !slices.Equal(p.Roster.Repositories, []string{"infra"}) {
		t.Errorf("roster entry %+v, diff:\n%s", p.Roster, p.RosterDiff)
	}
	saved, err := steward.ReadSetupPlan(planDir)
	if err != nil || saved.Digest != p.Digest {
		t.Fatalf("ReadSetupPlan = %+v, %v", saved, err)
	}
	if !steward.IsSetupPlan(planDir) {
		t.Error("IsSetupPlan = false")
	}
	if again := fx.dryRun(t, planDir); again.Digest != p.Digest {
		t.Errorf("second dry run digest %s, want %s", again.Digest, p.Digest)
	}
	if fx.head() != fx.git(fx.remote, "rev-parse", "main") {
		t.Error("the dry run committed to the private config")
	}
}

// TestTakeoverDryRunRefuses: a Director or copy the roster holds, a copy whose repositories another machine owns,
// a copy already managed and a new machine with no SSH alias are refused.
func TestTakeoverDryRunRefuses(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	plan := func(in steward.TakeoverInput) error {
		in.PlanDir = filepath.Join(t.TempDir(), "plan")
		_, err := fx.setup().PlanTakeover(context.Background(), in)
		return err
	}
	for name, c := range map[string]struct {
		in   steward.TakeoverInput
		want error
	}{
		"a Director the roster lists":  {steward.TakeoverInput{Machine: "workstation", Director: "tools", CopyDir: opsDir}, steward.ErrRosterConflict},
		"a copy directory it lists":    {steward.TakeoverInput{Machine: "workstation", Director: "ops", CopyDir: toolsDir}, steward.ErrRosterConflict},
		"another Director name":        {steward.TakeoverInput{Machine: "workstation", Director: "infra", CopyDir: opsDir}, steward.ErrCopyMismatch},
		"an alias another machine has": {steward.TakeoverInput{Machine: "spare", Director: "ops", CopyDir: opsDir, SSHAlias: "guild-laptop"}, steward.ErrRosterConflict},
	} {
		if err := plan(c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if err := plan(steward.TakeoverInput{Machine: "spare", Director: "ops", CopyDir: opsDir}); err == nil || !strings.Contains(err.Error(), "--ssh-alias") {
		t.Errorf("new machine without an alias: %v", err)
	}
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/config.toml"}, []byte(strings.Replace(opsConfig, `"workstation"`, `"laptop"`, 1)))
	if err := plan(steward.TakeoverInput{Machine: "workstation", Director: "ops", CopyDir: opsDir}); !errors.Is(err, steward.ErrCopyMismatch) || !strings.Contains(err.Error(), "owner machine") {
		t.Errorf("another owner machine: %v, want ErrCopyMismatch", err)
	}
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/config.toml"}, []byte(opsConfig))
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/managed.toml"}, []byte("# managed\n"))
	if err := plan(steward.TakeoverInput{Machine: "workstation", Director: "ops", CopyDir: opsDir}); !errors.Is(err, steward.ErrCopyMismatch) {
		t.Errorf("a copy with managed.toml: %v, want ErrCopyMismatch", err)
	}
}

// TestTakeoverApply covers a takeover with an attempt in flight: the first apply pauses the session and waits for
// the attempt, the next backs up, publishes, enrols, reloads, resumes and commits the roster entry alone, and an apply
// of the finished plan changes nothing.
func TestTakeoverApply(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	fx.ops.running = []string{"t-1"}
	if err := os.WriteFile(filepath.Join(fx.checkout, "managed.toml"), []byte("schema_version = 1\n# local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planDir := filepath.Join(t.TempDir(), "takeover-ops")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	out, err := s.ApplySetup(context.Background(), planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeWaiting || out.Step != steward.StepDrain || !strings.Contains(out.Diagnosis, "infra/t-1") {
		t.Fatalf("first apply = %v, %v; want waiting drain", out, err)
	}
	if fx.ops.state != "paused" || fx.ops.acted["director pause"] != 1 {
		t.Errorf("state %s, acted %v; want paused once", fx.ops.state, fx.ops.acted)
	}
	fx.ops.running = nil
	out, err = s.ApplySetup(context.Background(), planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("second apply = %v, %v", out, err)
	}
	if fx.ops.state != "running" || fx.ops.steward != "steward" || fx.ops.authorized == "" || len(fx.ops.exports) != 1 {
		t.Errorf("copy after takeover: %+v", fx.ops)
	}
	for _, words := range []string{"director pause", "store export", "director enrol", "config reload", "director resume"} {
		if fx.ops.acted[words] != 1 {
			t.Errorf("%s ran %d times, want once", words, fx.ops.acted[words])
		}
	}
	if fx.head() != fx.git(fx.remote, "rev-parse", "main") {
		t.Error("the roster commit was not pushed")
	}
	if files := fx.git(fx.checkout, "show", "--name-only", "--format=", "HEAD"); files != "roster.toml" {
		t.Errorf("roster commit holds %q, want roster.toml alone", files)
	}
	if status := fx.git(fx.checkout, "status", "--porcelain"); status != "M managed.toml" {
		t.Errorf("the Guildmaster's edit was not kept: status %q", status)
	}
	roster, err := steward.ParseRoster([]byte(fx.git(fx.checkout, "show", "HEAD:roster.toml") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := roster.Machine("workstation")
	if len(ws.Directors) != 3 || ws.Directors[2].Name != "ops" || ws.Directors[2].CopyDir != opsDir {
		t.Errorf("workstation in the committed roster: %+v", ws)
	}
	if laptop, _ := roster.Machine("laptop"); len(laptop.Directors) != 1 {
		t.Errorf("laptop in the committed roster: %+v", laptop)
	}

	fx.f.Calls()
	head := fx.head()
	out, err = s.ApplySetup(context.Background(), planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply of the finished plan = %v, %v", out, err)
	}
	if calls := mutating(fx.f.Calls()); len(calls) != 0 || fx.head() != head {
		t.Errorf("apply of the finished plan changed %q", calls)
	}
}

// TestTakeoverNoSession: a copy with no live session is taken over with no pause or resume, and backed up with
// backup now when it has a [backup] table.
func TestTakeoverNoSession(t *testing.T) {
	fx := newTakeoverFixture(t, "no session")
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/config.toml"},
		[]byte(opsConfig+"\n[backup]\nrepo = \"git@example.com:guild/backups.git\"\nbranch = \"workstation/ops\"\n"))
	planDir := filepath.Join(t.TempDir(), "plan")
	p := fx.dryRun(t, planDir)
	if !p.Probe.Backup || !slices.Equal(p.Steps[2].Argv, steward.BackupCommand("guildhall", opsDir)) {
		t.Fatalf("backup step %q with probe %+v", p.Steps[2].Argv, p.Probe)
	}
	out, err := fx.setup().ApplySetup(context.Background(), planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	if fx.ops.acted["director pause"] != 0 || fx.ops.acted["director resume"] != 0 || fx.ops.backups != 1 || fx.ops.authorized == "" {
		t.Errorf("acted %v, backups %d", fx.ops.acted, fx.ops.backups)
	}
}

// TestTakeoverRefusesUnapprovedOrChanged: an apply without the plan's digest, or after the copy's config, the
// copy's mode, the roster or a planned file changed, refuses and runs no step.
func TestTakeoverRefusesUnapprovedOrChanged(t *testing.T) {
	for name, c := range map[string]struct {
		change  func(fx *takeoverFixture, planDir string)
		approve func(p *steward.SetupPlan) string
		want    error
	}{
		"no approval":    {nil, func(*steward.SetupPlan) string { return "" }, steward.ErrNotApproved},
		"another digest": {nil, func(*steward.SetupPlan) string { return "sha256:" + strings.Repeat("0", 64) }, steward.ErrNotApproved},
		"config.toml edit": {func(fx *takeoverFixture, _ string) {
			fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/config.toml"}, []byte(opsConfig+"# edit\n"))
		}, nil, steward.ErrPlanChanged},
		"roster commit": {func(fx *takeoverFixture, _ string) {
			fx.git(fx.checkout, "commit", "-q", "--allow-empty", "-m", "x")
			if err := os.WriteFile(filepath.Join(fx.checkout, "roster.toml"), []byte(collectSrc+"# edit\n"), 0o644); err != nil {
				fx.t.Fatal(err)
			}
			fx.git(fx.checkout, "commit", "-q", "-am", "roster edit")
		}, nil, steward.ErrPlanChanged},
		"planned file edit": {func(_ *takeoverFixture, planDir string) {
			if err := os.WriteFile(filepath.Join(planDir, "files", "managed.toml"), []byte("schema_version = 1\n"), 0o600); err != nil {
				panic(err)
			}
		}, nil, steward.ErrPlanChanged},
		"managed.toml appeared": {func(fx *takeoverFixture, _ string) {
			fx.f.Write(steward.Remote{Alias: "guild-ws", Path: opsDir + "/managed.toml"}, []byte("# x\n"))
		}, nil, steward.ErrPlanChanged},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newTakeoverFixture(t, "running")
			planDir := filepath.Join(t.TempDir(), "plan")
			p := fx.dryRun(t, planDir)
			if c.change != nil {
				c.change(fx, planDir)
			}
			approve := p.Digest
			if c.approve != nil {
				approve = c.approve(p)
			}
			fx.f.Calls()
			if _, err := fx.setup().ApplySetup(context.Background(), planDir, approve); !errors.Is(err, c.want) {
				t.Errorf("apply: %v, want %v", err, c.want)
			}
			if calls := mutating(fx.f.Calls()); len(calls) != 0 || len(fx.ops.acted) != 0 {
				t.Errorf("a refused apply ran %q", calls)
			}
			if _, err := os.Stat(filepath.Join(planDir, "steps.json")); err == nil {
				t.Error("a refused apply wrote the step log")
			}
		})
	}
}

// TestTakeoverResumesAfterFailure: a failure at each step's act stops the apply there with a diagnosis; the next
// apply finishes the plan, and no step done acts again.
func TestTakeoverResumesAfterFailure(t *testing.T) {
	for _, words := range []string{"director pause", "store export", "put", "director enrol", "config reload", "director resume", "push"} {
		t.Run(words, func(t *testing.T) {
			fx := newTakeoverFixture(t, "running")
			planDir := filepath.Join(t.TempDir(), "plan")
			p := fx.dryRun(t, planDir)
			s := fx.setup()
			restore := func() {}
			switch words {
			case "put":
				// Only the first scp to the machine fails; the remote commands still run.
				s.Transport = &failPut{Copier: fx.f}
			case "push":
				fx.git(fx.checkout, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing"))
				restore = func() { fx.git(fx.checkout, "remote", "set-url", "origin", fx.remote) }
			default:
				fx.ops.fail[words] = true
			}
			out, err := s.ApplySetup(context.Background(), planDir, p.Digest)
			var stepErr *steward.ErrStepFailed
			if !errors.As(err, &stepErr) || out.State != steward.OutcomeFailed || out.Diagnosis == "" {
				t.Fatalf("apply with %s failing = %v, %v; want a failed step with a diagnosis", words, out, err)
			}
			restore()
			out, err = s.ApplySetup(context.Background(), planDir, p.Digest)
			if err != nil || out.State != steward.OutcomeDone {
				t.Fatalf("apply after the failure = %v, %v", out, err)
			}
			for _, w := range []string{"director pause", "store export", "director enrol", "config reload", "director resume"} {
				if fx.ops.acted[w] != 1 {
					t.Errorf("%s acted %d times, want once", w, fx.ops.acted[w])
				}
			}
			if fx.ops.state != "running" || fx.ops.authorized == "" || fx.head() != fx.git(fx.remote, "rev-parse", "main") {
				t.Errorf("copy %+v after the resumed apply, or the roster was not pushed", fx.ops)
			}
		})
	}
}

// failPut is a Copier whose first put fails, as a refused scp does.
type failPut struct {
	steward.Copier
	failed bool
}

func (c *failPut) Put(ctx context.Context, src string, dst steward.Remote) error {
	if !c.failed {
		c.failed = true
		return errors.New("scp: connection refused")
	}
	return c.Copier.Put(ctx, src, dst)
}

// TestTakeoverAbandon: abandoning a plan waiting on drain resumes the session it paused and the plan never runs
// again; once the managed step ran, abandon refuses.
func TestTakeoverAbandon(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	fx.ops.running = []string{"t-1"}
	planDir := filepath.Join(t.TempDir(), "plan")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	if out, err := s.ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting {
		t.Fatalf("apply = %v, %v", out, err)
	}
	out, err := s.Abandon(context.Background(), planDir)
	if err != nil || out.State != steward.OutcomeDone || fx.ops.state != "running" || fx.ops.acted["director resume"] != 1 {
		t.Fatalf("abandon = %v, %v; state %s", out, err, fx.ops.state)
	}
	if _, err := s.ApplySetup(context.Background(), planDir, p.Digest); !errors.Is(err, steward.ErrAbandoned) {
		t.Errorf("apply of an abandoned plan: %v, want ErrAbandoned", err)
	}

	fx = newTakeoverFixture(t, "running")
	planDir = filepath.Join(t.TempDir(), "plan")
	p = fx.dryRun(t, planDir)
	s = fx.setup()
	fx.ops.fail["director enrol"] = true
	if _, err := s.ApplySetup(context.Background(), planDir, p.Digest); err == nil {
		t.Fatal("apply with enrol failing succeeded")
	}
	if _, err := s.Abandon(context.Background(), planDir); err == nil || !strings.Contains(err.Error(), "--apply") {
		t.Errorf("abandon after the managed step: %v, want a refusal naming --apply", err)
	}
}

// TestTakeoverBusy: an apply refuses while .rollout.lock is held, running nothing.
func TestTakeoverBusy(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	planDir := filepath.Join(t.TempDir(), "plan")
	p := fx.dryRun(t, planDir)
	if err := os.WriteFile(filepath.Join(fx.checkout, ".rollout.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.setup().ApplySetup(context.Background(), planDir, p.Digest); !errors.Is(err, steward.ErrBusy) {
		t.Errorf("apply under a held lock: %v, want ErrBusy", err)
	}
	if len(fx.ops.acted) != 0 {
		t.Errorf("acted %v", fx.ops.acted)
	}
}

// TestTakeoverNewMachine: a machine not in the roster is added at its end with the Director, from its SSH alias.
func TestTakeoverNewMachine(t *testing.T) {
	fx := newTakeoverFixture(t, "stopped")
	fx.ops = newTakeoverCopy(fx.f, "guild-vds", opsDir, strings.Replace(opsConfig, `"workstation"`, `"vds"`, 1), "stopped")
	fx.f.Handle("guild-vds", fx.ops.handle)
	planDir := filepath.Join(t.TempDir(), "plan")
	p, err := fx.setup().PlanTakeover(context.Background(), steward.TakeoverInput{Machine: "vds", Director: "ops", CopyDir: opsDir, SSHAlias: "guild-vds", PlanDir: planDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatal(err)
	}
	if !p.Roster.NewMachine || !strings.Contains(p.RosterDiff, `+ssh_alias = "guild-vds"`) {
		t.Errorf("roster entry %+v, diff:\n%s", p.Roster, p.RosterDiff)
	}
	if out, err := fx.setup().ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	roster, err := steward.ParseRoster([]byte(fx.git(fx.checkout, "show", "HEAD:roster.toml") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if vds, ok := roster.Machine("vds"); !ok || vds.SSHAlias != "guild-vds" || len(vds.Directors) != 1 {
		t.Errorf("vds in the committed roster: %+v", vds)
	}
}

// TestTakeoverCommandsAllowList: the takeover's commands pass CheckCommand, and their values are held to the
// restricted character set.
func TestTakeoverCommandsAllowList(t *testing.T) {
	for _, argv := range [][]string{
		steward.StatusCommand(steward.DefaultGuildhall, toolsDir),
		steward.PauseCommand(steward.DefaultGuildhall, toolsDir),
		steward.ResumeCommand(steward.DefaultGuildhall, toolsDir),
		steward.BackupCommand(steward.DefaultGuildhall, toolsDir),
		steward.ExportCommand(steward.DefaultGuildhall, toolsDir+"/state/steward/takeover/plan-1", toolsDir),
		steward.EnrolCommand(steward.DefaultGuildhall, "steward", toolsDir),
		steward.EnrolCommand(gamesBin, "steward", gamesDir),
		steward.ExportCommand(gamesBin, gamesDir+"/state/x", gamesDir),
	} {
		if err := steward.CheckCommand(argv); err != nil {
			t.Errorf("CheckCommand(%q) = %v, want allowed", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"guildhall", "director", "status", "--copy", toolsDir},
		{"guildhall", "director", "stop", "--copy", toolsDir},
		{"guildhall", "store", "export", "--out", "state/x", "--copy", toolsDir},
		{"guildhall", "store", "export", "--out", toolsDir + "/x;id", "--copy", toolsDir},
		{"guildhall", "store", "export", "--out", toolsDir + "/../x", "--copy", toolsDir},
		{"guildhall", "store", "export", "--copy", toolsDir},
		{"guildhall", "director", "enrol", "--steward", "Steward", "--copy", toolsDir},
		{"guildhall", "director", "enrol", "--steward", "s;id", "--copy", toolsDir},
		{"guildhall", "director", "enrol", "--steward", "--copy", toolsDir},
		{"guildhall", "director", "enrol", "--copy", toolsDir},
		{"guildhall", "backup", "now", "--copy", toolsDir, "--extra"},
		{"guildhall", "backup", "restore", "--copy", toolsDir},
	} {
		if err := steward.CheckCommand(argv); !errors.Is(err, steward.ErrNotAllowed) {
			t.Errorf("CheckCommand(%q) = %v, want ErrNotAllowed", argv, err)
		}
	}
}
