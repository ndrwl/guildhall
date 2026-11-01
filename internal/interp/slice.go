package interp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// A multi-PR task delivers its accepted spec's `## Slices` one pull request at a time: its workflow's slice range,
// build through deliver in the default, runs once per slice, each on its own branch, and only the last slice's merge
// closes the task (docs/decisions/multi-pr-tasks.md). The current slice is worked out from the records alone: the
// slices with a merge record, plus one. Slice k >= 2 runs the range under the step ids workflow.SliceStep(id, k), so
// every lookup, staleness check, gate and mutation id of the engine sees only that slice's records, and slice 1 keeps
// today's names.

// SliceBranch is the branch slice k of task runs on and publish pushes: guildhall/<task> for slice 1, so a one-slice
// task's branch is unchanged, and guildhall/<task>.s<k> from slice 2 on, which no task's branch can be, since task ids
// have no dot. It is the one place a task branch's name is built.
func SliceBranch(task string, k int) string {
	if k <= 1 {
		return branchPrefix + task
	}
	return branchPrefix + task + ".s" + strconv.Itoa(k)
}

// SliceWorkDir is the name of slice k's working directory below the task's state directory: work for slice 1 and
// work.s<k> from slice 2 on, so each slice starts in a fresh worktree whatever the previous slice's retirement kept.
func SliceWorkDir(k int) string {
	if k <= 1 {
		return "work"
	}
	return "work.s" + strconv.Itoa(k)
}

// DocumentSlice is the slice a record name belongs to: k for a name carrying a slice step id, ...--s<k>-..., else 1.
func DocumentSlice(name string) int {
	for rest := name; ; {
		i := strings.Index(rest, "--s")
		if i < 0 {
			return 1
		}
		rest = rest[i+3:]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j > 0 && (j == len(rest) || rest[j] == '-') {
			if k, err := strconv.Atoi(rest[:j]); err == nil && k >= 2 && strconv.Itoa(k) == rest[:j] {
				return k
			}
		}
	}
}

// sliceState is where a task stands in its slices.
type sliceState struct {
	// current is the slice the range runs for, from 1; total the accepted spec's slice count, 1 without a list.
	current, total int
	// merged counts the slices, from the first, with a merge record.
	merged int
	// entries is the accepted spec's slice list, nil for a one-slice task.
	entries []spec.Slice
}

// multi reports whether the task is a multi-PR task.
func (s sliceState) multi() bool { return s.total > 1 }

// final reports whether the current slice is the task's last.
func (s sliceState) final() bool { return s.current >= s.total }

// title is slice k's title, or "".
func (s sliceState) title(k int) string {
	if k >= 1 && k <= len(s.entries) {
		return s.entries[k-1].Title
	}
	return ""
}

// of says "slice k of n" for a multi-PR task, with its title.
func (s sliceState) of(k int) string {
	text := fmt.Sprintf("slice %d of %d", k, s.total)
	if t := s.title(k); t != "" {
		text += fmt.Sprintf(" (%s)", t)
	}
	return text
}

// outsideStep is the step part of an outside merge's id in slice k: outside, then outside--s<k>.
func outsideStep(k int) string { return workflow.SliceStep(strings.TrimSuffix(outsidePrefix, "-"), k) }

// slicesOf works out def's slice state from h: the accepted spec's slice list, read from the output of the acceptance
// step before the range, and the slices merged in order. A definition with no slice range is one slice.
func slicesOf(def *workflow.Definition, h *history) (sliceState, error) {
	st := sliceState{current: 1, total: 1}
	if def.Slices == nil {
		return st, nil
	}
	from := slices.IndexFunc(def.Steps, func(s workflow.Step) bool { return s.ID == def.Slices.From })
	accept := ""
	for _, s := range def.Steps[:max(from, 0)] {
		if s.Type == "guildmaster-approval" {
			accept = s.ID
		}
	}
	if doc, _, err := h.output(accept, "spec"); accept != "" && err == nil {
		data, err := h.read(doc)
		if err != nil {
			return st, fmt.Errorf("accepted spec %s: %w", doc.Name, err)
		}
		if st.entries, err = spec.Slices(data); err != nil {
			return st, fmt.Errorf("accepted spec %s: %w", doc.Name, err)
		}
		st.total = max(1, len(st.entries))
	}
	for k := 1; h.sliceMerge(def.Slices.To, k) >= 0; k++ {
		st.merged = k
	}
	st.current = min(st.merged+1, st.total)
	return st, nil
}

// sliceMerge returns the index of slice k's merge record, by deliver step deliver or the outside-merge rule, or -1.
func (h *history) sliceMerge(deliver string, k int) int {
	for i, p := range h.docPlaces {
		if p.ok && p.part == "merge" && !p.exit && p.index == 0 && (p.step == workflow.SliceStep(deliver, k) || p.step == outsideStep(k)) {
			return i
		}
	}
	return -1
}

// loadSlices works out the task's slices from its records and, from slice 2 on, walks the bound definition's view
// for the current slice.
func (r *run) loadSlices() error {
	r.base = r.def
	st, err := slicesOf(r.base, r.history)
	if err != nil {
		return fmt.Errorf("interp: %s/%s slices: %w", r.task.Repository, r.task.ID, err)
	}
	r.slice = st
	r.def = workflow.SliceView(r.base, st.current)
	r.history.def = r.def
	return nil
}

// nextSlice moves the run on to the slice after the current one, whose merge was just recorded.
func (r *run) nextSlice() {
	r.slice.merged = r.slice.current
	r.slice.current++
	r.def = workflow.SliceView(r.base, r.slice.current)
	r.history.def = r.def
	r.sliceMerged = false
}

// sliceCount is a multi-PR task's slice count for a launch, 0 for a one-slice task.
func (r *run) sliceCount() int {
	if r.slice.multi() {
		return r.slice.total
	}
	return 0
}

// branch is the current slice's branch.
func (r *run) branch() string { return SliceBranch(r.task.ID, r.slice.current) }

// sliceTitle is a multi-PR task's pull request title for the current slice, "<slice title> (slice k of n)".
func (r *run) sliceTitle() string {
	return fmt.Sprintf("%s (slice %d of %d)", r.slice.title(r.slice.current), r.slice.current, r.slice.total)
}

// currentMerged reports whether the current slice has a merge record: for a workflow with no slice range, whether the
// task has any.
func (r *run) currentMerged() bool {
	if r.base.Slices == nil {
		return slices.ContainsFunc(r.documents, func(d store.DocumentRef) bool { return IsMergeRecord(d.Name) })
	}
	return r.sliceMerge(r.base.Slices.To, r.slice.current) >= 0
}

// checkSlices refuses a plan's spec whose slice list Guildhall would refuse to deliver: a malformed `## Slices`
// section, more than one slice under a revision with no slice range, or, once slices have merged, a revision that
// changes a merged slice's entry or leaves nothing after them (spec.CheckRevision). Each is a rejection of the result,
// so the plan attempt fails and no spec is recorded.
func (r *run) checkSlices(data []byte) error {
	entries, err := spec.Slices(data)
	if err != nil {
		return rejectOutput(err)
	}
	if len(entries) > 1 && r.base.Slices == nil {
		return rejectOutput(fmt.Errorf("%w: the spec lists %d slices, but the task's workflow revision %s %s declares no slice range, so it delivers one pull request",
			spec.ErrSlices, len(entries), r.task.Workflow.Name, r.task.Workflow.Digest))
	}
	if err := spec.CheckRevision(r.slice.entries, entries, r.slice.merged); err != nil {
		return rejectOutput(err)
	}
	return nil
}

// sliceStarted reports whether the current slice has recorded an attempt yet.
func (r *run) sliceStarted() bool {
	return slices.ContainsFunc(r.attempts, func(a store.StageAttempt) bool {
		step, k := workflow.ParseSliceStep(a.Step)
		return k == r.slice.current && r.base.InSlices(step)
	})
}

// settleSlice finishes the close of the slice merged last, before the next one starts: a crash after its merge was
// recorded leaves its message, retirement or merge-queue exit undone, and the advance that resumes at the next slice
// repeats them, each idempotent.
func (r *run) settleSlice() error {
	k := r.slice.merged
	if k < 1 || k >= r.slice.current || r.sliceStarted() {
		return nil
	}
	i := r.sliceMerge(r.base.Slices.To, k)
	var pr pullRequestDoc
	if _, err := r.readJSONDoc(r.documents[i].Name, &pr); err != nil {
		return err
	}
	p := r.docPlaces[i]
	id := DecisionID(p.step, p.round, false)
	repo, err := r.repository()
	if err != nil {
		return err
	}
	return r.closeSlice(k, id, repo, &pr, p.step == outsideStep(k))
}

// closeSlice closes non-final slice k after its merge record, merge id's, of pull request pr: it tells the Director,
// retires the slice's branch and worktree, takes the task out of the merge queue, so the next slice joins it afresh
// with its stays and hold reset, and voids every open decision, with no exceptions. The task stays open. Every write is keyed on id, so a repeat sends and records nothing new.
func (r *run) closeSlice(k int, id string, repo config.Repository, pr *pullRequestDoc, outside bool) error {
	key := id + ":" + digest.Of([]byte(pr.Remote)).Hex()[:12]
	// The slice's decisions still open, such as a merge decision its authorization superseded, would otherwise stay
	// open across the next slices and fail their nothing_open.
	reason := fmt.Sprintf("Slice %d merged: pull request %s", k, pr.URL)
	if outside {
		reason += " merged outside Guildhall"
	}
	for i := range r.decisions {
		d := &r.decisions[i]
		if !d.Open() {
			continue
		}
		if _, err := r.e.Records.VoidDecision(r.mutation("void", key, d.ID), r.task.Repository, r.task.ID, d.ID, reason); err != nil {
			return err
		}
		d.Void, d.VoidReason = true, reason
	}
	var text, what string
	if outside {
		text = fmt.Sprintf("Pull request %s was merged outside Guildhall at head %s, merge commit %s%s", pr.URL, pr.Head, pr.MergeCommit, r.sliceText(k))
		what = "merged.outside." + strconv.Itoa(pr.Number)
	} else {
		msg, err := r.mergedText(id, pr.Head, k)
		if err != nil {
			return err
		}
		text, what = msg, "merged."+id
	}
	if err := r.notify(what, store.MessageMerged, text); err != nil {
		return err
	}
	if err := r.retire(id, repo, pr.Head, k); err != nil {
		return err
	}
	if err := r.leave("merged:"+id, false); err != nil {
		return err
	}
	if mq, ok := r.e.Records.(MergeQueue); ok {
		q, err := mq.MergeQueue(r.task.Repository)
		if err != nil {
			return err
		}
		if q.Holds[r.task.ID] {
			if _, err := mq.SetMergeHold(r.mutation("merge-hold", key), r.task.Repository, r.task.ID, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// mergedText is the merged message of deliver's merge id at head h, slice k: what allowed it, which slice it was and
// what comes next, and the merge decision it left unneeded, if any.
func (r *run) mergedText(id, h string, k int) (string, error) {
	msg := fmt.Sprintf("Merged head %s (%s)%s", h, r.mergedBy(id), r.sliceText(k))
	var a authorizationDoc
	if _, err := r.readJSONDoc(id+"-authorized", &a); err != nil {
		return "", err
	}
	if a.Supersedes != "" {
		msg += fmt.Sprintf(" Merge decision %s, opened while the requirements did not all hold, is no longer needed.", a.Supersedes)
	}
	return msg, nil
}

// sliceText is the merged message's ending for the current slice: "; the task is done." for a one-slice task, as
// before, or which slice merged and what comes next.
func (r *run) sliceText(k int) string {
	switch {
	case !r.slice.multi():
		return "; the task is done."
	case k >= r.slice.total:
		return fmt.Sprintf("; %s merged, the last, so the task is done.", r.slice.of(k))
	}
	return fmt.Sprintf("; %s merged, and slice %d of %d starts from main on branch %s. The task stays open.", r.slice.of(k), k+1, r.slice.total, SliceBranch(r.task.ID, k+1))
}

// SliceProgress is where a task stands in its slices, for `director status` and Clerk.
type SliceProgress struct {
	// Current is the slice being delivered, from 1, and Total the accepted spec's slice count.
	Current, Total int
	Items          []SliceItem
	// Merging reports that a slice's merge is in progress: the current slice has a merge record, or the slice merged
	// last may not have finished closing, since the next one has recorded nothing yet; advancing the task finishes it.
	Merging bool
}

// Slice states.
const (
	SliceMerged  = "merged"
	SliceCurrent = "current"
	SlicePending = "pending"
)

// SliceItem is one slice of a multi-PR task.
type SliceItem struct {
	Index int
	Title string
	// State is SliceMerged, SliceCurrent or SlicePending.
	State string
	// PullRequest is the slice's merged pull request, or else the last one its records hold; nil before publish.
	PullRequest *forge.PullRequest
}

// TaskSlices reads a task's slice progress from its bound definition and its records, the documents read from
// documentDir with their digests checked. A one-slice task, which includes every task whose workflow has no slice
// range or whose spec is not yet accepted, returns Total 1 and no items.
func TaskSlices(def *workflow.Definition, repository, task string, docs []store.DocumentRef, decisions []store.Decision, documentDir string) (SliceProgress, error) {
	read := func(d store.DocumentRef) ([]byte, error) {
		data, err := os.ReadFile(filepath.Join(documentDir, filepath.FromSlash(d.Path)))
		if err != nil {
			return nil, err
		}
		if digest.Of(data).Hex() != d.SHA256 {
			return nil, fmt.Errorf("document %s does not match its recorded digest", d.Path)
		}
		return data, nil
	}
	h := newHistory(repository, task, def, read, nil, docs, decisions)
	st, err := slicesOf(def, h)
	if err != nil || !st.multi() {
		return SliceProgress{Current: 1, Total: 1}, err
	}
	out := SliceProgress{Current: st.current, Total: st.total}
	out.Merging = h.sliceMerge(def.Slices.To, st.current) >= 0 ||
		st.merged >= 1 && st.current == st.merged+1 && !slices.ContainsFunc(docs, func(d store.DocumentRef) bool { return DocumentSlice(d.Name) == st.current })
	for _, e := range st.entries {
		item := SliceItem{Index: e.Index, Title: e.Title, State: SlicePending}
		switch {
		case e.Index <= st.merged:
			item.State = SliceMerged
		case e.Index == st.current:
			item.State = SliceCurrent
		}
		if i := h.sliceMerge(def.Slices.To, e.Index); i >= 0 {
			var pr pullRequestDoc
			if data, err := read(docs[i]); err == nil && json.Unmarshal(data, &pr) == nil {
				item.PullRequest = &pr.PullRequest
			}
		} else {
			for _, d := range docs {
				if strings.HasSuffix(d.Name, "-pr") && DocumentSlice(d.Name) == e.Index {
					var pr pullRequestDoc
					if data, err := read(d); err == nil && json.Unmarshal(data, &pr) == nil {
						item.PullRequest = &pr.PullRequest
					}
				}
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
