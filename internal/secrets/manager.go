package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

type Manager struct{ aead cipher.AEAD }

func New(key string) (*Manager, error) {
	if key == "" {
		return nil, errors.New("secret encryption key is required")
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Manager{aead: aead}, nil
}

func (m *Manager) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return m.aead.Seal(nonce, nonce, plain, nil), nil
}

func (m *Manager) Decrypt(value []byte) ([]byte, error) {
	if len(value) < m.aead.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	nonce, ciphertext := value[:m.aead.NonceSize()], value[m.aead.NonceSize():]
	return m.aead.Open(nil, nonce, ciphertext, nil)
}
