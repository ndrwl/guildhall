package interp

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// AcceptGrill is how a task's grill ran, as Director acceptance judges it: the grill mode in effect for a Director
// grill, AcceptGrillArtisan for a workflow whose grill is run by an Artisan, or AcceptGrillNone for one with no grill.
type AcceptGrill string

const (
	AcceptGrillLive                   = AcceptGrill(workflow.GrillLive)
	AcceptGrillUnattended             = AcceptGrill(workflow.GrillUnattended)
	AcceptGrillArtisan    AcceptGrill = "artisan"
	AcceptGrillNone       AcceptGrill = "none"
)

// AcceptCase is what DirectorAcceptable judges: one plan acceptance and its setting.
type AcceptCase struct {
	// Accept is the repository's threshold, from the copy's config (config.Config.DirectorAccept).
	Accept config.DirectorAccept
	Grill  AcceptGrill
	// Spec is the plan's spec, the bytes the decision binds.
	Spec []byte
	// RequiresGuildmaster is the task's accept.requires_guildmaster override.
	RequiresGuildmaster bool
}

// AcceptVerdict is whether the Director may accept a plan and why: Reasons gives every rule that failed when OK is
// false, and what made it eligible when OK is true.
type AcceptVerdict struct {
	OK      bool
	Reasons []string
}

// String is the verdict as one line, as the decision message shows it.
func (v AcceptVerdict) String() string {
	if v.OK {
		return "the Director may accept it: " + strings.Join(v.Reasons, "; ")
	}
	return "the Guildmaster must answer: " + strings.Join(v.Reasons, "; ")
}

// DirectorAcceptable judges whether the Director may accept a feature plan and tell the Guildmaster rather than ask
// them (docs/configuration.md#repositories). It is eligible only when the setting is on, the grill was unattended or
// run by an Artisan, the spec has at most one slice, its open questions are exactly "None", its stated size is at or
// under both thresholds, its Touches item is "none", and the task does not keep acceptance with the Guildmaster. A
// rule it cannot judge, such as a missing or malformed Size item, fails closed. It is a pure function of c.
func DirectorAcceptable(c AcceptCase) AcceptVerdict {
	var no []string
	if !c.Accept.On() {
		no = append(no, "director_accept_lines and director_accept_files are not both set")
	}
	switch c.Grill {
	case AcceptGrillUnattended, AcceptGrillArtisan:
	case AcceptGrillLive:
		no = append(no, "the grill is live")
	case AcceptGrillNone:
		no = append(no, "the workflow has no grill")
	default:
		no = append(no, fmt.Sprintf("the grill mode %q is unknown", c.Grill))
	}
	list, err := spec.Slices(c.Spec)
	switch {
	case err != nil:
		no = append(no, "its slices do not read: "+err.Error())
	case len(list) > 1:
		no = append(no, fmt.Sprintf("it has %d slices", len(list)))
	}
	q, err := spec.OpenQuestions(c.Spec, spec.OpenQuestionsHeading)
	switch {
	case err != nil:
		no = append(no, "its open questions do not read: "+err.Error())
	case !q.None || len(q.Items) > 0:
		no = append(no, fmt.Sprintf("it has %d open questions", len(q.Items)))
	}
	size, err := spec.SummarySize(c.Spec)
	switch {
	case err != nil:
		no = append(no, "its size does not read: "+err.Error())
	case c.Accept.On():
		if size.Lines > c.Accept.Lines {
			no = append(no, fmt.Sprintf("about %d non-test lines is over %s", size.Lines, limit(c.Accept.Lines, c.Accept.SetLines, c.Accept.BudgetLines, "director_accept_lines", config.QualitySizeBudgetLinesKey)))
		}
		if size.Files > c.Accept.Files {
			no = append(no, fmt.Sprintf("%d files is over %s", size.Files, limit(c.Accept.Files, c.Accept.SetFiles, c.Accept.BudgetFiles, "director_accept_files", config.QualitySizeBudgetFilesKey)))
		}
	}
	touches, err := spec.SummaryTouches(c.Spec)
	switch {
	case err != nil:
		no = append(no, "its touches do not read: "+err.Error())
	case len(touches) > 0:
		no = append(no, "it touches "+strings.Join(touches, ", "))
	}
	if c.RequiresGuildmaster {
		no = append(no, "the task's override "+config.AcceptRequiresGuildmasterOverride+" keeps it with the Guildmaster")
	}
	if len(no) > 0 {
		return AcceptVerdict{Reasons: no}
	}
	return AcceptVerdict{OK: true, Reasons: []string{
		fmt.Sprintf("the grill was %s", c.Grill),
		"one slice",
		"no open questions",
		fmt.Sprintf("about %d non-test lines in %d files, within %d lines and %d files", size.Lines, size.Files, c.Accept.Lines, c.Accept.Files),
		"touches none",
	}}
}

// limit names the threshold a size went over: the key, or the budget that capped it.
func limit(effective, set, budget int, key, budgetKey string) string {
	if effective < set {
		return fmt.Sprintf("%s = %d, the repository's size budget, which caps %s = %d", budgetKey, budget, key, set)
	}
	return fmt.Sprintf("%s = %d", key, effective)
}

// errAcceptBase reports an engine given a task's overrides without the copy's config they were applied to.
var errAcceptBase = errors.New("the repository's own size budget is unknown")

// directorAcceptance returns the Director acceptance verdict for decision d of step s, and whether one applies: d is
// an approval accept-<k> of a guildmaster-approval step whose spec comes from a plan step, on a feature task of a
// repository whose setting is on. Where none applies the decision message is as it was before the setting existed.
// A rule it cannot read, such as the config's quality budget, fails closed with the reason.
func (r *run) directorAcceptance(s workflow.Step, d store.Decision) (AcceptVerdict, bool) {
	if s.Type != "guildmaster-approval" || d.Kind != store.DecisionApproval || !r.planSpec(s) {
		return AcceptVerdict{}, false
	}
	// Repository keys are never overridden per task, so the effective config's repository has the copy's.
	i := slices.IndexFunc(r.e.Config.Repositories, func(c config.Repository) bool { return c.ID == r.task.Repository })
	if i < 0 {
		return AcceptVerdict{}, false
	}
	repo := r.e.Config.Repositories[i]
	if repo.DirectorAcceptLines == 0 && repo.DirectorAcceptFiles == 0 {
		return AcceptVerdict{}, false
	}
	if TaskKindOf(r.task, repo, r.e.Workflow) != config.TaskKindFeature {
		return AcceptVerdict{}, false
	}
	base := r.e.Config
	var overrides map[string]string
	if o := r.e.Overrides; o != nil && o.Repository == r.task.Repository && o.TaskID == r.task.ID {
		base, overrides = o.Base, overrideValues(o.Active)
	}
	if base == nil {
		return AcceptVerdict{Reasons: []string{errAcceptBase.Error()}}, true
	}
	accept, err := base.DirectorAccept(r.task.Repository)
	if err != nil {
		return AcceptVerdict{Reasons: []string{"the setting does not read: " + err.Error()}}, true
	}
	data, err := r.subjectSpec(s, d)
	if err != nil {
		return AcceptVerdict{Reasons: []string{"the spec does not read: " + err.Error()}}, true
	}
	return DirectorAcceptable(AcceptCase{Accept: accept, Grill: r.acceptGrill(), Spec: data,
		RequiresGuildmaster: config.AcceptRequiresGuildmaster(overrides)}), true
}

// subjectSpec returns the bytes of s's spec input, after checking they are the ones decision d binds.
func (r *run) subjectSpec(s workflow.Step, d store.Decision) ([]byte, error) {
	in, err := r.input(s, "spec")
	if err != nil {
		return nil, err
	}
	data, err := r.read(store.DocumentRef{Name: in.Name, Path: in.Name, SHA256: in.SHA256})
	if err != nil {
		return nil, err
	}
	if d.Subject == nil || d.Subject.Kind != store.SubjectDocument || d.Subject.Digest != string(digest.Of(data)) {
		return nil, fmt.Errorf("decision %s does not bind spec %s", d.ID, in.Name)
	}
	return data, nil
}

// notifyAcceptance sends the Director the decision message of decision d of step s, as notifyDecision does, adding
// the Director acceptance verdict where one applies (directorAcceptance).
func (r *run) notifyAcceptance(s workflow.Step, d store.Decision) error {
	v, ok := r.directorAcceptance(s, d)
	if !ok {
		return r.notifyDecision(d, s.ID)
	}
	return r.notify("decision."+d.ID, store.MessageDecision, fmt.Sprintf("Decision %s is open for step %s: %s. Director acceptance: %s.", d.ID, s.ID, d.Kind, v))
}

// planSpec reports whether every source of s's spec input is a plan step's output.
func (r *run) planSpec(s workflow.Step) bool {
	refs := s.Inputs["spec"]
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		id, _, _ := strings.Cut(ref, ".")
		i := slices.IndexFunc(r.def.Steps, func(st workflow.Step) bool { return st.ID == id })
		if i < 0 || r.def.Steps[i].Type != "plan" {
			return false
		}
	}
	return true
}

// acceptGrill is how the task's grill ran: its grill mode for a Director grill, artisan for a grill the repository
// runs by an Artisan, and none for a workflow with no grill.
func (r *run) acceptGrill() AcceptGrill {
	if r.grill.step != "" {
		return AcceptGrill(r.grill.Mode)
	}
	if slices.ContainsFunc(r.base.Steps, func(s workflow.Step) bool { return s.Type == "grill" }) {
		return AcceptGrillArtisan
	}
	return AcceptGrillNone
}
