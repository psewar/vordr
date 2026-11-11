//go:build linux

package apt

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

type lock int

const (
	lockFree lock = iota
	lockBusy
	lockUnknown
)

// lockState asks the kernel whether someone holds apt's lock (F_GETLK takes no lock itself). apt locks with fcntl;
// F_GETLK also reports open file description locks.
func lockState(path string) lock {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return lockFree
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return lockUnknown
	}
	defer f.Close()
	lk := unix.Flock_t{Type: unix.F_WRLCK}
	if err := unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lk); err != nil {
		return lockUnknown
	}
	if lk.Type == unix.F_UNLCK {
		return lockFree
	}
	return lockBusy
}
