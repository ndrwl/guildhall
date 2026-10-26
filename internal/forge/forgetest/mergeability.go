package forgetest

import (
	"context"
	"fmt"
	"slices"

	"github.com/ndrwl/guildhall/internal/forge"
)

// SetMergeability sets subsequent observations; an empty Head means the requested head.
func (f *Fake) SetMergeability(m forge.Mergeability) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m.Files = slices.Clone(m.Files)
	f.mergeability = m
}

// Mergeability returns the scripted observation without touching the network.
func (f *Fake) Mergeability(ctx context.Context, m forge.Merge) (forge.Mergeability, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := m.Validate(); err != nil {
		return forge.Mergeability{}, err
	}
	out := f.mergeability
	out.Files = slices.Clone(out.Files)
	if out.Head == "" {
		out.Head = m.Head
	}
	if out.Head != m.Head {
		return forge.Mergeability{}, fmt.Errorf("%w: pull request moved from %s to %s", forge.ErrRefused, m.Head, out.Head)
	}
	if out.URL == "" {
		for _, pr := range f.prs[m.Remote] {
			if pr.Number == m.Number {
				out.URL = pr.URL
			}
		}
	}
	return out, nil
}
