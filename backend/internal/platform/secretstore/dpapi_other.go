//go:build !windows

package secretstore

import "errors"

func Protect([]byte) ([]byte, error) {
	return nil, errors.New("secure key storage is not available on this platform")
}

func Unprotect([]byte) ([]byte, error) {
	return nil, errors.New("secure key storage is not available on this platform")
}

func UnprotectLegacy([]byte) ([]byte, error) {
	return nil, errors.New("secure key storage is not available on this platform")
}
