package forgetest

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
)

// Fake is an in-memory forge for consumers' tests: it keeps commits per repository, branches and pull requests
// per remote, and passes RunForgeContract. It never touches git or the network. It is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	commits  map[string]map[string]bool
	branches map[string]map[string]string
	bases    map[string]string
	prs      map[string][]forge.PullRequest
	// texts holds each pull request's title and body, by remote and in the order of prs.
	texts map[string][]text
	// pushed holds the heads ever pushed to each remote, and checks what each remote reports for a head.
	pushed       map[string]map[string]bool
	checks       map[string]map[string][]forge.Check
	refuse       string
	mergeability forge.Mergeability
	// publishes and merges count calls, and edits the publishes that changed an open pull request's text.
	publishes, merges, edits int
}

// text is a pull request's title and body.
type text struct{ title, body string }

// NewFake returns a forge with no repositories.
func NewFake() *Fake {
	return &Fake{commits: map[string]map[string]bool{}, branches: map[string]map[string]string{}, bases: map[string]string{}, prs: map[string][]forge.PullRequest{},
		texts: map[string][]text{}, pushed: map[string]map[string]bool{}, checks: map[string]map[string][]forge.Check{}}
}

// AddCommit makes head a commit of repository.
func (f *Fake) AddCommit(repository, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commits[repository] == nil {
		f.commits[repository] = map[string]bool{}
	}
	f.commits[repository][head] = true
}

// SetBranch points a branch of remote at head; the first branch set on a remote is its default branch.
func (f *Fake) SetBranch(remote, name, head string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setBranch(remote, name, head)
}

func (f *Fake) setBranch(remote, name, head string) {
	if f.branches[remote] == nil {
		f.branches[remote] = map[string]string{}
		f.bases[remote] = name
	}
	f.branches[remote][name] = head
	if f.pushed[remote] == nil {
		f.pushed[remote] = map[string]bool{}
	}
	f.pushed[remote][head] = true
}

// Branch returns the commit a branch of remote is at, or "".
func (f *Fake) Branch(remote, name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.branches[remote][name]
}

// PullRequests returns remote's pull requests in the order they were opened.
func (f *Fake) PullRequests(remote string) []forge.PullRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forge.PullRequest(nil), f.prs[remote]...)
}

// Refuse makes every later merge fail with ErrRefused and reason; an empty reason stops refusing.
func (f *Fake) Refuse(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = reason
}

// Calls returns how many times Publish and Merge were called.
func (f *Fake) Calls() (publishes, merges int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publishes, f.merges
}

// Text returns the title and body of pull request number of remote, or empty strings when it has none.
func (f *Fake) Text(remote string, number int) (title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if number < 1 || number > len(f.texts[remote]) {
		return "", ""
	}
	t := f.texts[remote][number-1]
	return t.title, t.body
}

// Edits returns how many publishes changed the title or body of an open pull request.
func (f *Fake) Edits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.edits
}

// Publish implements forge.GitHub.Publish's contract.
func (f *Fake) Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishes++
	if err := c.Validate(); err != nil {
		return forge.PullRequest{}, err
	}
	if base, ok := f.bases[c.Remote]; ok && base == c.Branch {
		return forge.PullRequest{}, fmt.Errorf("%w: %s is the default branch of %s", forge.ErrInvalid, c.Branch, c.Remote)
	}
	if !f.commits[c.Repository][c.Head] {
		return forge.PullRequest{}, fmt.Errorf("%w: %s in %s", forge.ErrUnknownHead, c.Head, c.Repository)
	}
	f.setBranch(c.Remote, c.Branch, c.Head)
	prs, texts := f.prs[c.Remote], f.texts[c.Remote]
	for i := range prs {
		if prs[i].Branch == c.Branch && prs[i].State == forge.Open {
			prs[i].Head = c.Head
			if t := (text{c.Title, c.Body}); texts[i] != t {
				texts[i] = t
				f.edits++
			}
			return prs[i], nil
		}
	}
	n := len(prs) + 1
	pr := forge.PullRequest{Number: n, URL: fmt.Sprintf("https://forge.invalid/%d", n), Branch: c.Branch, Head: c.Head, State: forge.Open}
	f.prs[c.Remote] = append(prs, pr)
	f.texts[c.Remote] = append(texts, text{c.Title, c.Body})
	return pr, nil
}

// SetChecks makes checks the checks remote reports for head, replacing any it reported.
func (f *Fake) SetChecks(remote, head string, checks ...forge.Check) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checks[remote] == nil {
		f.checks[remote] = map[string][]forge.Check{}
	}
	f.checks[remote][head] = slices.Clone(checks)
}

// Checks implements forge.GitHub.Checks's contract: a head is held by the remote once a branch was set to it.
func (f *Fake) Checks(ctx context.Context, c forge.Commit) ([]forge.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if !f.pushed[c.Remote][c.Head] {
		return nil, fmt.Errorf("%w: %s is not on %s", forge.ErrUnknownHead, c.Head, c.Remote)
	}
	out := append([]forge.Check{}, f.checks[c.Remote][c.Head]...)
	slices.SortStableFunc(out, forge.CompareChecks)
	return out, nil
}

// Merge implements forge.GitHub.Merge's contract.
func (f *Fake) Merge(ctx context.Context, m forge.Merge) (forge.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merges++
	if err := m.Validate(); err != nil {
		return forge.PullRequest{}, err
	}
	prs := f.prs[m.Remote]
	if m.Number > len(prs) {
		return forge.PullRequest{}, fmt.Errorf("fake forge: no pull request %d on %s", m.Number, m.Remote)
	}
	pr := &prs[m.Number-1]
	switch {
	case pr.State == forge.Merged && pr.Head == m.Head:
		return *pr, nil
	case pr.State != forge.Open:
		return forge.PullRequest{}, fmt.Errorf("%w: pull request %d is %s at %s", forge.ErrRefused, pr.Number, pr.State, pr.Head)
	case f.refuse != "":
		return forge.PullRequest{}, fmt.Errorf("%w: %s", forge.ErrRefused, f.refuse)
	case pr.Head != m.Head:
		return forge.PullRequest{}, fmt.Errorf("%w: pull request %d is at %s, not %s", forge.ErrRefused, pr.Number, pr.Head, m.Head)
	}
	sum := sha1.Sum([]byte("merge " + pr.Head))
	pr.State, pr.MergeCommit = forge.Merged, hex.EncodeToString(sum[:])
	f.setBranch(m.Remote, f.bases[m.Remote], pr.MergeCommit)
	return *pr, nil
}

// NewFakeHarness returns a fresh Fake under the contract suite.
func NewFakeHarness(t *testing.T) Harness {
	f := NewFake()
	const remote = "https://github.com/acme/widgets.git"
	h := Harness{Forge: f, Repository: "/repo", Remote: remote, Base: "main",
		BaseHead: repeat('a'), Heads: [3]string{repeat('b'), repeat('c'), repeat('d')},
		Branch:    func(_ *testing.T, name string) string { return f.Branch(remote, name) },
		Refuse:    func(_ *testing.T, reason string) { f.Refuse(reason) },
		SetChecks: func(_ *testing.T, head string, checks []forge.Check) { f.SetChecks(remote, head, checks...) },
		Text:      func(_ *testing.T, number int) (string, string) { return f.Text(remote, number) },
		Edits:     func(_ *testing.T) int { return f.Edits() },
	}
	f.SetBranch(h.Remote, h.Base, h.BaseHead)
	for _, c := range append([]string{h.BaseHead}, h.Heads[:]...) {
		f.AddCommit(h.Repository, c)
	}
	return h
}

func repeat(c byte) string {
	b := make([]byte, 40)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
