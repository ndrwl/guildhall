package policies_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/git"
	"github.com/ndrwl/guildhall/internal/policies"
)

func TestParse(t *testing.T) {
	for _, c := range []struct {
		name, data string
		want       []string
		err        string
	}{
		{name: "empty file", data: ""},
		{name: "commentary only", data: "# Policies\n\nNone yet.\n"},
		{name: "items and commentary", data: "# Policies\n\nWhy these exist.\n\n- Keep the branch protection.\n- Squash merges only.  \n\n## More\n\n-   Spaced item\n",
			want: []string{"Keep the branch protection.", "Squash merges only.", "Spaced item"}},
		{name: "no final newline", data: "- One", want: []string{"One"}},
		{name: "a dash without a space is commentary", data: "-not an item\n---\n- Item\n", want: []string{"Item"}},
		{name: "indented text outside an item is commentary", data: "Paragraph\n    code\n- Item\n", want: []string{"Item"}},
		{name: "item at the limit", data: "- " + strings.Repeat("a", policies.MaxItemBytes) + "\n", want: []string{strings.Repeat("a", policies.MaxItemBytes)}},
		{name: "continuation", data: "- Keep the branch\n  protection.\n", err: "line 2: an indented line continues the item on line 1"},
		{name: "continuation after a blank line", data: "- Keep it.\n\n\tMore.\n", err: "line 3: an indented line continues the item on line 1"},
		{name: "nested item", data: "- Keep it.\n  - unless\n", err: "line 2"},
		{name: "blank item", data: "- One\n- \n", err: "line 2: the item is blank"},
		{name: "bare dash", data: "-\n", err: "line 1: the item is blank"},
		{name: "duplicate", data: "- One\n- Two\n-  One\n", err: "line 3: the item repeats line 1"},
		{name: "carriage return", data: "- One\r\n", err: "line 1: the item holds a carriage return"},
		{name: "over the item limit", data: "- " + strings.Repeat("a", policies.MaxItemBytes+1) + "\n", err: "line 1: the item is 1025 bytes"},
		{name: "over the file limit", data: strings.Repeat("x", policies.MaxFileBytes+1), err: "the limit is 16384"},
		{name: "invalid UTF-8", data: "- caf\xe9\n", err: "not valid UTF-8"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := policies.Parse([]byte(c.data))
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) || !strings.HasPrefix(err.Error(), policies.Path) {
					t.Fatalf("Parse = %q, %v; want an error naming %s and %q", got, err, policies.Path, c.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, c.want) {
				t.Fatalf("Parse = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// TestOwnPolicyFileParses: this repository's own policy file is accepted, with at least one policy.
func TestOwnPolicyFileParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(policies.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := policies.Parse(data); err != nil || len(got) == 0 {
		t.Fatalf("Parse(%s) = %q, %v; want at least one policy", policies.Path, got, err)
	}
}

// fakeGit answers Show with a fixed result.
type fakeGit struct {
	shown git.Shown
	err   error
	asked [3]string
}

func (f *fakeGit) Show(_ context.Context, repo, rev, path string) (git.Shown, error) {
	f.asked = [3]string{repo, rev, path}
	return f.shown, f.err
}

func TestLoad(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	g := &fakeGit{shown: git.Shown{Commit: commit, Data: []byte("- One\n")}}
	if got := policies.Load(ctx, g, "/srv/app"); got.Err != nil || got.NotRead || got.Commit != commit || !slices.Equal(got.Policies, []string{"One"}) {
		t.Errorf("Load = %+v", got)
	}
	if g.asked != [3]string{"/srv/app", policies.Ref, policies.Path} {
		t.Errorf("Load read %q, want the policy file at the policies ref", g.asked)
	}
	g.shown = git.Shown{Commit: commit, Absent: true}
	if got := policies.Load(ctx, g, "/srv/app"); got.Err != nil || got.NotRead || got.Commit != commit || got.Policies != nil {
		t.Errorf("Load of an absent file = %+v, want no policies", got)
	}
	g.shown = git.Shown{Commit: commit, Data: []byte("- One\n- One\n")}
	if got := policies.Load(ctx, g, "/srv/app"); got.Err == nil || got.Commit != commit {
		t.Errorf("Load of a refused file = %+v, want its error at the commit", got)
	}
	g.err = git.ErrNoRevision
	if got := policies.Load(ctx, g, "/srv/app"); !got.NotRead || got.Err != nil {
		t.Errorf("Load before the first fetch = %+v, want not read", got)
	}
	g.err = errors.New("git failed")
	if got := policies.Load(ctx, g, "/srv/app"); got.NotRead || got.Err == nil {
		t.Errorf("Load over a git failure = %+v, want the error", got)
	}
}
