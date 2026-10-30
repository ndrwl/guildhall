package cli

import (
	"path/filepath"
	"slices"
	"testing"
)

// The runtime's PWD is its session directory as given, after the supervisor's own PWD and before the spec's
// variables, so the commands it runs bare discover the copy spelled as --copy spells it.
func TestRuntimeEnvSetsPWD(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "copy", "director", "claude")
	got := runtimeEnv([]string{"HOME=/h", "PWD=/elsewhere"}, dir, []string{"GUILDHALL_SESSION=3"})
	want := []string{"HOME=/h", "PWD=/elsewhere", "PWD=" + dir, "GUILDHALL_SESSION=3"}
	if !slices.Equal(got, want) {
		t.Fatalf("runtimeEnv = %q, want %q", got, want)
	}
	if got := runtimeEnv([]string{"HOME=/h"}, "", nil); !slices.Equal(got, []string{"HOME=/h"}) {
		t.Fatalf("runtimeEnv with no dir = %q, want the environment unchanged", got)
	}
}
