package forge_test

import (
	"fmt"
	"os/exec"
	"regexp"
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
		"pr edit":     {values: []string{"--repo", "--title", "--body"}, positional: 1},
		"pr view":     {values: []string{"--repo", "--json"}, positional: 1},
		"pr merge":    {values: []string{"--repo", "--match-head-commit", "--subject", "--body"}, bools: []string{"--squash"}, positional: 1},
		"pr close":    {values: []string{"--repo", "--comment"}, positional: 1},
		"api graphql": {values: []string{"--hostname", "-f"}},
		// gh api <endpoint> is a REST GET; the only endpoint read is a job's logs.
		"api jobs-logs": {values: []string{"--hostname"}, bools: []string{"--allow-escape-sequences"}},
		// gh api <endpoint> --method POST: the only endpoint written is a job's rerun.
		"api jobs-rerun": {values: []string{"--hostname", "--method"}},
	}
	cmd := ghCommand(args)
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

// jobLogsEndpoint is the REST endpoint of one Actions job's log, the one gh api endpoint besides graphql.
var jobLogsEndpoint = regexp.MustCompile(`^repos/([^/]+)/([^/]+)/actions/jobs/([0-9]+)/logs$`)

// jobRerunEndpoint is the REST endpoint that re-runs one Actions job.
var jobRerunEndpoint = regexp.MustCompile(`^repos/([^/]+)/([^/]+)/actions/jobs/([0-9]+)/rerun$`)

// ghCommand names the command of args: its first two words, with a job logs endpoint named api jobs-logs and a
// job rerun endpoint api jobs-rerun.
func ghCommand(args []string) string {
	switch {
	case args[0] == "api" && jobLogsEndpoint.MatchString(args[1]):
		return "api jobs-logs"
	case args[0] == "api" && jobRerunEndpoint.MatchString(args[1]):
		return "api jobs-rerun"
	}
	return strings.Join(args[:2], " ")
}

func ghCallShapes() [][]string {
	return [][]string{
		{"repo", "view", "github.com/acme/widgets", "--json", "defaultBranchRef"},
		{"pr", "list", "--repo", "github.com/acme/widgets", "--head", "guildhall/t7", "--state", "open", "--json", "number,url,state,headRefName,headRefOid,mergeCommit,title,body"},
		{"pr", "create", "--repo", "github.com/acme/widgets", "--head", "guildhall/t7", "--title", "Task t7", "--body", "Body text"},
		{"pr", "view", "1", "--repo", "github.com/acme/widgets", "--json", "number,url,state,headRefName,headRefOid,mergeCommit"},
		{"pr", "view", "1", "--repo", "github.com/acme/widgets", "--json", "url,state,headRefOid,baseRefName,mergeable,mergeStateStatus"},
		{"pr", "merge", "1", "--repo", "github.com/acme/widgets", "--squash", "--match-head-commit", strings.Repeat("a", 40), "--subject", "Task t7 (#1)", "--body", ""},
		{"api", "graphql", "--hostname", "github.com", "-f", "query=query { viewer { login } }", "-f", "owner=acme", "-f", "name=widgets", "-f", "oid=" + strings.Repeat("a", 40)},
		{"pr", "edit", "1", "--repo", "github.com/acme/widgets", "--title", "Task t7", "--body", "Body text"},
		{"api", "repos/acme/widgets/actions/jobs/123/logs", "--hostname", "github.com", "--allow-escape-sequences"},
		{"pr", "close", "1", "--repo", "github.com/acme/widgets", "--comment", "Cancelled by Guildhall: duplicate"},
		{"api", "repos/acme/widgets/actions/jobs/123/rerun", "--method", "POST", "--hostname", "github.com"},
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
	// A cancelled task's branch is kept: the stand-in refuses a close that would delete it.
	for _, del := range []string{"--delete-branch", "-d"} {
		bad := append(slices.Clone(ghCallShapes()[7]), del)
		if code := runGH(&ghState{}, "", bad); code != 2 {
			t.Fatalf("close %s = %d, want 2", del, code)
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
