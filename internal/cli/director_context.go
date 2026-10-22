package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// maxStatusLineBytes bounds the status-line input context-report reads.
const maxStatusLineBytes = 1 << 20

// directorContextReport runs `guildhall director context-report [--copy <dir>]`, the Claude Code status-line
// command of a Director session (docs/director.md#automatic-handoff): it reads the used context percent from the
// JSON on standard input, writes it to the context report of the session GUILDHALL_SESSION names in the copy, and
// prints "context <n>%" as the status line. Input with no percent yet writes and prints nothing. It opens no store,
// since Claude Code runs it after every assistant message.
func (a *App) directorContextReport(args []string) int {
	const name = "director context-report"
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	err := fl.Parse(args)
	if err == nil {
		err = cp.apply(a.Getenv, nil, nil, nil)
	}
	switch {
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case fl.NArg() > 0:
		return a.fail(fmt.Errorf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return a.fail(fmt.Errorf("%s: --copy is required, or GUILDHALL_COPY", name))
	}
	var n int
	if a.Getenv != nil {
		n, err = strconv.Atoi(a.Getenv(session.NumberEnv))
	}
	if err != nil || n < 1 {
		return a.fail(fmt.Errorf("%s: %s must name the session number; the session supervisor sets it", name, session.NumberEnv))
	}
	copyDir, err := filepath.Abs(cp.dir)
	if err != nil {
		return a.fail(err)
	}
	b, err := io.ReadAll(io.LimitReader(a.Stdin, maxStatusLineBytes))
	if err != nil {
		return a.fail(fmt.Errorf("%s: read the status line input: %w", name, err))
	}
	percent, ok, err := claude.ParseStatusLine(b)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if !ok {
		return 0
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	if err := session.WriteContext(sessionDir(copyDir, n), session.Context{UsedPercent: percent, ReportedAt: now().UTC()}); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	_, _ = fmt.Fprintf(a.Stdout, "context %d%%\n", percent)
	return 0
}

// sessionDir is the directory of Director session n in the copy.
func sessionDir(copyDir string, n int) string {
	return filepath.Join(copyDir, "director", fmt.Sprintf("session-%d", n))
}

// handoffPolicy is the automatic handoff policy of the session in directory dir under cfg for runtime rt: the
// config's task and context thresholds and, when rt reports context, the session's context report.
func handoffPolicy(cfg *config.Config, rt runtimes.Session, dir string) (store.HandoffPolicy, error) {
	h, err := cfg.Handoff()
	if err != nil {
		return store.HandoffPolicy{}, err
	}
	p := store.HandoffPolicy{AfterTasks: h.AfterTasks, ContextPercent: h.ContextPercent}
	if rt.ContextReport() {
		p.ContextFile = filepath.Join(dir, session.ContextFile)
	}
	return p, nil
}
