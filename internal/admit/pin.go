package admit

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// PinRefusal is the refusal for the copy in dir whose release.pin and newest release-pin store event, newest (nil
// for none), disagree (docs/releases.md#the-pin): either present without the other, another version, or a pin
// file that cannot be read. It is "" when they agree, including for a copy with neither, which has not adopted.
func PinRefusal(dir string, newest *store.ReleasePin) string {
	pin, err := release.ReadPin(dir)
	absent := errors.Is(err, fs.ErrNotExist)
	path := release.PinPath(dir)
	switch {
	case err != nil && !absent:
		return fmt.Sprintf("%s cannot be read (%v), so this copy's release pin is unknown; repair it with guildhall release use <version>", path, err)
	case absent && newest == nil:
		return ""
	case absent:
		return fmt.Sprintf("%s is missing but this copy's store pins release %s; repair it with guildhall release use %s", path, newest.Version, newest.Version)
	case newest == nil:
		return fmt.Sprintf("%s names release %s but this copy's store records no release pin; repair it with guildhall release use %s", path, pin, pin)
	case newest.Version != pin:
		return fmt.Sprintf("%s names release %s but this copy's store pins release %s; repair it with guildhall release use %s", path, pin, newest.Version, newest.Version)
	}
	return ""
}
