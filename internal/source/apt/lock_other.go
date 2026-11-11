//go:build !linux

package apt

type lock int

const (
	lockFree lock = iota
	lockBusy
	lockUnknown
)

// apt only runs on Linux; elsewhere the lock state cannot be known.
func lockState(string) lock { return lockUnknown }
