package app

import (
	"crypto/rand"
	"errors"
	"io"
)

// Envelope encryption for values stored at rest.

func (a *App) seal(in []byte) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return a.aead.Seal(nonce, nonce, in, nil), nil
}
func (a *App) open(in []byte) ([]byte, error) {
	if len(in) < a.aead.NonceSize() {
		return nil, errors.New("encrypted setting is truncated")
	}
	nonce, ciphertext := in[:a.aead.NonceSize()], in[a.aead.NonceSize():]
	return a.aead.Open(nil, nonce, ciphertext, nil)
}
