package launch

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/workbench"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// Bridge is what a --pane-launch bridge presents: the owned launcher it starts and the attempt it shows.
type Bridge struct {
	// Argv is a fully validated artisan start invocation without --pane-launch; Env is its environment.
	Argv, Env []string
	// Attempt is the attempt Argv starts: its launcher.log, event file and result file are shown.
	Attempt Attempt
	// CloseAfter is the close delay FinishLine states.
	CloseAfter time.Duration
	// Output is workbench.OutputWorkbench, the default when empty, or workbench.OutputPlain.
	Output string
	// Title, Asked and URL are the status bar's task title, its one line of what the Artisan was asked, and the
	// task's page, empty with no [web] table; Slices is the accepted spec's slice count, zero when not known.
	// The CLI resolves them, so launch reads no config or store.
	Title, Asked, URL string
	Slices            int
	// Start is when the attempt started, which the bar's and banner's elapsed time counts from, so a bridge
	// replaying the files later still shows the attempt's time; zero is when the bridge started.
	Start time.Time
}

// bar is the status bar of b's attempt.
func (b Bridge) bar() workbench.Bar {
	k := b.Attempt.Key
	step, slice := workflow.ParseSliceStep(k.Step)
	return workbench.Bar{Repository: k.Repository, Task: k.TaskID, Title: b.Title, Step: step, Slice: slice, Slices: b.Slices,
		Attempt: k.Attempt, Asked: b.Asked, URL: b.URL, Start: b.Start}
}

// closing is what the banner shows beside b's result at now: the elapsed time, the usage file's cost and tokens, or
// failing a token count there the latest the wrapper reported, and the task's page.
func (b Bridge) closing(now time.Time, tokens workbench.Tokens) workbench.Closing {
	c := workbench.Closing{URL: b.URL, Tokens: tokens.Total()}
	if !b.Start.IsZero() {
		c.Elapsed = now.Sub(b.Start)
	}
	if u, err := adapter.ReadUsage(b.Attempt.Files().ResultFile); err == nil {
		if u.Cost != adapter.Unknown {
			cost := u.CostUSD
			c.CostUSD = &cost
		}
		if n := u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens; u.Tokens != adapter.Unknown && n > 0 {
			c.Tokens = n
		}
	}
	return c
}

// pollInterval is how often the bridge reads what the launcher has appended.
const pollInterval = 100 * time.Millisecond

// Present starts b.Argv as a detached owned launcher, appending its output to
// LogFile in the attempt directory, and shows that log on out until it exits. A
// lost output consumer never stops the launcher: an output error only ends the
// presentation. It is an internal CLI bridge, not a second attempt owner.
//
// In plain output the log is copied to out unchanged. Otherwise out shows the
// Workbench: launcher.log merged with the attempt's workbench.File, formatted,
// with a quiet line while nothing arrives (docs/artisan.md#workbench). When the
// launcher exits, after any advance output, out shows a closing banner from the
// attempt's result file and then FinishLine(b.CloseAfter), unchanged.
func Present(b Bridge, out io.Writer) error {
	logPath := filepath.Join(b.Attempt.Dir(), LogFile)
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	cmd, err := startDetached(func() *exec.Cmd {
		c := exec.Command(b.Argv[0], b.Argv[1:]...)
		c.Env = b.Env
		c.Stdout, c.Stderr = log, log
		return c
	})
	_ = log.Close()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if b.Start.IsZero() {
		b.Start = time.Now()
	}
	if b.Output == workbench.OutputPlain {
		return presentPlain(b, logPath, done, out)
	}
	return presentWorkbench(b, logPath, done, out, workbench.Detect(out, os.Getenv), time.Now)
}

// presentPlain copies launcher.log to out unchanged, as before the Workbench, then the banner as plain text and
// FinishLine.
func presentPlain(b Bridge, logPath string, done <-chan error, out io.Writer) error {
	reader, err := os.Open(filepath.Clean(logPath))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for {
		// Output errors are presentation errors only: the child remains owned.
		if _, err := io.Copy(out, reader); err != nil {
			return err
		}
		select {
		case err := <-done:
			_, _ = io.Copy(out, reader)
			workbench.NewRenderer(out, workbench.Options{Width: workbench.DefaultWidth}).Banner(bannerResult(b.Attempt), b.closing(time.Now(), workbench.Tokens{}))
			_, _ = fmt.Fprintln(out, "\n"+FinishLine(b.CloseAfter))
			return err
		case <-time.After(pollInterval):
		}
	}
}

// presentWorkbench shows the Workbench on out until done, with opts and the clock now.
func presentWorkbench(b Bridge, logPath string, done <-chan error, out io.Writer, opts workbench.Options, now func() time.Time) error {
	r := workbench.NewRenderer(out, opts)
	r.Bar(b.bar(), now())
	s := workbench.NewSession(r, logPath, b.Attempt.Dir(), now())
	defer s.Close()
	for {
		// Output errors are presentation errors only: the child remains owned.
		if err := s.Poll(now()); err != nil {
			return err
		}
		select {
		case err := <-done:
			_ = s.Finish(now())
			r.Banner(bannerResult(b.Attempt), b.closing(now(), r.Tokens()))
			r.Println("\n" + FinishLine(b.CloseAfter))
			r.Close()
			return err
		case <-time.After(pollInterval):
		}
	}
}

// bannerResult is what the closing banner shows of at's result file, nil when it is missing or malformed.
func bannerResult(at Attempt) *workbench.Result {
	f := at.Files()
	res, err := adapter.ReadResult(f.ResultFile, f.Identity)
	if err != nil {
		return nil
	}
	return &workbench.Result{Status: string(res.Status), Summary: res.Summary}
}
