package store

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestSettingsEncryption(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	aead, err := NewSettingsCipher(encoded)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("private collector credential")
	first, err := seal(aead, "collector_settings", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := seal(aead, "collector_settings", payload)
	if err != nil || bytes.Equal(first, second) || bytes.Contains(first, payload) {
		t.Fatal("encryption must use a fresh nonce and hide the payload")
	}
	got, err := unseal(aead, "collector_settings", first)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("round trip failed")
	}
	if _, err := unseal(aead, "runtime_settings", first); err == nil {
		t.Fatal("ciphertext from a different settings record was accepted")
	}
	first[len(first)-1] ^= 1
	if _, err := unseal(aead, "collector_settings", first); err == nil {
		t.Fatal("modified ciphertext was accepted")
	}
	if _, err := NewSettingsCipher("invalid"); err == nil {
		t.Fatal("invalid master key was accepted")
	}
}
