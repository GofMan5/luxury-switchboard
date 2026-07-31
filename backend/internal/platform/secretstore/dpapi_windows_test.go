//go:build windows

package secretstore

import (
	"bytes"
	"testing"
)

func TestDPAPIRoundTripDoesNotLeavePlaintext(t *testing.T) {
	plaintext := []byte("fixture-secret-must-not-appear")
	ciphertext, err := Protect(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, plaintext) {
		t.Fatal("DPAPI output contains plaintext")
	}
	decrypted, err := Unprotect(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatal("DPAPI round trip changed payload")
	}
}
