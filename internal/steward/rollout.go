package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// Plan and receipt schemas; PlanSchema is planSchemaPrefix followed by planSchemaVersion.
const (
	PlanSchema    = planSchemaPrefix + "1"
	ReceiptSchema = "guildhall.rollout-receipt/1"

	planSchemaPrefix  = "guildhall.rollout-plan/"
	planSchemaVersion = 1
)

// Copy plan statuses.
const (
	StatusChange    = "change"
	StatusUnchanged = "unchanged"
	StatusRefused   = "refused"
)

// Copy receipt statuses; StatusUnchanged is shared with plans.
const (
	StatusApplied      = "applied"
	StatusFailed       = "failed"
	StatusVerifyFailed = "verify_failed"
)

var (
	// ErrPlanStale reports that the copies, the private config or the staged files no longer match the dry-run plan.
	ErrPlanStale = errors.New("the plan no longer matches the copies or the private config; run the dry run again")
	// ErrNotApplicable reports a plan that refused at least one copy.
	ErrNotApplicable = errors.New("the plan refused at least one copy and cannot be applied")
	// ErrApprovalRequired reports a plan with changes outside routine authority applied without approval of its exact digest.
	ErrApprovalRequired = errors.New("the plan has changes that need Guildmaster approval of its exact digest")
	// ErrIncomplete reports a rollout in which at least one copy failed to apply or verify.
	ErrIncomplete = errors.New("the rollout did not complete on every copy")
)

// Remote names a file on an enrolled machine: its roster SSH alias and its path there.
type Remote struct {
	Alias string
	Path  string
}

// String returns the file as an scp operand, alias:path.
func (r Remote) String() string { return r.Alias + ":" + r.Path }

// Copier copies whole files between this Steward and enrolled machines; it is how a Rollout reaches them.
// Transport is the scp implementation and stewardtest.Fleet an in-memory one; both pass stewardtest.RunCopierContract.
//
// A copy replaces the destination file with exactly the source's bytes, creating it if absent.
// It fails with an error carrying the implementation's diagnostic when the source is missing,
// or the machine refuses or cannot be reached; it stops when ctx ends, with an error that wraps ctx.Err().
// A copy that fails in any of these ways before transferring data leaves the destination unchanged.
// A copy interrupted mid-transfer may leave the destination partial. A Rollout reads back only after successful puts;
// failed transfers are recorded as failed and may leave partial remote files without a read-back digest.
// Checksum-framed managed-file loading subsequently detects incomplete or corrupt transfers; no rollback is promised.
// A Copier adds no credentials of its own and runs no remote command.
type Copier interface {
	// Fetch copies src on a machine to the local file dst.
	Fetch(ctx context.Context, src Remote, dst string) error
	// Put copies the local file src to dst on a machine.
	Put(ctx context.Context, src string, dst Remote) error
}

// Rollout plans and applies managed-settings rollouts from one private config directory.
type Rollout struct {
	Roster        *Roster
	Managed       *ManagedSource
	rosterDigest  string
	managedDigest string
	privateDir    string
	// Transport reaches the enrolled machines.
	Transport Copier
	// Now stamps receipts.
	Now func() time.Time
}

// LoadRollout reads roster.toml and managed.toml from the private config directory.
func LoadRollout(privateDir string, t Copier) (*Rollout, error) {
	rosterData, err := os.ReadFile(filepath.Join(privateDir, "roster.toml"))
	if err != nil {
		return nil, err
	}
	roster, err := ParseRoster(rosterData)
	if err != nil {
		return nil, err
	}
	managedData, err := os.ReadFile(filepath.Join(privateDir, "managed.toml"))
	if err != nil {
		return nil, err
	}
	managed, err := ParseManaged(managedData)
	if err != nil {
		return nil, err
	}
	return &Rollout{
		Roster: roster, Managed: managed, privateDir: privateDir,
		rosterDigest: string(digest.Of(rosterData)), managedDigest: string(digest.Of(managedData)),
		Transport: t, Now: time.Now,
	}, nil
}

// Plan is a dry-run rollout: what each copy would become and the planned transport commands.
type Plan struct {
	Schema        string     `json:"schema"`
	Digest        string     `json:"digest"`
	RosterDigest  string     `json:"roster_digest"`
	ManagedDigest string     `json:"managed_digest"`
	Machines      []string   `json:"machines"`
	StageDir      string     `json:"stage_dir"`
	Copies        []CopyPlan `json:"copies"`
}

// CopyPlan is one Director copy's part of a plan.
type CopyPlan struct {
	Machine  string `json:"machine"`
	Director string `json:"director"`
	SSHAlias string `json:"ssh_alias"`
	Target   string `json:"target"`
	Status   string `json:"status"`
	Problem  string `json:"problem,omitempty"`
	// LocalSHA256 binds the fetched local config; BeforeSHA256 and AfterSHA256 identify managed revisions.
	LocalSHA256  string `json:"local_sha256,omitempty"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	// Revision is where the previous managed file is kept on the machine.
	Revision string     `json:"revision,omitempty"`
	Changes  []Change   `json:"changes,omitempty"`
	Diff     string     `json:"diff,omitempty"`
	Commands [][]string `json:"commands,omitempty"`

	before, after []byte
}

// NeedsApproval reports whether any change in the plan is outside routine rollout authority.
func (p *Plan) NeedsApproval() bool {
	for _, c := range p.Copies {
		for _, ch := range c.Changes {
			if ch.NeedsApproval {
				return true
			}
		}
	}
	return false
}

// transfer is one copy a rollout makes, between a local file and a machine.
type transfer struct {
	local  string
	remote Remote
	put    bool
}

// command is the scp command that makes the copy, as plans and receipts record it.
func (x transfer) command() []string {
	if x.put {
		return scpCommand(x.local, x.remote.String())
	}
	return scpCommand(x.remote.String(), x.local)
}

func (x transfer) run(ctx context.Context, c Copier) error {
	if x.put {
		return c.Put(ctx, x.local, x.remote)
	}
	return c.Fetch(ctx, x.remote, x.local)
}

// transfers are the copies that apply a changed copy plan: keep the previous revision, publish, read back.
func (c CopyPlan) transfers(previous, managed, verify string) []transfer {
	target := Remote{c.SSHAlias, c.Target}
	return []transfer{
		{local: previous, remote: Remote{c.SSHAlias, c.Revision}, put: true},
		{local: managed, remote: target, put: true},
		{local: verify, remote: target},
	}
}

func (c CopyPlan) stage(stageDir string) string {
	return filepath.Join(stageDir, "files", c.Machine, c.Director)
}

// Plan fetches each Director copy on the named machines and renders the rollout, changing nothing.
// stageDir is where the plan, its previews and its receipts are kept; the commands name files in it.
func (r *Rollout) Plan(ctx context.Context, machines []string, stageDir string) (*Plan, error) {
	if len(machines) == 0 {
		return nil, errors.New("name at least one enrolled machine")
	}
	stageDir, err := filepath.Abs(stageDir)
	if err != nil {
		return nil, err
	}
	fetchDir, err := os.MkdirTemp("", "guildhall-rollout-")
	if err != nil {
		return nil, err
	}
	// The fetched copies are scratch; failing to remove them changes no outcome.
	defer func() { _ = os.RemoveAll(fetchDir) }()

	p := &Plan{Schema: PlanSchema, RosterDigest: r.rosterDigest, ManagedDigest: r.managedDigest, Machines: machines, StageDir: stageDir}
	for i, label := range machines {
		if slices.Contains(machines[:i], label) {
			return nil, fmt.Errorf("machine %q is named twice", label)
		}
		m, ok := r.Roster.Machine(label)
		if !ok {
			return nil, fmt.Errorf("machine %q is not enrolled in the roster; enrolment comes first", label)
		}
		if len(m.Directors) == 0 {
			return nil, fmt.Errorf("machine %q has no Director copies", label)
		}
		for j, d := range m.Directors {
			c := CopyPlan{Machine: m.Label, Director: d.Name, SSHAlias: m.SSHAlias, Target: d.CopyDir + "/managed.toml"}
			r.planCopy(ctx, &c, d, filepath.Join(fetchDir, fmt.Sprintf("%d-%d.toml", i, j)), stageDir)
			p.Copies = append(p.Copies, c)
		}
	}
	p.Digest = p.digest()
	return p, nil
}

func (r *Rollout) planCopy(ctx context.Context, c *CopyPlan, d Director, fetched, stageDir string) {
	if err := r.Transport.Fetch(ctx, Remote{c.SSHAlias, d.ConfigPath()}, fetched+".local"); err != nil {
		c.Status, c.Problem = StatusRefused, err.Error()
		return
	}
	local, err := os.ReadFile(fetched + ".local")
	if err != nil {
		c.Status, c.Problem = StatusRefused, err.Error()
		return
	}
	c.LocalSHA256 = digest.Of(local).Hex()
	remote := Remote{c.SSHAlias, c.Target}
	if err := r.Transport.Fetch(ctx, remote, fetched); err != nil {
		c.Status, c.Problem = StatusRefused, "fetching "+remote.String()+": "+err.Error()
		return
	}
	before, err := os.ReadFile(fetched)
	if err != nil {
		c.Status, c.Problem = StatusRefused, err.Error()
		return
	}
	c.before, c.BeforeSHA256 = before, digest.Of(before).Hex()
	cfg, err := config.ParseCombined(local, before)
	if err == nil {
		err = checkTarget(cfg, Target{Machine: c.Machine, Director: d})
	}
	if err != nil {
		c.Status, c.Problem = StatusRefused, err.Error()
		return
	}
	after, err := config.EncodeManaged(r.Managed.Settings())
	if err == nil {
		_, err = config.ParseCombined(local, after)
	}
	changes := changes(cfg.Managed, r.Managed)
	if err != nil {
		c.Status, c.Problem = StatusRefused, err.Error()
		return
	}
	c.after, c.AfterSHA256, c.Changes = after, digest.Of(after).Hex(), changes
	if bytes.Equal(before, after) {
		c.Status = StatusUnchanged
		return
	}
	c.Status = StatusChange
	c.Revision = c.Target + ".rev-" + c.BeforeSHA256[:16]
	c.Diff = lineDiff(string(before), string(after))
	stage := c.stage(stageDir)
	for _, x := range c.transfers(filepath.Join(stage, "previous.toml"), filepath.Join(stage, "managed.toml"), filepath.Join(stage, "verify.toml")) {
		c.Commands = append(c.Commands, x.command())
	}
}

func (p *Plan) digest() string {
	q := *p
	q.Digest = ""
	data, err := json.Marshal(&q)
	if err != nil {
		panic(err) // A plan is plain data and always encodes.
	}
	return string(digest.Of(data))
}

// WritePlan writes plan.json and each changed copy's previous.toml and managed.toml previews into a new stage directory.
func WritePlan(p *Plan) error {
	if _, err := os.Stat(filepath.Join(p.StageDir, "plan.json")); err == nil {
		return fmt.Errorf("%s already holds a plan; use a new directory", p.StageDir)
	}
	for _, c := range p.Copies {
		if c.Status != StatusChange {
			continue
		}
		stage := c.stage(p.StageDir)
		if err := os.MkdirAll(stage, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, "previous.toml"), c.before, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, "managed.toml"), c.after, 0o600); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.StageDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.StageDir, "plan.json"), append(data, '\n'), 0o600)
}

// ReadPlan reads a plan written by WritePlan.
func ReadPlan(stageDir string) (*Plan, error) {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.json"))
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := strictjson.Decode(data, &p, strictjson.Options{Schema: planSchemaPrefix, Version: planSchemaVersion}); err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	return &p, nil
}

// Receipt records what a rollout did to each copy.
type Receipt struct {
	Schema     string        `json:"schema"`
	PlanDigest string        `json:"plan_digest"`
	Approved   bool          `json:"approved"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Copies     []CopyReceipt `json:"copies"`
	// File is where the receipt was written.
	File string `json:"-"`
}

// CopyReceipt is one copy's outcome.
type CopyReceipt struct {
	Machine        string `json:"machine"`
	Director       string `json:"director"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	BeforeSHA256   string `json:"before_sha256"`
	AfterSHA256    string `json:"after_sha256"`
	VerifiedSHA256 string `json:"verified_sha256,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Steps          []Step `json:"steps,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Step is one command a rollout ran.
type Step struct {
	Command []string `json:"command"`
	Error   string   `json:"error,omitempty"`
}

// Apply carries out the dry-run plan in stageDir.
// It plans again and refuses unless the result is identical and the staged previews are intact,
// and refuses a plan needing approval unless approve is that plan's digest.
// Each changed copy keeps its previous managed file as a revision, then gets the new managed file;
// only after both puts succeed is it read back and verified.
// The local config is never written; validated managed inputs replace preview source paths in transport commands.
// A copy that fails does not stop the others; the receipt, written to stageDir, records every outcome.
//
// Apply is the private config directory's single writer: after checking the machine set and before any transport
// it creates .rollout.lock there exclusively, and removes it when it returns, whether it succeeded or failed.
// While the lock exists Apply is refused with an error wrapping fs.ErrExist, and the lock is left as it is.
// A lock left by a writer that stopped without returning is not removed automatically;
// an operator removes it once they have confirmed that writer has stopped.
func (r *Rollout) Apply(ctx context.Context, stageDir, approve string, machines ...string) (*Receipt, error) {
	saved, err := ReadPlan(stageDir)
	if err != nil {
		return nil, err
	}
	requested := slices.Sorted(slices.Values(machines))
	planned := slices.Sorted(slices.Values(saved.Machines))
	if !slices.Equal(requested, planned) {
		return nil, errors.New("requested machines differ from saved plan; use its exact machine set or create a new plan")
	}
	lock := filepath.Join(r.privateDir, ".rollout.lock")
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("rollout writer lock %s: %w; confirm the writer has stopped before stale-lock recovery", lock, err)
	}
	defer func() { _ = os.Remove(lock) }()
	if err := f.Close(); err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(stageDir); err != nil || abs != saved.StageDir {
		return nil, fmt.Errorf("%w: the plan was written for %s", ErrPlanStale, saved.StageDir)
	}
	p, err := r.Plan(ctx, saved.Machines, saved.StageDir)
	if err != nil {
		return nil, err
	}
	if p.Digest != saved.Digest {
		return nil, ErrPlanStale
	}
	for _, c := range p.Copies {
		if c.Status == StatusRefused {
			return nil, ErrNotApplicable
		}
	}
	if p.NeedsApproval() && approve != p.Digest {
		return nil, ErrApprovalRequired
	}
	for _, c := range p.Copies {
		if c.Status != StatusChange {
			continue
		}
		stage := c.stage(p.StageDir)
		previous, err1 := os.ReadFile(filepath.Join(stage, "previous.toml"))
		next, err2 := os.ReadFile(filepath.Join(stage, "managed.toml"))
		if err1 != nil || err2 != nil || !bytes.Equal(previous, c.before) || !bytes.Equal(next, c.after) {
			return nil, fmt.Errorf("%w: staged files for %s/%s changed", ErrPlanStale, c.Machine, c.Director)
		}
	}

	receipt := &Receipt{Schema: ReceiptSchema, PlanDigest: p.Digest, Approved: approve == p.Digest, StartedAt: r.Now().UTC()}
	complete := true
	for _, c := range p.Copies {
		cr := r.applyCopy(ctx, c, p.StageDir)
		complete = complete && (cr.Status == StatusApplied || cr.Status == StatusUnchanged)
		receipt.Copies = append(receipt.Copies, cr)
	}
	receipt.FinishedAt = r.Now().UTC()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return receipt, err
	}
	file := filepath.Join(p.StageDir, "receipt-"+receipt.StartedAt.Format("20060102T150405Z")+".json")
	if err := os.WriteFile(file, append(data, '\n'), 0o600); err != nil {
		return receipt, err
	}
	receipt.File = file
	if !complete {
		return receipt, ErrIncomplete
	}
	return receipt, nil
}

func (r *Rollout) applyCopy(ctx context.Context, c CopyPlan, stageDir string) CopyReceipt {
	cr := CopyReceipt{
		Machine: c.Machine, Director: c.Director, Target: c.Target, Status: c.Status,
		BeforeSHA256: c.BeforeSHA256, AfterSHA256: c.AfterSHA256, Revision: c.Revision,
	}
	if c.Status != StatusChange {
		return cr
	}
	inputs, err := os.MkdirTemp(stageDir, ".apply-")
	if err != nil {
		cr.Status, cr.Error = StatusFailed, err.Error()
		return cr
	}
	defer func() { _ = os.RemoveAll(inputs) }()
	for name, data := range map[string][]byte{"previous.toml": c.before, "managed.toml": c.after} {
		if err := os.WriteFile(filepath.Join(inputs, name), data, 0o600); err != nil {
			cr.Status, cr.Error = StatusFailed, err.Error()
			return cr
		}
	}
	stage := c.stage(stageDir)
	for _, x := range c.transfers(filepath.Join(inputs, "previous.toml"), filepath.Join(inputs, "managed.toml"), filepath.Join(stage, "verify.toml")) {
		step := Step{Command: x.command()}
		err := x.run(ctx, r.Transport)
		if err != nil {
			step.Error = err.Error()
		}
		cr.Steps = append(cr.Steps, step)
		if err != nil {
			cr.Status, cr.Error = StatusFailed, err.Error()
			return cr
		}
	}
	verified, err := os.ReadFile(filepath.Join(stage, "verify.toml"))
	if err != nil {
		cr.Status, cr.Error = StatusFailed, err.Error()
		return cr
	}
	cr.VerifiedSHA256 = digest.Of(verified).Hex()
	if cr.VerifiedSHA256 != c.AfterSHA256 {
		cr.Status, cr.Error = StatusVerifyFailed, "the copy read back does not match the rolled-out config"
		return cr
	}
	cr.Status = StatusApplied
	return cr
}
