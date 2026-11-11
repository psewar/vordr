//go:build !linux

package images

// Images are updated on Linux only; elsewhere the free space cannot be known.
func FreeBytes(string) (uint64, bool) { return 0, false }
