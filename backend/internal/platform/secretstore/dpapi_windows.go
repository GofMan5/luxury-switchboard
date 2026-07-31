//go:build windows

package secretstore

import (
	"crypto/sha256"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxPlaintextBytes = 4 * 1024 * 1024

var entropy = sha256.Sum256([]byte("LuxuryPrivate.Switchboard.KeyPool.v2"))

func Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > maxPlaintextBytes {
		return nil, errors.New("secret payload size is invalid")
	}
	input := dataBlob(plaintext)
	optionalEntropy := dataBlob(entropy[:])
	output := windows.DataBlob{}
	if err := windows.CryptProtectData(
		&input,
		nil,
		&optionalEntropy,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&output,
	); err != nil {
		return nil, errors.New("DPAPI protection failed")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) //nolint:errcheck
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}

func Unprotect(ciphertext []byte) ([]byte, error) {
	return unprotect(ciphertext, true)
}

func UnprotectLegacy(ciphertext []byte) ([]byte, error) {
	return unprotect(ciphertext, false)
}

func unprotect(ciphertext []byte, withEntropy bool) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext) > maxPlaintextBytes*2 {
		return nil, errors.New("protected payload size is invalid")
	}
	input := dataBlob(ciphertext)
	var optionalEntropy *windows.DataBlob
	if withEntropy {
		value := dataBlob(entropy[:])
		optionalEntropy = &value
	}
	output := windows.DataBlob{}
	if err := windows.CryptUnprotectData(
		&input,
		nil,
		optionalEntropy,
		0,
		nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&output,
	); err != nil {
		return nil, errors.New("DPAPI unprotection failed")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data))) //nolint:errcheck
	if output.Size > maxPlaintextBytes {
		return nil, errors.New("unprotected payload is too large")
	}
	return append([]byte(nil), unsafe.Slice(output.Data, output.Size)...), nil
}

func dataBlob(value []byte) windows.DataBlob {
	if len(value) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
}
