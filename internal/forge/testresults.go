package forge

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/testresults"
)

// TestResults is what a commit's completed CI run recorded of its test suite: the run, and one leg per test2json
// stream its test-timings-* artifacts hold (docs/decisions/ci-test-timings.md).
type TestResults struct {
	Run CIRun
	// Legs are the run's streams, by artifact name and then test.json before managed.json; none when the run has no
	// test-timings-* artifact.
	Legs []TestLeg
	// Unavailable is why the run gives no legs, such as "run 7 has no test-timings-* artifact"; empty when it has.
	Unavailable string
}

// CIRun is one completed GitHub Actions workflow run.
type CIRun struct {
	// ID is the run's id and URL its page.
	ID  int64
	URL string
	// Head is the commit the run tested.
	Head string
	// Conclusion is how the run finished, lower-cased as GitHub names it, such as success or failure; empty when
	// GitHub gives none.
	Conclusion string
}

// TestLeg is one test2json stream of a run: the summary of an artifact's test.json or managed.json, or why it has
// none.
type TestLeg struct {
	// Name is the leg as CI's matrix names it, the artifact's name without test-timings- and -latest, with " managed"
	// for managed.json: "ubuntu", "ubuntu managed", "windows" or "macos".
	Name string
	// Artifact is the artifact's name, such as test-timings-ubuntu-latest, and File the stream's file in it.
	Artifact, File string
	// Summary is the stream's summary; the zero Summary when Unavailable is set.
	Summary testresults.Summary
	// Unavailable is why the leg has no summary: the artifact expired, is larger than a cap, has no test.json, or
	// failed to download or parse; empty when Summary is set.
	Unavailable string
}

// TestArtifactPrefix starts the name of every artifact TestResults reads.
const TestArtifactPrefix = "test-timings-"

// testStreams are the files a test-timings-* artifact may hold: test.json, which every leg uploads, and
// managed.json, which only the leg that runs the Director suites managed does.
var testStreams = []string{"test.json", "managed.json"}

// The caps TestResults reads an artifact within: its zip, by GitHub's size_in_bytes before the download and by the
// bytes downloaded, and each stream decompressed from it. They are constants, not settings.
const (
	MaxTestArtifactBytes = 64 << 20
	MaxTestStreamBytes   = 512 << 20
)

// TestResultsTimeout bounds each gh run TestResults makes; a shorter GitHub.Timeout bounds it instead.
const TestResultsTimeout = 2 * time.Minute

var (
	// ErrNoTestRun reports a commit with no completed push run, so the caller may look at an earlier commit.
	ErrNoTestRun = errors.New("no completed push run")
	// ErrTestResultsUnavailable reports test results the forge cannot read: gh failing, timing out or not signed
	// in, or a refused or malformed answer. The error text carries the reason.
	ErrTestResultsUnavailable = errors.New("test results unavailable")
)

// TestResultsUnavailableError is an ErrTestResultsUnavailable with its reason.
type TestResultsUnavailableError struct{ Reason string }

func (e *TestResultsUnavailableError) Error() string { return "test results unavailable: " + e.Reason }

// Is matches ErrTestResultsUnavailable.
func (e *TestResultsUnavailableError) Is(target error) bool {
	return target == ErrTestResultsUnavailable
}

// TestLegName is the leg a stream of an artifact is, as TestLeg.Name says.
func TestLegName(artifact, file string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(artifact, TestArtifactPrefix), "-latest")
	if file == "managed.json" {
		name += " managed"
	}
	return name
}

// TestArtifactLegs returns the legs of artifact name holding files, each file's content by its name, as TestResults
// reads a downloaded artifact: test.json's leg, or one saying the artifact has none, then managed.json's when it is
// there, each summarised within MaxTestStreamBytes. Doubles share it, so they name, order and cap legs as GitHub does.
func TestArtifactLegs(name string, files map[string]string) []TestLeg {
	return artifactLegs(name, MaxTestStreamBytes, func(file string) (io.ReadCloser, uint64, bool, error) {
		text, ok := files[file]
		return io.NopCloser(strings.NewReader(text)), uint64(len(text)), ok, nil
	})
}

// artifactLegs returns the legs of artifact name, whose files open finds, within stream bytes each.
func artifactLegs(name string, stream int64, open func(file string) (_ io.ReadCloser, size uint64, found bool, _ error)) []TestLeg {
	var legs []TestLeg
	for _, file := range testStreams {
		leg := TestLeg{Name: TestLegName(name, file), Artifact: name, File: file}
		rc, size, found, err := open(file)
		switch {
		case !found && file == "test.json":
			leg.Unavailable = "artifact " + name + " has no " + file
		case !found:
			continue
		case err != nil:
			leg.Unavailable = file + " of " + name + ": " + err.Error()
		case size > uint64(stream):
			leg.Unavailable = file + " of " + name + ": " + (&testresults.TooLargeError{Limit: stream}).Error()
		default:
			s, err := testresults.Parse(rc, stream)
			if err != nil {
				leg.Unavailable = file + " of " + name + ": " + err.Error()
			} else {
				leg.Summary = s
			}
		}
		if rc != nil {
			_ = rc.Close()
		}
		legs = append(legs, leg)
	}
	return legs
}

// testCaps are the caps an artifact is read within: MaxTestArtifactBytes and MaxTestStreamBytes in production.
type testCaps struct{ artifact, stream int64 }

// TestResults reads the test results of c.Head's completed push run from GitHub Actions with gh api, each gh run
// bounded by TestResultsTimeout: it lists the head's completed push runs, newest first, takes the first whose
// artifacts include test-timings-* ones, and downloads each such artifact's zip within MaxTestArtifactBytes, refusing
// one whose size GitHub reports over it before downloading, and summarises its test.json, and its managed.json when
// it holds one, each within MaxTestStreamBytes decompressed.
//
// A head with no completed push run is ErrNoTestRun. When none of its runs has a test-timings-* artifact, the newest
// run is returned with no legs and Unavailable saying so. An artifact that expired (GitHub's expired flag, or an HTTP
// 404 or 410 at the download), is over a cap, has no test.json or fails to download or parse gives one leg with the
// reason. Any other gh failure, including gh not signed in and a timeout, is ErrTestResultsUnavailable with the
// reason; a malformed commit is ErrInvalid and an unsupported remote ErrUnsupportedRemote. A cancelled ctx is
// returned as it is.
func (g GitHub) TestResults(ctx context.Context, c Commit) (TestResults, error) {
	return g.testResults(ctx, c, testCaps{artifact: MaxTestArtifactBytes, stream: MaxTestStreamBytes})
}

// maxRunsRead bounds how many of a head's completed push runs are listed and searched for test-timings-* artifacts.
const maxRunsRead = 10

// maxListing bounds a run or artifact listing gh prints.
const maxListing = 8 << 20

// ghArtifact is the part of GitHub's artifact JSON TestResults reads.
type ghArtifact struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	SizeInBytes int64  `json:"size_in_bytes"`
	Expired     bool   `json:"expired"`
}

func (g GitHub) testResults(ctx context.Context, c Commit, caps testCaps) (TestResults, error) {
	if err := c.Validate(); err != nil {
		return TestResults{}, err
	}
	repo, err := GitHubRepository(c.Remote)
	if err != nil {
		return TestResults{}, err
	}
	parts := strings.Split(repo, "/")
	timeout := TestResultsTimeout
	if g.Timeout > 0 && g.Timeout < timeout {
		timeout = g.Timeout
	}
	api := func(w io.Writer, endpoint string) error {
		_, err := runTo(ctx, timeout, c.Repository, orDefault(g.GH, "gh"), ghEnv, w, "api", "repos/"+parts[1]+"/"+parts[2]+"/"+endpoint, "--hostname", parts[0])
		return err
	}
	getJSON := func(endpoint string, v any) error {
		var out bytes.Buffer
		w := &headWriter{limit: maxListing, w: &out}
		if err := api(w, endpoint); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if w.over {
				return &TestResultsUnavailableError{Reason: fmt.Sprintf("gh api %s printed more than %d bytes", endpoint, maxListing)}
			}
			return &TestResultsUnavailableError{Reason: ghReason(err)}
		}
		if err := json.Unmarshal(out.Bytes(), v); err != nil {
			return &TestResultsUnavailableError{Reason: fmt.Sprintf("gh api %s: %v", endpoint, err)}
		}
		return nil
	}

	var runs struct {
		WorkflowRuns []struct {
			ID         int64   `json:"id"`
			HTMLURL    string  `json:"html_url"`
			HeadSHA    string  `json:"head_sha"`
			Event      string  `json:"event"`
			Status     string  `json:"status"`
			Conclusion *string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := getJSON("actions/runs?head_sha="+c.Head+"&event=push&status=completed&per_page="+strconv.Itoa(maxRunsRead), &runs); err != nil {
		return TestResults{}, err
	}
	var candidates []CIRun
	for _, r := range runs.WorkflowRuns {
		// The query filters these already; a run that does not match is not this head's to answer for.
		if r.HeadSHA == c.Head && r.Event == "push" && r.Status == "completed" && r.ID > 0 {
			candidates = append(candidates, CIRun{ID: r.ID, URL: r.HTMLURL, Head: r.HeadSHA, Conclusion: strings.ToLower(deref(r.Conclusion))})
		}
	}
	if len(candidates) == 0 {
		return TestResults{}, fmt.Errorf("%w at %s", ErrNoTestRun, c.Head)
	}
	for _, run := range candidates[:min(len(candidates), maxRunsRead)] {
		var listing struct {
			Artifacts []ghArtifact `json:"artifacts"`
		}
		if err := getJSON("actions/runs/"+strconv.FormatInt(run.ID, 10)+"/artifacts?per_page=100", &listing); err != nil {
			return TestResults{}, err
		}
		arts := slices.DeleteFunc(listing.Artifacts, func(a ghArtifact) bool { return !strings.HasPrefix(a.Name, TestArtifactPrefix) })
		if len(arts) == 0 {
			continue
		}
		slices.SortFunc(arts, func(a, b ghArtifact) int { return cmp.Or(strings.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
		out := TestResults{Run: run}
		for _, a := range arts {
			legs, err := readArtifact(ctx, a, caps, func(w io.Writer) error {
				return api(w, "actions/artifacts/"+strconv.FormatInt(a.ID, 10)+"/zip")
			})
			if err != nil {
				return TestResults{}, err
			}
			out.Legs = append(out.Legs, legs...)
		}
		return out, nil
	}
	return TestResults{Run: candidates[0], Unavailable: fmt.Sprintf("run %d has no %s* artifact", candidates[0].ID, TestArtifactPrefix)}, nil
}

// readArtifact downloads artifact a with download and returns its legs, or one leg with the reason it gives none;
// only a cancelled ctx is an error.
func readArtifact(ctx context.Context, a ghArtifact, caps testCaps, download func(io.Writer) error) ([]TestLeg, error) {
	unavailable := func(reason string) []TestLeg {
		return []TestLeg{{Name: TestLegName(a.Name, "test.json"), Artifact: a.Name, File: "test.json", Unavailable: reason}}
	}
	tooLarge := fmt.Sprintf("artifact %s is larger than %d bytes", a.Name, caps.artifact)
	switch {
	case a.Expired:
		return unavailable("artifact " + a.Name + " expired"), nil
	case a.SizeInBytes > caps.artifact:
		return unavailable(tooLarge), nil
	}
	var buf bytes.Buffer
	w := &headWriter{limit: caps.artifact, w: &buf}
	switch err := download(w); {
	case err == nil:
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case w.over:
		return unavailable(tooLarge), nil
	case expiredLog.MatchString(ghReason(err)):
		return unavailable("artifact " + a.Name + " expired: " + ghReason(err)), nil
	default:
		return unavailable("artifact " + a.Name + ": " + ghReason(err)), nil
	}
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return unavailable("artifact " + a.Name + ": " + err.Error()), nil
	}
	return artifactLegs(a.Name, caps.stream, func(file string) (io.ReadCloser, uint64, bool, error) {
		i := slices.IndexFunc(z.File, func(f *zip.File) bool { return path.Clean(f.Name) == file })
		if i < 0 {
			return nil, 0, false, nil
		}
		rc, err := z.File[i].Open()
		return rc, z.File[i].UncompressedSize64, true, err
	}), nil
}

// ghReason is why a gh run failed: what gh printed, or the error.
func ghReason(err error) string {
	if run, ok := errors.AsType[*runError](err); ok {
		return cmp.Or(run.output, run.err.Error())
	}
	return err.Error()
}

// headWriter writes the first limit bytes written to it to w and fails every write past them, so a download over a
// cap stops rather than being cut silently, as TailWriter would cut a zip.
type headWriter struct {
	limit, written int64
	w              io.Writer
	over           bool
}

var errOverLimit = errors.New("write past the limit")

func (h *headWriter) Write(p []byte) (int, error) {
	if h.over || h.written+int64(len(p)) > h.limit {
		h.over = true
		return 0, errOverLimit
	}
	n, err := h.w.Write(p)
	h.written += int64(n)
	return n, err
}
