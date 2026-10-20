package artisantest

import (
	"errors"
	"strconv"

	"github.com/ndrwl/guildhall/internal/artisan"
	"golang.org/x/sys/windows"
)

func KillVerified(id artisan.Identity) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(id.PID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return err
	}
	if strconv.FormatInt(c.Nanoseconds(), 10) != id.Token {
		return nil
	}
	return windows.TerminateProcess(h, 1)
}
