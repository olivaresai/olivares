// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// KeyWrapper is the seam to an external KMS KEK. Implementations are supplied
// by the Business edition (AWS KMS, Google Cloud KMS, Azure Key Vault); a
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
	ProviderAWS              = "aws-kms"
	ProviderGCP              = "gcp-kms"
	ProviderAzure            = "azure-kv"
	PurposeAuditSigningKey   = "audit-signing-key"
	PurposeCatalogSigningKey = "catalog-signing-key"
	PurposePolicySigningKey  = "policy-signing-key"
	PurposeOperatorConfig    = "operator-config"
)

// SealedEnvelope is the at-rest form of a CMEK-wrapped secret. Everything in it
// is non-secret (ciphertext, a wrapped DEK and operability metadata); it still
// must not be world-readable (defense in depth — ReadSealedFile enforces it).
type SealedEnvelope struct {
	// V is the on-disk format version (currently 1).
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
