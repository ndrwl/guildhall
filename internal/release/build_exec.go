package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Default bounds on each git and go run of a build: a fetch of a whole repository and a build that downloads its
// modules take far longer than the git package's default.
const (
	DefaultBuildGitTimeout = 5 * time.Minute
	DefaultBuildGoTimeout  = 10 * time.Minute
)

// ExecBuilder is the Builder that runs git and go, with a fixed build recipe: GOFLAGS cleared, GOWORK off,
// CGO_ENABLED=0, GOOS and GOARCH of the running binary, -buildvcs=true, -trimpath and -mod=readonly, so the build
// stamps the version and revision of the commit it checked out.
type ExecBuilder struct {
	// Git and Go are the programs run; "" means git and go on PATH.
	Git, Go string
	// GitTimeout and GoTimeout bound each run; zero means DefaultBuildGitTimeout and DefaultBuildGoTimeout.
	GitTimeout, GoTimeout time.Duration
}

// Fetch runs git init at repo and fetches every branch and tag of source into it, with no prompt.
func (b ExecBuilder) Fetch(ctx context.Context, repo, source string) error {
	if err := ValidSource(source); err != nil {
		return err
	}
	if _, err := b.git(ctx, "", "init", "--quiet", repo); err != nil {
		return err
	}
	_, err := b.git(ctx, repo, "fetch", "--quiet", "--no-write-fetch-head", "--no-recurse-submodules", source,
		"+refs/heads/*:refs/remotes/source/*", "+refs/tags/*:refs/tags/*")
	return err
}

// Resolve runs git rev-parse --verify of rev^{commit}; a revision git cannot resolve to one commit is
// ErrNotInSource.
func (b ExecBuilder) Resolve(ctx context.Context, repo, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("revision %q is not a revision", rev)
	}
	out, err := b.git(ctx, repo, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return "", ErrNotInSource
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Checkout checks out commit detached, discarding nothing, as the fresh repository has no work tree yet.
func (b ExecBuilder) Checkout(ctx context.Context, repo, commit string) error {
	_, err := b.git(ctx, repo, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit, "--")
	return err
}

// GoVersion runs go env GOVERSION in dir, which names the toolchain the module's go.mod selects.
func (b ExecBuilder) GoVersion(ctx context.Context, dir string) (string, error) {
	out, err := b.goRun(ctx, dir, "env", "GOVERSION")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Build runs go build with the fixed recipe in dir.
func (b ExecBuilder) Build(ctx context.Context, dir, pkg, out string) error {
	_, err := b.goRun(ctx, dir, "build", "-buildvcs=true", "-trimpath", "-mod=readonly", "-o", out, pkg)
	return err
}

// goEnv is the environment go runs in: the process's without the variables that redirect git, as go build runs git
// to stamp the revision, and with the build's fixed settings in place of its own.
func goEnv() []string {
	fixed := map[string]string{"GOFLAGS": "", "GOWORK": "off", "CGO_ENABLED": "0", "GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH}
	env := withoutEnv(os.Environ(), func(k string) bool { _, ok := fixed[k]; return ok || gitRedirect(k) })
	for k, v := range fixed {
		env = append(env, k+"="+v)
	}
	return env
}

// gitEnv is the environment git runs in: the process's without GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE, so an
// inherited one cannot point a build at another repository, and never prompting.
func gitEnv() []string {
	return append(withoutEnv(os.Environ(), gitRedirect), "GIT_TERMINAL_PROMPT=0")
}

// gitRedirect reports whether the upper-cased variable k points git at another repository, work tree or index.
func gitRedirect(k string) bool {
	return k == "GIT_DIR" || k == "GIT_WORK_TREE" || k == "GIT_INDEX_FILE"
}

// withoutEnv is env without each variable whose name, upper-cased, drop reports.
func withoutEnv(env []string, drop func(k string) bool) []string {
	var kept []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !drop(strings.ToUpper(k)) {
			kept = append(kept, kv)
		}
	}
	return kept
}

// git runs git, in dir unless it is "", bounded by GitTimeout and never prompting.
func (b ExecBuilder) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	path := b.Git
	if path == "" {
		path = "git"
	}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	return run(ctx, b.GitTimeout, DefaultBuildGitTimeout, "", gitEnv(), path, args...)
}

// goRun runs go in dir with goEnv, bounded by GoTimeout.
func (b ExecBuilder) goRun(ctx context.Context, dir string, args ...string) ([]byte, error) {
	path := b.Go
	if path == "" {
		path = "go"
	}
	return run(ctx, b.GoTimeout, DefaultBuildGoTimeout, dir, goEnv(), path, args...)
}

// run runs path with args in dir, bounded by timeout, or def when it is zero, and returns its standard output; a
// failure names the command and its standard error, and wraps the *exec.ExitError.
func run(ctx context.Context, timeout, def time.Duration, dir string, env []string, path string, args ...string) ([]byte, error) {
	if timeout == 0 {
		timeout = def
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, path, args...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, 5*time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	name := path + " " + strings.Join(args, " ")
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: stopped: %w", name, ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%s: no answer within %v: %w", name, timeout, context.DeadlineExceeded)
	}
	return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
}
