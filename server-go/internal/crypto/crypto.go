// Package crypto — крипта чанков, wire-совместима с WireTurn/webdav-tunnel.
//
// Зашифрованный чанк: [12 nonce][AES-256-GCM ciphertext][16 tag].
// Ключ выводится из WebDAV-пароля с доменным префиксом.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

const keyPrefix = "webdav-tunnel-v1:"

// DeriveKey — 32-байтный AES-256 ключ из пароля (SHA-256 с доменным префиксом).
func DeriveKey(password string) []byte {
	h := sha256.Sum256(append([]byte(keyPrefix), password...))
	return h[:]
}

// EncryptChunk → [12 nonce][ct][16 tag]. gcm.Seal(nonce, nonce, pt) даёт
// nonce‖ct‖tag — совпадает с Go-эталоном и Python AESGCM.encrypt.
func EncryptChunk(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptChunk — обратно к EncryptChunk.
func DecryptChunk(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns+gcm.Overhead() {
		return nil, fmt.Errorf("ciphertext too short (%d bytes)", len(blob))
	}
	return gcm.Open(nil, blob[:ns], blob[ns:], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
