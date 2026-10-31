package interp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/store"
)

// RecordedMerge is a task's merge record, the <id>-merge document deliver or the outside-merge rule wrote: the
// remote the pull request merged on and the forge's answer then.
type RecordedMerge struct {
	// Document is the record's name, such as deliver-2-merge or outside-3-merge.
	Document string
	Remote   string
	forge.PullRequest
}

// IsMergeRecord reports whether name is a merge record's document name, <id>-merge. It is the one definition.
func IsMergeRecord(name string) bool { return strings.HasSuffix(name, "-merge") }

// MergeRecord returns the task's newest merge record among docs, read with read, or nil when it has none.
//
// Newest is the last merge record in docs' order, which for store.Store.Documents is name order (the byte order of
// the store's keys), not time: deliver-10-merge sorts before deliver-2-merge and every outside-* after every
// deliver-*. A one-slice task normally has at most one merge record, as deliver closes the task and the outside-merge
// rule stops once one is recorded, so the order only settles a defensive case. A multi-PR task has one per slice, and
// the newest is its highest slice's (DocumentSlice), the one that closed it. Only the newest record is read; one that
// cannot be read, does not decode, is not a pull-request document or names no valid head is an error naming it,
// never skipped.
func MergeRecord(docs []store.DocumentRef, read func(store.DocumentRef) ([]byte, error)) (*RecordedMerge, error) {
	var last *store.DocumentRef
	for i := range docs {
		// A multi-PR task's later slice is newer, whatever the names' order.
		if IsMergeRecord(docs[i].Name) && (last == nil || DocumentSlice(docs[i].Name) >= DocumentSlice(last.Name)) {
			last = &docs[i]
		}
	}
	if last == nil {
		return nil, nil
	}
	data, err := read(*last)
	if err != nil {
		return nil, fmt.Errorf("merge record %s: %w", last.Name, err)
	}
	var pr pullRequestDoc
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("merge record %s: %w", last.Name, err)
	}
	if pr.Schema != pullRequestSchema {
		return nil, fmt.Errorf("merge record %s is not a %s document", last.Name, pullRequestSchema)
	}
	if !digest.ValidHead(pr.Head) {
		return nil, fmt.Errorf("merge record %s names no valid head", last.Name)
	}
	return &RecordedMerge{Document: last.Name, Remote: pr.Remote, PullRequest: pr.PullRequest}, nil
}

// SliceHeads returns the head each slice of a task merged at, by slice (DocumentSlice), from its merge records: the
// heads a multi-PR task's slice worktrees are safe at. A one-slice task has at most slice 1. A merge record that cannot
// be read or names no valid head is an error, as for MergeRecord.
func SliceHeads(docs []store.DocumentRef, read func(store.DocumentRef) ([]byte, error)) (map[int]string, error) {
	out := map[int]string{}
	for k := range docs {
		if !IsMergeRecord(docs[k].Name) {
			continue
		}
		m, err := MergeRecord(docs[k:k+1], read)
		if err != nil {
			return nil, err
		}
		out[DocumentSlice(docs[k].Name)] = m.Head
	}
	return out, nil
}

// SafeHead returns the head the task's work is safe at: its newest merge record's head (MergeRecord), else the head
// its finished document (FinishedDocument) names, else "". MergeRecord's errors pass through; a finished document
// that cannot be read, does not decode, has another schema or names an invalid head is an error. An empty head in
// the finished document is valid: it records a task that is not report-only, and gives "".
func SafeHead(docs []store.DocumentRef, read func(store.DocumentRef) ([]byte, error)) (string, error) {
	m, err := MergeRecord(docs, read)
	if err != nil {
		return "", err
	}
	if m != nil {
		return m.Head, nil
	}
	for _, d := range docs {
		if d.Name != FinishedDocument {
			continue
		}
		data, err := read(d)
		if err != nil {
			return "", fmt.Errorf("document %s: %w", d.Name, err)
		}
		var f finishedDoc
		if err := json.Unmarshal(data, &f); err != nil {
			return "", fmt.Errorf("document %s: %w", d.Name, err)
		}
		if f.Schema != finishedSchema {
			return "", fmt.Errorf("document %s is not a %s document", d.Name, finishedSchema)
		}
		if f.Head != "" && !digest.ValidHead(f.Head) {
			return "", fmt.Errorf("document %s names an invalid head", d.Name)
		}
		return f.Head, nil
	}
	return "", nil
}

// ParseResultName reads a result document's name, <step>-<round>-result, with round a positive number without
// leading zeros (artisan.ParseAttemptID).
func ParseResultName(name string) (step string, round int, ok bool) {
	id, ok := strings.CutSuffix(name, "-result")
	if !ok {
		return "", 0, false
	}
	return artisan.ParseAttemptID(id)
}
