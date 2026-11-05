package steward

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/secrets"
	"github.com/ndrwl/guildhall/internal/strictjson"
)

// Retire plan and receipt schemas (docs/secrets.md#retiring-a-machine).
const (
	RetirePlanSchema    = retirePlanSchemaPrefix + "1"
	RetireReceiptSchema = "guildhall.retire-receipt/1"

	retirePlanSchemaPrefix  = "guildhall.retire-plan/"
	retirePlanSchemaVersion = 1
)

// A retirement is open until every value the machine could read is rotated, then closed.
const (
	RetireOpen   = "open"
	RetireClosed = "closed"
)

// How a rotation closed: a new value set on its machine, that machine retired too, or, for a name no other machine
// holds, the Guildmaster's mark that it was rotated at its provider.
const (
	ClosedBySet     = "secrets set"
	ClosedByRetired = "machine retired"
	ClosedByMark    = "marked rotated"
)

// RetiredFile is the private config's receipt of a retired machine, a slash-separated path below the checkout.
func RetiredFile(machine string) string { return "retired/" + machine + ".json" }

// retiredDir holds every RetiredFile.
const retiredDir = "retired"

// openPattern matches the line MarshalIndent writes for an open receipt's state.
const openPattern = `^  "state": "open",?$`

// Rotation is one value the retired machine could read. When another machine still holds it, it is Name on Machine,
// closed once a new value is set there or that machine is retired too; when none does, Machine is empty and it closes
// once the Guildmaster marks it rotated at its provider.
type Rotation struct {
	Name     string     `json:"name"`
	Machine  string     `json:"machine,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`
	ClosedBy string     `json:"closed_by,omitempty"`
}

// RetirePlan is a dry-run retirement of one machine: the recipient and file it removes, every name the machine could
// read with the machines that must get a new value, and the credentials to revoke at their providers. It holds names
// and digests, never a value.
type RetirePlan struct {
	Schema       string `json:"schema"`
	Digest       string `json:"digest"`
	RosterDigest string `json:"roster_digest"`
	Commit       string `json:"commit"`
	Machine      string `json:"machine"`
	SSHAlias     string `json:"ssh_alias"`
	// Recipient is the age_recipient removed from the roster, empty when it has none.
	Recipient string `json:"recipient,omitempty"`
	// File is the secrets file removed from the private config; FileSHA256 its digest, empty when there is none.
	File       string   `json:"file"`
	FileSHA256 string   `json:"file_sha256,omitempty"`
	Names      []string `json:"names"`
	// Rotations are every name the machine could read: one for each other machine that still holds it, to be given
	// a new value there with secrets set, or, when none does, one with no machine, to be marked rotated.
	Rotations []Rotation `json:"rotations"`
	// Revoke are the manual steps that revoke the machine's own credentials at their providers.
	Revoke   []string `json:"revoke"`
	StageDir string   `json:"stage_dir"`

	roster []byte
}

// RetireReceipt is a retired machine's record in the private config, open until each rotation closes.
type RetireReceipt struct {
	Schema     string `json:"schema"`
	State      string `json:"state"`
	Machine    string `json:"machine"`
	PlanDigest string `json:"plan_digest"`
	Recipient  string `json:"recipient,omitempty"`
	File       string `json:"file"`
	FileSHA256 string `json:"file_sha256,omitempty"`
	// FileCommit is the commit the plan read File at, so its old values can be decrypted with the recovery key.
	FileCommit string     `json:"file_commit,omitempty"`
	Names      []string   `json:"names"`
	RetiredAt  time.Time  `json:"retired_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	Rotations  []Rotation `json:"rotations"`
	Revoke     []string   `json:"revoke"`
	// Commit is the private config commit that retired the machine, set once it is pushed; never in the record.
	Commit string `json:"-"`
	// Closed are the other retirements this one closed rotations of; never in the record.
	Closed []string `json:"-"`
}

// Pending are the rotations not closed yet.
func (r *RetireReceipt) Pending() []Rotation {
	var out []Rotation
	for _, x := range r.Rotations {
		if x.ClosedAt == nil {
			out = append(out, x)
		}
	}
	return out
}

// waits reports whether a rotation of name on machine is still open; machine is empty for a name no other machine
// holds.
func (r *RetireReceipt) waits(name, machine string) bool {
	return slices.ContainsFunc(r.Pending(), func(x Rotation) bool { return x.Name == name && x.Machine == machine })
}

// close closes each open rotation of name on machine, or every one on machine when name is empty, and the receipt
// when none is left open; it reports whether it closed any. An empty machine names the rotations no machine holds.
func (r *RetireReceipt) close(name, machine, by string, at time.Time) bool {
	closed := false
	for i, x := range r.Rotations {
		if x.ClosedAt == nil && x.Machine == machine && (name == "" || x.Name == name) {
			r.Rotations[i].ClosedAt, r.Rotations[i].ClosedBy = &at, by
			closed = true
		}
	}
	if closed && len(r.Pending()) == 0 {
		r.State, r.ClosedAt = RetireClosed, &at
	}
	return closed
}

func (r *RetireReceipt) encode() []byte {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		panic(err) // A receipt is plain data and always encodes.
	}
	return append(data, '\n')
}

// ReadRetireReceipt decodes a retired machine's record.
func ReadRetireReceipt(data []byte) (*RetireReceipt, error) {
	var r RetireReceipt
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("retire receipt: %w", err)
	}
	if r.Schema != RetireReceiptSchema {
		return nil, fmt.Errorf("retire receipt: schema %q, want %q", r.Schema, RetireReceiptSchema)
	}
	return &r, nil
}

// Retirement reads machine's retirement record at the checkout's HEAD, and false when it has none.
func (s *Secrets) Retirement(ctx context.Context, machine string) (*RetireReceipt, bool, error) {
	data, ok, err := s.Private.Show(ctx, "HEAD", RetiredFile(machine))
	if err != nil || !ok {
		return nil, false, err
	}
	r, err := ReadRetireReceipt(data)
	return r, err == nil, err
}

// openRetirements are the open retirement records at commit head.
func (s *Secrets) openRetirements(ctx context.Context, head string) ([]*RetireReceipt, error) {
	files, err := s.Private.Grep(ctx, head, []string{openPattern}, retiredDir)
	if err != nil {
		return nil, err
	}
	var out []*RetireReceipt
	for _, f := range files {
		data, ok, err := s.Private.Show(ctx, head, f)
		if err != nil || !ok {
			return nil, fmt.Errorf("reading %s: %v", f, err)
		}
		r, err := ReadRetireReceipt(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if r.State == RetireOpen {
			out = append(out, r)
		}
	}
	return out, nil
}

// revokeSteps are the manual steps that revoke m's own credentials at their providers; sign-ins are never copied
// between machines, so each is the machine's alone (docs/signin.md).
func revokeSteps(m Machine) []string {
	return []string{
		fmt.Sprintf("GitHub: revoke %s's gh sign-in (GitHub CLI under https://github.com/settings/applications) and any token, deploy key or session of it at https://github.com/settings/sessions", m.Label),
		fmt.Sprintf("SSH: remove %s's public keys from every authorized_keys and from https://github.com/settings/keys, and the Host %s entry from the Steward's SSH config", m.Label, m.SSHAlias),
		fmt.Sprintf("Herdr: remove %s's Herdr profile and any remote access it grants", m.Label),
		fmt.Sprintf("Claude: sign %s's Claude Code session out of the Claude account (claude.ai settings, active sessions), and revoke any API key it used in the Claude Console", m.Label),
		fmt.Sprintf("Codex: sign %s's Codex session out of the ChatGPT account (Settings, Security, log out of devices), and revoke any OpenAI API key it used", m.Label),
	}
}

// recipientLine matches roster.toml's age_recipient line for one key, with an optional comment.
func recipientLine(key string) *regexp.Regexp {
	return regexp.MustCompile(`^[ \t]*age_recipient[ \t]*=[ \t]*"` + regexp.QuoteMeta(key) + `"[ \t]*(#.*)?\r?$`)
}

// withoutRecipient is roster.toml text data with machine's age_recipient line removed, checked to change nothing else.
func withoutRecipient(data []byte, r *Roster, machine string) ([]byte, error) {
	m, _ := r.Machine(machine)
	if m.AgeRecipient == "" {
		return data, nil
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	match := recipientLine(m.AgeRecipient)
	at := -1
	for i, l := range lines {
		if match.Match(bytes.TrimSuffix(l, []byte("\n"))) {
			if at >= 0 {
				at = -2
				break
			}
			at = i
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("%w: roster.toml does not write %s's age_recipient on a line of its own, so it cannot be removed safely; remove it by hand", ErrRosterConflict, machine)
	}
	next := bytes.Join(slices.Delete(slices.Clone(lines), at, at+1), nil)
	after, err := ParseRoster(next)
	if err != nil {
		return nil, err
	}
	want := *r
	want.Machines = slices.Clone(r.Machines)
	for i := range want.Machines {
		if want.Machines[i].Label == machine {
			want.Machines[i].AgeRecipient = ""
		}
	}
	got, _ := json.Marshal(after)
	exp, _ := json.Marshal(&want)
	if !bytes.Equal(got, exp) {
		return nil, fmt.Errorf("%w: removing %s's age_recipient line would change more of roster.toml; remove it by hand", ErrRosterConflict, machine)
	}
	return next, nil
}

// RetirePlan reads machine's recipient and secrets file and every other machine's file at the checkout's HEAD and
// plans the retirement, changing nothing. It refuses a machine not in the roster, one already retired, a
// roster.toml with uncommitted changes, and a secrets file it cannot read the names of.
func (s *Secrets) RetirePlan(ctx context.Context, machine, stageDir string) (*RetirePlan, error) {
	m, ok := s.Roster.Machine(machine)
	if !ok {
		return nil, fmt.Errorf("machine %q is not in the roster", machine)
	}
	stageDir, err := filepath.Abs(stageDir)
	if err != nil {
		return nil, err
	}
	head, err := s.Private.Head(ctx)
	if err != nil {
		return nil, err
	}
	rosterData, ok, err := s.Private.Show(ctx, head, "roster.toml")
	switch {
	case err != nil:
		return nil, err
	case !ok || string(digest.Of(rosterData)) != s.rosterDigest:
		return nil, errors.New("roster.toml in the private config differs from its HEAD; commit or discard that change first")
	}
	if data, ok, err := s.Private.Show(ctx, head, RetiredFile(machine)); err != nil {
		return nil, err
	} else if ok {
		if prev, err := ReadRetireReceipt(data); err == nil && prev.State == RetireClosed {
			return nil, fmt.Errorf("machine %q is already retired, its retirement closed in %s", machine, RetiredFile(machine))
		}
		return nil, fmt.Errorf("machine %q has an open retirement in %s; finish its rotations first", machine, RetiredFile(machine))
	}
	p := &RetirePlan{Schema: RetirePlanSchema, RosterDigest: s.rosterDigest, Commit: head, Machine: machine, SSHAlias: m.SSHAlias,
		Recipient: m.AgeRecipient, File: SecretsFile(machine), Names: []string{}, Rotations: []Rotation{},
		Revoke: revokeSteps(m), StageDir: stageDir}
	if p.roster, err = withoutRecipient(rosterData, s.Roster, machine); err != nil {
		return nil, err
	}
	data, ok, err := s.Private.Show(ctx, head, p.File)
	if err != nil {
		return nil, err
	}
	if ok {
		if p.Names, _, err = secrets.Ciphertext(data); err != nil {
			return nil, fmt.Errorf("%s: %w", p.File, err)
		}
		p.FileSHA256 = digest.Of(data).Hex()
	}
	held := map[string]bool{}
	for _, o := range s.Roster.Machines {
		if o.Label == machine {
			continue
		}
		data, ok, err := s.Private.Show(ctx, head, SecretsFile(o.Label))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		names, _, err := secrets.Ciphertext(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", SecretsFile(o.Label), err)
		}
		for _, n := range p.Names {
			if slices.Contains(names, n) {
				p.Rotations = append(p.Rotations, Rotation{Name: n, Machine: o.Label})
				held[n] = true
			}
		}
	}
	for _, n := range p.Names {
		if !held[n] {
			p.Rotations = append(p.Rotations, Rotation{Name: n})
		}
	}
	slices.SortFunc(p.Rotations, func(a, b Rotation) int {
		return strings.Compare(a.Name+"\x00"+a.Machine, b.Name+"\x00"+b.Machine)
	})
	p.Digest = retirePlanDigest(*p)
	return p, nil
}

func retirePlanDigest(p RetirePlan) string {
	p.Digest = ""
	data, err := json.Marshal(&p)
	if err != nil {
		panic(err) // A plan is plain data and always encodes.
	}
	return string(digest.Of(data))
}

// WriteRetirePlan writes plan.json into a new stage directory.
func WriteRetirePlan(p *RetirePlan) error {
	file := filepath.Join(p.StageDir, "plan.json")
	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("%s already holds a plan; use a new directory", p.StageDir)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.StageDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o600)
}

// ReadRetirePlan reads a plan written by WriteRetirePlan.
func ReadRetirePlan(stageDir string) (*RetirePlan, error) {
	data, err := os.ReadFile(filepath.Join(stageDir, "plan.json"))
	if err != nil {
		return nil, err
	}
	var p RetirePlan
	if err := strictjson.Decode(data, &p, strictjson.Options{Schema: retirePlanSchemaPrefix, Version: retirePlanSchemaVersion}); err != nil {
		return nil, fmt.Errorf("retire plan: %w", err)
	}
	return &p, nil
}

// IsRetirePlan reports whether planDir holds a retire plan.
func IsRetirePlan(planDir string) bool {
	data, err := os.ReadFile(filepath.Join(planDir, "plan.json"))
	if err != nil {
		return false
	}
	var head struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(data, &head) == nil && strings.HasPrefix(head.Schema, retirePlanSchemaPrefix)
}

// Retire carries out the retire plan in stageDir for machine once the Guildmaster approved it: approve must be its
// digest. Holding .rollout.lock, it plans again and refuses unless the digest is the same; then in one commit it
// removes the machine's age_recipient from roster.toml, regenerates .sops.yaml without it, removes its secrets file
// and writes its open retirement record, closes the rotations other open retirements wait for on this machine, and
// pushes. No other machine's secrets file changes. The record, also written to stageDir as receipt.json, stays open
// until secrets set gives each held name a new value and the Guildmaster marks each name no machine holds rotated
// with MarkRotated. A failed push is returned with the commit made, which stays.
func (s *Secrets) Retire(ctx context.Context, stageDir, approve, machine string) (*RetireReceipt, error) {
	saved, err := ReadRetirePlan(stageDir)
	if err != nil {
		return nil, err
	}
	if machine != saved.Machine {
		return nil, fmt.Errorf("the plan retires %q, not %q", saved.Machine, machine)
	}
	release, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer release()
	if abs, err := filepath.Abs(stageDir); err != nil || abs != saved.StageDir {
		return nil, fmt.Errorf("%w: the plan was written for %s", ErrPlanStale, saved.StageDir)
	}
	p, err := s.RetirePlan(ctx, machine, saved.StageDir)
	if err != nil {
		return nil, err
	}
	if p.Digest != saved.Digest {
		return nil, ErrPlanStale
	}
	if approve != p.Digest {
		return nil, ErrApprovalRequired
	}
	now := s.Now().UTC()
	r := &RetireReceipt{Schema: RetireReceiptSchema, State: RetireOpen, Machine: machine, PlanDigest: p.Digest, Recipient: p.Recipient,
		File: p.File, FileSHA256: p.FileSHA256, Names: p.Names, RetiredAt: now, Rotations: p.Rotations, Revoke: p.Revoke}
	if p.FileSHA256 != "" {
		r.FileCommit = p.Commit
	}
	if len(r.Rotations) == 0 {
		r.State, r.ClosedAt = RetireClosed, &now
	}
	files := map[string][]byte{"roster.toml": p.roster, RetiredFile(machine): r.encode()}
	after, err := ParseRoster(p.roster)
	if err != nil {
		return nil, err
	}
	if after.RecoveryRecipient != "" {
		if files[SopsConfigFile], err = SopsConfig(after); err != nil {
			return nil, err
		}
	}
	others, err := s.openRetirements(ctx, p.Commit)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		if o.Machine != machine && o.close("", machine, ClosedByRetired, now) {
			files[RetiredFile(o.Machine)] = o.encode()
			r.Closed = append(r.Closed, o.Machine)
		}
	}
	var remove []string
	if p.FileSHA256 != "" {
		remove = append(remove, p.File)
	}
	if r.Commit, err = s.Private.CommitChanges(ctx, files, remove, "Retire machine "+machine, s.signature()); err != nil {
		return nil, fmt.Errorf("committing the retirement: %w", err)
	}
	if err := os.WriteFile(filepath.Join(p.StageDir, "receipt.json"), r.encode(), 0o600); err != nil {
		return r, err
	}
	pushed, err := s.Private.Publish(ctx, s.signature())
	if err != nil {
		return r, fmt.Errorf("committed the retirement as %s but pushing the private config failed: %w", r.Commit, err)
	}
	r.Commit = pushed
	return r, nil
}

// MarkRotated records the Guildmaster's word that name, which retired machine could read and no other machine holds,
// was rotated or revoked at its provider: holding .rollout.lock, it closes that rotation in the machine's open
// retirement record with its date, closing the record with its last rotation, and commits and pushes it. It refuses a
// name another machine holds, which closes only when secrets set gives it a new value there. A failed push is
// returned with the commit made, which stays.
func (s *Secrets) MarkRotated(ctx context.Context, machine, name string) (*RetireReceipt, error) {
	release, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer release()
	r, ok, err := s.Retirement(ctx, machine)
	switch {
	case err != nil:
		return nil, err
	case !ok:
		return nil, fmt.Errorf("machine %q has no retirement in the private config", machine)
	case r.State != RetireOpen:
		return nil, fmt.Errorf("the retirement of %q is already closed", machine)
	case !r.waits(name, ""):
		var on []string
		for _, x := range r.Pending() {
			if x.Name == name {
				on = append(on, x.Machine)
			}
		}
		if len(on) > 0 {
			return nil, fmt.Errorf("%s is held by %s; rotate it at its provider and set the new value there with guildhall steward secrets set", name, strings.Join(on, ", "))
		}
		return nil, fmt.Errorf("the retirement of %q waits for no rotation of %s", machine, name)
	}
	r.close(name, "", ClosedByMark, s.Now().UTC())
	if r.Commit, err = s.Private.CommitFiles(ctx, map[string][]byte{RetiredFile(machine): r.encode()},
		fmt.Sprintf("Mark %s rotated for retired machine %s", name, machine), s.signature()); err != nil {
		return nil, fmt.Errorf("committing %s: %w", RetiredFile(machine), err)
	}
	pushed, err := s.Private.Publish(ctx, s.signature())
	if err != nil {
		return r, fmt.Errorf("committed %s as %s but pushing the private config failed: %w", RetiredFile(machine), r.Commit, err)
	}
	r.Commit = pushed
	return r, nil
}
