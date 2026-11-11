//go:build linux

package hub

import "syscall"

// freeBytes: the space a non-root writer has left on dir's filesystem; known false when it cannot be told.
func freeBytes(dir string) (free uint64, known bool) {
	var st syscall.Statfs_t
	if syscall.Statfs(dir, &st) != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
