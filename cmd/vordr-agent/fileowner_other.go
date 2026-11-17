//go:build !linux

package main

import "io/fs"

// fileOwner: install runs on Linux only and refuses elsewhere before it looks at a file's owner; here no owner is
// known, so nothing counts as root's.
func fileOwner(fs.FileInfo) (uid int, known bool) { return -1, false }
