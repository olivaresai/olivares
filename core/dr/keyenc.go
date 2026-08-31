// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

// The DR bundle carries the ledger SIGNING KEYS, which are the most sensitive
// bytes a backup can hold (whoever has them can re-sign the ledger). They are
// therefore never written to the bundle in the clear: each key file is sealed
// with AES-256-GCM under a 32-byte key-encryption key (KEK) the OPERATOR holds,
// either derived from a passphrase via Argon2id (memory-hard) or supplied raw
// (the path a KMS/HSM-wrapped KEK takes — coordinates with BYOK). The KDF
// parameters (not the KEK) travel in the bundle so a restore can re-derive the
// KEK from the same passphrase; a wrong passphrase fails the GCM tag, so a
// decrypt error is authenticated, never a silent garbage key.

// KDF identifiers recorded in KDFParams.KDF.
const (
	// kdfArgon2id derives the KEK from a passphrase with Argon2id.
	kdfArgon2id = "argon2id"
	// kdfRaw means the KEK is supplied directly as 32 raw bytes (no passphrase);
	// the operator (or a KMS unwrap) provides the same bytes on restore.
	kdfRaw = "raw"
	// aeadAESGCM is the only AEAD: AES-256-GCM from the standard library (pure-Go,
	// FIPS-friendly, no extra dependency on the verify/restore path).
	aeadAESGCM = "aes-256-gcm"
)

// Default Argon2id cost. Interactive-but-strong: 64 MiB, 3 passes, 4 lanes. A DR
// passphrase is unsealed rarely (a restore), so a high cost is cheap insurance.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB => 64 MiB
	argonThreads = 4
	kekLen       = 32 // AES-256
	saltLen      = 16
)

// KDFParams are the non-secret parameters needed to re-derive the KEK on
// restore. They travel in the bundle (keys/kek.json). They contain no secret.
type KDFParams struct {
	// KDF is kdfArgon2id or kdfRaw.
	KDF string `json:"kdf"`
	// AEAD is aeadAESGCM.
	AEAD string `json:"aead"`
	// Salt is the Argon2id salt (hex). Empty for kdfRaw.
	Salt string `json:"salt,omitempty"`
	// Time, Memory, Threads, KeyLen are the Argon2id cost parameters (so a future
	// cost change still decrypts old bundles). Zero for kdfRaw.
	Time    uint32 `json:"t,omitempty"`
	Memory  uint32 `json:"m,omitempty"`
	Threads uint8  `json:"p,omitempty"`
	KeyLen  uint32 `json:"klen,omitempty"`
}

// KeyCipher seals and opens key material under a KEK. Build one with
// NewPassphraseCipher / NewRawKeyCipher (backup side) or OpenCipher (restore side,
// from the bundle's recorded KDFParams).
type KeyCipher struct {
	kek    []byte
	params KDFParams
}

// NewPassphraseCipher derives a fresh KEK from passphrase with a random salt and
// the default Argon2id cost. Use it on the BACKUP side; its Params() must be
// stored in the bundle so a restore can re-derive the KEK.
func NewPassphraseCipher(passphrase []byte) (*KeyCipher, error) {
	if len(passphrase) == 0 {
		return nil, fmt.Errorf("dr: empty backup passphrase")
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("dr: salt: %w", err)
	}
	p := KDFParams{
		KDF: kdfArgon2id, AEAD: aeadAESGCM, Salt: hex.EncodeToString(salt),
		Time: argonTime, Memory: argonMemory, Threads: argonThreads, KeyLen: kekLen,
	}
	kek := argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return &KeyCipher{kek: kek, params: p}, nil
}

// NewRawKeyCipher uses a 32-byte KEK directly (the KMS/HSM-wrapped-KEK path: the
// operator unwraps the KEK out-of-band and passes the bytes). Its Params() record
// only that the KDF was "raw"; the operator supplies the same bytes on restore.
func NewRawKeyCipher(key []byte) (*KeyCipher, error) {
	if len(key) != kekLen {
		return nil, fmt.Errorf("dr: raw KEK is %d bytes, want %d", len(key), kekLen)
	}
	kek := make([]byte, kekLen)
	copy(kek, key)
	return &KeyCipher{kek: kek, params: KDFParams{KDF: kdfRaw, AEAD: aeadAESGCM}}, nil
}

// OpenCipher rebuilds the KEK on the RESTORE side from the bundle's KDFParams and
// the operator's secret (a passphrase for argon2id, or the raw 32 bytes for raw).
func OpenCipher(secret []byte, p KDFParams) (*KeyCipher, error) {
	if p.AEAD != aeadAESGCM {
		return nil, fmt.Errorf("dr: unsupported AEAD %q", p.AEAD)
	}
	switch p.KDF {
	case kdfArgon2id:
		salt, err := hex.DecodeString(p.Salt)
		if err != nil || len(salt) == 0 {
			return nil, fmt.Errorf("dr: bad KDF salt")
		}
		if p.KeyLen != kekLen {
			return nil, fmt.Errorf("dr: KDF key length %d unsupported (want %d)", p.KeyLen, kekLen)
		}
		if len(secret) == 0 {
			return nil, fmt.Errorf("dr: empty passphrase for argon2id bundle")
		}
		kek := argon2.IDKey(secret, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
		return &KeyCipher{kek: kek, params: p}, nil
	case kdfRaw:
		return NewRawKeyCipher(secret)
	default:
		return nil, fmt.Errorf("dr: unsupported KDF %q", p.KDF)
	}
}

// Params returns the non-secret KDF parameters to store in the bundle.
func (c *KeyCipher) Params() KDFParams { return c.params }

// Seal encrypts plaintext with AES-256-GCM under the KEK. The 12-byte random
// nonce is prepended to the ciphertext: out = nonce ‖ ciphertext ‖ tag.
func (c *KeyCipher) Seal(plaintext []byte) ([]byte, error) {
	gcm, err := c.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("dr: nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open authenticates and decrypts a Seal output. A wrong KEK (wrong passphrase)
// fails the GCM tag and returns an error — never a silently wrong key.
func (c *KeyCipher) Open(blob []byte) ([]byte, error) {
	gcm, err := c.gcm()
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, fmt.Errorf("dr: sealed key too short")
	}
	pt, err := gcm.Open(nil, blob[:ns], blob[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("dr: decrypt key (wrong passphrase or corrupted bundle): %w", err)
	}
	return pt, nil
}

func (c *KeyCipher) gcm() (cipher.AEAD, error) {
	if len(c.kek) != kekLen {
		return nil, fmt.Errorf("dr: KEK not initialized")
	}
	block, err := aes.NewCipher(c.kek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// keyMatches reports whether two hex fingerprints are equal in constant time
// (defense-in-depth; fingerprints are public, so timing is not truly sensitive).
func keyMatches(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sha256hex returns the lowercase-hex SHA-256 of b.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
