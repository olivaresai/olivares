// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Credential format: "<prefix>_<selector>_<secret>". The selector is a public,
// indexed lookup key; the secret is never stored — only SHA-256(secret), compared
// in constant time. base32 (alphabet [A-Z2-7], no underscore) keeps the three
// parts unambiguously splittable on "_".
const (
	// PrefixSession marks a human/panel session token.
	PrefixSession = "olvs"
	// PrefixToken marks a programmatic API token.
	PrefixToken = "olvk"
	// PrefixInvite marks a single-use user-onboarding invitation token. It
	// is consumed once at /v1/invites/accept to set a password and activate the
	// account; like every credential, only SHA-256(secret) is stored.
	PrefixInvite = "olvi"
	// PrefixDelegation marks an opaque delegation handle (the verifier): a
	// single-use reference to a stored DelegationHandle a PEP presents as the
	// DelegationProof "handle" scheme. Like every credential only SHA-256(secret)
	// is stored, and it never authenticates through the ordinary Authenticate path.
	PrefixDelegation = "olvd"

	selectorBytes = 16 // 128-bit public selector
	secretBytes   = 32 // 256-bit secret
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Credential is a freshly minted credential: the full wire token (shown to the
// caller exactly once), plus the selector and secret hash to persist.
type Credential struct {
	// Token is the full secret string to hand to the caller once.
	Token string
	// Selector is the public lookup key to store and index.
	Selector string
	// SecretHash is SHA-256(secret) to store; never the secret itself.
	SecretHash []byte
}

// NewCredential mints a credential with the given prefix (PrefixSession or
// PrefixToken). The returned Token must be shown once and never stored.
func NewCredential(prefix string) (Credential, error) {
	sel := make([]byte, selectorBytes)
	if _, err := rand.Read(sel); err != nil {
		return Credential{}, fmt.Errorf("auth: read entropy: %w", err)
	}
	sec := make([]byte, secretBytes)
	if _, err := rand.Read(sec); err != nil {
		return Credential{}, fmt.Errorf("auth: read entropy: %w", err)
	}
	selector := b32.EncodeToString(sel)
	secret := b32.EncodeToString(sec)
	return Credential{
		Token:      prefix + "_" + selector + "_" + secret,
		Selector:   selector,
		SecretHash: hashSecret(secret),
	}, nil
}

// ParseToken splits a wire token into its prefix, selector and secret. It does no
// validation beyond shape; ok is false for a malformed token.
func ParseToken(token string) (prefix, selector, secret string, ok bool) {
	parts := strings.SplitN(token, "_", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// hashSecret returns SHA-256(secret).
func hashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

// SecretMatches reports whether secret hashes to storedHash, compared in constant
// time so a timing side-channel cannot reveal how much of the hash matched.
func SecretMatches(secret string, storedHash []byte) bool {
	got := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(got[:], storedHash) == 1
}

// dummySecretHash is a fixed, valid-length hash used to run SecretMatches on a
// credential-lookup MISS, so an unknown selector costs the same constant-time
// SHA-256 hash + compare as a wrong secret — a lookup miss and a wrong secret are
// then timing-indistinguishable.
var dummySecretHash = sha256.Sum256([]byte("olivares.constant-time.dummy.secret"))

// --- Password hashing (argon2id) ---------------------------------------------

// argon2id parameters. m is the memory cost in KiB (64 MiB, well over the OWASP
// 19 MiB floor); t the time cost; p the parallelism. They are stored in the
// encoded hash so they can be raised later and old hashes re-hashed on login.
const (
	argonMemKiB  = 64 * 1024
	argonTime    = 3
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32
)

// ErrMalformedHash is returned when an encoded password hash cannot be parsed.
var ErrMalformedHash = errors.New("auth: malformed password hash")

// HashPassword returns an argon2id PHC-format encoded hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read entropy: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded argon2id hash, recomputing
// with the hash's own parameters and comparing in constant time.
func VerifyPassword(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrMalformedHash
	}
	mem, time, threads, err := parseArgonParams(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrMalformedHash
	}
	got := argon2.IDKey([]byte(pw), salt, time, mem, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseArgonParams(s string) (mem uint32, time uint32, threads uint8, err error) {
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, 0, 0, ErrMalformedHash
		}
		n, perr := strconv.ParseUint(v, 10, 32)
		if perr != nil {
			return 0, 0, 0, ErrMalformedHash
		}
		switch k {
		case "m":
			mem = uint32(n)
		case "t":
			time = uint32(n)
		case "p":
			threads = uint8(n)
		default:
			return 0, 0, 0, ErrMalformedHash
		}
	}
	if mem == 0 || time == 0 || threads == 0 {
		return 0, 0, 0, ErrMalformedHash
	}
	return mem, time, threads, nil
}

// dummyHash is a valid argon2id hash of a fixed string, computed once. Verifying
// against it on an unknown-user login path makes that path cost the same as a
// real verify, flattening timing-based user enumeration (docs/SECURITY-HARDENING.md).
var dummyHash struct {
	once sync.Once
	val  string
}

// DummyVerify runs an argon2id verification against a fixed dummy hash and
// discards the result. It exists solely to equalize the timing of the
// unknown-user login path with the known-user path.
func DummyVerify(pw string) {
	dummyHash.once.Do(func() {
		h, err := HashPassword("olivares-dummy-password-for-constant-time")
		if err == nil {
			dummyHash.val = h
		}
	})
	if dummyHash.val != "" {
		_, _ = VerifyPassword(pw, dummyHash.val)
	}
}
