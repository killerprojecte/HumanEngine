// Package credentials preserves the source writer's canonical v1 envelopes.
// See COMPATIBILITY.md for noncanonical source-reader differences.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"golang.org/x/text/encoding/unicode"
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

// Encrypt accepts JavaScript strings in jsjson's canonical WTF-8 representation.
// The caller owns SecretStore's mutation policy; no value is trimmed here.
func (c *Cipher) Encrypt(namespace, name, value string) (string, error) {
	// AAD preserves JS code units in JSON.stringify escapes; cipher.update's
	// UTF-8 text input instead replaces each unpaired surrogate with U+FFFD.
	aad, err := jsjson.Marshal([]any{namespace, name})
	if err != nil {
		return "", err
	}
	plain, err := jsjson.UTF8Text(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plain), aad)
	split := len(sealed) - c.aead.Overhead()
	b64 := base64.StdEncoding.EncodeToString
	return strings.Join([]string{"v1", b64(nonce), b64(sealed[split:]), b64(sealed[:split])}, "."), nil
}

// Decrypt authenticates canonical nonce-12/tag-16 envelopes before applying
// Node's UTF-8 replacement decoding. AAD strings must be canonical WTF-8.
func (c *Cipher) Decrypt(namespace, name, envelope string) (string, error) {
	parts := strings.Split(envelope, ".")
	if len(parts) != 4 || parts[0] != "v1" || parts[3] == "" {
		return "", ErrDecrypt
	}
	nonce, e1 := base64.StdEncoding.DecodeString(parts[1])
	tag, e2 := base64.StdEncoding.DecodeString(parts[2])
	body, e3 := base64.StdEncoding.DecodeString(parts[3])
	if e1 != nil || e2 != nil || e3 != nil || len(nonce) != c.aead.NonceSize() || len(tag) != c.aead.Overhead() {
		return "", ErrDecrypt
	}
	aad, err := jsjson.Marshal([]any{namespace, name})
	if err != nil {
		return "", ErrDecrypt
	}
	plain, err := c.aead.Open(nil, nonce, append(body, tag...), aad)
	if err != nil {
		return "", ErrDecrypt
	}
	// Node Buffer.toString('utf8') uses maximal-subpart replacement for invalid
	// sequences. A rune loop or strings.ToValidUTF8 does not have that behavior.
	value, err := decodeUTF8(plain)
	if err != nil {
		return "", ErrDecrypt
	}
	return value, nil
}

func decodeUTF8(value []byte) (string, error) {
	// UTF8 (not UTF8BOM) retains an initial BOM, as Node's Buffer does.
	return unicode.UTF8.NewDecoder().String(string(value))
}
