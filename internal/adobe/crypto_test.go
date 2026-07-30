package adobe

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCipherRoundTripAndAADBinding(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	cipher, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aad := SecretAAD("adobe_accounts", "account-1", "access_token")
	encrypted, err := cipher.Encrypt([]byte("secret-token"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, encryptedValuePrefix) || strings.Contains(encrypted, "secret-token") {
		t.Fatalf("unexpected encrypted value %q", encrypted)
	}
	plain, err := cipher.Decrypt(encrypted, aad)
	if err != nil || string(plain) != "secret-token" {
		t.Fatalf("plain=%q err=%v", plain, err)
	}
	if _, err := cipher.Decrypt(encrypted, aad+"-wrong"); err == nil {
		t.Fatal("decrypt accepted the wrong AAD")
	}
}

func TestNewCipherRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "not-base64", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := NewCipher(key); err == nil {
			t.Fatalf("key %q was accepted", key)
		}
	}
}
