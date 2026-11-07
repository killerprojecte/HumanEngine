// Package credentials preserves the installation's existing v1 credential envelope.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsoncompat"
)

var ErrDecrypt = errors.New("secret_decryption_failed")

type Cipher struct {
	aead   cipher.AEAD
	random io.Reader
}

func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, errors.New("secret_key_invalid")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, random: rand.Reader}, nil
}

func (c *Cipher) Encrypt(namespace, name, value string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(value), jsoncompat.StringArray(namespace, name))
	split := len(sealed) - c.aead.Overhead()
	b64 := base64.StdEncoding.EncodeToString
	return strings.Join([]string{"v1", b64(nonce), b64(sealed[split:]), b64(sealed[:split])}, "."), nil
}

func (c *Cipher) Decrypt(namespace, name, envelope string) (string, error) {
	parts := strings.Split(envelope, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", ErrDecrypt
	}
	nonce, e1 := base64.StdEncoding.DecodeString(parts[1])
	tag, e2 := base64.StdEncoding.DecodeString(parts[2])
	body, e3 := base64.StdEncoding.DecodeString(parts[3])
	if e1 != nil || e2 != nil || e3 != nil || len(nonce) != c.aead.NonceSize() || len(tag) != c.aead.Overhead() {
		return "", ErrDecrypt
	}
	plain, err := c.aead.Open(nil, nonce, append(body, tag...), jsoncompat.StringArray(namespace, name))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}
