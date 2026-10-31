//go:build !windows

package launcher

import (
	"io"
	"syscall"
)

// run replaces the launcher's image with the release at binary, run with args and env, so the release is the
// process its caller started, with the same standard streams; it returns only when the exec failed.
func run(binary string, args, env []string, _ io.Reader, _, _ io.Writer) (int, error) {
	return 1, syscall.Exec(binary, append([]string{binary}, args...), env)
}
