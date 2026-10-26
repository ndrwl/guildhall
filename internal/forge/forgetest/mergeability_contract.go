package forgetest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
)

// MergeabilityReader is the read-only PR observation boundary tested by this suite.
type MergeabilityReader interface {
	Mergeability(context.Context, forge.Merge) (forge.Mergeability, error)
}

// MergeabilityHarness holds a PR with known, stable conflicting/behind evidence.
// The real fixture uses git/strict gh; the fake fixture scripts the same evidence.
type MergeabilityHarness struct {
	Reader  MergeabilityReader
	Request forge.Merge
	Want    forge.Mergeability
}

// RunMergeabilityContract checks identity, evidence, replay, ownership of returned
// slices and refusal of malformed requests and stale expected heads.
func RunMergeabilityContract(t *testing.T, h MergeabilityHarness) {
	t.Helper()
	ctx := context.Background()
	for range 2 {
		got, err := h.Reader.Mergeability(ctx, h.Request)
		if err != nil || !reflect.DeepEqual(got, h.Want) {
			t.Fatalf("Mergeability = %+v, %v; want %+v", got, err, h.Want)
		}
		if len(got.Files) > 0 {
			got.Files[0] = "caller-mutated"
		}
	}
	for _, bad := range []forge.Merge{{}, {Repository: h.Request.Repository, Remote: h.Request.Remote, Number: 0, Head: h.Request.Head}, {Repository: h.Request.Repository, Remote: h.Request.Remote, Number: h.Request.Number, Head: "short"}} {
		if _, err := h.Reader.Mergeability(ctx, bad); !errors.Is(err, forge.ErrInvalid) {
			t.Errorf("malformed request %+v = %v", bad, err)
		}
	}
	moved := h.Request
	moved.Head = strings.Repeat("e", 40)
	if moved.Head == h.Request.Head {
		moved.Head = strings.Repeat("f", 40)
	}
	if _, err := h.Reader.Mergeability(ctx, moved); !errors.Is(err, forge.ErrRefused) {
		t.Errorf("moved head = %v", err)
	}
}
