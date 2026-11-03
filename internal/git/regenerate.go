package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

// RegenerateTimeout bounds the whole of one Regenerate's commands together; a run past it is ended and reported as
// Failed.
const RegenerateTimeout = 10 * time.Minute

// regenerateOutputLimit is how much of the commands' combined output Regenerated keeps: the last 4 KiB.
const regenerateOutputLimit = 4 << 10

// Regeneration asks for a marked replay's generated files to be rewritten by the repository's declared commands
// (docs/contracts/git.md#regenerate).
type Regeneration struct {
	// Repository is the absolute path of the repository holding Marked and Onto.
	Repository string
	// Marked is the replay's commit with conflict markers written in, on Onto.
	Marked string
	// Onto is the base tip the replay is onto; each Take path is taken from it before the commands run.
	Onto string
	// Take lists the conflicting paths, relative with / separators.
	Take []string
	// Commands are argvs, run in order with no shell from the scratch worktree's root.
	Commands [][]string
	// Dir is the absolute path of the scratch worktree; it must not be the task's worktree, and is removed again.
	Dir string
}

// Regenerated is what a Regeneration made. With Failed empty, Commit is a commit on Marked of the regenerated tree
// and Changed the sorted paths it changes from Marked; with Failed set, Commit is empty. Output holds the last 4 KiB
// of the commands' combined output.
type Regenerated struct {
	Commit  string   `json:"commit,omitempty"`
	Changed []string `json:"changed"`
	Output  []byte   `json:"output,omitempty"`
	// Failed names the command that failed and how, such as its exit status.
	Failed string `json:"failed,omitempty"`
}

// Regenerate rewrites a marked replay's generated files in a scratch, detached worktree at g.Dir, never the task's
// worktree, and commits the result on g.Marked. It takes the base side of each g.Take path from g.Onto, runs each
// command in order from the worktree root, with the environment inherited minus GIT_DIR, GIT_WORK_TREE and
// GIT_INDEX_FILE and all of them bounded together by RegenerateTimeout, then stages everything and commits it with
// Marked's message and author. It moves no branch, and it removes the scratch worktree on every path, a leftover
// from an earlier run included. A command that fails or runs past the timeout is not an error: Regenerated.Failed
// says which and how. An error means git itself failed, the request was invalid or ctx ended.
func (p Program) Regenerate(ctx context.Context, g Regeneration) (out Regenerated, err error) {
	if !digest.ValidHead(g.Marked) || !digest.ValidHead(g.Onto) {
		return Regenerated{}, fmt.Errorf("git: regeneration of %q onto %q: not full object ids", g.Marked, g.Onto)
	}
	if !filepath.IsAbs(g.Repository) || !filepath.IsAbs(g.Dir) {
		return Regenerated{}, fmt.Errorf("git: regeneration in %q of %q: paths must be absolute", g.Dir, g.Repository)
	}
	if len(g.Commands) == 0 {
		return Regenerated{}, errors.New("git: regeneration with no commands")
	}
	for _, c := range g.Commands {
		if len(c) == 0 || c[0] == "" {
			return Regenerated{}, errors.New("git: regeneration with an empty command")
		}
	}
	if err := ctx.Err(); err != nil {
		return Regenerated{}, err
	}
	p.removeScratch(ctx, g.Repository, g.Dir)
	defer p.removeScratch(context.WithoutCancel(ctx), g.Repository, g.Dir)
	if err := os.MkdirAll(filepath.Dir(g.Dir), 0o755); err != nil {
		return Regenerated{}, err
	}
	if _, err := p.git(ctx, g.Repository, "worktree", "add", "--detach", "--quiet", g.Dir, g.Marked); err != nil {
		return Regenerated{}, err
	}
	if len(g.Take) > 0 {
		args := append([]string{"--literal-pathspecs", "checkout", g.Onto, "--"}, g.Take...)
		if _, err := p.git(ctx, g.Dir, args...); err != nil {
			return Regenerated{}, err
		}
	}
	out.Output, out.Failed, err = runCommands(ctx, g.Dir, g.Commands)
	if err != nil || out.Failed != "" {
		return out, err
	}
	if _, err := p.git(ctx, g.Dir, "add", "--all"); err != nil {
		return Regenerated{}, err
	}
	tree, err := p.git(ctx, g.Dir, "write-tree")
	if err != nil {
		return Regenerated{}, err
	}
	commit, err := p.commitTree(ctx, g.Repository, strings.TrimSpace(string(tree)), g.Marked, g.Marked)
	if err != nil {
		return Regenerated{}, err
	}
	changed, err := p.files(ctx, g.Repository, g.Marked, commit)
	if err != nil {
		return Regenerated{}, err
	}
	out.Commit, out.Changed = commit, changed
	if out.Changed == nil {
		out.Changed = []string{}
	}
	return out, nil
}

// removeScratch removes the scratch worktree at dir and git's record of it, whether or not it exists.
func (p Program) removeScratch(ctx context.Context, repository, dir string) {
	_, _ = p.git(ctx, repository, "worktree", "remove", "--force", dir)
	_ = os.RemoveAll(dir)
	_, _ = p.git(ctx, repository, "worktree", "prune")
}

// runCommands runs each command in dir in order, under one RegenerateTimeout, and returns the last 4 KiB of their
// combined output and, for the first that fails, which and how. Only ctx ending is an error.
func runCommands(ctx context.Context, dir string, commands [][]string) (output []byte, failed string, err error) {
	runCtx, cancel := context.WithTimeout(ctx, RegenerateTimeout)
	defer cancel()
	env := regenerateEnv(os.Environ())
	var combined bytes.Buffer
	for _, argv := range commands {
		cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &combined, &combined
		cmd.WaitDelay = 5 * time.Second
		runErr := cmd.Run()
		switch {
		case runErr == nil:
			continue
		case ctx.Err() != nil:
			return nil, "", ctx.Err()
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			failed = fmt.Sprintf("%s: no answer within %v", strings.Join(argv, " "), RegenerateTimeout)
		default:
			failed = fmt.Sprintf("%s: %v", strings.Join(argv, " "), runErr)
		}
		break
	}
	output = combined.Bytes()
	if len(output) > regenerateOutputLimit {
		output = output[len(output)-regenerateOutputLimit:]
	}
	return bytes.Clone(output), failed, nil
}

// regenerateEnv is env without GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE, which would point a command's git at
// another repository than the scratch worktree.
func regenerateEnv(env []string) []string {
	var kept []string
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(key, "GIT_DIR") || strings.EqualFold(key, "GIT_WORK_TREE") || strings.EqualFold(key, "GIT_INDEX_FILE") {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}
