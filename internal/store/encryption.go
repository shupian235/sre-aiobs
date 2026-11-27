package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

func NewSettingsCipher(encoded string) (cipher.AEAD, error) {
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != 32 {
		return nil, errors.New("AIOBS_SETTINGS_KEY must be a Base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func seal(a cipher.AEAD, name string, payload []byte) ([]byte, error) {
	if a == nil {
		return nil, errors.New("settings encryption is not configured")
	}
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	return a.Seal(nonce, nonce, payload, []byte(name)), nil
}

func unseal(a cipher.AEAD, name string, sealed []byte) ([]byte, error) {
	if a == nil || len(sealed) < a.NonceSize() {
		return nil, errors.New("encrypted settings unavailable")
	}
	size := a.NonceSize()
	payload, err := a.Open(nil, sealed[:size], sealed[size:], []byte(name))
	if err != nil {
		return nil, errors.New("could not decrypt saved settings")
	}
	return payload, nil
}
