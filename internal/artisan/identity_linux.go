package artisan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var bootID = sync.OnceValues(func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
})

// procStat returns the state, parent, process group and start time of pid
// from /proc/<pid>/stat.
func procStat(pid int) (state byte, ppid, pgrp int, start string, err error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, 0, "", err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, 0, "", fmt.Errorf("malformed stat for %d", pid)
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, 0, 0, "", fmt.Errorf("short stat for %d", pid)
	}
	if ppid, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, 0, "", err
	}
	if pgrp, err = strconv.Atoi(f[2]); err != nil {
		return 0, 0, 0, "", err
	}
	return f[0][0], ppid, pgrp, f[19], nil
}

// Lookup returns the identity of the live process pid: its boot id and start
// time. A zombie is not live.
func Lookup(pid int) (Identity, error) {
	id, _, err := lookupGroup(pid)
	return id, err
}

func lookupGroup(pid int) (Identity, int, error) {
	id, pgrp, isZombie, err := readProcess(pid)
	if err == nil && isZombie {
		return Identity{}, 0, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	return id, pgrp, err
}

// lookupExited returns the identity of pid, live or exited and unreaped.
func lookupExited(pid int) (Identity, error) {
	id, _, _, err := readProcess(pid)
	return id, err
}

// readProcess returns the identity and process group of pid and whether it
// is a zombie; a pid with no process is errNoProcess.
func readProcess(pid int) (id Identity, pgrp int, isZombie bool, err error) {
	boot, err := bootID()
	if err != nil {
		return Identity{}, 0, false, err
	}
	state, _, pgrp, start, err := procStat(pid)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return Identity{}, 0, false, fmt.Errorf("pid %d: %w", pid, errNoProcess)
	}
	if err != nil {
		return Identity{}, 0, false, err
	}
	return Identity{PID: pid, Token: boot + ":" + start}, pgrp, state == 'Z' || state == 'X', nil
}
