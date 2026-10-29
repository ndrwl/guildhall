package adapter

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
)

// SessionSpec is what Guildhall gives a runtime for one interactive Director session (docs/director.md#start).
// Unlike an attempt, a session has no result file: it ends when its runtime ends.
type SessionSpec struct {
	// Dir is the absolute session directory the runtime runs in; its skills are already rendered under the
	// runtime's skills directory. It lies under Copy, so the session's commands find the copy by copy discovery
	// (docs/running.md#copies).
	Dir string
	// Copy is the absolute Director copy directory.
	Copy string
	// RolePrompt is the absolute path of the rendered role prompt the runtime adds to its system prompt.
	RolePrompt string
	// FirstMessage is the one-line instruction the session starts with.
	FirstMessage string
	// Model and Effort are passed as given where the runtime supports them; empty means its default.
	Model, Effort string
	// Env holds extra NAME=VALUE entries for the session's environment.
	Env []string
}

// Validate checks that every path is absolute, that the first message is one non-blank line and that every
// environment entry is NAME=VALUE.
func (s SessionSpec) Validate() error {
	for name, p := range map[string]string{"session directory": s.Dir, "copy": s.Copy, "role prompt": s.RolePrompt} {
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%s %q must be an absolute path", name, p)
		}
	}
	if strings.TrimSpace(s.FirstMessage) == "" || strings.ContainsAny(s.FirstMessage, "\r\n") {
		return errors.New("first message must be one non-blank line")
	}
	for _, kv := range s.Env {
		if name, _, ok := strings.Cut(kv, "="); !ok || name == "" {
			return fmt.Errorf("environment entry %q is not NAME=VALUE", kv)
		}
	}
	return nil
}

// Under reports whether path is base or lies under it, compared cleaned, with symbolic links resolved where both
// exist, and ignoring case on Windows: the rule that a session directory lies under its copy.
func Under(base, path string) bool {
	if rb, err := filepath.EvalSymlinks(base); err == nil {
		if rp, err := filepath.EvalSymlinks(path); err == nil {
			base, path = rb, rp
		}
	}
	base, path = filepath.Clean(base), filepath.Clean(path)
	if runtime.GOOS == "windows" {
		base, path = strings.ToLower(base), strings.ToLower(path)
	}
	rel, err := filepath.Rel(base, path)
	return err == nil && filepath.IsLocal(rel)
}

// SessionEnv is the session's environment entries, spec's own; no entry names the copy.
func (s SessionSpec) SessionEnv() []string {
	return append([]string(nil), s.Env...)
}
