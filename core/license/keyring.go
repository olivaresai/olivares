// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// keyring.go — which license public keys a deployment trusts, in which state, and under which
// epoch fences (connect-v1 contract §6 and acceptance §7.9).
//
// A Keyring replaces "the one embedded key" as the trust anchor of every product consumer: boot,
// reload, install, the upgrade gates and the connected client all verify through Verify. The
// single-key VerifyEnvelope primitive stays as it was for the issuer-side tools and the golden
// vectors.
//
// ============ SELECTION NEVER ESTABLISHES TRUST ==========================================
// A v3 credential names its signing key in `key_id`, and a caller could use that field to pick a
// key. Verify does not. It tries the signature of every key in the (bounded) keyring over the
// payload bytes as received, and only a key whose signature verifies is considered. The signed
// `key_id` is then compared with the KID DERIVED from that key's public bytes. The unauthenticated
// `key_id` is read only after no key verified, and only to choose the wording of the refusal. No
// key is ever fetched from a location a document names.
//
// ============ THE FENCES ================================================================
//   - revoked: a document that verifies under a revoked key is refused.
//   - verify_only: a retired key keeps verifying documents issued BEFORE its retirement, and
//     only until the end of its verification window when one is set.
//   - key_epoch: a v3 credential's signed key_epoch must be >= 1, equal the epoch pinned for its
//     key when one is pinned, and not below the keyring's minimum (the key-compromise fence of
//     LICENSING.md: documents signed in an earlier epoch are invalid). A flat v1/v2 license carries no
//     epoch, so under a non-zero minimum it is accepted only through a key whose pinned epoch
//     meets it.
//
// Like every error in this package, a refusal means "no valid commercial license" for display
// and entitlement purposes. It never blocks a request or boot by itself.

package license

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// KeyState is the trust state of one license public key. Closed set.
type KeyState string

const (
	// KeyStateCurrent accepts documents without a retirement window.
	KeyStateCurrent KeyState = "current"
	// KeyStateVerifyOnly accepts only documents issued before the key's retirement, and only
	// until its verification window ends when one is set.
	KeyStateVerifyOnly KeyState = "verify_only"
	// KeyStateRevoked accepts nothing. It is kept so the refusal can say why.
	KeyStateRevoked KeyState = "revoked"
)

func (s KeyState) valid() bool {
	switch s {
	case KeyStateCurrent, KeyStateVerifyOnly, KeyStateRevoked:
		return true
	}
	return false
}

// MaxKeyringKeys bounds the signatures one verification may try.
const MaxKeyringKeys = 16

// MaxLicenseBlobBytes bounds a blob before any decoding. Issued credentials are a few KiB.
const MaxLicenseBlobBytes = 256 << 10

// Keyring refusals. They wrap into *TrustError, which carries the KID when one is known.
var (
	// ErrNoTrustedKeys reports a keyring with no key at all (a release build without an
	// injected anchor and no administrative trust).
	ErrNoTrustedKeys = errors.New("license: no trusted license key is configured")
	// ErrKeyUnknown reports that no trusted key verifies the document's signature.
	ErrKeyUnknown = errors.New("license: the document is not signed by a trusted license key")
	// ErrKeyRevoked reports a document whose signature verifies under a revoked key.
	ErrKeyRevoked = errors.New("license: the document is signed by a revoked license key")
	// ErrKeyRetired reports a document outside its verify-only key's window.
	ErrKeyRetired = errors.New("license: the document is outside the verification window of its retired key")
	// ErrKeyIDMismatch reports a signed key_id that does not name the key that verified it.
	ErrKeyIDMismatch = errors.New("license: the signed key_id does not name the key whose signature verified")
	// ErrKeyEpochFenced reports a signed key_epoch the keyring does not accept.
	ErrKeyEpochFenced = errors.New("license: the signed key_epoch is fenced by this deployment's license trust")
	// ErrKeyringInvalid reports a keyring or trust configuration that cannot be used.
	ErrKeyringInvalid = errors.New("license: invalid license trust configuration")
)

// TrustError is a keyring refusal with its subject.
type TrustError struct {
	// Err is one of the keyring sentinels above.
	Err error
	// KID is the derived key id involved, or the declared one (display-safe form only) when no
	// trusted key verified. Empty when none is known.
	KID string
	// Detail is a short, fixed explanation without document content.
	Detail string
	// badSignature marks ErrKeyUnknown answers that also satisfy errors.Is(err, ErrBadSignature),
	// so existing callers that route on a signature failure keep routing.
	badSignature bool
}

func (e *TrustError) Error() string {
	msg := e.Err.Error()
	if e.KID != "" {
		msg += " (key " + e.KID + ")"
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap reports the sentinel and, for an unverifiable signature, ErrBadSignature too.
func (e *TrustError) Unwrap() []error {
	if e.badSignature {
		return []error{e.Err, ErrBadSignature}
	}
	return []error{e.Err}
}

// kidPattern is the only KID shape this package derives and the only declared value it displays.
var kidPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// KeyID derives the KID of a license public key: "sha256:" and the lowercase hex SHA-256 of the
// raw 32-byte key. It is the same derivation the license Worker uses for `key_id`.
func KeyID(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: a license public key is %d bytes, got %d", ErrKeyringInvalid, ed25519.PublicKeySize, len(pub))
	}
	sum := sha256.Sum256(pub)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// TrustedKey is one entry of a Keyring.
type TrustedKey struct {
	PublicKey ed25519.PublicKey
	State     KeyState
	// Epoch pins the key_epoch this key signs under. Zero means not pinned.
	Epoch int
	// RetiredAt is required for verify_only: documents issued at or after it are refused.
	RetiredAt time.Time
	// VerifyUntil optionally ends a verify_only key's verification window.
	VerifyUntil time.Time
	// Source names where the entry came from ("embedded", "data-dir", "--pubkey").
	Source string

	kid string
}

// KID is the key id derived from PublicKey. Empty on an entry that did not pass NewKeyring.
func (k TrustedKey) KID() string { return k.kid }

// Keyring is an immutable, validated set of trusted license keys. The zero value trusts nothing.
type Keyring struct {
	keys        []TrustedKey
	minKeyEpoch int
}

// NewKeyring validates keys and returns a keyring ordered current, verify_only, revoked. It
// derives every KID from the public bytes; duplicates, malformed windows, unknown states and a
// current key fenced out by minKeyEpoch are refused.
func NewKeyring(keys []TrustedKey, minKeyEpoch int) (Keyring, error) {
	if len(keys) > MaxKeyringKeys {
		return Keyring{}, fmt.Errorf("%w: %d keys exceeds the maximum of %d", ErrKeyringInvalid, len(keys), MaxKeyringKeys)
	}
	if minKeyEpoch < 0 {
		return Keyring{}, fmt.Errorf("%w: min_key_epoch %d is negative", ErrKeyringInvalid, minKeyEpoch)
	}
	out := make([]TrustedKey, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for i, k := range keys {
		kid, err := KeyID(k.PublicKey)
		if err != nil {
			return Keyring{}, fmt.Errorf("key %d: %w", i, err)
		}
		if _, dup := seen[kid]; dup {
			return Keyring{}, fmt.Errorf("%w: key %s appears twice", ErrKeyringInvalid, kid)
		}
		seen[kid] = struct{}{}
		if !k.State.valid() {
			return Keyring{}, fmt.Errorf("%w: key %s has unknown state %q", ErrKeyringInvalid, kid, k.State)
		}
		if k.Epoch < 0 {
			return Keyring{}, fmt.Errorf("%w: key %s has negative epoch %d", ErrKeyringInvalid, kid, k.Epoch)
		}
		switch k.State {
		case KeyStateVerifyOnly:
			if k.RetiredAt.IsZero() {
				return Keyring{}, fmt.Errorf("%w: verify_only key %s needs retired_at", ErrKeyringInvalid, kid)
			}
			if !k.VerifyUntil.IsZero() && !k.VerifyUntil.After(k.RetiredAt) {
				return Keyring{}, fmt.Errorf("%w: verify_only key %s ends its window at or before its retirement", ErrKeyringInvalid, kid)
			}
		case KeyStateCurrent:
			if !k.RetiredAt.IsZero() || !k.VerifyUntil.IsZero() {
				return Keyring{}, fmt.Errorf("%w: current key %s carries a retirement window", ErrKeyringInvalid, kid)
			}
			if minKeyEpoch > 0 && k.Epoch > 0 && k.Epoch < minKeyEpoch {
				return Keyring{}, fmt.Errorf("%w: current key %s is pinned to epoch %d, below min_key_epoch %d", ErrKeyringInvalid, kid, k.Epoch, minKeyEpoch)
			}
		}
		pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(pub, k.PublicKey)
		k.PublicKey = pub
		k.kid = kid
		out = append(out, k)
	}
	rank := map[KeyState]int{KeyStateCurrent: 0, KeyStateVerifyOnly: 1, KeyStateRevoked: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].State] < rank[out[j].State] })
	return Keyring{keys: out, minKeyEpoch: minKeyEpoch}, nil
}

// SingleKeyKeyring trusts pub as a current, unpinned key. A nil or empty pub yields an empty
// keyring, which verifies nothing.
func SingleKeyKeyring(pub ed25519.PublicKey, source string) (Keyring, error) {
	if len(pub) == 0 {
		return Keyring{}, nil
	}
	return NewKeyring([]TrustedKey{{PublicKey: pub, State: KeyStateCurrent, Source: source}}, 0)
}

// Keys returns a copy of the entries in verification order.
func (k Keyring) Keys() []TrustedKey {
	out := make([]TrustedKey, len(k.keys))
	for i, e := range k.keys {
		e.PublicKey = append(ed25519.PublicKey(nil), e.PublicKey...)
		out[i] = e
	}
	return out
}

// MinKeyEpoch is the keyring-wide key_epoch fence. Zero means none.
func (k Keyring) MinKeyEpoch() int { return k.minKeyEpoch }

// Len is the number of trusted entries, revoked ones included.
func (k Keyring) Len() int { return len(k.keys) }

func (k Keyring) lookup(kid string) (TrustedKey, bool) {
	for _, e := range k.keys {
		if e.kid == kid {
			return e, true
		}
	}
	return TrustedKey{}, false
}

// Trust records which keyring entry verified a document.
type Trust struct {
	KID    string
	State  KeyState
	Epoch  int
	Source string
}

// Verify checks blob against the keyring at now and returns its container. See the file header
// for the order and the fences.
func (k Keyring) Verify(blob string, now time.Time) (Verified, error) {
	if len(k.keys) == 0 {
		return Verified{}, &TrustError{Err: ErrNoTrustedKeys}
	}
	if len(blob) > MaxLicenseBlobBytes {
		return Verified{}, fmt.Errorf("%w: the blob exceeds %d bytes", ErrMalformed, MaxLicenseBlobBytes)
	}
	payload, sig, err := splitEnvelope(blob)
	if err != nil {
		return Verified{}, err
	}
	var matched *TrustedKey
	for i := range k.keys {
		if ed25519.Verify(k.keys[i].PublicKey, payload, sig) {
			matched = &k.keys[i]
			break
		}
	}
	if matched == nil {
		// Diagnostic only: the declared key_id is NOT authenticated, never selects a key, and is
		// displayed only in the derived-KID shape.
		if declared, ok := declaredKeyID(payload); ok {
			if _, trusted := k.lookup(declared); trusted {
				return Verified{}, ErrBadSignature
			}
			return Verified{}, &TrustError{Err: ErrKeyUnknown, KID: declared, badSignature: true, //verifier-truth:allow failed Ed25519 checks classify an error, not successful verification.
				Detail: "the key it names is not in this deployment's license trust"}
		}
		return Verified{}, &TrustError{Err: ErrKeyUnknown, badSignature: true, //verifier-truth:allow failed Ed25519 checks classify an error, not successful verification.
			Detail: "no trusted license key verifies its signature"}
	}
	if matched.State == KeyStateRevoked {
		return Verified{}, &TrustError{Err: ErrKeyRevoked, KID: matched.kid}
	}
	v, err := readContainer(payload)
	if err != nil {
		return Verified{}, err
	}
	var issued time.Time
	switch v.Container {
	case ContainerCredentialV3:
		c := v.Credential
		if c.KeyID != matched.kid {
			return Verified{}, &TrustError{Err: ErrKeyIDMismatch, KID: matched.kid}
		}
		if c.KeyEpoch < 1 {
			return Verified{}, &TrustError{Err: ErrKeyEpochFenced, KID: matched.kid, Detail: "key_epoch must be at least 1"}
		}
		if matched.Epoch > 0 && c.KeyEpoch != matched.Epoch {
			return Verified{}, &TrustError{Err: ErrKeyEpochFenced, KID: matched.kid,
				Detail: fmt.Sprintf("key_epoch %d is not the epoch %d pinned for this key", c.KeyEpoch, matched.Epoch)}
		}
		if c.KeyEpoch < k.minKeyEpoch {
			return Verified{}, &TrustError{Err: ErrKeyEpochFenced, KID: matched.kid,
				Detail: fmt.Sprintf("key_epoch %d is below the minimum %d", c.KeyEpoch, k.minKeyEpoch)}
		}
		issued = c.IssuedAt
	default:
		if k.minKeyEpoch > 0 && (matched.Epoch == 0 || matched.Epoch < k.minKeyEpoch) {
			return Verified{}, &TrustError{Err: ErrKeyEpochFenced, KID: matched.kid,
				Detail: fmt.Sprintf("a flat license carries no epoch and its key is not pinned at or above %d", k.minKeyEpoch)}
		}
		issued = v.Claims.IssuedAt
	}
	if matched.State == KeyStateVerifyOnly {
		if issued.IsZero() || !issued.Before(matched.RetiredAt) {
			return Verified{}, &TrustError{Err: ErrKeyRetired, KID: matched.kid, Detail: "issued at or after the key's retirement"}
		}
		if !matched.VerifyUntil.IsZero() && !now.Before(matched.VerifyUntil) {
			return Verified{}, &TrustError{Err: ErrKeyRetired, KID: matched.kid, Detail: "the key's verification window has ended"}
		}
	}
	v.Trust = Trust{KID: matched.kid, State: matched.State, Epoch: matched.Epoch, Source: matched.Source}
	return v, nil
}

// declaredKeyID reads the top-level string `key_id` of an UNAUTHENTICATED payload for a refusal
// message. It returns ok only for the derived-KID shape, so arbitrary document text is never
// echoed.
func declaredKeyID(payload []byte) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", false
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", false
		}
		if d, ok := tok.(json.Delim); ok && d == '}' {
			return "", false
		}
		key, ok := tok.(string)
		if !ok {
			return "", false
		}
		val, err := dec.Token()
		if err != nil {
			return "", false
		}
		if _, ok := val.(json.Delim); ok {
			if err := skipContainer(dec); err != nil {
				return "", false
			}
			continue
		}
		if key != "key_id" {
			continue
		}
		s, ok := val.(string)
		if !ok || !kidPattern.MatchString(s) {
			return "", false
		}
		return s, true
	}
}
