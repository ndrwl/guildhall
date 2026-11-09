package interp

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// A multi-PR task's slice whose work an earlier slice or a merged pull request already shipped, or a one-PR task whose
// work a merged pull request already shipped, has nothing left to build, so its build can only fail, and no merge
// record will ever close it. The Director closes it with `guildhall
// slice delivered` (docs/tasks.md#a-slice-already-delivered), which feeds the SliceDelivered event: the engine checks
// the evidence it names and records delivered--s<k>-1-delivery, which counts as slice k's close wherever a merge
// record does for the slice position (history.sliceClosed). It is deliberately not a -merge document, so the hallmark
// baseline, TaskPullRequests' merged slices and the outside-merge rule never read it as a merge with a commit.

// deliverySchema versions the delivery record, delivered--s<k>-1-delivery.
const deliverySchema = "guildhall.slice-delivery/1"

// deliveryPart is the delivery record's part, the last segment of its name.
const deliveryPart = "delivery"

// deliveredStep is the step part of slice k's delivery record: delivered, then delivered--s<k>.
func deliveredStep(k int) string { return workflow.SliceStep(workflow.DeliveredStep, k) }

// deliveredID is the id of slice k's delivery, delivered--s<k>-1, which prefixes its record and keys its writes.
func deliveredID(k int) string { return DecisionID(deliveredStep(k), 1, false) }

// DeliveryRecord is the name of slice k's delivery record, delivered--s<k>-1-delivery.
func DeliveryRecord(k int) string { return deliveredID(k) + "-" + deliveryPart }

// deliveryDoc is a slice's delivery record: which slice it closes, what delivered its work, or that it needed no
// change, and why.
type deliveryDoc struct {
	Schema string `json:"schema"`
	Slice  int    `json:"slice"`
	// Kind is DeliveryNoChange for a slice, or one-PR task, closed because it needs no change, with neither BySlice
	// nor ByPullRequest set; empty for one an earlier slice or a merged pull request already delivered.
	Kind string `json:"kind,omitempty"`
	// BySlice is the earlier slice of the task whose merge delivered the work, or 0.
	BySlice int `json:"by_slice,omitempty"`
	// ByPullRequest is the merged pull request that delivered it, or nil.
	ByPullRequest *deliveredPullRequest `json:"by_pull_request,omitempty"`
	Reason        string                `json:"reason"`
	// Decision is the Guildmaster's close decision whose close answer recorded it, or empty for the Director's
	// `slice delivered`.
	Decision string    `json:"decision,omitempty"`
	At       time.Time `json:"at"`
}

// DeliveryNoChange is the delivery record's kind for a slice, or one-PR task, closed because it needs no change.
const DeliveryNoChange = "nothing-to-do"

// NoChangeNeeded is what a no-change delivery record says delivered its work (deliveryDoc.by), as status's
// delivered_by and the close texts say it.
const NoChangeNeeded = "no change needed"

// noChange reports whether the record closes its slice as needing no change.
func (d deliveryDoc) noChange() bool { return d.Kind == DeliveryNoChange }

// closedAs is how the close texts say what closed the slice: "already delivered by <by>" or "no change needed".
func (d deliveryDoc) closedAs() string {
	if d.noChange() {
		return NoChangeNeeded
	}
	return "already delivered by " + d.by()
}

// closedBy is who closed it: the Guildmaster in the decision named, or the Director.
func (d deliveryDoc) closedBy() string {
	if d.Decision != "" {
		return "The Guildmaster closed"
	}
	return "The Director closed"
}

// deliveredPullRequest is the merged pull request a delivery names, as the forge reported it.
type deliveredPullRequest struct {
	URL         string `json:"url"`
	Number      int    `json:"number"`
	MergeCommit string `json:"merge_commit"`
}

// by says what delivered the slice's work: "slice <j>", the pull request's URL, or NoChangeNeeded.
func (d deliveryDoc) by() string {
	if d.noChange() {
		return NoChangeNeeded
	}
	if d.ByPullRequest != nil {
		return d.ByPullRequest.URL
	}
	return "slice " + strconv.Itoa(d.BySlice)
}

// sliceClosed returns the index of the record that closed slice k: its merge record (sliceMerge) or its delivery
// record, or -1. Slice position counts both; only a merge record carries a pull request.
func (h *history) sliceClosed(deliver string, k int) int {
	if i := h.sliceMerge(deliver, k); i >= 0 {
		return i
	}
	return h.sliceDelivery(k)
}

// sliceDelivery returns the index of slice k's delivery record, or -1.
func (h *history) sliceDelivery(k int) int {
	for i, p := range h.docPlaces {
		if p.ok && p.part == deliveryPart && !p.exit && p.index == 0 && p.round == 1 && p.step == deliveredStep(k) {
			return i
		}
	}
	return -1
}

// sliceDelivered validates the SliceDelivered event, the Director's or the one a close decision's close answer feeds
// (closeOrRetry), and records the slice's delivery. It is refused, with nothing written, unless ev.Slice is the task's
// current slice with no merge record, none of its attempts runs, a reason is given and either ev.NoChange with no
// evidence or exactly one piece of evidence: an earlier slice with a merge record, or a pull request of the task's
// repository that the forge, read through PullRequestReader, reports merged. A one-PR task is slice 1 of 1, so only
// a pull request can have delivered it. A slice that already has a delivery record is a replay, which records
// nothing and lets the advance go on.
func (r *run) sliceDelivered(ev Event) error {
	k := ev.Slice
	switch {
	case k < 1 || k > r.slice.total:
		return fmt.Errorf("interp: %w: %s/%s has slices 1 to %d, not %d", ErrEvent, r.task.Repository, r.task.ID, r.slice.total, k)
	case r.sliceDelivery(k) >= 0:
		return nil
	case k != r.slice.current:
		return fmt.Errorf("interp: %w: slice %d is not %s/%s's current slice, %d", ErrEvent, k, r.task.Repository, r.task.ID, r.slice.current)
	case r.currentMerged():
		return fmt.Errorf("interp: %w: slice %d has a merge record, so it closes as merged", ErrEvent, k)
	case strings.TrimSpace(ev.Reason) == "":
		return fmt.Errorf("interp: %w: a slice delivery needs a reason", ErrEvent)
	case ev.NoChange && (ev.BySlice > 0 || ev.PullRequest != ""):
		return fmt.Errorf("interp: %w: a no-change close names no earlier slice or pull request", ErrEvent)
	case !r.slice.multi() && ev.BySlice > 0:
		return fmt.Errorf("interp: %w: %s/%s delivers one pull request; name the PR that shipped it with --by-pr", ErrEvent, r.task.Repository, r.task.ID)
	case !ev.NoChange && (ev.BySlice > 0) == (ev.PullRequest != ""):
		return fmt.Errorf("interp: %w: a slice delivery names one earlier slice or one pull request", ErrEvent)
	}
	if a := r.runningAttempt(); a != nil {
		return fmt.Errorf("interp: %w: attempt %d of step %s is running", ErrEvent, a.Number, a.Step)
	}
	doc := deliveryDoc{Schema: deliverySchema, Slice: k, BySlice: ev.BySlice, Reason: ev.Reason, Decision: ev.Decision, At: r.now().UTC()}
	switch {
	case ev.NoChange:
		doc.Kind = DeliveryNoChange
	case ev.BySlice > 0:
		if ev.BySlice >= k {
			return fmt.Errorf("interp: %w: slice %d is not earlier than slice %d", ErrEvent, ev.BySlice, k)
		}
		if r.sliceMerge(r.base.Slices.To, ev.BySlice) < 0 {
			return fmt.Errorf("interp: %w: slice %d has no merge record, so it delivered nothing", ErrEvent, ev.BySlice)
		}
	default:
		pr, err := r.mergedPullRequest(ev.PullRequest)
		if err != nil {
			return err
		}
		doc.ByPullRequest = pr
	}
	return r.putDelivery(doc)
}

// runningAttempt is one of the task's running attempts, or nil.
func (r *run) runningAttempt() *store.StageAttempt {
	for i := range r.attempts {
		if r.attempts[i].Outcome == "" {
			return &r.attempts[i]
		}
	}
	return nil
}

// mergedPullRequest reads the pull request at url from the forge: its number is the URL's last path segment, it is
// read from the task repository's remote, and the forge must report it at url, so it is the repository's own, and
// merged. A forge that cannot read pull requests is ErrUnsupported.
func (r *run) mergedPullRequest(url string) (*deliveredPullRequest, error) {
	reader, ok := r.e.Forge.(PullRequestReader)
	if !ok {
		if !r.slice.multi() {
			return nil, fmt.Errorf("interp: %w: the forge does not read pull requests, so a one-PR task cannot be closed as delivered; only cancel remains", ErrUnsupported)
		}
		return nil, fmt.Errorf("interp: %w: the forge does not read pull requests, so name the earlier slice instead", ErrUnsupported)
	}
	n, err := strconv.Atoi(path.Base(strings.TrimRight(url, "/")))
	if err != nil || n < 1 {
		return nil, fmt.Errorf("interp: %w: %q is not a pull request URL ending in its number", ErrEvent, url)
	}
	repo, err := r.repository()
	if err != nil {
		return nil, err
	}
	got, err := reader.PullRequest(r.ctx, forge.View{Repository: repo.Path, Remote: repo.Remote, Number: n})
	if err != nil {
		return nil, fmt.Errorf("interp: pull request %s: %w", url, err)
	}
	switch {
	case got.URL != strings.TrimRight(url, "/"):
		return nil, fmt.Errorf("interp: %w: pull request %d of %s is %s, not %s", ErrEvent, n, repo.Remote, got.URL, url)
	case got.State != forge.Merged:
		return nil, fmt.Errorf("interp: %w: pull request %s is %s, not merged", ErrEvent, got.URL, got.State)
	}
	return &deliveredPullRequest{URL: got.URL, Number: got.Number, MergeCommit: got.MergeCommit}, nil
}

// putDelivery records doc as its slice's delivery record. A record file a crash left with no record, written by the
// same delivery at another time, is recorded as it stands, so the replay of a crashed delivery succeeds.
func (r *run) putDelivery(doc deliveryDoc) error {
	name := DeliveryRecord(doc.Slice)
	p := path.Join("tasks", r.task.Repository, r.task.ID, deliveredID(doc.Slice), "delivery.json")
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if prior, err := os.ReadFile(filepath.Join(r.e.DocumentDir, filepath.FromSlash(p))); err == nil {
		var was deliveryDoc
		if json.Unmarshal(prior, &was) == nil {
			was.At = doc.At
			if same, _ := json.Marshal(was); string(same) == string(data) {
				data = prior
			}
		}
	}
	return r.put(store.DocumentRef{Repository: r.task.Repository, TaskID: r.task.ID, Name: name, Path: p, SHA256: digest.Of(data).Hex()}, data)
}

// delivery reads slice k's delivery record.
func (r *run) delivery(k int) (deliveryDoc, error) {
	var d deliveryDoc
	ok, err := r.readJSONDoc(DeliveryRecord(k), &d)
	if err == nil && !ok {
		err = fmt.Errorf("slice %d has no delivery record", k)
	}
	return d, err
}

// closeDelivered closes the current slice when it has a delivery record: recorded by this advance's SliceDelivered
// event, or before a crash that left the last slice's close undone. A slice before the last closes and the run moves
// on to the next; the last closes the task done, which done reports, with next the wait.
func (r *run) closeDelivered() (done bool, next Next, err error) {
	if r.sliceDelivery(r.slice.current) < 0 {
		return false, Next{}, nil
	}
	k := r.slice.current
	doc, err := r.delivery(k)
	if err != nil {
		return false, Next{}, err
	}
	repo, err := r.repository()
	if err != nil {
		return false, Next{}, err
	}
	if err := r.closeSliceDelivered(k, deliveredID(k), repo, &doc); err != nil {
		return false, Next{}, err
	}
	if !r.slice.final() {
		r.nextSlice()
		return false, Next{}, nil
	}
	s := r.closingStep()
	text := fmt.Sprintf("Task done: slice %d, the last, %s", k, doc.closedAs())
	if !r.slice.multi() {
		text = "Task done: " + doc.closedAs()
	}
	if err := r.closeTask(text); err != nil {
		return false, Next{}, err
	}
	return true, Next{Wait: WaitDone, Step: s.ID}, nil
}

// closeSliceDelivered closes slice k after its delivery record, delivery id's, doc, as closeSlice closes a merged one
// but with no pull request: it voids the slice's open decisions, tells the Director with a slice-delivered message,
// retires the slice's branch and worktree, takes the task out of the merge queue and clears its merge hold. Every
// write is keyed on id, so a repeat sends and records nothing new. The task stays open; closing it after the last
// slice is the caller's.
func (r *run) closeSliceDelivered(k int, id string, repo config.Repository, doc *deliveryDoc) error {
	reason := fmt.Sprintf("Slice %d %s: %s", k, doc.closedAs(), doc.Reason)
	if !r.slice.multi() {
		reason = fmt.Sprintf("%s: %s", upper(doc.closedAs()), doc.Reason)
	}
	for i := range r.decisions {
		d := &r.decisions[i]
		if !d.Open() {
			continue
		}
		if _, err := r.e.Records.VoidDecision(r.mutation("void", id, d.ID), r.task.Repository, r.task.ID, d.ID, reason); err != nil {
			return err
		}
		d.Void, d.VoidReason = true, reason
	}
	next := fmt.Sprintf("slice %d of %d starts from main on branch %s. The task stays open.", k+1, r.slice.total, SliceBranch(r.task.ID, k+1))
	if k >= r.slice.total {
		next = "it was the last, so the task is done."
	}
	// The text names the record's kind: "already delivered by <by>" or "no change needed".
	how := "as " + doc.closedAs()
	if doc.noChange() {
		how = "as needing no change"
	}
	if doc.Decision != "" {
		how += " in decision " + doc.Decision
	}
	text := fmt.Sprintf("%s %s %s, with nothing merged for it: %s. %s", doc.closedBy(), r.slice.of(k), how, strings.TrimRight(doc.Reason, ". "), upper(next))
	if !r.slice.multi() {
		text = fmt.Sprintf("%s the task %s, with nothing merged for it: %s. The task is done.", doc.closedBy(), how, strings.TrimRight(doc.Reason, ". "))
	}
	if err := r.notify("slice-delivered."+id, store.MessageSliceDelivered, text); err != nil {
		return err
	}
	if err := r.retireDelivered(id, repo, k); err != nil {
		return err
	}
	if err := r.leave("delivered:"+id, false); err != nil {
		return err
	}
	if mq, ok := r.e.Records.(MergeQueue); ok {
		q, err := mq.MergeQueue(r.task.Repository)
		if err != nil {
			return err
		}
		if q.Holds[r.task.ID] {
			if _, err := mq.SetMergeHold(r.mutation("merge-hold", id), r.task.Repository, r.task.ID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// upper is s with its first letter upper case.
func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// retireDelivered removes delivered slice k's branch and worktree, as a merged slice's are retired, at the base its
// latest code-head attempt started from: nothing merged, so only a branch with no commits past that base, a build
// that committed nothing, is removed, and any other is kept with a workspace-kept message. A slice none of whose
// builds launched has no base recorded and no worktree, and nothing is retired.
func (r *run) retireDelivered(id string, repo config.Repository, k int) error {
	if r.e.Workspaces == nil {
		return nil
	}
	base := ""
	for i, p := range r.docPlaces {
		if !p.ok || p.part != "base" || p.exit || p.index != 0 {
			continue
		}
		if step, j := workflow.ParseSliceStep(p.step); j != k || !r.base.InSlices(step) {
			continue
		}
		data, err := r.read(r.documents[i])
		if err != nil {
			return err
		}
		base = strings.TrimSpace(string(data))
	}
	w := git.Workspace{Repository: repo.Path, Remote: repo.Remote, Branch: SliceBranch(r.task.ID, k), Dir: r.slicePath(k)}
	if base == "" {
		if _, err := os.Lstat(w.Dir); err != nil {
			return nil
		}
		b, err := r.e.Workspaces.Base(r.ctx, repo.Path, w.Branch)
		if err != nil {
			return r.keptDelivered(id, w, "", []string{"its base could not be read: " + err.Error()})
		}
		base = b
	}
	kept, err := r.e.Workspaces.Retire(r.ctx, git.Retirement{Workspace: w, Head: base, Outputs: r.outputs()})
	if err != nil {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		kept = []string{"it could not be removed: " + err.Error()}
	}
	return r.keptDelivered(id, w, base, kept)
}

// keptDelivered tells the Director, once, that delivered slice workspace w was kept, at base, for the reasons kept;
// no reasons sends nothing.
func (r *run) keptDelivered(id string, w git.Workspace, base string, kept []string) error {
	if len(kept) == 0 {
		return nil
	}
	at := ""
	if base != "" {
		at = " at its base " + base
	}
	text := fmt.Sprintf("Kept the working directory %s and branch %s after closing the slice as already delivered (%s)%s, because %s.\nNothing was removed; the Director decides what happens to them.",
		w.Dir, w.Branch, id, at, strings.Join(kept, "; "))
	return r.notify(fmt.Sprintf("workspace-kept.%s.%s", id, digest.Of([]byte(text)).Hex()[:12]), store.MessageWorkspaceKept, text)
}
