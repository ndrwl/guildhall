package forge_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
)

func TestFakeMergeabilityContract(t *testing.T) {
	for _, state := range []string{"conflicting", "behind", ""} {
		t.Run(state, func(t *testing.T) {
			f := forgetest.NewFake()
			head := strings.Repeat("a", 40)
			evidence := forge.Mergeability{URL: "https://forge.invalid/1", Head: head, Base: strings.Repeat("b", 40), State: state, Behind: 1, Files: []string{"one"}}
			f.SetMergeability(evidence)
			evidence.Files[0] = "caller-mutated"
			evidence.Files = []string{"one"}
			forgetest.RunMergeabilityContract(t, forgetest.MergeabilityHarness{Reader: f, Request: forge.Merge{Repository: "/repo", Remote: remote, Number: 1, Head: head}, Want: evidence})
		})
	}
}
