package launch_test

import (
	"os"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/adapter/codex/codextest"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestRunRecordsACodexJudgesUsage (R9 of d170): an owned quality-judge attempt launched through the shipped Codex
// adapter, over the stub `codex`, records the tokens its turn reported as measured and its cost as unknown, since
// Codex reports no price; a turn that reports no usage leaves the spend unknown, not zero.
func TestRunRecordsACodexJudgesUsage(t *testing.T) {
	if u := codextest.Usage; u.Tokens != adapter.Measured || u.Cost != adapter.Unknown || u.InputTokens == 0 || u.OutputTokens == 0 {
		t.Fatalf("the stub's usage %+v is not measured tokens with an unknown cost", u)
	}
	measured := store.Spend{Tokens: store.SpendMeasured, InputTokens: codextest.Usage.InputTokens,
		CacheReadInputTokens: codextest.Usage.CacheReadInputTokens, CacheCreationInputTokens: codextest.Usage.CacheCreationInputTokens,
		OutputTokens: codextest.Usage.OutputTokens, Cost: store.SpendUnknown}
	for _, tc := range []struct {
		name string
		b    adaptertest.Behaviour
		want store.Spend
	}{
		{"usage", adaptertest.Succeed, measured},
		{"no usage", codextest.NoUsage, store.UnknownSpend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codextest.Install(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			a := newJudgeAttempt(t)
			spec := codextest.Lay(t, a.Identity(), t.TempDir(), codextest.Prompt(tc.b))
			spec.ResultFile = a.Files().ResultFile
			cmd, err := codex.Adapter{Executable: exe}.Command(spec)
			if err != nil {
				t.Fatal(err)
			}
			done, cancel := start(a, cmd)
			defer cancel()
			r := wait(t, done)
			if r.err != nil || r.r.Outcome.State != artisan.Finished || r.r.AttemptOutcome != store.AttemptSucceeded {
				t.Fatalf("Run() = %+v, %v; want a finished, succeeded judge", r.r, r.err)
			}
			if got := storedSpend(t, a); got == nil || *got != tc.want {
				t.Errorf("stored spend %+v; want %+v", got, tc.want)
			}
		})
	}
}
