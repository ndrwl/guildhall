package forge

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// GitHub is the forge on github.com, reached by running git to push and the gh program for pull requests,
// each with an argument list and never through a shell. gh uses its own sign-in.
type GitHub struct {
	// Git and GH are the git and gh executables; empty means each found on PATH.
	Git, GH string
	// Timeout bounds each git or gh run; zero means DefaultTimeout. A run past it is ended and is a plain
	// error naming the program and the timeout, never ErrRefused.
	Timeout time.Duration
}

// DefaultTimeout is how long one git or gh run may take when GitHub.Timeout is zero.
const DefaultTimeout = 5 * time.Minute

// Publish pushes c.Head to refs/heads/<c.Branch> on c.Remote, replacing what that task branch held, and returns
// the branch's open pull request, opening one against the remote's default branch when none is open. An open
// pull request whose title or body differs from c's is edited to read c's, overwriting any edit made on GitHub;
// one that already reads c's is not edited.
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
	if err := g.ghJSON(ctx, c.Repository, &view, "repo", "view", repo, "--json", "defaultBranchRef"); err != nil {
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
	pr, text, ok, err := g.open(ctx, c.Repository, repo, c.Branch)
	if err != nil {
		return PullRequest{}, err
	}
	if ok && (text.Title != c.Title || text.Body != c.Body) {
		if _, err := g.gh(ctx, c.Repository, "pr", "edit", strconv.Itoa(pr.Number), "--repo", repo, "--title", c.Title, "--body", c.Body); err != nil {
			return PullRequest{}, err
		}
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
// The merge commit's subject is m.Subject(), the published title plus " (#N)", and its body is passed explicitly
// empty, so GitHub's default squash message is never used; a blank m.Title is ErrInvalid before gh runs.
// A pull request already merged at m.Head is returned as it is, so repeating a merge is safe.
// A merge gh fails is checked by viewing the pull request again: merged at m.Head, it is returned; still open,
// closed or at another head, the failure is the forge's refusal, ErrRefused with gh's reason. A closed pull
// request, one merged at another head or one not merged after the forge answered is ErrRefused too. A failure
// that leaves the forge's answer unknown, such as gh's failed sign-in or a view that fails after the merge,
// is returned as a plain error.
func (g GitHub) Merge(ctx context.Context, m Merge) (PullRequest, error) {
	if err := m.ValidateTitled(); err != nil {
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
	var run *runError
	_, mergeErr := g.gh(ctx, m.Repository, "pr", "merge", strconv.Itoa(m.Number), "--repo", repo, "--squash", "--match-head-commit", m.Head,
		"--subject", m.Subject(), "--body", "")
	if mergeErr != nil {
		if ctx.Err() != nil {
			return PullRequest{}, ctx.Err()
		}
		var exit *exec.ExitError
		if !errors.As(mergeErr, &run) || errors.As(mergeErr, &exit) && exit.ExitCode() == ghAuthRequired {
			return PullRequest{}, mergeErr
		}
	}
	pr, err = g.view(ctx, m.Repository, repo, m.Number)
	switch {
	case err != nil:
		return PullRequest{}, errors.Join(mergeErr, err)
	case pr.State == Merged && pr.Head == m.Head && digest.ValidHead(pr.MergeCommit):
		return pr, nil
	case mergeErr != nil:
		return PullRequest{}, fmt.Errorf("%w: %s", ErrRefused, run.output)
	}
	return PullRequest{}, fmt.Errorf("%w: pull request %s is %s at %s after the merge", ErrRefused, pr.URL, pr.State, pr.Head)
}

// PullRequest returns pull request v.Number as the forge reports it now: open, closed, or merged at whatever head
// with its merge commit. It changes nothing, so a task's pull request merged outside Guildhall can be recognized
// without Merge's strict head check.
func (g GitHub) PullRequest(ctx context.Context, v View) (PullRequest, error) {
	if err := v.Validate(); err != nil {
		return PullRequest{}, err
	}
	repo, err := GitHubRepository(v.Remote)
	if err != nil {
		return PullRequest{}, err
	}
	return g.view(ctx, v.Repository, repo, v.Number)
}

// Close closes pull request c.Number without merging it, posting c.Comment, and returns it closed. It never passes
// --delete-branch, so the task branch stays on the remote. A pull request already closed is returned as it is, so
// repeating a close is safe; a merged one is ErrRefused, and nothing is closed or commented. A close gh fails is
// checked by viewing the pull request again, as Merge does: closed, it is returned; otherwise the failure is returned.
func (g GitHub) Close(ctx context.Context, c Close) (PullRequest, error) {
	if err := c.Validate(); err != nil {
		return PullRequest{}, err
	}
	repo, err := GitHubRepository(c.Remote)
	if err != nil {
		return PullRequest{}, err
	}
	pr, err := g.view(ctx, c.Repository, repo, c.Number)
	if err != nil {
		return PullRequest{}, err
	}
	switch pr.State {
	case Closed:
		return pr, nil
	case Merged:
		return PullRequest{}, fmt.Errorf("%w: pull request %s is merged", ErrRefused, pr.URL)
	}
	_, closeErr := g.gh(ctx, c.Repository, "pr", "close", strconv.Itoa(c.Number), "--repo", repo, "--comment", c.Comment)
	if closeErr != nil && ctx.Err() != nil {
		return PullRequest{}, ctx.Err()
	}
	pr, err = g.view(ctx, c.Repository, repo, c.Number)
	switch {
	case err != nil:
		return PullRequest{}, errors.Join(closeErr, err)
	case pr.State == Closed:
		return pr, nil
	case closeErr != nil:
		return PullRequest{}, closeErr
	}
	return PullRequest{}, fmt.Errorf("pull request %s is %s after the close", pr.URL, pr.State)
}

// checksQuery reads a commit's status check rollup: its check runs and commit statuses, at most one page.
const checksQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) {
      __typename
      ... on Commit {
        statusCheckRollup {
          contexts(first: 100) {
            pageInfo { hasNextPage }
            nodes {
              __typename
              ... on CheckRun { id databaseId name status conclusion detailsUrl checkSuite { app { slug } } }
              ... on StatusContext { id context state targetUrl }
            }
          }
        }
      }
    }
  }
}`

// ghContext is one check run or commit status of a rollup.
type ghContext struct {
	Typename   string `json:"__typename"`
	ID         string `json:"id"`
	DatabaseID int64  `json:"databaseId"`
	CheckSuite *struct {
		App *struct {
			Slug string `json:"slug"`
		} `json:"app"`
	} `json:"checkSuite"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	DetailsURL *string `json:"detailsUrl"`
	Context    string  `json:"context"`
	State      string  `json:"state"`
	TargetURL  *string `json:"targetUrl"`
}

// check maps a context to a check; a state GitHub has added since is an error rather than a guess.
func (c ghContext) check() (Check, error) {
	switch c.Typename {
	case "CheckRun":
		out := Check{Name: c.Name, URL: deref(c.DetailsURL), ID: c.ID}
		if c.DatabaseID > 0 && c.CheckSuite != nil && c.CheckSuite.App != nil && c.CheckSuite.App.Slug == actionsApp {
			out.Job = strconv.FormatInt(c.DatabaseID, 10)
		}
		switch c.Status {
		case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
			out.State = CheckPending
			return out, nil
		case "COMPLETED":
		default:
			return Check{}, fmt.Errorf("check run %q has unknown status %q", c.Name, c.Status)
		}
		switch deref(c.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			out.State = CheckPassed
		case "FAILURE", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED", "STALE", "STARTUP_FAILURE":
			out.State = CheckFailed
		default:
			return Check{}, fmt.Errorf("check run %q has unknown conclusion %q", c.Name, deref(c.Conclusion))
		}
		return out, nil
	case "StatusContext":
		out := Check{Name: c.Context, URL: deref(c.TargetURL), ID: c.ID}
		switch c.State {
		case "EXPECTED", "PENDING":
			out.State = CheckPending
		case "SUCCESS":
			out.State = CheckPassed
		case "FAILURE", "ERROR":
			out.State = CheckFailed
		default:
			return Check{}, fmt.Errorf("commit status %q has unknown state %q", c.Context, c.State)
		}
		return out, nil
	}
	return Check{}, fmt.Errorf("unknown check kind %q", c.Typename)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Checks returns every check run and commit status github.com reports for c.Head, with its node id, sorted by
// name, URL and then id, read in one GraphQL call bound to the commit, so a pull request that moved since cannot
// answer for it.
// A head the remote does not hold is ErrUnknownHead; no checks is an empty list. More than one page of checks,
// or a state this implementation does not know, is an error rather than a partial or guessed answer.
func (g GitHub) Checks(ctx context.Context, c Commit) ([]Check, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	repo, err := GitHubRepository(c.Remote)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(repo, "/")
	var resp struct {
		Data struct {
			Repository *struct {
				Object *struct {
					Typename          string `json:"__typename"`
					StatusCheckRollup *struct {
						Contexts struct {
							PageInfo struct {
								HasNextPage bool `json:"hasNextPage"`
							} `json:"pageInfo"`
							Nodes []ghContext `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := g.ghJSON(ctx, c.Repository, &resp, "api", "graphql", "--hostname", parts[0],
		"-f", "query="+checksQuery, "-f", "owner="+parts[1], "-f", "name="+parts[2], "-f", "oid="+c.Head); err != nil {
		return nil, err
	}
	r := resp.Data.Repository
	switch {
	case r == nil:
		return nil, fmt.Errorf("gh api graphql: no repository %s", repo)
	case r.Object == nil || r.Object.Typename != "Commit":
		return nil, fmt.Errorf("%w: %s is not a commit of %s", ErrUnknownHead, c.Head, repo)
	case r.Object.StatusCheckRollup == nil:
		return []Check{}, nil
	case r.Object.StatusCheckRollup.Contexts.PageInfo.HasNextPage:
		return nil, fmt.Errorf("%s reports more than 100 checks for %s; reading more than one page is not supported", repo, c.Head)
	}
	out := []Check{}
	for _, n := range r.Object.StatusCheckRollup.Contexts.Nodes {
		check, err := n.check()
		if err != nil {
			return nil, fmt.Errorf("%s at %s: %w", repo, c.Head, err)
		}
		out = append(out, check)
	}
	slices.SortStableFunc(out, CompareChecks)
	return out, nil
}

// actionsApp is the slug of the GitHub app whose check runs are GitHub Actions jobs.
const actionsApp = "github-actions"

// LogTimeout bounds the gh run that reads one job log; a shorter GitHub.Timeout bounds it instead.
const LogTimeout = 2 * time.Minute

// expiredLog matches gh's report of an HTTP 404 or 410 answer, which for a job's log means GitHub no longer keeps it.
var expiredLog = regexp.MustCompile(`\(HTTP 4(04|10)\)`)

// CheckLog reads the log of GitHub Actions job job of c's repository with gh api, following GitHub's redirect to
// the plain-text log, and keeps its last limit bytes from a line boundary as it streams. The log is kept as
// GitHub serves it: a UTF-8 byte order mark first, CRLF line ends and ANSI escape sequences included.
// An empty job, a limit of zero or less, and any gh failure, including a timeout and gh not signed in, are
// ErrLogUnavailable with the reason; an HTTP 404 or 410 is also ErrLogExpired. A cancelled ctx is returned as it is.
func (g GitHub) CheckLog(ctx context.Context, c Commit, job string, limit int64) (CheckLog, error) {
	switch {
	case job == "":
		return CheckLog{}, &LogUnavailableError{Reason: "not a GitHub Actions job"}
	case limit <= 0:
		return CheckLog{}, &LogUnavailableError{Reason: "log fetching is off"}
	case strings.Trim(job, "0123456789") != "":
		return CheckLog{}, fmt.Errorf("%w: job %q is not a GitHub Actions job id", ErrInvalid, job)
	}
	if err := c.Validate(); err != nil {
		return CheckLog{}, err
	}
	repo, err := GitHubRepository(c.Remote)
	if err != nil {
		return CheckLog{}, err
	}
	parts := strings.Split(repo, "/")
	timeout := LogTimeout
	if g.Timeout > 0 && g.Timeout < timeout {
		timeout = g.Timeout
	}
	w := &TailWriter{Limit: limit}
	_, err = runTo(ctx, timeout, c.Repository, orDefault(g.GH, "gh"), ghEnv, w,
		"api", "repos/"+parts[1]+"/"+parts[2]+"/actions/jobs/"+job+"/logs", "--hostname", parts[0],
		// A job log carries the ANSI colour codes its steps printed, and gh refuses to print those without this flag.
		"--allow-escape-sequences")
	switch {
	case err == nil:
		return w.Log(), nil
	case ctx.Err() != nil:
		return CheckLog{}, ctx.Err()
	}
	reason := err.Error()
	if run, ok := errors.AsType[*runError](err); ok {
		reason = cmp.Or(run.output, run.err.Error())
	}
	return CheckLog{}, &LogUnavailableError{Reason: reason, Expired: expiredLog.MatchString(reason)}
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

// ghText is a pull request's title and body as gh reports them.
type ghText struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// open returns the branch's open pull request with its title and body, if it has one.
func (g GitHub) open(ctx context.Context, dir, repo, branch string) (PullRequest, ghText, bool, error) {
	var list []struct {
		ghPullRequest
		ghText
	}
	if err := g.ghJSON(ctx, dir, &list, "pr", "list", "--repo", repo, "--head", branch, "--state", "open", "--json", ghFields+",title,body"); err != nil {
		return PullRequest{}, ghText{}, false, err
	}
	for _, p := range list {
		if p.HeadRefName == branch {
			return p.pullRequest(), p.ghText, true, nil
		}
	}
	return PullRequest{}, ghText{}, false, nil
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
	return run(ctx, g.timeout(), dir, orDefault(g.GH, "gh"), ghEnv, args...)
}

// ghEnv keeps gh from prompting or checking for updates.
var ghEnv = []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}

func (g GitHub) timeout() time.Duration {
	if g.Timeout == 0 {
		return DefaultTimeout
	}
	return g.Timeout
}

func (g GitHub) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return run(ctx, g.timeout(), dir, orDefault(g.Git, "git"), []string{"GIT_TERMINAL_PROMPT=0"}, append([]string{"-C", dir}, args...)...)
}

// ghAuthRequired is the exit code gh exits with when it is not signed in or its sign-in failed.
const ghAuthRequired = 4

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

// runWaitDelay is how long a run ended at its timeout waits for its output pipes before they are closed.
const runWaitDelay = 5 * time.Second

func run(ctx context.Context, timeout time.Duration, dir, program string, env []string, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	if failed, err := runTo(ctx, timeout, dir, program, env, &stdout, args...); err != nil {
		if !failed {
			return nil, err
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// runTo runs program like run, streaming its stdout to stdout; failed reports a run that exited unsuccessfully,
// whose error is a runError.
func runTo(ctx context.Context, timeout time.Duration, dir, program string, env []string, stdout io.Writer, args ...string) (failed bool, _ error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, program, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = runWaitDelay
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Run(); err != nil {
		// A run past its timeout gave no answer: it is not the forge's refusal, so it is not a runError.
		if ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return false, fmt.Errorf("%s %s: no answer within %v: %w", program, strings.Join(args, " "), timeout, context.DeadlineExceeded)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return true, &runError{program: program, args: args, output: strings.TrimSpace(stderr.String()), err: err}
		}
		return false, fmt.Errorf("%s: %w", program, err)
	}
	return false, nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
