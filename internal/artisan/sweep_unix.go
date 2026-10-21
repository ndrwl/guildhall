//go:build unix

package artisan

import "errors"

// sweepMembers kills each member Contained lists with Kill.
func sweepMembers(c Containment, worker Identity) ([]Identity, error) {
	members, err := Contained(c, worker)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, m := range members {
		errs = append(errs, Kill(m))
	}
	return members, errors.Join(errs...)
}
