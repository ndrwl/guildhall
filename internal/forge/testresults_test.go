package forge_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/testresults"
)

const passJSON = `{"Action":"run","Package":"acme/a","Test":"TestA"}
{"Action":"pass","Package":"acme/a","Test":"TestA","Elapsed":1.5}
{"Action":"pass","Package":"acme/a","Elapsed":1.6}
`

// TestGitHubTestResults: TestResults lists the head's completed push runs, the chosen run's artifacts and
// downloads each test-timings-* zip with gh api; the size GitHub reports, the download and each decompressed stream
// are capped; an expired artifact (flag, 404 or 410) and a forbidden download are a leg's reason; gh signed out is
// ErrTestResultsUnavailable; a hung download is a leg naming the timeout; and a cancelled context is returned as it is.
func TestGitHubTestResults(t *testing.T) {
	g := newGitHub(t)
	h := g.harness
	f := h.Forge.(forge.GitHub)
	ctx := context.Background()
	head := h.Heads[0]
	commit := forge.Commit{Repository: h.Repository, Remote: remote, Head: head}

	s := g.readState(t)
	s.Runs = map[string][]ghRun{head: {
		// The newest run has no test-timings artifact, and runs still going or from another event are not listed.
		{ID: 50, Event: "push", Status: "in_progress"},
		{ID: 49, Event: "workflow_dispatch", Status: "completed", Conclusion: "success",
			Artifacts: []ghRunArtifact{{ID: 4901, Name: "test-timings-ubuntu-latest", Zip: zipFiles(t, map[string]string{"test.json": passJSON})}}},
		{ID: 48, Event: "push", Status: "completed", Conclusion: "success", Artifacts: []ghRunArtifact{{ID: 4801, Name: "coverage", Zip: zipFiles(t, map[string]string{"c.out": "x"})}}},
		{ID: 47, Event: "push", Status: "completed", Conclusion: "failure", Artifacts: []ghRunArtifact{
			{ID: 4701, Name: "test-timings-ubuntu-latest", Zip: zipFiles(t, map[string]string{"test.json": passJSON, "managed.json": passJSON})},
			{ID: 4702, Name: "test-timings-windows-latest", Zip: zipFiles(t, map[string]string{"test.json": passJSON})},
			{ID: 4703, Name: "test-timings-macos-latest", Expired: true},
			{ID: 4704, Name: "test-timings-zz-gone"},
			{ID: 4705, Name: "test-timings-zz-forbidden"},
			{ID: 4706, Name: "test-timings-zz-corrupt", Zip: []byte("not a zip")},
		}},
	}}
	s.ArtifactStatus = map[string]int{"4704": 410, "4705": 403}
	g.writeState(t, s)

	got, err := f.TestResults(ctx, commit)
	if err != nil {
		t.Fatalf("TestResults = %v", err)
	}
	if got.Run != (forge.CIRun{ID: 47, URL: "https://github.com/acme/widgets/actions/runs/47", Head: head, Conclusion: "failure"}) || got.Unavailable != "" {
		t.Errorf("Run = %+v, %q; want run 47, the newest completed push run with test-timings artifacts", got.Run, got.Unavailable)
	}
	var legs []string
	for _, l := range got.Legs {
		legs = append(legs, l.Name+": "+l.Unavailable)
	}
	want := []string{
		"macos: artifact test-timings-macos-latest expired",
		"ubuntu: ", "ubuntu managed: ", "windows: ",
		"zz-corrupt: artifact test-timings-zz-corrupt: zip: not a valid zip file",
		"zz-forbidden: artifact test-timings-zz-forbidden: gh: Forbidden (HTTP 403)",
		"zz-gone: artifact test-timings-zz-gone expired: gh: Gone (HTTP 410)",
	}
	if !slices.Equal(legs, want) {
		t.Errorf("legs =\n%s\nwant\n%s", strings.Join(legs, "\n"), strings.Join(want, "\n"))
	}
	if p := got.Legs[1].Summary.Packages; !slices.Equal(p, []testresults.Package{{Path: "acme/a", State: testresults.Pass, Elapsed: 1.6}}) {
		t.Errorf("ubuntu packages = %+v", p)
	}

	calls := g.readState(t).Calls
	runsCall := []string{"api", "repos/acme/widgets/actions/runs?head_sha=" + head + "&event=push&status=completed&per_page=10", "--hostname", "github.com"}
	var downloads []string
	for _, c := range calls {
		if len(c) > 1 && artifactZipEndpoint.MatchString(c[1]) {
			downloads = append(downloads, artifactZipEndpoint.FindStringSubmatch(c[1])[3])
		}
	}
	// One runs listing, two artifact listings (48, then 47), and a download of every test-timings artifact of run 47
	// that has not expired by GitHub's flag.
	if len(calls) != 8 || !slices.Equal(calls[0], runsCall) || !slices.Equal(calls[1], []string{"api", "repos/acme/widgets/actions/runs/48/artifacts?per_page=100", "--hostname", "github.com"}) ||
		!slices.Equal(downloads, []string{"4701", "4702", "4706", "4705", "4704"}) {
		t.Errorf("gh calls = %q", calls)
	}
	t.Run("installed-gh-flags", func(t *testing.T) {
		for _, c := range calls {
			checkInstalledGHFlags(t, c)
		}
	})

	t.Run("caps", func(t *testing.T) {
		s := g.readState(t)
		s.Calls = nil
		g.writeState(t, s)
		small := s.Runs[head][3].Artifacts[1] // windows
		// Over the artifact cap by GitHub's size: not downloaded. Over it by the download: stopped. Over the stream
		// cap: the stream's leg says so.
		got, err := forge.ReadTestResultsCapped(ctx, f, commit, int64(len(small.Zip)), 10)
		if err != nil {
			t.Fatal(err)
		}
		reasons := map[string]string{}
		for _, l := range got.Legs {
			reasons[l.Name] = l.Unavailable
		}
		if r := reasons["ubuntu"]; !strings.Contains(r, "larger than "+strconv.Itoa(len(small.Zip))+" bytes") {
			t.Errorf("ubuntu over the artifact cap = %q", r)
		}
		if r := reasons["windows"]; r != "test.json of test-timings-windows-latest: larger than 10 bytes" {
			t.Errorf("windows over the stream cap = %q", r)
		}
		for _, c := range g.readState(t).Calls {
			if strings.HasSuffix(c[1], "/4701/zip") {
				t.Errorf("an artifact GitHub reports over the cap was downloaded: %q", c)
			}
		}

		s = g.readState(t)
		s.Runs[head][3].Artifacts[0].Size = 1 // GitHub under-reports: the download itself is capped
		g.writeState(t, s)
		got, err = forge.ReadTestResultsCapped(ctx, f, commit, int64(len(small.Zip)), 1<<20)
		if err != nil || !strings.Contains(got.Legs[1].Unavailable, "test-timings-ubuntu-latest is larger than") {
			t.Errorf("a download over the cap = %+v, %v", got.Legs, err)
		}
	})

	t.Run("no run and no artifacts", func(t *testing.T) {
		if _, err := f.TestResults(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[1]}); !errors.Is(err, forge.ErrNoTestRun) {
			t.Errorf("TestResults of a head with no run = %v, want ErrNoTestRun", err)
		}
		s := g.readState(t)
		s.Runs[h.Heads[2]] = []ghRun{{ID: 60, Event: "push", Status: "completed", Conclusion: "success"}}
		g.writeState(t, s)
		got, err := f.TestResults(ctx, forge.Commit{Repository: h.Repository, Remote: remote, Head: h.Heads[2]})
		if err != nil || got.Run.ID != 60 || len(got.Legs) != 0 || got.Unavailable != "run 60 has no test-timings-* artifact" {
			t.Errorf("TestResults of a run with no artifacts = %+v, %v", got, err)
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		s := g.readState(t)
		s.SignedOut = true
		g.writeState(t, s)
		if _, err := f.TestResults(ctx, commit); !errors.Is(err, forge.ErrTestResultsUnavailable) || !strings.Contains(err.Error(), "gh auth login") {
			t.Errorf("TestResults signed out = %v, want unavailable carrying gh's message", err)
		}
		s.SignedOut, s.HangArtifacts = false, true
		s.Runs[head][3].Artifacts = s.Runs[head][3].Artifacts[1:2]
		g.writeState(t, s)
		// The timeout bounds every gh run, so the run and artifact listings before the hung download must finish
		// within it too. Like TestGitHubMergeTimeout, try a ladder of rising timeouts and assert on the first rung
		// where those listings answered, so a slow (race) runner moves up the ladder instead of failing.
		hung := f
		var got forge.TestResults
		var err error
		// timing:allow the shortest rung deliberately expires a download that never answers (HangArtifacts); a rung
		// whose listings miss their budget changes nothing and is retried at a larger rung; the last is policy-sized.
		for _, hung.Timeout = range []time.Duration{500 * time.Millisecond, 2 * time.Second, 10 * time.Second} {
			got, err = hung.TestResults(ctx, commit)
			if err == nil || !strings.Contains(err.Error(), "no answer within") {
				break
			}
		}
		if err != nil || len(got.Legs) != 1 || !strings.Contains(got.Legs[0].Unavailable, "no answer within "+hung.Timeout.String()) {
			t.Errorf("TestResults with a hung download = %+v, %v; want a leg naming the %v timeout", got.Legs, err, hung.Timeout)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := f.TestResults(cancelled, commit); !errors.Is(err, context.Canceled) || errors.Is(err, forge.ErrTestResultsUnavailable) {
			t.Errorf("TestResults cancelled = %v, want context.Canceled unwrapped", err)
		}
		for _, bad := range []forge.Commit{{Repository: h.Repository, Remote: remote, Head: "HEAD"}, {Repository: h.Repository, Remote: "https://gitlab.com/a/b.git", Head: head}} {
			if _, err := f.TestResults(ctx, bad); !errors.Is(err, forge.ErrInvalid) && !errors.Is(err, forge.ErrUnsupportedRemote) {
				t.Errorf("TestResults(%+v) = %v, want ErrInvalid or ErrUnsupportedRemote", bad, err)
			}
		}
	})
}

// TestFakeTestResults: unconfigured, the fake reports "not configured"; SetTestResultsError's failure replaces any
// answer until cleared; and TestResultsCalls counts every call.
func TestFakeTestResults(t *testing.T) {
	f := forgetest.NewFake()
	ctx := context.Background()
	commit := forge.Commit{Repository: "/repo", Remote: remote, Head: strings.Repeat("a", 40)}
	if _, err := f.TestResults(ctx, commit); !errors.Is(err, forge.ErrTestResultsUnavailable) || !strings.HasSuffix(err.Error(), ": not configured") {
		t.Errorf("unconfigured TestResults = %v, want unavailable: not configured", err)
	}
	f.SetTestRun(remote, commit.Head, forgetest.RunFixture{ID: 7, Conclusion: "success",
		Artifacts: []forgetest.ArtifactFixture{{Name: "test-timings-ubuntu-latest", Files: map[string]string{"test.json": passJSON}}}})
	boom := errors.New("boom")
	f.SetTestResultsError(boom)
	if _, err := f.TestResults(ctx, commit); !errors.Is(err, boom) {
		t.Errorf("TestResults after SetTestResultsError = %v, want boom", err)
	}
	f.SetTestResultsError(nil)
	got, err := f.TestResults(ctx, commit)
	if err != nil || got.Run.ID != 7 || len(got.Legs) != 1 || got.Legs[0].Name != "ubuntu" {
		t.Errorf("TestResults = %+v, %v", got, err)
	}
	if n := f.TestResultsCalls(); n != 3 {
		t.Errorf("TestResultsCalls() = %d, want 3", n)
	}
}
