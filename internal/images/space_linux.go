//go:build linux

package images

import "golang.org/x/sys/unix"

// FreeBytes: space available to root's processes on the file system holding path; false when it cannot be read.
func FreeBytes(path string) (uint64, bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
