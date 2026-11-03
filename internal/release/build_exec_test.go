package release

import (
	"slices"
	"strings"
	"testing"
)

// TestBuildEnvDropsGitRedirects: git and go run without GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE, in any case,
// keeping the rest of the environment; git never prompts.
func TestBuildEnvDropsGitRedirects(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Setenv("Git_Index_File", "/elsewhere/.git/index")
	t.Setenv("GIT_COMMON_DIR", "/kept")
	for name, env := range map[string][]string{"git": gitEnv(), "go": goEnv()} {
		for _, kv := range env {
			k, _, _ := strings.Cut(kv, "=")
			if gitRedirect(strings.ToUpper(k)) {
				t.Errorf("%s runs with %s", name, kv)
			}
		}
		if !slices.Contains(env, "GIT_COMMON_DIR=/kept") {
			t.Errorf("%s runs without GIT_COMMON_DIR", name)
		}
	}
	if !slices.Contains(gitEnv(), "GIT_TERMINAL_PROMPT=0") {
		t.Error("git may prompt")
	}
}
