package modelgw

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const sealPrefix = "enc:v1:"

// Box encrypts the provider API keys at rest (AES-256-GCM).
type Box struct{ aead cipher.AEAD }

// NewBox derives the key from a secret passphrase.
func NewBox(secret string) *Box {
	sum := sha256.Sum256([]byte(secret))
	block, _ := aes.NewCipher(sum[:])
	aead, _ := cipher.NewGCM(block)
	return &Box{aead: aead}
}

// DevSecret protects the keys when no GOAP_SECRET_KEY is configured: fine on a
// developer machine, not a secret in production.
const DevSecret = "goap-dev-insecure-key"

// Seal encrypts plain; the empty string stays empty.
func (b *Box) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return sealPrefix + base64.RawStdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

// Open decrypts a sealed value.
func (b *Box) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	raw, ok := strings.CutPrefix(sealed, sealPrefix)
	if !ok {
		return "", errors.New("stored key is not encrypted")
	}
	data, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil || len(data) < b.aead.NonceSize() {
		return "", errors.New("stored key is corrupt")
	}
	plain, err := b.aead.Open(nil, data[:b.aead.NonceSize()], data[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("stored key cannot be decrypted (GOAP_SECRET_KEY changed?)")
	}
	return string(plain), nil
}

// KeyHint masks an API key for display: the last four characters.
func KeyHint(key string) string {
	if len(key) <= 8 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}
