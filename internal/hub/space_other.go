//go:build !linux

package hub

// freeBytes: unknown outside Linux, where the hub does not run; the download then goes ahead as before.
func freeBytes(string) (uint64, bool) { return 0, false }
