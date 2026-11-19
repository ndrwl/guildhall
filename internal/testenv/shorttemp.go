package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TempT is the part of testing.TB that ShortTempDir calls.
type TempT interface {
	Helper()
	TempDir() string
	Cleanup(func())
}

// ShortTempDir returns a temporary directory whose forward-slash form fits
// accepts, for a test whose paths must pass a rule the default temporary
// directory can break, such as the Steward roster's Windows copy_dir, which
// refuses the ~ of C:/Users/RUNNER~1/... The rule stays with the caller.
//
// It tries, in order: a new directory under RUNNER_TEMP, when that is set,
// removed at cleanup with any removal error ignored, since a Windows handle can
// delay removal; t.TempDir(); and that directory with its links resolved. When
// none fits, it returns an error naming each candidate and fits' reason for
// it, and the caller decides whether to skip or fail.
func ShortTempDir(t TempT, fits func(slashDir string) error) (string, error) {
	t.Helper()
	var refused []string
	try := func(dir string) bool {
		err := fits(filepath.ToSlash(dir))
		if err != nil {
			refused = append(refused, fmt.Sprintf("%s: %v", filepath.ToSlash(dir), err))
		}
		return err == nil
	}
	if runner := os.Getenv("RUNNER_TEMP"); runner != "" {
		dir, err := os.MkdirTemp(runner, "gh")
		switch {
		case err != nil:
			refused = append(refused, fmt.Sprintf("RUNNER_TEMP %s: %v", filepath.ToSlash(runner), err))
		case try(dir):
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir, nil
		default:
			_ = os.RemoveAll(dir)
		}
	}
	temp := t.TempDir()
	if try(temp) {
		return temp, nil
	}
	resolved, err := filepath.EvalSymlinks(temp)
	if err != nil {
		refused = append(refused, fmt.Sprintf("%s resolved: %v", filepath.ToSlash(temp), err))
	} else if try(resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("no temporary directory fits: %s", strings.Join(refused, "; "))
}
