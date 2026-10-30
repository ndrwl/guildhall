package interp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// RecordedPullRequest is a pull request a task's publish recorded: the remote its branch was pushed to and the
// forge's answer then, with Head the head its last -pr document recorded.
type RecordedPullRequest struct {
	// Document is the name of the first <step>-<round>-pr document that recorded it.
	Document string
	Remote   string
	forge.PullRequest
}

// TaskPullRequests reads what `guildhall task cancel` and the outside-merge rule need of a task's pull requests from
// its documents in documentDir: each pull request its -pr documents record, once per remote and number in document
// order, and whether a -merge document is recorded, by deliver or by the outside-merge rule, which means the task
// merged or is merging. A document that does not
// match its recorded digest or is not a pull request is an error.
func TaskPullRequests(docs []store.DocumentRef, documentDir string) (prs []RecordedPullRequest, merging bool, err error) {
	seen := map[string]int{}
	for _, d := range docs {
		switch {
		case strings.HasSuffix(d.Name, "-merge"):
			merging = true
		case strings.HasSuffix(d.Name, "-pr"):
			data, err := os.ReadFile(filepath.Join(documentDir, filepath.FromSlash(d.Path)))
			if err != nil {
				return nil, false, err
			}
			if digest.Of(data).Hex() != d.SHA256 {
				return nil, false, fmt.Errorf("document %s does not match its recorded digest", d.Path)
			}
			var pr pullRequestDoc
			if err := json.Unmarshal(data, &pr); err != nil {
				return nil, false, fmt.Errorf("document %s: %w", d.Name, err)
			}
			if pr.Schema != pullRequestSchema || pr.Number < 1 || pr.Remote == "" {
				return nil, false, fmt.Errorf("document %s is not a %s pull request", d.Name, pullRequestSchema)
			}
			key := fmt.Sprintf("%s#%d", pr.Remote, pr.Number)
			if i, ok := seen[key]; ok {
				prs[i].Head = pr.Head
				continue
			}
			seen[key] = len(prs)
			prs = append(prs, RecordedPullRequest{Document: d.Name, Remote: pr.Remote, PullRequest: pr.PullRequest})
		}
	}
	return prs, merging, nil
}

// maxQuotedReasonBytes bounds the cancel reason a dependency-cancelled message quotes.
const maxQuotedReasonBytes = 1 << 10

// DependencyCancelledMessage is the dependency-cancelled Director message telling it that dependent's dependency
// cancelled was cancelled with reason, quoted up to 1 KiB. Its id is derived from both tasks, so a repeated cancel
// resends nothing.
func DependencyCancelledMessage(dependent, cancelled store.TaskRef, reason string) (messaging.Envelope, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > maxQuotedReasonBytes {
		reason = strings.ToValidUTF8(reason[:maxQuotedReasonBytes], "") + "..."
	}
	text := fmt.Sprintf("Task %s/%s depends on task %s/%s, which was cancelled (%s), so it can never merge. "+
		"Task %s/%s stays open: start it anyway with guildhall task start --ignore-deps --reason <reason> --repo %s %s, "+
		"or cancel it too with guildhall task cancel --reason <reason> --repo %s %s.",
		dependent.Repository, dependent.ID, cancelled.Repository, cancelled.ID, reason,
		dependent.Repository, dependent.ID, dependent.Repository, dependent.ID, dependent.Repository, dependent.ID)
	return directorMessage(dependent.Repository, dependent.ID, store.MessageDependencyCancelled+"."+cancelled.Repository+"."+cancelled.ID,
		store.MessageDependencyCancelled, text)
}
