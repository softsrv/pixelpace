package concept2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"

	"github.com/google/uuid"
)

// ErrDecrypt is returned when opening a sealed token fails for any reason
// (such as a wrong secret, tampered or truncated ciphertext, or wrong user).
// It never contains token or key material.
var ErrDecrypt = errors.New("concept2: cannot decrypt token")

const (
	hkdfInfo    = "concept2-tokens"
	keyLength   = 32
	nonceLength = 12
)

// Sealer encrypts and decrypts OAuth tokens using AES-256-GCM under a key
// derived from a secret via HKDF-SHA256 with info 'concept2-tokens'.
// Each sealed token has a 12-byte random nonce prefix and binds the user ID
// as AEAD associated data.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer constructs a Sealer by deriving a 32-byte AES-256 key from secret
// via HKDF-SHA256 with info "concept2-tokens".
func NewSealer(secret []byte) *Sealer {
	key, err := hkdf.Key(sha256.New, secret, nil, hkdfInfo, keyLength)
	if err != nil {
		panic("concept2: hkdf key derivation: " + err.Error())
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("concept2: aes cipher: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("concept2: gcm: " + err.Error())
	}
	return &Sealer{aead: aead}
}

// Seal encrypts plaintext using AES-256-GCM with a fresh random 12-byte nonce
// and binds userID as AEAD associated data. The returned slice is
// nonce || ciphertext || tag.
func (s *Sealer) Seal(plaintext []byte, userID uuid.UUID) ([]byte, error) {
	nonce := make([]byte, nonceLength)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.New("concept2: generate nonce failed")
	}
	// aead.Seal appends the ciphertext and tag to nonce.
	return s.aead.Seal(nonce, nonce, plaintext, userID[:]), nil
}

// Open decrypts a sealed token previously produced by Seal, verifying that it
// was sealed with the same secret and for the same userID. Any failure
// (wrong secret, corrupted data, truncated blob, or different user) returns ErrDecrypt.
func (s *Sealer) Open(sealed []byte, userID uuid.UUID) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize()+s.aead.Overhead() {
		return nil, ErrDecrypt
	}
	nonce := sealed[:s.aead.NonceSize()]
	ciphertext := sealed[s.aead.NonceSize():]
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, userID[:])
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
