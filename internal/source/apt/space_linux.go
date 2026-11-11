//go:build linux

package apt

import "golang.org/x/sys/unix"

// freeBytes: space available to root's processes on the file system holding path.
func freeBytes(path string) (uint64, bool) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
