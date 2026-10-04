//go:build linux || darwin

package secretstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/appdata"
	"github.com/zalando/go-keyring"
	"golang.org/x/sys/unix"
)

const (
	linuxKeyBytes      = 32
	linuxPayloadFormat = 1
	linuxKeyService    = "one.luxuryprivate.switchboard"
	linuxKeyUser       = "encrypted-files-v1"
	maxPlaintextBytes  = 4 * 1024 * 1024
)

const linuxAdditionalData = "LuxuryPrivate.Switchboard.SecretStore.v1"

func Protect(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > maxPlaintextBytes {
		return nil, errors.New("secret payload size is invalid")
	}
	key, err := linuxMasterKey()
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return sealLinux(plaintext, key)
}

func Unprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext) > maxPlaintextBytes*2 {
		return nil, errors.New("protected payload size is invalid")
	}
	key, err := linuxMasterKey()
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return openLinux(ciphertext, key)
}

func UnprotectLegacy([]byte) ([]byte, error) {
	return nil, errors.New("legacy DPAPI storage is not available on this platform")
}

func linuxMasterKey() ([]byte, error) {
	encoded, err := keyring.Get(linuxKeyService, linuxKeyUser)
	if err == nil {
		return decodeLinuxKey(encoded)
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, unavailable(keyringBackend + " is unavailable")
	}
	return createLinuxMasterKey()
}

func createLinuxMasterKey() ([]byte, error) {
	root, err := appdata.Root()
	if err != nil || os.MkdirAll(root, 0o700) != nil {
		return nil, unavailable("secure key storage is unavailable")
	}
	lock, err := os.OpenFile(filepath.Join(root, ".secret-store.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, unavailable("secure key storage is unavailable")
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return nil, unavailable("secure key storage is unavailable")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck

	if encoded, err := keyring.Get(linuxKeyService, linuxKeyUser); err == nil {
		return decodeLinuxKey(encoded)
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return nil, unavailable(keyringBackend + " is unavailable")
	}
	generated := make([]byte, linuxKeyBytes)
	if _, err := rand.Read(generated); err != nil {
		return nil, errors.New("secure random source is unavailable")
	}
	defer clear(generated)
	if err := keyring.Set(linuxKeyService, linuxKeyUser, base64.RawStdEncoding.EncodeToString(generated)); err != nil {
		return nil, unavailable(keyringBackend + " could not store the encryption key")
	}
	stored, err := keyring.Get(linuxKeyService, linuxKeyUser)
	if err != nil {
		return nil, unavailable(keyringBackend + " could not verify the encryption key")
	}
	return decodeLinuxKey(stored)
}

func unavailable(message string) error {
	return fmt.Errorf("%w: %s", ErrUnavailable, message)
}

func decodeLinuxKey(encoded string) ([]byte, error) {
	key, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(key) != linuxKeyBytes {
		clear(key)
		return nil, errors.New(keyringBackend + " contains an invalid encryption key")
	}
	return key, nil
}

func sealLinux(plaintext, key []byte) ([]byte, error) {
	gcm, err := linuxGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("secure random source is unavailable")
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(linuxAdditionalData))
	payload := make([]byte, 1, 1+len(nonce)+len(sealed))
	payload[0] = linuxPayloadFormat
	payload = append(payload, nonce...)
	payload = append(payload, sealed...)
	return payload, nil
}

func openLinux(payload, key []byte) ([]byte, error) {
	gcm, err := linuxGCM(key)
	if err != nil {
		return nil, err
	}
	if len(payload) < 1+gcm.NonceSize()+gcm.Overhead() || payload[0] != linuxPayloadFormat {
		return nil, errors.New("protected payload is invalid")
	}
	nonceEnd := 1 + gcm.NonceSize()
	plaintext, err := gcm.Open(nil, payload[1:nonceEnd], payload[nonceEnd:], []byte(linuxAdditionalData))
	if err != nil {
		return nil, errors.New("protected payload could not be decrypted")
	}
	return plaintext, nil
}

func linuxGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("encryption key is invalid")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("encryption is unavailable")
	}
	return gcm, nil
}
