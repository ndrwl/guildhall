package quality_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/render"
)

func TestJudgeDoesNotInferHistoricalFamilyFromCurrentConfig(t *testing.T) {
	for _, profile := range []string{"builder-claude", "", "removed-profile"} {
		t.Run("profile="+profile, func(t *testing.T) {
			original := strings.Replace(judgeConfig, `quality_judge_profiles = "judge-fake, judge-claude"`, `quality_judge_profiles = "judge-claude, judge-fake"`, 1)
			e := newEnv(t, original)
			k := artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}
			m := render.Manifest{Schema: render.ManifestSchema, Identity: render.Identity{Repository: "app", Task: "t-1", Step: "build", Kind: "build", Attempt: 1, Profile: profile}}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			e.write(filepath.Join(k.Dir(e.judge.StateDir), "prompt", "manifest.json"), string(data))
			changed := strings.Replace(original, "[profile.builder-claude]\nadapter = \"claude\"", "[profile.builder-claude]\nadapter = \"fake\"", 1)
			e.judge.Config, err = config.Parse([]byte(changed))
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.judge.Start(context.Background(), "app", "t-1", 1)
			if err != nil {
				t.Fatal(err)
			}
			if got.Judge.Profile != "judge-claude" || got.Judge.Builder != "" || got.Judge.SeparateFamily || got.Judge.Reason != "the builder's family is unknown" {
				t.Fatalf("choice after configuration drift = %+v", got.Judge)
			}
		})
	}
}
