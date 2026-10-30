package cilog_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/cilog"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

var commit = forge.Commit{Repository: "/repo", Remote: "https://github.com/acme/widgets.git", Head: strings.Repeat("a", 40)}

// TestGet: a miss fetches the log and caches it; a hit reads the cache without the forge; a pruned cache fetches
// again; a log cut short keeps its header in the file and its dropped count on reading it back.
func TestGet(t *testing.T) {
	ctx := context.Background()
	dir := cilog.Dir(t.TempDir(), "guildhall", "d45")
	f := forgetest.NewFake()
	f.SetLog("7", "one\ntwo\n")
	got, err := cilog.Get(ctx, f, dir, commit, "7", 1<<20)
	if err != nil || string(got.Text) != "one\ntwo\n" || got.Dropped != 0 || got.Path != filepath.Join(dir, "7.log") {
		t.Fatalf("Get miss = %+v, %v", got, err)
	}
	if data, err := os.ReadFile(got.Path); err != nil || string(data) != "one\ntwo\n" {
		t.Errorf("cached file = %q, %v", data, err)
	}
	f.SetLog("7", "changed\n")
	if got, err := cilog.Get(ctx, f, dir, commit, "7", 1<<20); err != nil || string(got.Text) != "one\ntwo\n" || f.LogCalls() != 1 {
		t.Errorf("Get hit = %q, %v after %d forge calls; want the cached log and one call", got.Text, err, f.LogCalls())
	}
	if got, err := cilog.Get(ctx, nil, dir, commit, "7", 1<<20); err != nil || string(got.Text) != "one\ntwo\n" {
		t.Errorf("Get hit without a forge = %q, %v", got.Text, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := cilog.Get(ctx, f, dir, commit, "7", 1<<20); err != nil || string(got.Text) != "changed\n" || f.LogCalls() != 2 {
		t.Errorf("Get after pruning = %q, %v after %d forge calls; want a fetch", got.Text, err, f.LogCalls())
	}

	f.SetLog("8", strings.Repeat("line\n", 100))
	got, err = cilog.Get(ctx, f, dir, commit, "8", 64)
	if err != nil || string(got.Text) != strings.Repeat("line\n", 12) || got.Dropped != 440 {
		t.Fatalf("Get truncated = %+v, %v", got, err)
	}
	data, err := os.ReadFile(got.Path)
	if want := "[guildhall: 440 bytes dropped from the head of this log]\n" + strings.Repeat("line\n", 12); err != nil || string(data) != want {
		t.Errorf("cached truncated file = %q, %v; want %q", data, err, want)
	}
	if again, err := cilog.Get(ctx, f, dir, commit, "8", 64); err != nil || string(again.Text) != string(got.Text) || again.Dropped != 440 {
		t.Errorf("Get truncated hit = %+v, %v", again, err)
	}
}

// TestGetUnavailable: a failed fetch leaves no file, partial or temporary; a job that is not an Actions job id, a
// limit of zero and no forge are unavailable without a forge call.
func TestGetUnavailable(t *testing.T) {
	ctx := context.Background()
	dir := cilog.Dir(t.TempDir(), "guildhall", "d45")
	f := forgetest.NewFake()
	f.SetLogError("9", &forge.LogUnavailableError{Reason: "gh: Forbidden (HTTP 403)"})
	if _, err := cilog.Get(ctx, f, dir, commit, "9", 1<<20); !errors.Is(err, forge.ErrLogUnavailable) {
		t.Errorf("Get of a failing fetch = %v", err)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 || err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cache after a failed fetch holds %v, %v", entries, err)
	}
	for _, tc := range []struct {
		job    string
		limit  int64
		r      cilog.Reader
		reason string
	}{
		{"", 1 << 20, f, "not a GitHub Actions job"},
		{"../x", 1 << 20, f, "not a GitHub Actions job"},
		{"9", 0, f, "log fetching is off"},
		{"10", 1 << 20, nil, "forge does not provide logs"},
	} {
		if _, err := cilog.Get(ctx, tc.r, dir, commit, tc.job, tc.limit); !errors.Is(err, forge.ErrLogUnavailable) || forge.LogReason(err) != tc.reason {
			t.Errorf("Get(%q, %d) = %v, want unavailable: %s", tc.job, tc.limit, err, tc.reason)
		}
	}
	if n := f.LogCalls(); n != 1 {
		t.Errorf("forge called %d times, want 1", n)
	}
}
