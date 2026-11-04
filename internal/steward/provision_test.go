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

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/setup"
	"github.com/ndrwl/guildhall/internal/steward"
	"github.com/ndrwl/guildhall/internal/steward/stewardtest"
)

// The copy the provisioning tests make: Director "ops" on the roster's workstation, cloning one repository.
const (
	newCopy   = "/home/guild/directors/ops"
	newSetup  = "/home/guild/.guildhall/setup"
	infraPath = "/home/guild/src/infra"
	// customSteps is a Steward-written step file with one fixable step.
	customSteps = `schema = "guildhall.steps/1"
name = "custom"
title = "A tool no built-in installs"

[[step]]
id = "custom-tool"
title = "Install the custom tool"
tag = "fixable"
os = ["linux"]
check = ["custom-tool", "--version"]
action = ["sh", "-c", "install-custom-tool"]
undo = "Remove ~/.local/bin/custom-tool."
`
)

// provisionMachine is a roster machine that already has Guildhall, as the provisioning's remote commands see it: the
// steps whose checks fail, the new copy's store, enrolment, authorized managed digest and session, and its files on
// the Fleet. Its doctor answers from the real step files, built-in or copied to it.
type provisionMachine struct {
	mu    sync.Mutex
	f     *stewardtest.Fleet
	alias string
	// open are the step ids whose checks fail.
	open       map[string]bool
	steward    string
	authorized string
	state      string
	tab        string
	// acted counts each command that changes the machine, by its words or the fixed step id; fail makes the next
	// such command fail once before acting, and failAfter once after acting.
	acted     map[string]int
	fail      map[string]bool
	failAfter map[string]bool
	// startsLater makes director start --tab record the session only on the second status read after it.
	startsLater bool
	reads       int
}

func newProvisionMachine(f *stewardtest.Fleet, alias string, open ...string) *provisionMachine {
	m := &provisionMachine{f: f, alias: alias, open: map[string]bool{"copy-dir": true}, state: "no session",
		acted: map[string]int{}, fail: map[string]bool{}, failAfter: map[string]bool{}}
	for _, id := range open {
		m.open[id] = true
	}
	return m
}

func (m *provisionMachine) has(path string) bool {
	_, ok := m.f.Read(steward.Remote{Alias: m.alias, Path: path})
	return ok
}

func (m *provisionMachine) managed() (string, bool) {
	data, ok := m.f.Read(steward.Remote{Alias: m.alias, Path: newCopy + "/managed.toml"})
	if !ok {
		return "", false
	}
	return string(digest.Of(data)), true
}

// steps loads the step file a setup doctor command names, refusing another digest as the engine does.
func (m *provisionMachine) steps(ref, want string) (setup.Steps, error) {
	if !strings.HasPrefix(ref, "/") {
		return setup.LoadBuiltin(ref, want)
	}
	data, ok := m.f.Read(steward.Remote{Alias: m.alias, Path: ref})
	if !ok {
		return setup.Steps{}, fmt.Errorf("%s: no such file", ref)
	}
	return setup.Parse(data, want)
}

// report is the doctor's report of s given its --set values: the new-copy steps' checks read the Fleet's files,
// taking a path with a .git/HEAD as not empty, and every other check fails while its id is open.
func (m *provisionMachine) report(s setup.Steps, sets map[string]string) []byte {
	r := setup.Report{Schema: setup.ReportSchema, Steps: s.Name, Ref: s.Ref, Digest: s.Digest, OS: "linux", Checks: []setup.Check{}, Gaps: []setup.Gap{}, Skipped: []string{}}
	for _, st := range s.Steps {
		if !st.ForOS("linux") {
			r.Skipped = append(r.Skipped, st.ID)
			continue
		}
		detail := ""
		if st.ID == "herdr" {
			detail = "herdr 0.5.0"
		}
		ok := !m.open[st.ID]
		if !ok {
			detail = "exit 1"
		}
		switch st.ID {
		case "copy-free":
			for _, f := range []string{"config.toml", "director.store"} {
				if dir := sets["copy_dir"]; dir != "" && m.has(dir+"/"+f) {
					ok, detail = false, "exit 1: "+dir+"/"+f+" exists"
				}
			}
		case "clone-paths-free":
			for p := range strings.SplitSeq(sets["clone_paths"], ":") {
				if p != "" && m.has(p+"/.git/HEAD") {
					ok, detail = false, "exit 1: "+p+" is not empty"
				}
			}
		}
		r.Checks = append(r.Checks, setup.Check{ID: st.ID, Title: st.Title, Tag: st.Tag, OK: ok, Detail: detail})
		if !ok {
			r.Gaps = append(r.Gaps, setup.Gap{ID: st.ID, Title: st.Title, Tag: st.Tag, Action: "guildhall setup ... " + st.ID, Detail: detail})
		}
	}
	out, _ := json.Marshal(r)
	return out
}

func (m *provisionMachine) handle(argv []string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if argv[0] == "git" {
		return m.act("git clone", argv, func() ([]byte, error) {
			path := argv[4]
			if m.has(path + "/.git/HEAD") {
				return nil, fmt.Errorf("fatal: destination path '%s' already exists and is not an empty directory", path)
			}
			m.f.Write(steward.Remote{Alias: m.alias, Path: path + "/.git/HEAD"}, []byte("ref: refs/heads/main\n"))
			return []byte("Cloning into '" + path + "'...\n"), nil
		})
	}
	words := strings.Join(argv[1:3], " ")
	switch words {
	case "setup doctor":
		at := slices.Index(argv, "--steps")
		s, err := m.steps(argv[at+1], argv[at+3])
		if err != nil {
			return nil, true, err
		}
		sets := map[string]string{}
		for i, a := range argv {
			if a == "--set" {
				k, v, _ := strings.Cut(argv[i+1], "=")
				sets[k] = v
			}
		}
		if argv[4] != "--fix" {
			return m.report(s, sets), true, nil
		}
		for _, id := range strings.Split(argv[6], ",") {
			st, ok := s.Step(id)
			if !ok || st.Tag != setup.Fixable {
				return nil, true, fmt.Errorf("setup doctor --fix: %s is not a fixable step", id)
			}
			if !m.open[id] {
				continue
			}
			if _, _, err := m.act(id, argv, func() ([]byte, error) { delete(m.open, id); return nil, nil }); err != nil {
				return nil, true, err
			}
		}
		return m.report(s, sets), true, nil
	case "director mode":
		mode := map[string]any{"mode": "standalone", "authorized": false}
		if d, ok := m.managed(); ok {
			mode["managed_digest"], mode["mode"] = d, "refused"
			if m.steward != "" && m.authorized == d {
				mode["mode"], mode["authorized"] = "managed", true
			}
		}
		if m.steward != "" {
			mode["steward"] = m.steward
		}
		out, _ := json.Marshal(mode)
		return out, true, nil
	case "director status":
		m.reads++
		st := map[string]any{"state": m.state}
		if m.tab != "" && (!m.startsLater || m.reads > 2) {
			st["pane"] = map[string]string{"session": "guild", "workspace": "w-1", "tab": m.tab, "pane": "p-1"}
		} else if m.tab != "" {
			st["state"] = "no session"
		}
		out, _ := json.Marshal(map[string]any{"session": st, "running": []any{}, "health": map[string]string{"pin": "v0.9.0"}})
		return out, true, nil
	}
	return m.act(words, argv, func() ([]byte, error) {
		switch words {
		case "store init":
			if m.has(newCopy + "/director.store") {
				return nil, errors.New("store init: " + newCopy + "/director.store already exists, and store init never replaces or repairs it")
			}
			m.f.Write(steward.Remote{Alias: m.alias, Path: newCopy + "/director.store"}, []byte("store"))
		case "release adopt":
		case "director enrol":
			if _, ok := m.managed(); !ok {
				return nil, errors.New("enrolment needs a valid managed.toml")
			}
			m.steward = argv[4]
		case "config reload":
			d, ok := m.managed()
			if !ok || m.steward == "" {
				return nil, errors.New("this copy is standalone")
			}
			m.authorized = d
		case "director start":
			if d, _ := m.managed(); d == "" || m.authorized != d || !m.has(newCopy+"/director.store") {
				return nil, errors.New("director start: preflight failed")
			}
			m.state, m.tab, m.reads = "running", "t-1", 0
		default:
			return nil, fmt.Errorf("unexpected command %q", argv)
		}
		return []byte("ok\n"), nil
	})
}

// act runs one changing command, failing before or after it as fail and failAfter ask.
func (m *provisionMachine) act(words string, argv []string, do func() ([]byte, error)) ([]byte, bool, error) {
	if m.fail[words] {
		delete(m.fail, words)
		return nil, true, fmt.Errorf("%s: injected failure", words)
	}
	out, err := do()
	if err != nil {
		return nil, true, err
	}
	m.acted[words]++
	if m.failAfter[words] {
		delete(m.failAfter, words)
		return nil, true, fmt.Errorf("%s: injected failure after acting", words)
	}
	return out, true, nil
}

type provisionFixture struct {
	*collectFixture
	f       *stewardtest.Fleet
	m       *provisionMachine
	request string
	custom  string
}

func newProvisionFixture(t *testing.T, open ...string) *provisionFixture {
	t.Helper()
	fx := &provisionFixture{collectFixture: newCollectFixture(t), f: stewardtest.NewFleet()}
	fx.m = newProvisionMachine(fx.f, "guild-ws", open...)
	fx.f.Handle("guild-ws", fx.m.handle)
	dir := t.TempDir()
	fx.custom = filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(fx.custom, []byte(customSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.request = fmt.Sprintf(`schema_version = 1
machine = "workstation"
director = "ops"
copy_dir = %q
setup_dir = %q
herdr_session = "guild"
step_files = ["herdr", "sign-in", "custom.toml"]

[pins]
herdr_version = "0.5.0"
herdr_url = "https://example.com/herdr-0.5.0-linux-amd64"
herdr_sha256 = "%s"
guildhall = "v0.9.0"

[[repository]]
id = "infra"
remote = "git@example.com:guild/infra.git"
path = %q
`, newCopy, newSetup, strings.Repeat("ab", 32), infraPath)
	return fx
}

func (fx *provisionFixture) setup() *steward.Setup {
	return &steward.Setup{Private: git.WorkTree{Dir: fx.checkout}, Transport: fx.f, Runner: fx.f, Steward: "steward",
		Now: func() time.Time { return time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) }, PollEvery: time.Millisecond}
}

func (fx *provisionFixture) req(t *testing.T) *steward.ProvisionRequest {
	t.Helper()
	r, err := steward.ParseProvisionRequest([]byte(fx.request), filepath.Dir(fx.custom))
	if err != nil {
		t.Fatalf("ParseProvisionRequest: %v", err)
	}
	return r
}

func (fx *provisionFixture) dryRun(t *testing.T, planDir string) *steward.SetupPlan {
	t.Helper()
	p, err := fx.setup().PlanProvision(context.Background(), fx.req(t), planDir)
	if err != nil {
		t.Fatalf("PlanProvision: %v", err)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatalf("WriteSetupPlan: %v", err)
	}
	return p
}

// provisionMutating returns the Fleet's calls that change a machine: puts and every remote command but the reads.
func provisionMutating(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "fetch ") || strings.Contains(c, " director mode ") || strings.Contains(c, " director status ") ||
			(strings.Contains(c, " setup doctor --json --steps ")) {
			continue
		}
		out = append(out, c)
	}
	return out
}

func stepIDs(p *steward.SetupPlan) []string {
	var ids []string
	for _, st := range p.Steps {
		ids = append(ids, st.ID)
	}
	return ids
}

var provisionSteps = []string{"copy-dir", "claude-sign-in", "steps-custom", "custom-tool", "config", "managed", "store", "clone-infra", "adopt", "enrol", "reload", "start", "roster", "summary"}

// TestProvisionDryRun: the dry run runs the doctor on the machine and changes nothing there, turns each built-in's
// gap into a step with the gap's id (a fix for a fixable one, a wizard for a human one), lists the Steward-written
// step file with its digest and every step in it, renders a config.toml that loads, and gives the same digest twice.
func TestProvisionDryRun(t *testing.T) {
	fx := newProvisionFixture(t, "claude-sign-in", "custom-tool")
	planDir := filepath.Join(t.TempDir(), "provision-ops")
	p := fx.dryRun(t, planDir)
	if calls := provisionMutating(fx.f.Calls()); len(calls) != 0 {
		t.Errorf("dry run changed the machine: %q", calls)
	}
	if p.Kind != steward.SetupKindProvision || p.SetupDir != newSetup || p.Roster.NewMachine || !slices.Equal(p.Roster.Repositories, []string{"infra"}) {
		t.Errorf("plan = %+v", p)
	}
	if got := stepIDs(p); !slices.Equal(got, provisionSteps) {
		t.Errorf("steps %q, want %q", got, provisionSteps)
	}
	var names []string
	for _, sf := range p.StepFiles {
		names = append(names, sf.Name)
	}
	if !slices.Equal(names, []string{"director", "herdr", "sign-in", "custom"}) {
		t.Errorf("step files %q", names)
	}
	custom := p.StepFiles[3]
	if custom.Builtin || custom.SHA256 != setup.Digest([]byte(customSteps)) || custom.Ref != newCopy+"/state/steward/setup/custom-"+custom.SHA256[:12]+".toml" {
		t.Errorf("Steward-written step file %+v", custom)
	}
	if data, err := os.ReadFile(filepath.Join(planDir, filepath.FromSlash(custom.File))); err != nil || string(data) != customSteps {
		t.Errorf("plan copy of the step file = %q, %v", data, err)
	}
	if len(p.Doctor) != 4 || p.Doctor[0].Steps != "new-copy" || len(p.Doctor[0].Gaps) != 0 || p.Doctor[1].Gaps[0].ID != "copy-dir" || p.Doctor[3].Gaps[0].ID != "claude-sign-in" {
		t.Errorf("doctor reports %+v", p.Doctor)
	}
	if pre := p.Preflight; pre == nil || pre.Name != "new-copy" || !slices.Equal(pre.Sets, []string{"copy_dir=" + newCopy, "clone_paths=" + infraPath}) {
		t.Errorf("preflight step file %+v", pre)
	}
	dirFile := p.StepFiles[0]
	if want := steward.FixCommand("guildhall", "copy-dir", "director", dirFile.SHA256, newSetup, []string{"copy_dir=" + newCopy, "guildhall=guildhall", "guildhall_pin=v0.9.0"}); !slices.Equal(p.Steps[0].Argv, want) || p.Steps[0].Kind != steward.StepKindFix {
		t.Errorf("copy-dir step %+v, want argv %q", p.Steps[0], want)
	}
	if sets := p.StepFiles[1].Sets; !slices.Contains(sets, "herdr_session=guild") {
		t.Errorf("herdr step file sets %q, want herdr_session=guild", sets)
	}
	wizard := p.Steps[1]
	if wizard.Kind != steward.StepKindWizard || !strings.Contains(strings.Join(wizard.Argv, " "), "setup wizard --steps sign-in --digest "+p.StepFiles[2].SHA256+" --only claude-sign-in --setup-dir "+newSetup) {
		t.Errorf("wizard step %+v", wizard)
	}
	for _, st := range p.Steps {
		if st.Kind == steward.StepKindRemote || st.Kind == steward.StepKindFix {
			if err := steward.CheckCommand(st.Argv); err != nil {
				t.Errorf("step %s argv %q: %v", st.ID, st.Argv, err)
			}
		}
	}
	cfg, err := os.ReadFile(filepath.Join(planDir, "files", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse(cfg)
	if err != nil || parsed.Name != "ops" || len(parsed.Repositories) != 1 || parsed.Repositories[0].OwnerMachine != "workstation" {
		t.Errorf("config.toml %q: %+v, %v", cfg, parsed, err)
	}
	if session, _ := parsed.HerdrSession(); session != "guild" {
		t.Errorf("herdr_session %q", session)
	}
	saved, err := steward.ReadSetupPlan(planDir)
	if err != nil || saved.Digest != p.Digest || saved.Request == nil {
		t.Fatalf("ReadSetupPlan = %+v, %v", saved, err)
	}
	if again := fx.dryRun(t, planDir); again.Digest != p.Digest {
		t.Errorf("second dry run digest %s, want %s", again.Digest, p.Digest)
	}
}

// TestProvisionDryRunRefuses: an existing config, store or clone is ErrCopyExists, and a Director, repository or
// machine the roster cannot take is ErrRosterConflict; a missing Herdr with no pins is refused.
func TestProvisionDryRunRefuses(t *testing.T) {
	plan := func(fx *provisionFixture, edit func(r *steward.ProvisionRequest)) error {
		r := fx.req(t)
		if edit != nil {
			edit(r)
		}
		_, err := fx.setup().PlanProvision(context.Background(), r, filepath.Join(t.TempDir(), "plan"))
		return err
	}
	for _, path := range []string{newCopy + "/config.toml", newCopy + "/director.store", infraPath + "/.git/HEAD"} {
		fx := newProvisionFixture(t)
		fx.f.Write(steward.Remote{Alias: "guild-ws", Path: path}, []byte("x"))
		if err := plan(fx, nil); !errors.Is(err, steward.ErrCopyExists) {
			t.Errorf("with %s: %v, want ErrCopyExists", path, err)
		}
	}
	fx := newProvisionFixture(t)
	for name, edit := range map[string]func(r *steward.ProvisionRequest){
		"a Director the roster lists":   func(r *steward.ProvisionRequest) { r.Director = "tools" },
		"a repository another Director": func(r *steward.ProvisionRequest) { r.Repositories[0].ID = "cli" },
		"a machine not in the roster":   func(r *steward.ProvisionRequest) { r.Machine = "spare" },
		"a copy directory it lists":     func(r *steward.ProvisionRequest) { r.CopyDir = toolsDir },
	} {
		if err := plan(fx, edit); !errors.Is(err, steward.ErrRosterConflict) {
			t.Errorf("%s: %v, want ErrRosterConflict", name, err)
		}
	}
	if err := plan(fx, func(r *steward.ProvisionRequest) { r.Machine = "laptop" }); err == nil {
		t.Error("a Windows machine was planned")
	}
	fx = newProvisionFixture(t, "herdr")
	if err := plan(fx, func(r *steward.ProvisionRequest) { r.Pins = steward.Pins{} }); err == nil || !strings.Contains(err.Error(), "herdr_url") {
		t.Errorf("Herdr missing with no pins: %v", err)
	}
	if calls := provisionMutating(fx.f.Calls()); len(calls) != 0 {
		t.Errorf("refused dry runs changed the machine: %q", calls)
	}
}

// TestParseProvisionRequest: a request is strict TOML whose values are checked, and a step file path is relative to
// the request.
func TestParseProvisionRequest(t *testing.T) {
	fx := newProvisionFixture(t)
	r := fx.req(t)
	if !slices.Equal(r.StepFiles, []string{"herdr", "sign-in", fx.custom}) || r.Pins.Guildhall != "v0.9.0" {
		t.Errorf("request %+v", r)
	}
	for name, src := range map[string]string{
		"an unknown key":       fx.request + "extra = 1\n",
		"a remote with ;":      strings.Replace(fx.request, "git@example.com:guild/infra.git", "git@example.com:x;id", 1),
		"a relative copy dir":  strings.Replace(fx.request, `copy_dir = "/home`, `copy_dir = "home`, 1),
		"an unknown built-in":  strings.Replace(fx.request, `"herdr", `, `"nope", `, 1),
		"no repository":        fx.request[:strings.Index(fx.request, "[[repository]]")],
		"a bad herdr session":  strings.Replace(fx.request, `herdr_session = "guild"`, `herdr_session = "default"`, 1),
		"herdr pins in part":   strings.Replace(fx.request, `herdr_version = "0.5.0"`, ``, 1),
		"a ../ remote segment": strings.Replace(fx.request, "git@example.com:guild/infra.git", "https://example.com/../infra", 1),
	} {
		r, err := steward.ParseProvisionRequest([]byte(src), t.TempDir())
		if err == nil {
			_, err = fx.setup().PlanProvision(context.Background(), r, filepath.Join(t.TempDir(), "plan"))
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestProvisionApply covers provisioning from an empty copy directory: the first apply fixes the copy directory and
// waits at the human sign-in, whose wizard the Steward never runs; the wait holds while the record is missing, of
// another step file, or present while the doctor still reports the gap; once the gap is closed the apply copies the
// Steward-written step file, fixes its step, writes config.toml and managed.toml, makes the store, clones, adopts,
// enrols, reloads, starts the tab, commits the roster entry and writes the verified summary. An apply of the
// finished plan changes nothing.
func TestProvisionApply(t *testing.T) {
	fx := newProvisionFixture(t, "claude-sign-in", "custom-tool")
	planDir := filepath.Join(t.TempDir(), "provision-ops")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	ctx := context.Background()
	if _, err := s.ApplySetup(ctx, planDir, "sha256:0"); !errors.Is(err, steward.ErrNotApproved) {
		t.Fatalf("unapproved apply: %v", err)
	}
	out, err := s.ApplySetup(ctx, planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeWaiting || out.Step != "claude-sign-in" ||
		!strings.Contains(out.Diagnosis, steward.ErrWizardRecord.Error()) || !strings.Contains(out.Diagnosis, "setup wizard --steps sign-in") {
		t.Fatalf("first apply = %v, %v; want waiting claude-sign-in for its record", out, err)
	}
	if fx.m.open["copy-dir"] || fx.m.acted["copy-dir"] != 1 {
		t.Errorf("copy-dir open %v, acted %d", fx.m.open["copy-dir"], fx.m.acted["copy-dir"])
	}
	signIn := p.StepFiles[2]
	record := setup.RecordPath(newSetup, "sign-in", signIn.SHA256)
	writeRecord := func(dig string, status string) {
		rec := setup.Record{Schema: setup.RecordSchema, Steps: "sign-in", Ref: "sign-in", Digest: dig, StepIDs: []string{"claude-sign-in"}, Status: status,
			Results: []setup.StepResult{{ID: "claude-sign-in", Status: status}}}
		data, _ := json.Marshal(rec)
		fx.f.Write(steward.Remote{Alias: "guild-ws", Path: filepath.ToSlash(record)}, data)
	}
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: filepath.ToSlash(record)}, []byte("not json\n"))
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting || !strings.Contains(out.Diagnosis, "is not a "+setup.RecordSchema+" record") {
		t.Fatalf("apply with a malformed record = %v, %v", out, err)
	}
	writeRecord(strings.Repeat("0", 64), "done")
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting || !strings.Contains(out.Diagnosis, "not the plan's") {
		t.Fatalf("apply with another step file's record = %v, %v", out, err)
	}
	writeRecord(signIn.SHA256, "done")
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting || !strings.Contains(out.Diagnosis, "doctor still reports") {
		t.Fatalf("apply with a record while the gap is open = %v, %v", out, err)
	}
	delete(fx.m.open, "claude-sign-in")
	fx.m.startsLater = true
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply after the wizard = %v, %v", out, err)
	}
	for _, argv := range fx.f.Ran("guild-ws") {
		if slices.Contains(argv, "wizard") || slices.Contains(argv, "sudo") {
			t.Errorf("the Steward ran %q", argv)
		}
	}
	for _, words := range []string{"copy-dir", "custom-tool", "store init", "git clone", "release adopt", "director enrol", "config reload", "director start"} {
		if fx.m.acted[words] != 1 {
			t.Errorf("%s acted %d times, want once", words, fx.m.acted[words])
		}
	}
	cfg, _ := os.ReadFile(filepath.Join(planDir, "files", "config.toml"))
	if got, ok := fx.f.Read(steward.Remote{Alias: "guild-ws", Path: newCopy + "/config.toml"}); !ok || string(got) != string(cfg) {
		t.Errorf("config.toml on the machine %q", got)
	}
	if _, ok := fx.f.Read(steward.Remote{Alias: "guild-ws", Path: p.StepFiles[3].Ref}); !ok {
		t.Error("the Steward-written step file was not copied")
	}
	if fx.m.steward != "steward" || fx.m.tab != "t-1" {
		t.Errorf("machine after provisioning: %+v", fx.m)
	}
	if fx.head() != fx.git(fx.remote, "rev-parse", "main") || !strings.Contains(fx.git(fx.checkout, "log", "-1", "--format=%s"), "Provision Director ops on workstation") {
		t.Error("the roster commit was not made and pushed")
	}
	sum, err := steward.ReadSetupSummary(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Mode != "managed" || sum.Pane.Tab != "t-1" || !sum.Ready || len(sum.Outstanding) != 0 || len(sum.Versions) != 2 ||
		!sum.Versions[0].Match || sum.Versions[1].Found != "herdr 0.5.0" || !sum.Versions[1].Match {
		t.Errorf("summary %+v", sum)
	}
	if note := out.Log.Record(steward.StepSummary).Note; !strings.Contains(note, "tab t-1") || !strings.Contains(note, "no outstanding manual item") {
		t.Errorf("summary note %q", note)
	}

	fx.f.Calls()
	head := fx.head()
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply of the finished plan = %v, %v", out, err)
	}
	if calls := provisionMutating(fx.f.Calls()); len(calls) != 0 || fx.head() != head {
		t.Errorf("apply of the finished plan changed %q", calls)
	}
}

// TestProvisionResume: a failure at each acting step stops the apply there with its diagnosis, and the next apply
// finishes the plan without acting again for any step done; a store made or a clone finished just before the
// failure is taken as done.
func TestProvisionResume(t *testing.T) {
	for _, c := range []struct {
		words, step string
		after       bool
	}{
		{"copy-dir", "copy-dir", false},
		{"store init", "store", false},
		{"store init", "store", true},
		{"git clone", "clone-infra", false},
		{"git clone", "clone-infra", true},
		{"release adopt", "adopt", false},
		{"director enrol", "enrol", false},
		{"config reload", "reload", false},
		{"director start", "start", false},
	} {
		t.Run(fmt.Sprintf("%s after=%v", c.step, c.after), func(t *testing.T) {
			fx := newProvisionFixture(t)
			fx.request = strings.Replace(fx.request, `, "custom.toml"`, "", 1)
			planDir := filepath.Join(t.TempDir(), "provision-ops")
			p := fx.dryRun(t, planDir)
			if c.after {
				fx.m.failAfter[c.words] = true
			} else {
				fx.m.fail[c.words] = true
			}
			s := fx.setup()
			out, err := s.ApplySetup(context.Background(), planDir, p.Digest)
			var stepErr *steward.ErrStepFailed
			if !errors.As(err, &stepErr) || out.State != steward.OutcomeFailed || out.Step != c.step || !strings.Contains(out.Diagnosis, "injected failure") {
				t.Fatalf("failing apply = %v, %v; want failed %s", out, err, c.step)
			}
			if out, err = s.ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
				t.Fatalf("resumed apply = %v, %v", out, err)
			}
			for _, words := range []string{"copy-dir", "store init", "git clone", "release adopt", "director enrol", "config reload", "director start"} {
				if fx.m.acted[words] != 1 {
					t.Errorf("%s acted %d times, want once", words, fx.m.acted[words])
				}
			}
		})
	}
}

// TestProvisionApplyRefuses: a Steward-written step file changed after approval, in the plan or at its source, and
// a machine whose doctor report changed, are ErrPlanChanged with no step run; a clone path filled after the dry run
// is ErrCopyExists at its step, and git clone leaves it as it is.
func TestProvisionApplyRefuses(t *testing.T) {
	ctx := context.Background()
	fx := newProvisionFixture(t)
	planDir := filepath.Join(t.TempDir(), "provision-ops")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	if err := os.WriteFile(fx.custom, []byte(customSteps+"# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySetup(ctx, planDir, p.Digest); !errors.Is(err, steward.ErrPlanChanged) {
		t.Errorf("source step file changed: %v, want ErrPlanChanged", err)
	}
	if err := os.WriteFile(fx.custom, []byte(customSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	inPlan := filepath.Join(planDir, filepath.FromSlash(p.StepFiles[3].File))
	if err := os.WriteFile(inPlan, []byte(customSteps+"# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplySetup(ctx, planDir, p.Digest); !errors.Is(err, steward.ErrPlanChanged) {
		t.Errorf("plan step file changed: %v, want ErrPlanChanged", err)
	}
	if err := os.WriteFile(inPlan, []byte(customSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.m.open["gh-sign-in"] = true
	if _, err := s.ApplySetup(ctx, planDir, p.Digest); !errors.Is(err, steward.ErrPlanChanged) {
		t.Errorf("doctor report changed: %v, want ErrPlanChanged", err)
	}
	if calls := provisionMutating(fx.f.Calls()); len(calls) != 0 {
		t.Errorf("refused applies changed the machine: %q", calls)
	}
	if log, _ := steward.ReadStepLog(planDir); len(log.Steps) != 0 {
		t.Errorf("refused applies ran steps: %+v", log.Steps)
	}
	delete(fx.m.open, "gh-sign-in")
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: infraPath + "/.git/HEAD"}, []byte("someone else's\n"))
	if _, err := s.ApplySetup(ctx, planDir, p.Digest); !errors.Is(err, steward.ErrPlanChanged) || !strings.Contains(err.Error(), steward.ErrCopyExists.Error()) {
		t.Errorf("clone path filled before the first apply: %v, want ErrPlanChanged for ErrCopyExists", err)
	}

	// A clone path filled once the apply has started is refused by git clone at its own step.
	fx = newProvisionFixture(t, "claude-sign-in")
	fx.request = strings.Replace(fx.request, `, "custom.toml"`, "", 1)
	p = fx.dryRun(t, planDir+"-2")
	s = fx.setup()
	if out, err := s.ApplySetup(ctx, planDir+"-2", p.Digest); err != nil || out.State != steward.OutcomeWaiting {
		t.Fatalf("first apply = %v, %v", out, err)
	}
	delete(fx.m.open, "claude-sign-in")
	fx.f.Write(steward.Remote{Alias: "guild-ws", Path: infraPath + "/.git/HEAD"}, []byte("someone else's\n"))
	out, err := s.ApplySetup(ctx, planDir+"-2", p.Digest)
	if err == nil || out.Step != "clone-infra" || !strings.Contains(out.Diagnosis, steward.ErrCopyExists.Error()) {
		t.Errorf("apply into a filled clone path = %v, %v; want failed clone-infra with ErrCopyExists", out, err)
	}
	if got, _ := fx.f.Read(steward.Remote{Alias: "guild-ws", Path: infraPath + "/.git/HEAD"}); string(got) != "someone else's\n" || fx.m.acted["git clone"] != 0 {
		t.Errorf("the clone path was changed: %q", got)
	}
}

// TestProvisionGuildhallFacts: the doctor's Guildhall facts, a Guildhall release other than the pin, Tailscale SSH
// in check mode and no running server for the Herdr session, are gaps in the dry run, each a wizard step given the
// request's values, and the apply waits at the first before it writes any file of the copy.
func TestProvisionGuildhallFacts(t *testing.T) {
	fx := newProvisionFixture(t, "guildhall-pin", "tailscale-ssh", "herdr-session")
	fx.request = strings.Replace(fx.request, `, "custom.toml"`, "", 1)
	planDir := filepath.Join(t.TempDir(), "provision-ops")
	p := fx.dryRun(t, planDir)
	want := []string{"copy-dir", "guildhall-pin", "tailscale-ssh", "herdr-session", "config", "managed", "store", "clone-infra", "adopt", "enrol", "reload", "start", "roster", "summary"}
	if got := stepIDs(p); !slices.Equal(got, want) {
		t.Fatalf("steps %q, want %q", got, want)
	}
	for _, c := range []struct{ step, set string }{{"guildhall-pin", "guildhall_pin=v0.9.0"}, {"tailscale-ssh", "copy_dir=" + newCopy}, {"herdr-session", "herdr_session=guild"}} {
		st := p.Steps[slices.Index(want, c.step)]
		if st.Kind != steward.StepKindWizard || !slices.Contains(st.Argv, c.set) {
			t.Errorf("%s step %+v, want a wizard given %s", c.step, st, c.set)
		}
	}
	out, err := fx.setup().ApplySetup(context.Background(), planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeWaiting || out.Step != "guildhall-pin" {
		t.Fatalf("apply = %v, %v; want waiting guildhall-pin", out, err)
	}
	if fx.m.has(newCopy + "/config.toml") {
		t.Error("config.toml was written while a Guildhall fact was still a gap")
	}
}

// TestProvisionNeverCopiesOverUnreadable: a copy step whose target the machine cannot read back, for a reason other
// than its being absent, fails and leaves the target as it is, rather than taking it as absent and copying over it.
func TestProvisionNeverCopiesOverUnreadable(t *testing.T) {
	for _, target := range []string{newCopy + "/config.toml", newCopy + "/managed.toml"} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			fx := newProvisionFixture(t, "claude-sign-in")
			fx.request = strings.Replace(fx.request, `, "custom.toml"`, "", 1)
			planDir := filepath.Join(t.TempDir(), "provision-ops")
			p := fx.dryRun(t, planDir)
			s := fx.setup()
			ctx := context.Background()
			if out, err := s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting {
				t.Fatalf("first apply = %v, %v", out, err)
			}
			delete(fx.m.open, "claude-sign-in")
			file := steward.Remote{Alias: "guild-ws", Path: target}
			fx.f.Write(file, []byte("someone else's\n"))
			fx.f.FailFetch(file, "Permission denied")
			fx.f.Calls()
			out, err := s.ApplySetup(ctx, planDir, p.Digest)
			if err == nil || out.State != steward.OutcomeFailed || !strings.Contains(out.Diagnosis, "Permission denied") || !strings.Contains(out.Diagnosis, "nothing was copied") {
				t.Fatalf("apply = %v, %v; want the copy step failed on the unreadable target", out, err)
			}
			if got, _ := fx.f.Read(file); string(got) != "someone else's\n" {
				t.Errorf("%s was overwritten: %q", target, got)
			}
			if slices.Contains(fx.f.Calls(), "put guild-ws:"+target) {
				t.Errorf("the apply copied over %s", target)
			}
		})
	}
}
