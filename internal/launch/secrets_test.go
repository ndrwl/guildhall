package launch_test

import (
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/secrets"
)

// TestWorkerEnvWithholdsInjectedSecrets: an Artisan's worker gets no name the supervisor injected, only the names
// its command adds, which are those its profile's inherit_env lists, and the attempt marker last.
func TestWorkerEnvWithholdsInjectedSecrets(t *testing.T) {
	k := artisan.AttemptKey{Repository: "r", TaskID: "d1", Step: "build", Attempt: 1}
	environ := []string{"PATH=/bin", secrets.NamesEnv + "=API_KEY,DB_PASS", "API_KEY=injected-1", "DB_PASS=injected-2"}
	got := launch.WorkerEnv(environ, nil, k)
	want := []string{"PATH=/bin", secrets.NamesEnv + "=API_KEY,DB_PASS", k.Env()}
	if !slices.Equal(got, want) {
		t.Fatalf("env %q, want %q", got, want)
	}
	got = launch.WorkerEnv(environ, []string{"API_KEY=inherited", secrets.RedactEnv + "=API_KEY"}, k)
	if !slices.Contains(got, "API_KEY=inherited") || slices.Contains(got, "DB_PASS=injected-2") || slices.Contains(got, "API_KEY=injected-1") {
		t.Fatalf("env %q, want only the inherited API_KEY", got)
	}
}
