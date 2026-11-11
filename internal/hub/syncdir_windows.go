//go:build windows

package hub

// syncDir: Windows has no handle to sync a directory with; NTFS journals the rename itself. The hub runs on Linux;
// this keeps the package building for the Windows vet.
func syncDir(string) error { return nil }
