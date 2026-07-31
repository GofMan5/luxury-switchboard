package encryptedfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/luxuryprivate/switchboard/backend/internal/platform/atomicfile"
	"github.com/luxuryprivate/switchboard/backend/internal/platform/secretstore"
)

const DefaultMaxPlaintext = 4 * 1024 * 1024

func Load(path string, magic []byte, maxPlaintext int, target any) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("encrypted settings could not be read")
	}
	defer file.Close()
	maxEncrypted := int64(maxPlaintext) * 2
	raw, err := io.ReadAll(io.LimitReader(file, maxEncrypted+1))
	if err != nil || int64(len(raw)) > maxEncrypted || len(raw) <= len(magic) || !bytes.Equal(raw[:len(magic)], magic) {
		return false, errors.New("encrypted settings could not be read")
	}
	plaintext, err := secretstore.Unprotect(raw[len(magic):])
	if err != nil {
		return false, err
	}
	defer clear(plaintext)
	if len(plaintext) > maxPlaintext {
		return false, errors.New("encrypted settings are too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return false, errors.New("encrypted settings are invalid")
	}
	return true, nil
}

func Save(path string, magic []byte, maxPlaintext int, value any) error {
	plaintext, err := json.Marshal(value)
	if err != nil || len(plaintext) > maxPlaintext {
		return errors.New("settings could not be encoded")
	}
	protected, err := secretstore.Protect(plaintext)
	clear(plaintext)
	if err != nil {
		return err
	}
	payload := append(append([]byte(nil), magic...), protected...)
	clear(protected)
	defer clear(payload)
	return atomicfile.Replace(path, payload, 0o600)
}
