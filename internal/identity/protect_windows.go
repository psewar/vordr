//go:build windows

package identity

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func protect(b []byte) ([]byte, error) { return dpapi(b, true) }

func unprotect(b []byte) ([]byte, error) { return dpapi(b, false) }

// dpapi with the machine scope: the service account and administrators of this machine can read the key, a copy of
// the file on another machine cannot.
func dpapi(b []byte, protect bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
	var out windows.DataBlob
	flags := uint32(windows.CRYPTPROTECT_UI_FORBIDDEN | windows.CRYPTPROTECT_LOCAL_MACHINE)
	var err error
	if protect {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, flags, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, flags, &out)
	}
	if err != nil {
		return nil, fmt.Errorf("DPAPI: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, out.Size)
	copy(res, unsafe.Slice(out.Data, out.Size))
	return res, nil
}
