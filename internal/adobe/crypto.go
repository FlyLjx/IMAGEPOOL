package adobe

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const encryptedValuePrefix = "v1:"

type Cipher struct {
	aead cipher.AEAD
}

func NewCipher(encodedKey string) (*Cipher, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	if encodedKey == "" {
		return nil, errors.New("IMAGE_POOL_MASTER_KEY is required when Adobe is enabled")
	}
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, fmt.Errorf("decode IMAGE_POOL_MASTER_KEY: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("IMAGE_POOL_MASTER_KEY must decode to 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext []byte, aad string) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("Adobe cipher is not configured")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, plaintext, []byte(aad))
	payload := append(nonce, sealed...)
	return encryptedValuePrefix + base64.RawStdEncoding.EncodeToString(payload), nil
}

func (c *Cipher) Decrypt(value, aad string) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, errors.New("Adobe cipher is not configured")
	}
	if !strings.HasPrefix(value, encryptedValuePrefix) {
		return nil, errors.New("unsupported encrypted Adobe value")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedValuePrefix))
	if err != nil {
		return nil, fmt.Errorf("decode encrypted Adobe value: %w", err)
	}
	if len(payload) < c.aead.NonceSize() {
		return nil, errors.New("encrypted Adobe value is truncated")
	}
	nonce := payload[:c.aead.NonceSize()]
	ciphertext := payload[c.aead.NonceSize():]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, errors.New("decrypt Adobe value: authentication failed")
	}
	return plaintext, nil
}

func SecretAAD(table, id, field string) string {
	return strings.Join([]string{"image-pool", strings.TrimSpace(table), strings.TrimSpace(id), strings.TrimSpace(field)}, "/")
}
