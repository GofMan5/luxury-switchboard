//go:build linux

package secretstore

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestLinuxUnavailableKeyringEnvironment(t *testing.T) {
	if os.Getenv("SWITCHBOARD_TEST_NO_KEYRING") != "1" {
		t.Skip("requires an isolated Linux session without Secret Service")
	}
	if _, err := linuxMasterKey(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing Secret Service was not classified as unavailable: %T %v", err, err)
	}
}

func TestLinuxPayloadRoundTripAndTamperRejection(t *testing.T) {
	key := bytes.Repeat([]byte{7}, linuxKeyBytes)
	plaintext := []byte("write-only provider credential")
	payload, err := sealLinux(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openLinux(payload, key)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("payload did not round-trip: opened=%q err=%v", opened, err)
	}
	payload[len(payload)-1] ^= 1
	if _, err := openLinux(payload, key); err == nil {
		t.Fatal("tampered payload was accepted")
	}
}

func TestLinuxKeyringFailureIsClassified(t *testing.T) {
	keyring.MockInitWithError(errors.New("injected keyring failure"))
	if _, err := linuxMasterKey(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("keyring failure was not classified as unavailable: %v", err)
	}
}
