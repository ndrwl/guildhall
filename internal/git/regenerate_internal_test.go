package git

import (
	"slices"
	"testing"
)

// TestRegenerateEnv: a regenerate command's environment keeps everything but the git variables that would point its
// git at another repository than the scratch worktree.
func TestRegenerateEnv(t *testing.T) {
	got := regenerateEnv([]string{"PATH=/bin", "GIT_DIR=/x/.git", "GIT_WORK_TREE=/x", "GIT_INDEX_FILE=/x/index", "GIT_AUTHOR_NAME=a", "HOME=/h"})
	if want := []string{"PATH=/bin", "GIT_AUTHOR_NAME=a", "HOME=/h"}; !slices.Equal(got, want) {
		t.Errorf("regenerateEnv = %q, want %q", got, want)
	}
}
