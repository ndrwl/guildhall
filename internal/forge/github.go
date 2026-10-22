package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// GitHub is the forge on github.com, reached by running git to push and the gh program for pull requests,
// each with an argument list and never through a shell. gh uses its own sign-in.
type GitHub struct {
	// Git and GH are the git and gh executables; empty means each found on PATH.
	Git, GH string
}

// Publish pushes c.Head to refs/heads/<c.Branch> on c.Remote, replacing what that task branch held, and returns
// the branch's open pull request, opening one against the remote's default branch when none is open.
//
// It refuses, pushing nothing, a change that fails Validate, a remote GitHub does not host
// (ErrUnsupportedRemote), a branch that is the remote's default branch (ErrInvalid) and a head that is not a
// commit of c.Repository (ErrUnknownHead). Repeating a change returns the same pull request; a new head on the
// same branch, even one that rewrites it, updates the open pull request. Concurrent calls for one branch are
// not safe.
func (g GitHub) Publish(ctx context.Context, c Change) (PullRequest, error) {
	if err := c.Validate(); err != nil {
		return PullRequest{}, err
	}
	repo, err := GitHubRepository(c.Remote)
	if err != nil {
		return PullRequest{}, err
	}
	var view struct {
		DefaultBranchRef struct{ Name string } `json:"defaultBranchRef"`
	}
	if err := g.ghJSON(ctx, c.Repository, &view, "repo", "view", "--repo", repo, "--json", "defaultBranchRef"); err != nil {
		return PullRequest{}, err
	}
	if view.DefaultBranchRef.Name == "" || view.DefaultBranchRef.Name == c.Branch {
		return PullRequest{}, fmt.Errorf("%w: %s is the default branch of %s", ErrInvalid, c.Branch, repo)
	}
	if _, err := g.git(ctx, c.Repository, "cat-file", "-e", c.Head+"^{commit}"); err != nil {
		var run *runError
		if !errors.As(err, &run) {
			return PullRequest{}, err
		}
		return PullRequest{}, fmt.Errorf("%w: %s in %s", ErrUnknownHead, c.Head, c.Repository)
	}
	// The leading + replaces the task branch whatever it held; the refspec names only that branch.
	if _, err := g.git(ctx, c.Repository, "push", "--quiet", "--porcelain", c.Remote, "+"+c.Head+":refs/heads/"+c.Branch); err != nil {
		return PullRequest{}, err
	}
	pr, ok, err := g.open(ctx, c.Repository, repo, c.Branch)
	if err != nil {
		return PullRequest{}, err
	}
	if !ok {
		out, err := g.gh(ctx, c.Repository, "pr", "create", "--repo", repo, "--head", c.Branch, "--title", c.Title, "--body", c.Body)
		if err != nil {
			return PullRequest{}, err
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return PullRequest{}, errors.New("gh pr create printed no pull request URL")
		}
		u := fields[len(fields)-1]
		n, err := strconv.Atoi(u[strings.LastIndex(u, "/")+1:])
		if err != nil || n < 1 {
			return PullRequest{}, fmt.Errorf("gh pr create printed %q, not a pull request URL", u)
		}
		pr = PullRequest{Number: n, URL: u, Branch: c.Branch, State: Open}
	}
	// The forge may report the pull request's head a moment after the push: it is the head just pushed.
	pr.Head = c.Head
	return pr, nil
}

// Merge squash-merges pull request m.Number only while it is at m.Head, and returns it merged with its merge
// commit. It never passes --admin or --auto, so the forge's protection, required checks and review rules apply.
// A pull request already merged at m.Head is returned as it is, so repeating a merge is safe.
// A forge refusal, a closed pull request, one merged at another head or one not merged after the forge
// answered is ErrRefused with the reason; an error reaching the forge is returned as it is.
func (g GitHub) Merge(ctx context.Context, m Merge) (PullRequest, error) {
	if err := m.Validate(); err != nil {
		return PullRequest{}, err
	}
	repo, err := GitHubRepository(m.Remote)
	if err != nil {
		return PullRequest{}, err
	}
	pr, err := g.view(ctx, m.Repository, repo, m.Number)
	if err != nil {
		return PullRequest{}, err
	}
	switch {
	case pr.State == Merged && pr.Head == m.Head:
		return pr, nil
	case pr.State != Open:
		return PullRequest{}, fmt.Errorf("%w: pull request %s is %s at %s", ErrRefused, pr.URL, pr.State, pr.Head)
	}
	if _, err := g.gh(ctx, m.Repository, "pr", "merge", strconv.Itoa(m.Number), "--repo", repo, "--squash", "--match-head-commit", m.Head); err != nil {
		if ctx.Err() != nil {
			return PullRequest{}, ctx.Err()
		}
		var run *runError
		if errors.As(err, &run) {
			return PullRequest{}, fmt.Errorf("%w: %s", ErrRefused, run.output)
		}
		return PullRequest{}, err
	}
	pr, err = g.view(ctx, m.Repository, repo, m.Number)
	if err != nil {
		return PullRequest{}, err
	}
	if pr.State != Merged || pr.Head != m.Head || !digest.ValidHead(pr.MergeCommit) {
		return PullRequest{}, fmt.Errorf("%w: pull request %s is %s at %s after the merge", ErrRefused, pr.URL, pr.State, pr.Head)
	}
	return pr, nil
}

// GitHubRepository returns the github.com/<owner>/<repo> a remote names, from its https://, ssh:// or
// scp-like git@ form, with or without .git; any other remote is ErrUnsupportedRemote.
func GitHubRepository(remote string) (string, error) {
	var host, p string
	if rest, ok := strings.CutPrefix(remote, "git@"); ok && !strings.Contains(remote, "://") {
		host, p, _ = strings.Cut(rest, ":")
	} else if u, err := url.Parse(remote); err == nil && (u.Scheme == "https" || u.Scheme == "ssh") {
		host, p = u.Host, strings.TrimPrefix(u.Path, "/")
		if u.Scheme == "ssh" && (u.User == nil || u.User.Username() != "git") {
			host = ""
		}
	}
	parts := strings.Split(strings.TrimSuffix(p, ".git"), "/")
	if host != "github.com" || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("%w: %q is not a github.com repository; only GitHub is supported", ErrUnsupportedRemote, remote)
	}
	return host + "/" + parts[0] + "/" + parts[1], nil
}

// ghPullRequest is the part of gh's pull request JSON the forge reads.
type ghPullRequest struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	MergeCommit *struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
}

const ghFields = "number,url,state,headRefName,headRefOid,mergeCommit"

func (p ghPullRequest) pullRequest() PullRequest {
	pr := PullRequest{Number: p.Number, URL: p.URL, Branch: p.HeadRefName, Head: p.HeadRefOid, State: State(strings.ToLower(p.State))}
	if p.MergeCommit != nil && pr.State == Merged {
		pr.MergeCommit = p.MergeCommit.Oid
	}
	return pr
}

// open returns the branch's open pull request, if it has one.
func (g GitHub) open(ctx context.Context, dir, repo, branch string) (PullRequest, bool, error) {
	var list []ghPullRequest
	if err := g.ghJSON(ctx, dir, &list, "pr", "list", "--repo", repo, "--head", branch, "--state", "open", "--json", ghFields); err != nil {
		return PullRequest{}, false, err
	}
	for _, p := range list {
		if p.HeadRefName == branch {
			return p.pullRequest(), true, nil
		}
	}
	return PullRequest{}, false, nil
}

func (g GitHub) view(ctx context.Context, dir, repo string, number int) (PullRequest, error) {
	var p ghPullRequest
	if err := g.ghJSON(ctx, dir, &p, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", ghFields); err != nil {
		return PullRequest{}, err
	}
	return p.pullRequest(), nil
}

func (g GitHub) ghJSON(ctx context.Context, dir string, v any, args ...string) error {
	out, err := g.gh(ctx, dir, args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, v); err != nil {
		return fmt.Errorf("gh %s: %w", strings.Join(args[:2], " "), err)
	}
	return nil
}

func (g GitHub) gh(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return run(ctx, dir, orDefault(g.GH, "gh"), []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}, args...)
}

func (g GitHub) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return run(ctx, dir, orDefault(g.Git, "git"), []string{"GIT_TERMINAL_PROMPT=0"}, append([]string{"-C", dir}, args...)...)
}

// runError is a program that ran and exited unsuccessfully, with what it printed.
type runError struct {
	program string
	args    []string
	output  string
	err     error
}

func (e *runError) Error() string {
	return fmt.Sprintf("%s %s: %v: %s", e.program, strings.Join(e.args, " "), e.err, e.output)
}

func (e *runError) Unwrap() error { return e.err }

func run(ctx context.Context, dir, program string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, &runError{program: program, args: args, output: strings.TrimSpace(stderr.String()), err: err}
		}
		return nil, fmt.Errorf("%s: %w", program, err)
	}
	return stdout.Bytes(), nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
