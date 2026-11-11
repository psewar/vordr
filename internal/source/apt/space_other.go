//go:build !linux

package apt

// apt only runs on Linux; elsewhere the free space cannot be known.
func freeBytes(string) (uint64, bool) { return 0, false }
