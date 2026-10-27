package forge_test

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// These are the supported subset of gh's CLI used by the forge, not a complete
// emulator. Keep the command-specific flags and positional arity independent of
// the production argument builder. Audited against gh 2.102.0 command help.
func validateGHArgs(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("unknown command %q", args)
	}
	type shape struct {
		values, bools []string
		positional    int
	}
	shapes := map[string]shape{
		"repo view":   {values: []string{"--json"}, positional: 1},
		"pr list":     {values: []string{"--repo", "--head", "--state", "--json"}},
		"pr create":   {values: []string{"--repo", "--head", "--title", "--body"}},
		"pr view":     {values: []string{"--repo", "--json"}, positional: 1},
		"pr merge":    {values: []string{"--repo", "--match-head-commit"}, bools: []string{"--squash"}, positional: 1},
		"api graphql": {values: []string{"--hostname", "-f"}},
	}
	cmd := strings.Join(args[:2], " ")
	s, ok := shapes[cmd]
	if !ok {
		return fmt.Errorf("unknown command %q", cmd)
	}
	n := 0
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch {
		case slices.Contains(s.bools, a):
		case slices.Contains(s.values, a):
			i++
			if i == len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("%s does not take %s", cmd, a)
		default:
			n++
		}
	}
	if n != s.positional {
		return fmt.Errorf("%s takes %d positional arguments, got %d", cmd, s.positional, n)
	}
	return nil
}

func ghCallShapes() [][]string {
	return [][]string{
		{"repo", "view", "github.com/acme/widgets", "--json", "defaultBranchRef"},
		{"pr", "list", "--repo", "github.com/acme/widgets", "--head", "guildhall/t7", "--state", "open", "--json", "number,url,state,headRefName,headRefOid,mergeCommit"},
		{"pr", "create", "--repo", "github.com/acme/widgets", "--head", "guildhall/t7", "--title", "Task t7", "--body", "Body text"},
		{"pr", "view", "1", "--repo", "github.com/acme/widgets", "--json", "number,url,state,headRefName,headRefOid,mergeCommit"},
		{"pr", "view", "1", "--repo", "github.com/acme/widgets", "--json", "url,state,headRefOid,baseRefName,mergeable,mergeStateStatus"},
		{"pr", "merge", "1", "--repo", "github.com/acme/widgets", "--squash", "--match-head-commit", strings.Repeat("a", 40)},
		{"api", "graphql", "--hostname", "github.com", "-f", "query=query { viewer { login } }", "-f", "owner=acme", "-f", "name=widgets", "-f", "oid=" + strings.Repeat("a", 40)},
	}
}

func TestGHCommandShapes(t *testing.T) {
	for _, args := range ghCallShapes() {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			if err := validateGHArgs(args); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(args[2], "-") {
				bad := append(slices.Clone(args[:2]), args[3:]...)
				if code := runGH(&ghState{}, "", bad); code != 2 {
					t.Fatalf("missing positional argument: runGH(%q) = %d, want 2", bad, code)
				}
			}
			for name, bad := range map[string][]string{
				"unknown-flag":     append(slices.Clone(args), "--not-a-gh-flag"),
				"extra-positional": append(slices.Clone(args), "extra"),
				"missing-value":    append(slices.Clone(args), args[len(args)-2]),
			} {
				t.Run(name, func(t *testing.T) {
					if code := runGH(&ghState{}, "", bad); code != 2 {
						t.Fatalf("runGH(%q) = %d, want usage error 2", bad, code)
					}
				})
			}
		})
	}
	// The production bug: repo view has no --repo, unlike pr commands.
	bad := []string{"repo", "view", "--repo", "github.com/acme/widgets", "--json", "defaultBranchRef"}
	if code := runGH(&ghState{}, "", bad); code != 2 {
		t.Fatalf("repo view --repo = %d, want 2", code)
	}
	for _, bypass := range []string{"--admin", "--auto"} {
		bad := append(slices.Clone(ghCallShapes()[5]), bypass)
		if code := runGH(&ghState{}, "", bad); code != 2 {
			t.Fatalf("merge %s = %d, want 2", bypass, code)
		}
	}
}

func checkInstalledGHFlags(t *testing.T, args []string) {
	t.Helper()
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh is not installed")
	}
	cmd := exec.Command(gh, append(slices.Clone(args), "--help")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gh %q --help: %v\n%s", args, err, out)
	}
}

// --help still parses flags in gh, but exits before authentication or network.
// It does not validate positional arity; the stand-in contract above does that.
func TestInstalledGHFlags(t *testing.T) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Skip("gh is not installed")
	}
	for _, args := range ghCallShapes() {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			checkInstalledGHFlags(t, args)
		})
	}
	// Ensure --help is not masking the exact unsupported-flag regression.
	out, err := exec.Command(gh, "repo", "view", "--repo", "github.com/acme/widgets", "--json", "defaultBranchRef", "--help").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unknown flag: --repo") {
		t.Fatalf("unsupported repo flag: %v\n%s", err, out)
	}
}
