//go:build !windows

package identity

// No DPAPI outside Windows: the key file is protected by its 0600 mode.
func protect(b []byte) ([]byte, error) { return b, nil }

func unprotect(b []byte) ([]byte, error) { return b, nil }
