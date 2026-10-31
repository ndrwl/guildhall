package cli

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

const ciLogHelp = `Usage: guildhall ci log [--copy <dir>] [--tail <lines>] [--out <file>] <repo>/<task> <job|check>
  --copy <dir>      Director copy (or the copy found from the current directory)
  --tail <lines>    print only the log's last lines
  --out <file>      write the log to file and print its path, size and dropped bytes
  --help            show this help
Serves the full log of a failed GitHub Actions job the task's validate results record, or a hallmark task's
baseline input lists, by job id or by check name (that check's latest failed job), from the CI log cache or
fetched with Guildhall's own gh.
Exit 0: the log was served; 1: the log is unavailable, with the reason; 2: a usage error.
Examples:
  guildhall ci log guildhall/d45 123456789
  guildhall ci log --tail 200 guildhall/d45 "test (ubuntu)"
  guildhall ci log --out /tmp/ci.log guildhall/d45 123456789
`

// ciCmd is guildhall ci; its only subcommand is log (docs/running.md#ci-logs).
func (a *App) ciCmd(args []string) int {
	if len(args) == 0 || args[0] != "log" {
		return a.usageError("ci: use guildhall ci log --help")
	}
	fl := flag.NewFlagSet("ci log", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	tail := fl.Int("tail", 0, "last lines")
	out := fl.String("out", "", "file")
	if err := fl.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(a.Stdout, ciLogHelp); err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		return a.usageError(err.Error() + "\n" + ciLogHelp)
	}
	if fl.NArg() != 2 || *tail < 0 {
		return a.usageError("ci log: give <repo>/<task> and a job id or check name, and a --tail of at least 0\n" + ciLogHelp)
	}
	repo, task, ok := strings.Cut(fl.Arg(0), "/")
	if !ok || repo == "" || task == "" || strings.Contains(task, "/") {
		return a.usageError(fmt.Sprintf("ci log: %q is not <repo>/<task>\n%s", fl.Arg(0), ciLogHelp))
	}
	var storePath, cfgPath, state string
	if err := cp.apply(a, &storePath, &cfgPath, &state); err != nil {
		return a.usageError(err.Error())
	}
	if cp.dir == "" {
		return a.usageError("ci log: " + cp.missing(a).Error() + "\n" + ciLogHelp)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("config: %w", err))
	}
	limit, err := cfg.CILogLimit()
	if err != nil {
		return a.fail(fmt.Errorf("config: %w", err))
	}
	r, err := cfg.ListedRepository(repo)
	if err != nil {
		return a.fail(fmt.Errorf("config %s: %w", cfgPath, err))
	}
	wait, err := cfg.LockWait()
	if err != nil {
		return a.fail(fmt.Errorf("config: %w", err))
	}
	// The store is read, never written, and no task lock is taken: serving a log changes nothing of the task.
	jobs, err := withRecords(storeRecords{path: storePath, wait: wait}, true, nil, func(s *store.Store) ([]ciJob, error) {
		return recordedJobs(s, filepath.Join(state, "documents"), repo, task)
	})
	if err != nil {
		return a.fail(err)
	}
	job, ok := matchJob(jobs, fl.Arg(1))
	if !ok {
		var b strings.Builder
		fmt.Fprintf(&b, "ci log: %s/%s records no failed GitHub Actions job %q; its failed checks are:", repo, task, fl.Arg(1))
		if len(jobs) == 0 {
			b.WriteString(" none")
		}
		for _, j := range jobs {
			fmt.Fprintf(&b, "\n  %s\t%s\t(%s)", cmp.Or(j.Job, "-"), j.Check, j.Result)
		}
		_, _ = fmt.Fprintln(a.Stderr, "guildhall: "+b.String())
		return 2
	}
	var reader cilog.Reader = forge.GitHub{}
	if lr, ok := a.Forge.(interp.CheckLogReader); ok {
		reader = lr
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	l, err := cilog.Get(ctx, reader, cilog.Dir(state, repo, task), forge.Commit{Repository: r.Path, Remote: job.Remote, Head: job.Head}, job.Job, limit)
	switch {
	case errors.Is(err, forge.ErrLogExpired):
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: GitHub no longer keeps the log of job %s (expired); only the excerpt in %s remains\n", job.Job, job.Path)
		return 1
	case errors.Is(err, forge.ErrLogUnavailable):
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: the log of job %s is unavailable: %s\n", job.Job, forge.LogReason(err))
		return 1
	case err != nil:
		return a.fail(err)
	}
	text := l.Text
	if *tail > 0 {
		text = tailLines(text, *tail)
	}
	// The dropped-bytes line heads only output that starts where the kept log starts; a --tail within it has none.
	if l.Dropped > 0 && len(text) == len(l.Text) {
		text = append(fmt.Appendf(nil, "[guildhall: %d bytes dropped from the head of this log]\n", l.Dropped), text...)
	}
	if *out == "" {
		if _, err := a.Stdout.Write(text); err != nil {
			return 1
		}
		return 0
	}
	p, err := filepath.Abs(*out)
	if err != nil {
		return a.fail(err)
	}
	if err := atomicfile.Write(p, text); err != nil {
		return a.fail(err)
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s: %d bytes, %d bytes dropped from the head of job %s's log\n", p, len(text), l.Dropped, job.Job); err != nil {
		return 1
	}
	return 0
}

// ciJob is one failed check a task's validate result or hallmark baseline records, with the document it is in.
type ciJob struct {
	Check, Job string
	// Result names the result; Path is its file; round orders the results of a step.
	Result, Path string
	round        int
	Remote, Head string
}

// resultName matches a validate result's document name, <step>-<round>-result.
var resultName = regexp.MustCompile(`^(.+)-([0-9]+)-result$`)

// recordedJobs lists the failed checks of every failed validate result of the task, the latest round first, then
// the failed jobs the failed-jobs block of each of a hallmark task's baselines lists, its merges newest first; each
// document is read from under docs and checked against its recorded digest.
func recordedJobs(s *store.Store, docs, repo, task string) ([]ciJob, error) {
	if _, err := s.Task(repo, task); err != nil {
		return nil, err
	}
	refs, err := s.Documents(repo, task)
	if err != nil {
		return nil, err
	}
	read := func(d store.DocumentRef) (string, []byte, error) {
		p := filepath.Join(docs, filepath.FromSlash(d.Path))
		data, err := os.ReadFile(p)
		if err != nil {
			return "", nil, err
		}
		if digest.Of(data).Hex() != d.SHA256 {
			return "", nil, fmt.Errorf("document %s at %s does not match its recorded digest", d.Name, p)
		}
		return p, data, nil
	}
	var out, baseline []ciJob
	for _, d := range refs {
		if interp.IsBaselineDocument(d) {
			p, data, err := read(d)
			if err != nil {
				return nil, err
			}
			jobs, err := interp.BaselineJobs(data)
			if err != nil {
				return nil, fmt.Errorf("document %s at %s: %w", d.Name, p, err)
			}
			for _, j := range jobs {
				baseline = append(baseline, ciJob{Check: j.Check, Job: j.Job, Result: d.Name, Path: p, Remote: j.Remote, Head: j.Head})
			}
			continue
		}
		m := resultName.FindStringSubmatch(d.Name)
		if m == nil || filepath.Base(d.Path) != "result.json" {
			continue
		}
		p, data, err := read(d)
		if err != nil {
			return nil, err
		}
		var v struct {
			Schema  string `json:"schema"`
			Verdict string `json:"verdict"`
			Remote  string `json:"remote"`
			Head    string `json:"head"`
			Logs    []struct {
				Check string `json:"check"`
				Job   string `json:"job"`
			} `json:"logs"`
		}
		if json.Unmarshal(data, &v) != nil || v.Schema != "guildhall.validation-result/1" || v.Verdict != "failed" {
			continue
		}
		round, _ := strconv.Atoi(m[2])
		for _, l := range v.Logs {
			out = append(out, ciJob{Check: l.Check, Job: l.Job, Result: d.Name, Path: p, round: round, Remote: v.Remote, Head: v.Head})
		}
	}
	slices.SortStableFunc(out, func(a, b ciJob) int { return cmp.Compare(b.round, a.round) })
	return append(out, baseline...), nil
}

// matchJob finds arg among jobs: a job id, or else a check name, meaning that check's latest failed Actions job.
func matchJob(jobs []ciJob, arg string) (ciJob, bool) {
	if i := slices.IndexFunc(jobs, func(j ciJob) bool { return j.Job != "" && j.Job == arg }); i >= 0 {
		return jobs[i], true
	}
	if i := slices.IndexFunc(jobs, func(j ciJob) bool { return j.Job != "" && j.Check == arg }); i >= 0 {
		return jobs[i], true
	}
	return ciJob{}, false
}

// tailLines is the last n lines of text.
func tailLines(text []byte, n int) []byte {
	end := len(text)
	if end > 0 && text[end-1] == '\n' {
		end--
	}
	for i := 0; i < n; i++ {
		j := bytes.LastIndexByte(text[:end], '\n')
		if j < 0 {
			return text
		}
		end = j
	}
	return text[end+1:]
}
