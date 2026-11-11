//go:build !windows

package hub

import "os"

// syncDir makes a rename in dir durable: the directory entry is on disk once it returns.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	return serr
}
