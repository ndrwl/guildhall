// Package policies reads a repository's policies: the Guildmaster's standing rules for one repository, kept in
// the repository itself at Path and read from its default branch as last fetched into Ref
// (docs/configuration.md#repository-policies). Policies are guidance shown to the Director and Artisans; nothing
// reads their text to decide anything.
package policies

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ndrwl/guildhall/internal/git"
)

// Path is the policy file, relative to the repository root; it is fixed, not configurable.
const Path = ".guildhall/policies.md"

// Ref is the ref each repository's default branch is fetched into for its policies, so every reader reads the
// file at the same commit.
const Ref = "refs/guildhall/policies"

// MaxItemBytes bounds one policy, as a note is bounded; MaxFileBytes bounds the whole file.
const (
	MaxItemBytes = 1024
	MaxFileBytes = 16 << 10
)

// Parse returns the policies of a policy file, in order: each top-level list item, a line starting with "- ", is
// one policy, written on one line. Headings, blank lines and paragraphs are commentary and are not returned. An
// indented line under an item, a blank item, a duplicate item, an item over MaxItemBytes or holding a CR, a file
// over MaxFileBytes and a file that is not valid UTF-8 are refused. An empty file has no policies.
func Parse(data []byte) ([]string, error) {
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", Path, len(data), MaxFileBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%s is not valid UTF-8", Path)
	}
	var out []string
	seen := map[string]int{}
	item := 0
	for i, line := range strings.Split(string(data), "\n") {
		n := i + 1
		switch {
		case strings.TrimSpace(line) == "":
			continue
		case line[0] == ' ' || line[0] == '\t':
			if item > 0 {
				return nil, fmt.Errorf("%s line %d: an indented line continues the item on line %d; write each policy on one line", Path, n, item)
			}
			continue
		case line != "-" && !strings.HasPrefix(line, "- "):
			item = 0
			continue
		}
		item = n
		text := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		switch {
		case strings.ContainsRune(line, '\r'):
			return nil, fmt.Errorf("%s line %d: the item holds a carriage return", Path, n)
		case text == "":
			return nil, fmt.Errorf("%s line %d: the item is blank", Path, n)
		case len(text) > MaxItemBytes:
			return nil, fmt.Errorf("%s line %d: the item is %d bytes; the limit is %d", Path, n, len(text), MaxItemBytes)
		}
		if first, ok := seen[text]; ok {
			return nil, fmt.Errorf("%s line %d: the item repeats line %d", Path, n, first)
		}
		seen[text] = n
		out = append(out, text)
	}
	return out, nil
}

// Git reads one file at one revision; git.Program is the real one.
type Git interface {
	Show(ctx context.Context, repo, rev, path string) (git.Shown, error)
}

var _ Git = git.Program{}

// Read is one repository's policies as read from Ref.
type Read struct {
	// Commit is the commit Ref was at; empty when Ref was not read.
	Commit string
	// Policies are the file's policies; none when the file is absent.
	Policies []string
	// NotRead is set when Ref does not exist yet, as before the first fetch: not an error.
	NotRead bool
	// Err says why the file was refused or could not be read.
	Err error
}

// Load reads the policies of the repository at repo from Ref, never from a task branch or a work tree.
func Load(ctx context.Context, g Git, repo string) Read {
	shown, err := g.Show(ctx, repo, Ref, Path)
	switch {
	case errors.Is(err, git.ErrNoRevision):
		return Read{NotRead: true}
	case err != nil:
		return Read{Err: err}
	case shown.Absent:
		return Read{Commit: shown.Commit}
	}
	p, err := Parse(shown.Data)
	if err != nil {
		return Read{Commit: shown.Commit, Err: err}
	}
	return Read{Commit: shown.Commit, Policies: p}
}
