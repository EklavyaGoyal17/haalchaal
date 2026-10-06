// Package crypto encrypts and decrypts the *_enc columns with AES-256-GCM.
//
// Ciphertext layout (SPEC §4): key_id (1 byte) || nonce (12 bytes) || sealed data.
// Every call takes an associated-data string naming the column, such as
// "parents.safe_word_enc", so a ciphertext copied into another column fails to
// decrypt. Errors never contain plaintext or key material.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	keySize   = 32
	nonceSize = 12
	headerLen = 1 + nonceSize
)

// ErrDecrypt is returned for any ciphertext that cannot be opened: tampered,
// truncated, wrong associated data or wrong key.
var ErrDecrypt = errors.New("crypto: cannot decrypt value")

// Keyring holds every configured key and the one used for new writes.
type Keyring struct {
	aeads  map[byte]cipher.AEAD
	derive map[byte][]byte // per-key root for derived MAC keys, never the key itself
	active byte
}

// ParseKeyring builds a Keyring from ENCRYPTION_KEYS ("1:<base64>,2:<base64>")
// and ENCRYPTION_ACTIVE_KID. Key ids are integers from 1 to 255 because they
// are stored in one byte; each key must decode to exactly 32 bytes.
func ParseKeyring(keys, activeKID string) (*Keyring, error) {
	kr := &Keyring{aeads: map[byte]cipher.AEAD{}, derive: map[byte][]byte{}}
	for _, entry := range strings.Split(keys, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idStr, b64, ok := strings.Cut(entry, ":")
		if !ok {
			return nil, errors.New("ENCRYPTION_KEYS entries must look like <kid>:<base64 key>")
		}
		kid, err := parseKID(idStr)
		if err != nil {
			return nil, err
		}
		if _, dup := kr.aeads[kid]; dup {
			return nil, fmt.Errorf("ENCRYPTION_KEYS has key id %d more than once", kid)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("ENCRYPTION_KEYS key %d is not valid base64", kid)
		}
		if len(raw) != keySize {
			return nil, fmt.Errorf("ENCRYPTION_KEYS key %d must be 32 bytes, got %d", kid, len(raw))
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, err
		}
		kr.aeads[kid] = aead
		root := hmac.New(sha256.New, raw)
		root.Write([]byte("haalchaal/derive/v1"))
		kr.derive[kid] = root.Sum(nil)
	}
	if len(kr.aeads) == 0 {
		return nil, errors.New("ENCRYPTION_KEYS has no keys")
	}
	active, err := parseKID(activeKID)
	if err != nil {
		return nil, fmt.Errorf("ENCRYPTION_ACTIVE_KID: %w", err)
	}
	if _, ok := kr.aeads[active]; !ok {
		return nil, fmt.Errorf("ENCRYPTION_ACTIVE_KID %d is not in ENCRYPTION_KEYS", active)
	}
	kr.active = active
	return kr, nil
}

func parseKID(s string) (byte, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 255 {
		return 0, errors.New("key id must be a whole number from 1 to 255")
	}
	return byte(n), nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	return cipher.NewGCM(block)
}

// DeriveKey returns a 32-byte key for a purpose such as "csrf", derived from
// the active encryption key with HMAC-SHA256. Every instance with the same
// keys derives the same value; rotating the active key changes it.
func (k *Keyring) DeriveKey(label string) []byte {
	m := hmac.New(sha256.New, k.derive[k.active])
	m.Write([]byte(label))
	return m.Sum(nil)
}

// ActiveKID returns the key id used for new writes.
func (k *Keyring) ActiveKID() byte { return k.active }

// Encrypt seals plaintext under the active key, bound to aad.
func (k *Keyring) Encrypt(plaintext []byte, aad string) ([]byte, error) {
	aead := k.aeads[k.active]
	out := make([]byte, headerLen, headerLen+len(plaintext)+aead.Overhead())
	out[0] = k.active
	if _, err := rand.Read(out[1:headerLen]); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return aead.Seal(out, out[1:headerLen], plaintext, []byte(aad)), nil
}

// Decrypt opens a value written by Encrypt with any configured key.
func (k *Keyring) Decrypt(ciphertext []byte, aad string) ([]byte, error) {
	if len(ciphertext) < headerLen {
		return nil, ErrDecrypt
	}
	aead, ok := k.aeads[ciphertext[0]]
	if !ok {
		return nil, fmt.Errorf("%w: unknown key id %d", ErrDecrypt, ciphertext[0])
	}
	if len(ciphertext) < headerLen+aead.Overhead() {
		return nil, ErrDecrypt
	}
	pt, err := aead.Open(nil, ciphertext[1:headerLen], ciphertext[headerLen:], []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// EncryptString and DecryptString are conveniences for text columns.
func (k *Keyring) EncryptString(s, aad string) ([]byte, error) { return k.Encrypt([]byte(s), aad) }

func (k *Keyring) DecryptString(ciphertext []byte, aad string) (string, error) {
	pt, err := k.Decrypt(ciphertext, aad)
	return string(pt), err
}

// NeedsRotation reports whether ciphertext was written with a key other than
// the active one.
func (k *Keyring) NeedsRotation(ciphertext []byte) bool {
	return len(ciphertext) > 0 && ciphertext[0] != k.active
}

// Rotate re-encrypts ciphertext under the active key. Values already on the
// active key are returned unchanged.
func (k *Keyring) Rotate(ciphertext []byte, aad string) ([]byte, error) {
	pt, err := k.Decrypt(ciphertext, aad)
	if err != nil {
		return nil, err
	}
	if !k.NeedsRotation(ciphertext) {
		return ciphertext, nil
	}
	return k.Encrypt(pt, aad)
}

// GenerateKey returns a new random 32-byte key, base64 encoded, for ENCRYPTION_KEYS.
func GenerateKey() (string, error) {
	b := make([]byte, keySize)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto: generate key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
