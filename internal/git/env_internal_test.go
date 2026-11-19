package git

import (
	"slices"
	"testing"
)

// TestCommandEnv: git always runs with GIT_TERMINAL_PROMPT=0 and the extra entries; batch mode adds
// GIT_SSH_COMMAND=ssh -o BatchMode=yes only when the environment sets neither GIT_SSH_COMMAND nor GIT_SSH, even empty.
func TestCommandEnv(t *testing.T) {
	const batch = "GIT_SSH_COMMAND=ssh -o BatchMode=yes"
	for _, tc := range []struct {
		name     string
		environ  []string
		batchSSH bool
		want     []string
	}{
		{"off", []string{"HOME=/h"}, false, []string{"HOME=/h", "GIT_TERMINAL_PROMPT=0", "X=1"}},
		{"on, nothing set", []string{"HOME=/h"}, true, []string{"HOME=/h", "GIT_TERMINAL_PROMPT=0", batch, "X=1"}},
		{"on, GIT_SSH_COMMAND set", []string{"GIT_SSH_COMMAND=ssh -i k"}, true, []string{"GIT_SSH_COMMAND=ssh -i k", "GIT_TERMINAL_PROMPT=0", "X=1"}},
		{"on, GIT_SSH set", []string{"GIT_SSH=/usr/bin/plink"}, true, []string{"GIT_SSH=/usr/bin/plink", "GIT_TERMINAL_PROMPT=0", "X=1"}},
		{"on, GIT_SSH_COMMAND empty", []string{"GIT_SSH_COMMAND="}, true, []string{"GIT_SSH_COMMAND=", "GIT_TERMINAL_PROMPT=0", "X=1"}},
		{"on, a look-alike name", []string{"GIT_SSH_COMMANDS=x"}, true, []string{"GIT_SSH_COMMANDS=x", "GIT_TERMINAL_PROMPT=0", batch, "X=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environ := slices.Clip(slices.Clone(tc.environ))
			before := slices.Clone(environ)
			got := commandEnv(environ, tc.batchSSH, "X=1")
			if !slices.Equal(got, tc.want) {
				t.Errorf("commandEnv = %q, want %q", got, tc.want)
			}
			if !slices.Equal(environ, before) {
				t.Errorf("commandEnv changed its input to %q", environ)
			}
		})
	}
}
