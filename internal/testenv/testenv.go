// Package testenv lets a CI job turn a test's skip for a missing platform
// capability into a failure.
//
// A test that needs an installed tool or a particular operating system skips
// by name when the host lacks it, so a local run proves what the host can. A CI
// job that is meant to prove that case names it in GUILDHALL_CI_REQUIRE, and
// the same skip then fails the job instead of passing silently.
// See docs/contracts/testenv.md.
package testenv

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// RequireEnv names the environment variable a CI job sets to the
// comma-separated names it requires, such as "pwsh,powershell,windows".
// Space around a name and empty entries are ignored; names are exact.
const RequireEnv = "GUILDHALL_CI_REQUIRE"

// Requirable lists the names RequireEnv may hold: the tools and platform whose
// absence a test skips today. A name outside it is refused, so a misspelt
// requirement cannot pass as an unmet one.
var Requirable = []string{"ksh", "powershell", "pwsh", "windows"}

// T is the part of testing.TB that SkipUnlessRequired calls.
type T interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Required returns the names in RequireEnv, or an error naming the first one
// that is not Requirable.
func Required() ([]string, error) {
	var names []string
	for _, name := range strings.Split(os.Getenv(RequireEnv), ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(Requirable, name) {
			return nil, fmt.Errorf("%s names %q, which is not one of %q", RequireEnv, name, Requirable)
		}
		names = append(names, name)
	}
	return names, nil
}

// SkipUnlessRequired skips t with the reason format and args, unless RequireEnv
// lists name; then it fails t with that reason and the requirement. It fails
// t when RequireEnv holds a name that is not Requirable. It calls exactly one
// of Skipf and Fatalf.
func SkipUnlessRequired(t T, name, format string, args ...any) {
	t.Helper()
	required, err := Required()
	if err != nil {
		t.Fatalf("%v", err)
		return
	}
	reason := fmt.Sprintf(format, args...)
	if slices.Contains(required, name) {
		t.Fatalf("%s=%s requires %s on this runner, but: %s", RequireEnv, os.Getenv(RequireEnv), name, reason)
		return
	}
	t.Skipf("%s", reason)
}
