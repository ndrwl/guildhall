package artisantest

import (
	"errors"

	"github.com/ndrwl/guildhall/internal/artisan"
)

func KillVerified(id artisan.Identity) error {
	if lv, err := artisan.Probe(id); lv != artisan.Alive {
		return err
	}
	return errors.ErrUnsupported
}
