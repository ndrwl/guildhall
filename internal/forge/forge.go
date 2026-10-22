// Package forge is Guildhall's boundary to the host of a repository's pull requests: pushing a task's head
// to its branch, opening or updating its pull request, reading the checks of a pushed head and merging it under
// the repository's merge policy (docs/contracts/forge.md).
//
// Guildhall prescribes only these edges. It pushes only a task branch, never the base; it merges only the
// head the Guildmaster's merge decision was bound to, with a squash merge; and it never bypasses the forge's
// branch protection, required checks or review rules. Anything the forge refuses is ErrRefused, for the
// Director to recover from. Credentials stay in the forge program's own sign-in store: nothing here reads,
// stores or passes one.
package forge

import (
	"cmp"
	"errors"
	"fmt"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// Change is one task head to publish.
type Change struct {
	// Repository is the absolute path of the repository on the owner machine; Head must be one of its commits.
	Repository string
	// Remote is the repository's configured remote: Branch is pushed there and its forge holds the pull request.
	Remote string
	// Branch is the task branch, such as guildhall/<task>; it must be a valid branch name and not the base.
	Branch string
	// Head is the full commit id to publish.
	Head string
	// Title and Body describe a new pull request; an open one keeps its own.
	Title, Body string
}

// Validate reports a change missing a field or naming an invalid branch or head, as ErrInvalid.
func (c Change) Validate() error {
	switch {
	case c.Repository == "" || c.Remote == "":
		return fmt.Errorf("%w: a change names its repository and remote", ErrInvalid)
	case !ValidBranch(c.Branch):
		return fmt.Errorf("%w: branch %q is not a valid branch name", ErrInvalid, c.Branch)
	case !digest.ValidHead(c.Head):
		return fmt.Errorf("%w: head %q is not a full commit id", ErrInvalid, c.Head)
	case strings.TrimSpace(c.Title) == "":
		return fmt.Errorf("%w: a change has a title", ErrInvalid)
	}
	return nil
}

// Merge asks for one pull request to be merged at one head.
type Merge struct {
	// Repository and Remote are as in Change.
	Repository, Remote string
	// Number is the pull request's number on the forge.
	Number int
	// Head is the full commit id the merge was approved for; the forge merges only while the pull request is at it.
	Head string
}

// Validate reports a merge missing a field or naming an invalid head, as ErrInvalid.
func (m Merge) Validate() error {
	switch {
	case m.Repository == "" || m.Remote == "":
		return fmt.Errorf("%w: a merge names its repository and remote", ErrInvalid)
	case m.Number < 1:
		return fmt.Errorf("%w: pull request number %d", ErrInvalid, m.Number)
	case !digest.ValidHead(m.Head):
		return fmt.Errorf("%w: head %q is not a full commit id", ErrInvalid, m.Head)
	}
	return nil
}

// State is where a pull request stands.
type State string

const (
	Open   State = "open"
	Merged State = "merged"
	Closed State = "closed"
)

// PullRequest is a pull request as the forge reports it.
type PullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	// Head is the commit the pull request is at: the head just published, or the head it was merged at.
	Head  string `json:"head"`
	State State  `json:"state"`
	// MergeCommit is the commit the merge landed on the base; set only when State is Merged.
	MergeCommit string `json:"merge_commit,omitempty"`
}

var (
	// ErrInvalid reports a change or merge that is malformed, or that names the base branch as the task branch.
	ErrInvalid = errors.New("invalid forge request")
	// ErrUnknownHead reports a head that is not a commit of the repository, so nothing is pushed, or, reading
	// checks, a head the remote does not hold.
	ErrUnknownHead = errors.New("head is not a commit of the repository")
	// ErrRefused reports a merge the forge refused, such as failing or pending required checks, a branch
	// behind its base, branch protection, a pull request no longer at the approved head, or a closed one.
	// The error text carries the forge's reason. Nothing was merged.
	ErrRefused = errors.New("the forge refused the merge")
	// ErrUnsupportedRemote reports a remote that is not on a forge this implementation supports.
	ErrUnsupportedRemote = errors.New("remote is not on a supported forge")
)

// ValidBranch reports whether name is a branch name git accepts as refs/heads/<name> (git check-ref-format),
// without the single-component and "@" forms some commands treat specially.
func ValidBranch(name string) bool {
	if name == "" || name == "@" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.Contains(name, "@{") {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(" ~^:?*[\\", c) {
			return false
		}
	}
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Commit names one pushed head whose checks are read.
type Commit struct {
	// Repository and Remote are as in Change.
	Repository, Remote string
	// Head is the full commit id whose checks are read.
	Head string
}

// Validate reports a commit missing a field or naming an invalid head, as ErrInvalid.
func (c Commit) Validate() error {
	switch {
	case c.Repository == "" || c.Remote == "":
		return fmt.Errorf("%w: a commit names its repository and remote", ErrInvalid)
	case !digest.ValidHead(c.Head):
		return fmt.Errorf("%w: head %q is not a full commit id", ErrInvalid, c.Head)
	}
	return nil
}

// CheckState is where one check of a head stands.
type CheckState string

const (
	// CheckPending: the check is queued or running, or a status is expected but not yet reported.
	CheckPending CheckState = "pending"
	// CheckPassed: the check succeeded, or finished neutral or skipped.
	CheckPassed CheckState = "passed"
	// CheckFailed: the check finished any other way, such as failed, timed out, cancelled or needing action.
	CheckFailed CheckState = "failed"
)

// Check is one check or commit status the forge reports for a head.
type Check struct {
	Name  string     `json:"name"`
	State CheckState `json:"state"`
	// URL links to the check's details, when the forge gives one.
	URL string `json:"url,omitempty"`
}

// CompareChecks orders checks by name and then URL, the order Checks returns them in.
func CompareChecks(a, b Check) int {
	return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.URL, b.URL))
}
