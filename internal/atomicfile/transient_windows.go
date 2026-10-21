package atomicfile

import (
	"errors"

	"golang.org/x/sys/windows"
)

// transient reports an error Windows returns while another process holds
// the same file for a rename or an open.
func transient(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
