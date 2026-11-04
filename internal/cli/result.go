package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const resultHelp = `Usage: guildhall result check|submit [--copy <dir>] <file>
  --copy <dir>  Director copy (or the copy found from the current directory)
  --help        show this help
Run inside an attempt, which GUILDHALL_ATTEMPT names, with the checks intake runs on the attempt's result, against
the attempt's own records and git state.
  check   prints ok, or rejected: <reason> with the reason intake would record, and changes nothing.
  submit  checks the same way; an accepted result becomes the attempt's draft result, model/result.json. A rejected
          one leaves the draft untouched and uses one of 2 resubmissions; the 3rd rejection becomes the draft, so
          intake fails the attempt with its reason. A rejection for unacknowledged steering messages uses none:
          acknowledge each with guildhall messages ack <id> and submit again.
Exit 0: ok; 1: rejected; 2: the file was not checked (no copy, no attempt, attempt not running, unreadable file).
Examples:
  guildhall result check model/draft.json
  guildhall result submit model/draft.json
`

// Resubmissions is how many rejected submissions `guildhall result submit` allows an attempt before the next
// rejected one fails it.
const Resubmissions = 2

// resultCmd is guildhall result check|submit (docs/contracts/cli.md).
func (a *App) resultCmd(args []string) int {
	if len(args) == 0 || args[0] != "check" && args[0] != "submit" {
		if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
			if _, err := io.WriteString(a.Stdout, resultHelp); err != nil {
				return 1
			}
			return 0
		}
		return a.usageError("result: use guildhall result check|submit <file>\n" + resultHelp)
	}
	sub := args[0]
	fl := flag.NewFlagSet("result "+sub, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if err := parseInterspersed(fl, args[1:]); errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(a.Stdout, resultHelp); err != nil {
			return 1
		}
		return 0
	} else if err != nil {
		return a.usageError("result " + sub + ": " + err.Error() + "\n" + resultHelp)
	}
	if fl.NArg() != 1 {
		return a.usageError("result " + sub + ": give one <file>\n" + resultHelp)
	}
	env := a.getenv(artisan.AttemptEnv)
	if env == "" {
		return a.notChecked(fmt.Errorf("result %s: %s is not set; run it inside a Guildhall attempt", sub, artisan.AttemptEnv))
	}
	key, err := artisan.ParseAttemptKey(env)
	if err != nil {
		return a.notChecked(fmt.Errorf("result %s: %s: %w", sub, artisan.AttemptEnv, err))
	}
	if cp.dir == "" {
		dir, err := attemptCopy(a.Getwd, key)
		if err != nil {
			return a.notChecked(err)
		}
		cp.dir = dir
	}
	var storePath, cfgPath, state string
	if err := cp.apply(a, &storePath, &cfgPath, &state); err != nil {
		return a.notChecked(err)
	}
	if cp.dir == "" {
		return a.notChecked(fmt.Errorf("result %s: %w", sub, cp.missing(a)))
	}
	file := fl.Arg(0)
	if !filepath.IsAbs(file) && a.Getwd != nil {
		wd, err := a.Getwd()
		if err != nil {
			return a.notChecked(err)
		}
		file = filepath.Join(wd, file)
	}
	data, err := readResultFile(file)
	if err != nil {
		return a.notChecked(fmt.Errorf("result %s: %w", sub, err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	attemptDir := key.Dir(state)
	if _, err := os.Stat(attemptDir); err != nil {
		return a.notChecked(fmt.Errorf("result %s: attempt %s: %w", sub, key, err))
	}
	var release func()
	if sub == "submit" {
		// Submissions are serialised, so two at once count, and write the draft, one after the other.
		if release, err = artisan.LockPath(ctx, filepath.Join(attemptDir, artisan.SubmitLockFile)); err != nil {
			return a.notChecked(fmt.Errorf("result submit: lock: %w", err))
		}
		defer release()
	}
	check := a.checkResult(ctx, storePath, cfgPath, state, key, data)
	var rejection *interp.Rejection
	if check != nil && !errors.As(check, &rejection) {
		return a.notChecked(fmt.Errorf("result %s: %w", sub, check))
	}
	if sub == "check" {
		return a.printResult(rejection)
	}
	draft := filepath.Join(attemptDir, artisan.ModelDir, artisan.ResultFile)
	if rejection == nil {
		if err := writeDraft(draft, data); err != nil {
			return a.notChecked(fmt.Errorf("result submit: %w", err))
		}
		return a.printResult(nil)
	}
	submissions := filepath.Join(attemptDir, artisan.SubmissionsFile)
	if rejection.Unacknowledged {
		// Acknowledging the messages clears this rejection, so it spends no resubmission and leaves the draft as it is.
		k, err := countedRejections(submissions)
		if err != nil {
			return a.notChecked(fmt.Errorf("result submit: %w", err))
		}
		code := a.printResult(rejection)
		line := fmt.Sprintf("%d of %d resubmissions used, none for this; acknowledge the messages and submit again\n", k, Resubmissions)
		if _, err := io.WriteString(a.Stdout, line); err != nil {
			return 1
		}
		return code
	}
	k, err := countRejection(submissions, rejection.Reason, data)
	if err != nil {
		return a.notChecked(fmt.Errorf("result submit: %w", err))
	}
	if k > Resubmissions {
		// The last rejection is published as the attempt's result, so intake fails it with this reason.
		if err := writeDraft(draft, data); err != nil {
			return a.notChecked(fmt.Errorf("result submit: %w", err))
		}
	}
	code := a.printResult(rejection)
	line := fmt.Sprintf("%d of %d resubmissions used; fix the result and submit again\n", k, Resubmissions)
	if k > Resubmissions {
		line = "no resubmissions left: this result will fail the attempt; stop now\n"
	}
	if _, err := io.WriteString(a.Stdout, line); err != nil {
		return 1
	}
	return code
}

// attemptCopy finds, without --copy, the copy an Artisan's working directory lies in: the nearest directory above it
// with a copy marker whose state/ holds the working directory and the attempt directory of key. Copy discovery
// refuses a directory under state/ (copydir.Discover), so an Artisan reaches no store by accident; this finds only
// the copy that runs the attempt GUILDHALL_ATTEMPT names. "" is no such copy.
func attemptCopy(getwd func() (string, error), key artisan.AttemptKey) (string, error) {
	if getwd == nil {
		return "", nil
	}
	wd, err := getwd()
	if err != nil {
		return "", err
	}
	start, err := filepath.Abs(wd)
	if err != nil {
		return "", err
	}
	for d := start; ; {
		if _, err := os.Lstat(filepath.Join(d, copyMarkerFile)); err == nil {
			state := filepath.Join(d, "state")
			if !copydir.Within(state, start) {
				return "", nil
			}
			if _, err := os.Stat(key.Dir(state)); err != nil {
				return "", nil
			}
			if err := readCopyMarker(filepath.Join(d, copyMarkerFile)); err != nil {
				return "", err
			}
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", nil
		}
		d = parent
	}
}

// notChecked reports a failure that kept the file from being checked: stderr and exit 2, never a rejection.
func (a *App) notChecked(err error) int {
	_, _ = fmt.Fprintf(a.Stderr, "guildhall: %v\n", err)
	return 2
}

// printResult prints ok and returns 0 for no rejection, or rejected: <reason> and 1.
func (a *App) printResult(r *interp.Rejection) int {
	line, code := "ok\n", 0
	if r != nil {
		line, code = "rejected: "+r.Reason+"\n", 1
	}
	if _, err := io.WriteString(a.Stdout, line); err != nil {
		return 1
	}
	return code
}

// readResultFile reads a result file, at most one byte over adapter.MaxResultBytes, so an oversized one is refused
// as intake refuses it.
func readResultFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, adapter.MaxResultBytes+1))
}

// checkResult runs interp.Engine.CheckResult for key over the copy's store, opened read-only for each read and
// waiting the copy's lock wait, as `guildhall ci log` does; it takes no task lock.
func (a *App) checkResult(ctx context.Context, storePath, cfgPath, state string, key artisan.AttemptKey, data []byte) error {
	cfg, err := admitDirector(cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	r, err := cfg.ListedRepository(key.Repository)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	wait, err := cfg.LockWait()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	records := storeRecords{path: storePath, wait: wait}
	overrides, err := interp.ApplyTaskOverrides(records, cfg, key.Repository, key.TaskID)
	if err != nil {
		return fmt.Errorf("task overrides: %w", err)
	}
	var ws interp.Workspaces = git.Program{}
	if a.Workspaces != nil {
		ws = a.Workspaces
	}
	cfgDir := filepath.Dir(cfgPath)
	e := &interp.Engine{
		Copy: filepath.Dir(cfgPath), Records: records, Config: overrides.Config, Overrides: &overrides,
		Workflow: func(rev workflow.Revision) (*workflow.Definition, error) {
			return boundWorkflow(filepath.Join(cfgDir, "workflows"), cfg, r, rev)
		},
		DocumentDir: filepath.Join(state, "documents"), StateDir: state, Workspaces: ws,
	}
	return e.CheckResult(ctx, key, data)
}

// writeDraft writes data as the attempt's draft result through a temporary file beside it, which a crash may
// leave behind and the next submission overwrites, renamed over the draft, so the wrapper reads the old draft whole
// or the new one.
func writeDraft(draft string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(draft), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(draft), "."+filepath.Base(draft)+".submit.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return atomicfile.Rename(tmp, draft)
}

// submission is one line of the attempt's SubmissionsFile: a rejected submission.
type submission struct {
	At     string `json:"at"`
	SHA256 string `json:"sha256"`
	Reason string `json:"reason"`
}

// countRejection appends a rejected submission to the submissions file and returns how many it now holds.
func countRejection(path, reason string, data []byte) (int, error) {
	line, err := json.Marshal(submission{At: time.Now().UTC().Format(time.RFC3339), SHA256: digest.Of(data).Hex(), Reason: reason})
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	_, werr := f.Write(append(line, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return 0, err
	}
	return countedRejections(path)
}

// countedRejections is how many rejected submissions the submissions file at path holds; none when it is absent.
func countedRejections(path string) (int, error) {
	all, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strings.Count(string(all), "\n"), nil
}
