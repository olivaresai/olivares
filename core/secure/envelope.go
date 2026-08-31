// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// This file is the CMEK half of the key-custody layer: envelope encryption
// of engine-persisted secrets (the per-event audit signing key, the catalog
// signing key, operator config files) under a customer-managed KEK that lives in
// an EXTERNAL KMS and never touches this host.
//
// The shape is classic envelope encryption: a fresh random 256-bit DEK encrypts
// the payload locally (AES-256-GCM), and only the DEK — never the payload — is
// wrapped by the remote KEK. That keeps the KMS round-trip off every hot path
// (one Unwrap at boot, docs/SECURITY-HARDENING.md: per-event signing stays on-box), works for
// payloads beyond the KMS direct-encrypt size limits, and gives the customer the
// CMEK guarantee that matters: disable the KEK in THEIR KMS and the next boot
// fails closed — the vendor cannot recover the signing key from what is on disk.
//
// What CMEK does NOT give (honest limit, docs/SECURITY-HARDENING.md): while the engine is
// running, the unwrapped key is in process memory; a live host compromise can
// use it until the process dies. The control against the host-compromised
// attacker remains the off-box checkpoint signer (HYOK, core/audit/kmssign).

// KeyWrapper is the seam to an external KMS KEK. Implementations live in
// core/secure/kmswrap (AWS KMS Encrypt/Decrypt, GCP KMS encrypt/decrypt, Azure
// Key Vault wrapKey/unwrapKey — pure-Go REST, mirroring core/audit/kmssign); a
// test supplies an in-process fake. The aad map is non-secret binding context
// (e.g. the envelope purpose): backends that support authenticated context
// (AWS EncryptionContext, GCP additionalAuthenticatedData) MUST bind it so a
// ciphertext cannot be unwrapped under different context; backends that cannot
// (Azure RSA-OAEP wrap has no AAD) ignore it — there the binding rests on the
// local AES-GCM AAD alone.
type KeyWrapper interface {
	// WrapKey wraps a small plaintext (a 32-byte DEK) under the remote KEK.
	WrapKey(ctx context.Context, plaintext []byte, aad map[string]string) ([]byte, error)
	// UnwrapKey reverses WrapKey. It MUST fail if aad does not match what the
	// ciphertext was wrapped under (where the provider supports AAD).
	UnwrapKey(ctx context.Context, ciphertext []byte, aad map[string]string) ([]byte, error)
	// KeyID is the non-secret KEK reference (ARN / resource name / key URL),
	// recorded in the envelope for operability.
	KeyID() string
	// Provider names the backend ("aws-kms" | "gcp-kms" | "azure-kv"), recorded in
	// the envelope and validated at open so a mis-pointed KEK fails loudly.
	Provider() string
}

// Envelope purposes. The purpose is bound into both the KEK wrap context (where
// the provider supports AAD) and the local AES-GCM AAD, so an envelope sealed
// for one purpose can never be opened as another (e.g. a catalog-key envelope
// swapped in as the audit key fails cryptographically, not by convention).
const (
	PurposeAuditSigningKey   = "audit-signing-key"
	PurposeCatalogSigningKey = "catalog-signing-key"
	PurposePolicySigningKey  = "policy-signing-key"
	PurposeOperatorConfig    = "operator-config"
)

// wrapContextKey is the AAD key carrying the purpose to the KMS wrap call.
const wrapContextKey = "olivares:purpose"

// sealedVersion is the on-disk format version.
const sealedVersion = 1

// gcmAADPrefix domain-separates the local AEAD from any other AES-GCM use.
const gcmAADPrefix = "olivares.sealed.v1\x00"

// SealedEnvelope is the at-rest form of a CMEK-wrapped secret. Everything in it
// is non-secret (ciphertext, a wrapped DEK and operability metadata); it still
// must not be world-readable (defense in depth — ReadSealedFile enforces it).
type SealedEnvelope struct {
	// V is the format version (sealedVersion).
	V int `json:"olivares_sealed"`
	// Purpose names what the payload is (Purpose* constants).
	Purpose string `json:"purpose"`
	// Provider / KeyID record the KEK that wrapped the DEK (operability; the KEK
	// reference is non-secret). Open validates Provider against the configured
	// wrapper so a mis-pointed KEK fails with a clear error, not a KMS 4xx.
	Provider string `json:"provider"`
	KeyID    string `json:"key_id"`
	// Context is the AAD the DEK was wrapped under (purpose binding). It is input
	// to UnwrapKey; tampering with it makes the KMS unwrap fail on providers with
	// AAD support.
	Context map[string]string `json:"context,omitempty"`
	// WrappedDEK is the KEK-wrapped 32-byte data-encryption key.
	WrappedDEK []byte `json:"wrapped_dek"`
	// Nonce and Ciphertext are the local AES-256-GCM encryption of the payload
	// under the DEK, with the purpose bound as AAD.
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	// PublicKey is set for signing-key envelopes: the Ed25519 VERIFICATION key of
	// the sealed private key, so operators and verifiers can know which public key
	// an envelope corresponds to without unwrapping it. AUTHENTICATED: bound into
	// the GCM AAD, so editing it on disk makes Open fail.
	PublicKey []byte `json:"public_key,omitempty"`
	// PriorPublicKeys preserves the verification keys of previously sealed
	// generations across `keys rotate`, oldest first — the non-secret rotation
	// history a verifier needs to check a chain whose signing key rotated
	// (audit.VerifyEventsWith). AUTHENTICATED like PublicKey: the default
	// verifier trusts these as candidate keys, so an attacker who can write the
	// envelope file must NOT be able to append a key of their own — the GCM AAD
	// binding makes that a decryption failure instead of a forged candidate.
	PriorPublicKeys [][]byte `json:"prior_public_keys,omitempty"`
	// CreatedAt records when this envelope was sealed (UTC).
	CreatedAt time.Time `json:"created_at"`
}

// gcmAAD builds the local AEAD's additional data: the purpose PLUS the
// envelope's signing-key custody metadata (public key and rotation history),
// length-prefixed. Binding the metadata here is load-bearing: the rotation
// history feeds the default verifier's candidate set (audit.VerifyEventsWith),
// so it must not be attacker-writable JSON — tampering PublicKey or any
// PriorPublicKeys entry makes Open fail authentication.
func gcmAAD(purpose string, pub []byte, priors [][]byte) []byte {
	buf := append([]byte(gcmAADPrefix), lenPrefixed(nil, []byte(purpose))...)
	buf = lenPrefixed(buf, pub)
	for _, p := range priors {
		buf = lenPrefixed(buf, p)
	}
	return buf
}

// lenPrefixed appends a 4-byte big-endian length followed by the bytes.
func lenPrefixed(dst, b []byte) []byte {
	dst = append(dst, byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b)))
	return append(dst, b...)
}

// wrapContext builds the KEK AAD for a purpose.
func wrapContext(purpose string) map[string]string {
	return map[string]string{wrapContextKey: purpose}
}

// Seal envelope-encrypts plaintext for purpose under w's remote KEK: fresh
// 32-byte DEK, AES-256-GCM locally, DEK wrapped by the KEK with the purpose as
// AAD. The DEK is wiped from memory before returning.
func Seal(ctx context.Context, w KeyWrapper, purpose string, plaintext []byte) (*SealedEnvelope, error) {
	return seal(ctx, w, purpose, plaintext, nil, nil)
}

// seal is the full form: pub/priors are the signing-key custody metadata bound
// into the GCM AAD (nil for non-key payloads).
func seal(ctx context.Context, w KeyWrapper, purpose string, plaintext, pub []byte, priors [][]byte) (*SealedEnvelope, error) {
	if w == nil {
		return nil, fmt.Errorf("secure: no key wrapper configured")
	}
	if purpose == "" {
		return nil, fmt.Errorf("secure: a sealed envelope needs a purpose")
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("secure: generate DEK: %w", err)
	}
	defer wipe(dek)
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("secure: init DEK cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secure: init GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secure: generate nonce: %w", err)
	}
	// The KMS wrap happens AFTER the metadata is fixed, so the recorded KeyID
	// reflects what actually wrapped the DEK (an AWS alias resolves to a key ARN
	// during WrapKey — read w.KeyID() after the call, not before).
	wrapped, err := w.WrapKey(ctx, dek, wrapContext(purpose))
	if err != nil {
		return nil, fmt.Errorf("secure: wrap DEK with %s KEK %s: %w", w.Provider(), w.KeyID(), err)
	}
	e := &SealedEnvelope{
		V:          sealedVersion,
		Purpose:    purpose,
		Provider:   w.Provider(),
		KeyID:      w.KeyID(),
		Context:    wrapContext(purpose),
		WrappedDEK: wrapped,
		Nonce:      nonce,
		Ciphertext: gcm.Seal(nil, nonce, plaintext, gcmAAD(purpose, pub, priors)),
		CreatedAt:  time.Now().UTC(),
	}
	if len(pub) > 0 {
		e.PublicKey = append([]byte(nil), pub...)
	}
	for _, p := range priors {
		e.PriorPublicKeys = append(e.PriorPublicKeys, append([]byte(nil), p...))
	}
	return e, nil
}

// Open reverses Seal: it unwraps the DEK through w and decrypts the payload,
// failing closed on a version, purpose or provider mismatch. purpose is what the
// CALLER expects the envelope to hold — never trusted from the file.
func (e *SealedEnvelope) Open(ctx context.Context, w KeyWrapper, purpose string) ([]byte, error) {
	if w == nil {
		return nil, fmt.Errorf("secure: no key wrapper configured")
	}
	if e.V != sealedVersion {
		return nil, fmt.Errorf("secure: unsupported sealed envelope version %d (want %d)", e.V, sealedVersion)
	}
	if e.Purpose != purpose {
		return nil, fmt.Errorf("secure: sealed envelope holds %q, expected %q", e.Purpose, purpose)
	}
	if e.Provider != w.Provider() {
		return nil, fmt.Errorf("secure: sealed envelope was wrapped by %s but the KEK being used to open it is %s — point OLIVARES_KEY_WRAP at the right provider, or, if you are MIGRATING custody, declare the provider that wrapped this envelope in OLIVARES_KEY_WRAP_OLD and run `keys rewrap` (a stale OLIVARES_KEY_WRAP_OLD from a finished migration produces this same error: unset it)", e.Provider, w.Provider())
	}
	// Pass the envelope's recorded context: on AAD-capable providers the KMS
	// authenticates it, so a tampered context fails there; the purpose is
	// INDEPENDENTLY bound below via the GCM AAD built from the caller's purpose.
	dek, err := w.UnwrapKey(ctx, e.WrappedDEK, e.Context)
	if err != nil {
		return nil, fmt.Errorf("secure: unwrap DEK with %s KEK %s: %w", w.Provider(), w.KeyID(), err)
	}
	defer wipe(dek)
	if len(dek) != 32 {
		return nil, fmt.Errorf("secure: unwrapped DEK is %d bytes, want 32", len(dek))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("secure: init DEK cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secure: init GCM: %w", err)
	}
	if len(e.Nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("secure: sealed envelope nonce is %d bytes, want %d", len(e.Nonce), gcm.NonceSize())
	}
	// The AAD re-binds the RECORDED custody metadata: a tampered PublicKey or
	// prior_public_keys entry (the rotation history the default verifier trusts)
	// fails here cryptographically, not by convention.
	pt, err := gcm.Open(nil, e.Nonce, e.Ciphertext, gcmAAD(purpose, e.PublicKey, e.PriorPublicKeys))
	if err != nil {
		return nil, fmt.Errorf("secure: sealed envelope failed authentication (tampered payload or custody metadata, or sealed under a different purpose)")
	}
	return pt, nil
}

// IsSealedEnvelope reports whether data looks like a sealed envelope (the JSON
// magic field). It is the cheap sniff loaders use to decide between a plaintext
// config/key file and a CMEK-wrapped one.
func IsSealedEnvelope(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var probe struct {
		V int `json:"olivares_sealed"`
	}
	return json.Unmarshal(trimmed, &probe) == nil && probe.V != 0
}

// DecodeSealedEnvelope parses a sealed envelope, rejecting non-envelope input.
func DecodeSealedEnvelope(data []byte) (*SealedEnvelope, error) {
	if !IsSealedEnvelope(data) {
		return nil, fmt.Errorf("secure: not a sealed envelope (missing olivares_sealed marker)")
	}
	var e SealedEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(data), &e); err != nil {
		return nil, fmt.Errorf("secure: parse sealed envelope: %w", err)
	}
	return &e, nil
}

// ReadSealedFile loads a sealed envelope from path. Envelope files hold only
// ciphertext and metadata, but they are still read under the shared-secret
// permission rule (no world bits) as defense in depth — they are typically
// mounted from the same Secret as a plaintext key would have been.
func ReadSealedFile(path string) (*SealedEnvelope, error) {
	b, err := readSharedSecret(path)
	if err != nil {
		return nil, err
	}
	return DecodeSealedEnvelope(b)
}

// WriteSealedFile persists a sealed envelope at path (0600, atomic).
func WriteSealedFile(path string, e *SealedEnvelope) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("secure: encode sealed envelope: %w", err)
	}
	return writeSecret(path, append(b, '\n'))
}

// SealSigningKey envelopes an Ed25519 signing key for purpose, recording its
// public half (and the prior generations' public halves on rotation) so the
// rotation history stays verifiable without ever unwrapping. Both are BOUND
// into the envelope's GCM AAD — the history feeds verification, so it must be
// tamper-evident, not plain JSON. The payload is the same base64 wire form
// LoadOrCreateSigningKey writes, so an unwrapped envelope feeds
// DecodeSigningKey unchanged.
func SealSigningKey(ctx context.Context, w KeyWrapper, purpose string, priv ed25519.PrivateKey, priors [][]byte) (*SealedEnvelope, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("secure: bad private key size %d", len(priv))
	}
	return seal(ctx, w, purpose, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"),
		priv.Public().(ed25519.PublicKey), priors)
}

// OpenSigningKey unwraps a signing-key envelope and validates that the sealed
// private key matches the envelope's recorded public key — a mismatch means the
// envelope was assembled inconsistently and MUST NOT be trusted as custody
// metadata (rotation history would point at the wrong key).
func (e *SealedEnvelope) OpenSigningKey(ctx context.Context, w KeyWrapper, purpose string) (ed25519.PrivateKey, error) {
	pt, err := e.Open(ctx, w, purpose)
	if err != nil {
		return nil, err
	}
	defer wipe(pt)
	priv, err := DecodeSigningKey(string(pt))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), e.PublicKey) {
		return nil, fmt.Errorf("secure: sealed signing key does not match the envelope's recorded public key — refusing an inconsistent custody record")
	}
	return priv, nil
}

// LoadSealedSigningKey reads and unwraps a signing-key envelope from path. It is
// the CMEK counterpart of LoadSigningKey: load-only and fail-closed — a
// missing or unopenable envelope is a configuration (or revocation) signal,
// never a cue to mint. It returns the envelope too, so the caller can surface
// the rotation history (PriorPublicKeys) to verification.
func LoadSealedSigningKey(ctx context.Context, path string, w KeyWrapper, purpose string) (ed25519.PrivateKey, *SealedEnvelope, error) {
	e, err := ReadSealedFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("secure: read sealed signing key %q: %w", path, err)
	}
	priv, err := e.OpenSigningKey(ctx, w, purpose)
	if err != nil {
		return nil, nil, err
	}
	return priv, e, nil
}

// MintSealedSigningKey generates a fresh Ed25519 key and seals it for purpose.
// It NEVER persists the private key in clear — the only at-rest form is the
// envelope the caller writes. Minting is a deliberate operator ceremony (the
// `keys wrap --mint` / `keys rotate` CLI), never an implicit boot side effect:
// in CMEK custody an absent envelope fails the boot closed instead.
func MintSealedSigningKey(ctx context.Context, w KeyWrapper, purpose string, priors [][]byte) (ed25519.PrivateKey, *SealedEnvelope, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("secure: generate signing key: %w", err)
	}
	e, err := SealSigningKey(ctx, w, purpose, priv, priors)
	if err != nil {
		return nil, nil, err
	}
	return priv, e, nil
}

// authenticate opens the envelope purely to PROVE its custody metadata is
// unedited, and never hands the plaintext back.
//
// The shape is the point. This started as an inline Open whose returned plaintext
// — the sealed PRIVATE KEY — was discarded unwiped, turning a custody fix into a
// secret-lifetime regression; the wipe that repaired it was then shown by an
// adversarial panel to be UNGATED (deleting it left every test green, because
// zeroing a local slice is not observable from outside the package). A test cannot
// hold that invariant, so the API does instead: a caller that only needs the
// authentication verdict cannot obtain the secret to forget about it.
func (e *SealedEnvelope) authenticate(ctx context.Context, w KeyWrapper, purpose string) error {
	pt, err := e.Open(ctx, w, purpose)
	if err != nil {
		return err
	}
	wipe(pt)
	return nil
}

// RotateSealedSigningKey mints a NEW signing key sealed under newW, carrying the
// old envelope's public key (and its own priors) forward as rotation history.
// The caller stops the engine, writes the returned envelope over the old one and
// restarts; events from then on are signed by the new key, and a verifier covers
// the whole chain by pinning current + prior public keys (audit.VerifyEventsWith).
//
// It OPENS the old envelope with oldW first, and that is not a formality. The
// history it carries forward becomes the default verifier's candidate set, so
// SealedEnvelope binds PublicKey and PriorPublicKeys into the GCM AAD precisely
// so an attacker who can write the file cannot append a key of their own. This
// function used to copy those fields straight out of the decoded JSON: the edit
// failed on the read path, then the documented rotation ceremony sealed it into a
// FRESH, valid envelope and laundered it into authenticated custody metadata
// (found by the Codex contrast of 2026-08-06, F-02). Opening first means an
// envelope that cannot prove its own history is refused instead of blessed.
//
// oldW and newW are separate for the same reason RewrapSealed takes two: the old
// envelope may record a KEK version that the configured (post-rotation) KEK no
// longer addresses. Pass the unwrap-side wrapper for oldW.
func RotateSealedSigningKey(ctx context.Context, oldW, newW KeyWrapper, old *SealedEnvelope) (ed25519.PrivateKey, *SealedEnvelope, error) {
	purpose := PurposeAuditSigningKey
	var priors [][]byte
	if old != nil {
		if old.Purpose != "" {
			purpose = old.Purpose
		}
		if err := old.authenticate(ctx, oldW, purpose); err != nil {
			return nil, nil, fmt.Errorf("secure: refusing to rotate an envelope whose custody metadata does not authenticate — its public key and rotation history become verification candidates, so they must be proven unedited BEFORE being carried forward: %w", err)
		}
		priors = append(priors, old.PriorPublicKeys...)
		if len(old.PublicKey) > 0 {
			priors = append(priors, old.PublicKey)
		}
	}
	return MintSealedSigningKey(ctx, newW, purpose, priors)
}

// RewrapSealed re-envelopes an existing sealed payload under a NEW KEK (KEK
// rotation): open with the old wrapper, seal fresh (new DEK, new nonce) with
// the new one, preserving purpose, public key and rotation history (re-bound
// into the new AAD). The payload exists only in memory in between.
func RewrapSealed(ctx context.Context, e *SealedEnvelope, oldW, newW KeyWrapper) (*SealedEnvelope, error) {
	pt, err := e.Open(ctx, oldW, e.Purpose)
	if err != nil {
		return nil, err
	}
	defer wipe(pt)
	return seal(ctx, newW, e.Purpose, pt, e.PublicKey, e.PriorPublicKeys)
}

// wipe best-effort zeroes sensitive bytes (the GC may have copied them, but not
// leaving the only reachable copy around is still worth the loop).
func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
