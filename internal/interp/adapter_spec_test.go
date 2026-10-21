package interp_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
)

func TestSpecMapsOntoAdapterSpec(t *testing.T) {
	for name, a := range map[string]interptest.Attempt{
		"build":            interptest.Build(t, "spec-v1.md"),
		"automated review": interptest.AutomatedReview(t, "spec-v2.md"),
	} {
		t.Run(name, func(t *testing.T) {
			a.Adapter = interp.FakeAdapter
			spec := a.Render(t)
			if err := spec.Validate(); err != nil {
				t.Fatal(err)
			}
			got := spec.AdapterSpec()
			want := adapter.Spec{Identity: spec.Identity, WorkDir: spec.WorkDir, PromptFile: spec.PromptFile, ResultFile: spec.ResultFile}
			if got.Identity != want.Identity || got.WorkDir != want.WorkDir || got.PromptFile != want.PromptFile || got.ResultFile != want.ResultFile || got.Env != nil {
				t.Errorf("AdapterSpec() = %+v, want %+v", got, want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("mapped spec refused: %v", err)
			}
		})
	}
}
