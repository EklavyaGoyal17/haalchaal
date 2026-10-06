package crypto

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func key(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }

func mustKeyring(t *testing.T, keys, active string) *Keyring {
	t.Helper()
	kr, err := ParseKeyring(keys, active)
	if err != nil {
		t.Fatalf("ParseKeyring: %v", err)
	}
	return kr
}

const aad = "parents.safe_word_enc"

func TestRoundTrip(t *testing.T) {
	kr := mustKeyring(t, "1:"+key(1), "1")
	for _, pt := range [][]byte{
		{},
		[]byte("Kamla ji"),
		[]byte("सीने में दर्द"),
		bytes.Repeat([]byte("x"), 1<<20),
	} {
		ct, err := kr.Encrypt(pt, aad)
		if err != nil {
			t.Fatal(err)
		}
		if ct[0] != 1 || len(ct) != 1+12+len(pt)+16 {
			t.Fatalf("bad layout: kid %d, len %d for plaintext len %d", ct[0], len(ct), len(pt))
		}
		if len(pt) > 0 && bytes.Contains(ct, pt) {
			t.Fatal("ciphertext contains plaintext")
		}
		got, err := kr.Decrypt(ct, aad)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, pt) {
			t.Fatal("round trip mismatch")
		}
	}
}

func TestStringHelpers(t *testing.T) {
	kr := mustKeyring(t, "7:"+key(7), "7")
	ct, err := kr.EncryptString("knee pain", "memories.content_enc")
	if err != nil {
		t.Fatal(err)
	}
	got, err := kr.DecryptString(ct, "memories.content_enc")
	if err != nil || got != "knee pain" {
		t.Fatalf("DecryptString = %q, %v", got, err)
	}
}

func TestEncryptionIsRandomised(t *testing.T) {
	kr := mustKeyring(t, "1:"+key(1), "1")
	a, _ := kr.Encrypt([]byte("same"), aad)
	b, _ := kr.Encrypt([]byte("same"), aad)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext are identical")
	}
}

func TestDecryptFailures(t *testing.T) {
	kr := mustKeyring(t, "1:"+key(1), "1")
	ct, err := kr.Encrypt([]byte("Kamla ji"), aad)
	if err != nil {
		t.Fatal(err)
	}
	flip := func(i int) []byte {
		c := bytes.Clone(ct)
		c[i] ^= 0x01
		return c
	}
	unknownKID := bytes.Clone(ct)
	unknownKID[0] = 9

	tests := []struct {
		name string
		ct   []byte
		aad  string
	}{
		{"wrong aad", ct, "medicines.name_enc"},
		{"tampered nonce", flip(1), aad},
		{"tampered body", flip(13), aad},
		{"tampered tag", flip(len(ct) - 1), aad},
		{"unknown key id", unknownKID, aad},
		{"truncated header", ct[:5], aad},
		{"truncated tag", ct[:headerLen+3], aad},
		{"empty", nil, aad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pt, err := kr.Decrypt(tt.ct, tt.aad)
			if !errors.Is(err, ErrDecrypt) {
				t.Fatalf("err = %v, want ErrDecrypt", err)
			}
			if pt != nil {
				t.Fatal("plaintext returned on failure")
			}
			if strings.Contains(err.Error(), "Kamla") {
				t.Fatal("error leaks plaintext")
			}
		})
	}
}

func TestWrongKeyFails(t *testing.T) {
	a := mustKeyring(t, "1:"+key(1), "1")
	b := mustKeyring(t, "1:"+key(2), "1") // same kid, different key material
	ct, _ := a.Encrypt([]byte("x"), aad)
	if _, err := b.Decrypt(ct, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
}

func TestRotation(t *testing.T) {
	old := mustKeyring(t, "1:"+key(1), "1")
	ct, _ := old.Encrypt([]byte("Kamla ji"), aad)

	// Add key 2 and make it active; key 1 stays for reading.
	kr := mustKeyring(t, "1:"+key(1)+",2:"+key(2), "2")
	if !kr.NeedsRotation(ct) {
		t.Fatal("old ciphertext should need rotation")
	}
	if pt, err := kr.DecryptString(ct, aad); err != nil || pt != "Kamla ji" {
		t.Fatalf("old key no longer decrypts: %q, %v", pt, err)
	}
	rotated, err := kr.Rotate(ct, aad)
	if err != nil {
		t.Fatal(err)
	}
	if rotated[0] != 2 || kr.NeedsRotation(rotated) {
		t.Fatalf("rotated value uses kid %d", rotated[0])
	}
	if pt, err := kr.DecryptString(rotated, aad); err != nil || pt != "Kamla ji" {
		t.Fatalf("rotated value: %q, %v", pt, err)
	}
	again, err := kr.Rotate(rotated, aad)
	if err != nil || !bytes.Equal(again, rotated) {
		t.Fatal("rotating an up-to-date value should return it unchanged")
	}
	if _, err := kr.Rotate(rotated, "wrong.aad"); !errors.Is(err, ErrDecrypt) {
		t.Fatal("rotate must verify aad")
	}

	// Once key 1 is removed, unrotated values become unreadable.
	newOnly := mustKeyring(t, "2:"+key(2), "2")
	if _, err := newOnly.Decrypt(ct, aad); !errors.Is(err, ErrDecrypt) {
		t.Fatal("removed key should not decrypt")
	}
	if _, err := newOnly.Decrypt(rotated, aad); err != nil {
		t.Fatalf("rotated value should survive key removal: %v", err)
	}
}

func TestParseKeyringErrors(t *testing.T) {
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	tests := []struct {
		name, keys, active, want string
	}{
		{"empty", "", "1", "no keys"},
		{"no colon", key(1), "1", "<kid>:<base64 key>"},
		{"kid zero", "0:" + key(1), "0", "1 to 255"},
		{"kid too big", "256:" + key(1), "256", "1 to 255"},
		{"kid not number", "a:" + key(1), "a", "1 to 255"},
		{"bad base64", "1:not*base64", "1", "not valid base64"},
		{"wrong length", "1:" + short, "1", "must be 32 bytes"},
		{"duplicate kid", "1:" + key(1) + ",1:" + key(2), "1", "more than once"},
		{"active missing", "1:" + key(1), "2", "not in ENCRYPTION_KEYS"},
		{"active blank", "1:" + key(1), "", "ENCRYPTION_ACTIVE_KID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseKeyring(tt.keys, tt.active)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), key(1)) {
				t.Fatal("error leaks key material")
			}
		})
	}
}

func TestGenerateKey(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := GenerateKey()
	if a == b {
		t.Fatal("keys repeat")
	}
	if _, err := ParseKeyring("3:"+a, "3"); err != nil {
		t.Fatalf("generated key does not parse: %v", err)
	}
}

func TestDeriveKey(t *testing.T) {
	k1, _ := GenerateKey()
	k2, _ := GenerateKey()
	a, _ := ParseKeyring("1:"+k1, "1")
	b, _ := ParseKeyring("1:"+k1, "1")
	c, _ := ParseKeyring("1:"+k1+",2:"+k2, "2")
	if string(a.DeriveKey("csrf")) != string(b.DeriveKey("csrf")) {
		t.Fatal("same keys derive different values")
	}
	if string(a.DeriveKey("csrf")) == string(a.DeriveKey("other")) {
		t.Fatal("labels not separated")
	}
	if string(a.DeriveKey("csrf")) == string(c.DeriveKey("csrf")) {
		t.Fatal("rotation did not change the derived key")
	}
	if len(a.DeriveKey("csrf")) != 32 {
		t.Fatal("length")
	}
}
