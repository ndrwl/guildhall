package artisan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
)

var bootID = sync.OnceValues(func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
})

// procStat returns the state, process group and start time of pid from
// /proc/<pid>/stat.
func procStat(pid int) (state byte, pgrp int, start string, err error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, "", err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, "", fmt.Errorf("malformed stat for %d", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, 0, "", fmt.Errorf("short stat for %d", pid)
	}
	if pgrp, err = strconv.Atoi(f[2]); err != nil {
		return 0, 0, "", err
	}
	return f[0][0], pgrp, f[19], nil
}

// Lookup returns the identity of the live process pid: its boot id and start
// time. A zombie is not live.
func Lookup(pid int) (Identity, error) {
	id, _, err := lookupGroup(pid)
	return id, err
}

func lookupGroup(pid int) (Identity, int, error) {
	boot, err := bootID()
	if err != nil {
		return Identity{}, 0, err
	}
	state, pgrp, start, err := procStat(pid)
	if errors.Is(err, fs.ErrNotExist) || err == nil && (state == 'Z' || state == 'X') {
		return Identity{}, 0, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, 0, err
	}
	return Identity{PID: pid, Token: boot + ":" + start}, pgrp, nil
}
