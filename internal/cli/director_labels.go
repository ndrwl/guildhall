package cli

import (
	"time"

	"github.com/ndrwl/guildhall/internal/lanterns"
	"github.com/ndrwl/guildhall/internal/store"
)

// refreshLabels is advisory and bounded in frequency, independent of rendering,
// model turns and Claude's optional function hooks. Store inspection is read-only.
func (w *sessionWatch) refreshLabels(now time.Time) {
	if w.copyDir == "" || (!w.labelsAt.IsZero() && now.Sub(w.labelsAt) < 15*time.Second) {
		return
	}
	w.labelsAt = now
	view, err := withStore(w.storePath, true, func(s *store.Store) (store.DirectorView, error) { return s.DirectorView() })
	if err == nil {
		_ = lanterns.PublishLabels(w.copyDir, view, now)
	}
}
