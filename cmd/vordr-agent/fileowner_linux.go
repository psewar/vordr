package main

import (
	"io/fs"
	"syscall"
)

// fileOwner is the uid a file belongs to; known is false where the file system does not say.
func fileOwner(fi fs.FileInfo) (uid int, known bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, false
	}
	return int(st.Uid), true
}
