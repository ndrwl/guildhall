package cli

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// ownSHA256 is the sha256 of the test binary, as an in-process command hashes its own image.
func ownSHA256(t *testing.T) string {
	t.Helper()
	app, _, _ := newTestApp(nil, nil)
	sum, err := app.imageSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

// TestVersionStartupComparesTheSupervisorBySHA256: with the same "dev" release, --startup warns when the session's
// supervisor recorded another binary sha256 and is silent for its own; a supervisor that recorded none is compared
// by release.
func TestVersionStartupComparesTheSupervisorBySHA256(t *testing.T) {
	setVersion(t, "dev")
	other := strings.Repeat("e", 64)
	for _, tt := range []struct {
		name, release, sha256, want string
	}{
		{"same binary", "dev", ownSHA256(t), ""},
		{"another binary", "dev", other, "warning: this binary is not the one running this session's supervisor (dev sha256:eeeeeeeeeeee)\n"},
		{"no sha, same release", "dev", "", ""},
		{"no sha, another release", "v1.2.2", "", "warning: this binary is not the one running this session's supervisor (v1.2.2)\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := versionCopy(t, "git@github.com:example/app.git")
			withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
				return s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
					Release: tt.release, BinarySHA256: tt.sha256, ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
			})
			env := map[string]string{copyEnvVar: dir, session.NumberEnv: "1"}
			if code, out, _ := runVersion(t, testBuildInfo(nil), &fakeVersionGit{}, env, "--startup"); code != 0 || out != "guildhall abd97485 2026-10-27\n"+tt.want {
				t.Errorf("version --startup = %d\n%s\nwant the warning %q", code, out, tt.want)
			}
		})
	}
}

// TestDirectorStatusComparesTheSupervisorBinary: the feed says whether the newest session's supervisor runs the
// guildhall on PATH, by sha256.
func TestDirectorStatusComparesTheSupervisorBinary(t *testing.T) {
	bin := t.TempDir()
	name := "guildhall"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(bin, name)
	writeFile(t, path, "a guildhall build\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	onPath, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, sha256 string
		differs      bool
		summary      string
	}{
		{"same", onPath, false, "session 1's supervisor runs the guildhall on PATH"},
		{"another", strings.Repeat("e", 64), true, "session 1's supervisor runs guildhall sha256:eeeeeeeeeeee, not the guildhall on PATH " + path},
		{"none recorded", "", false, "recorded no binary sha256"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := versionCopy(t, "git@github.com:example/app.git")
			withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
				return s.StartDirectorSession(store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"},
					Release: "dev", BinarySHA256: tt.sha256, ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
			})
			app, stdout, stderr := newTestApp(nil, nil)
			app.LookPath = exec.LookPath
			if code := app.Run([]string{"director", "status", "--json", "--copy", dir}); code != 0 {
				t.Fatalf("status: exit %d, stderr %s", code, stderr)
			}
			var out struct {
				Session          statusSession           `json:"session"`
				SupervisorBinary *supervisorBinaryStatus `json:"supervisor_binary"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			b := out.SupervisorBinary
			if b == nil || b.Differs != tt.differs || b.SHA256 != tt.sha256 || b.PathSHA256 != onPath || !strings.Contains(b.Summary, tt.summary) || out.Session.BinarySHA256 != tt.sha256 {
				t.Errorf("supervisor_binary = %+v, session binary %q; want differs %v and %q", b, out.Session.BinarySHA256, tt.differs, tt.summary)
			}
		})
	}
}

// TestRefusalReasonIsOneBoundedLine: a refusal's reason names what failed, the error and its output's last line,
// on one line within the store's bound.
func TestRefusalReasonIsOneBoundedLine(t *testing.T) {
	got := refusalReason("director start --check", errors.New("exit status 1"), []byte("config: admitted\nstore:  refused\n  newer  schema\n"))
	if got != "director start --check: exit status 1: newer schema" {
		t.Errorf("reason = %q", got)
	}
	long := refusalReason("exec", os.ErrPermission, []byte(strings.Repeat("é", store.MaxRefusedReasonBytes)))
	if len(long) > store.MaxRefusedReasonBytes || strings.ContainsRune(long, '\n') || !strings.HasPrefix(long, "exec: permission denied: é") {
		t.Errorf("long reason = %d bytes, %q...", len(long), long[:min(40, len(long))])
	}
}
