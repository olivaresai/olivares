// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package opgate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// KeyPurpose and CustodySource are the closed selected-signer vocabulary of v1.
// Historical auxiliary custody and checkpoint dependencies require a later format.
type KeyPurpose string
type CustodySource string

const (
	KeyAudit        KeyPurpose    = "audit"
	KeyCatalog      KeyPurpose    = "catalog"
	KeyPolicy       KeyPurpose    = "policy"
	CustodyLocal    CustodySource = "local"
	CustodyBYOKEnv  CustodySource = "byok-env"
	CustodyBYOKFile CustodySource = "byok-file"
	CustodyCMEK     CustodySource = "cmek"
	KeysetFormat                  = 1
)

type PublicFingerprint [sha256.Size]byte
type KeysetDigest [sha256.Size]byte

func (f PublicFingerprint) String() string { return hex.EncodeToString(f[:]) }
func (d KeysetDigest) String() string      { return hex.EncodeToString(d[:]) }

// FingerprintPublicKey accepts only the raw Ed25519 public key, never private bytes.
func FingerprintPublicKey(public []byte) (PublicFingerprint, error) {
	if len(public) != ed25519.PublicKeySize {
		return PublicFingerprint{}, errors.New("opgate: selected public key must be 32 bytes")
	}
	return PublicFingerprint(sha256.Sum256(public)), nil
}

// CustodySourceForLoader is the single translation of the existing loader modes.
// An installed local file and a newly minted key have the same source.
func CustodySourceForLoader(mode string) (CustodySource, error) {
	switch mode {
	case "minted":
		return CustodyLocal, nil
	case "byok-env":
		return CustodyBYOKEnv, nil
	case "byok-file":
		return CustodyBYOKFile, nil
	case "cmek":
		return CustodyCMEK, nil
	default:
		return "", errors.New("opgate: unsupported custody loader mode")
	}
}

type SelectedKey struct {
	Purpose      KeyPurpose
	Source       CustodySource
	PublicSHA256 PublicFingerprint
}

// SelectedKeyset is an immutable, validated public commitment, not restore
// authority or evidence that the application's actual signers were selected.
// Keyset is its localized wire codec, retained at the existing record interface.
type SelectedKeyset struct {
	keys   [3]SelectedKey
	digest KeysetDigest
}

func (s SelectedKeyset) Keys() [3]SelectedKey { return s.keys }
func (s SelectedKeyset) Digest() KeysetDigest { return s.digest }

// NewKeyset validates exactly one audit/catalog/policy entry, in that order, and
// derives the commitment. No caller-supplied digest participates in construction.
func NewKeyset(keys []SelectedKey) (Keyset, error) {
	digest, err := canonicalKeysetDigest(keys)
	if err != nil {
		return Keyset{}, err
	}
	wire := Keyset{Format: KeysetFormat, SHA256: digest.String(), Keys: make([]KeyFingerprint, len(keys))}
	for i, k := range keys {
		wire.Keys[i] = KeyFingerprint{Purpose: string(k.Purpose), Source: string(k.Source), PublicSHA256: k.PublicSHA256.String()}
	}
	return wire, nil
}

// Typed recomputes the commitment, including source, from the closed wire set.
func (k Keyset) Typed() (SelectedKeyset, error) {
	var out SelectedKeyset
	if k.Format != KeysetFormat {
		return out, errors.New("opgate: unsupported keyset format")
	}
	if err := validDigest("keyset", k.SHA256); err != nil {
		return out, err
	}
	keys := make([]SelectedKey, len(k.Keys))
	for i, v := range k.Keys {
		if err := validDigest("public key", v.PublicSHA256); err != nil {
			return out, err
		}
		b, _ := hex.DecodeString(v.PublicSHA256)
		keys[i] = SelectedKey{Purpose: KeyPurpose(v.Purpose), Source: CustodySource(v.Source)}
		copy(keys[i].PublicSHA256[:], b)
	}
	digest, err := canonicalKeysetDigest(keys)
	if err != nil {
		return out, err
	}
	if digest.String() != k.SHA256 {
		return out, errors.New("opgate: keyset digest does not commit to its keys")
	}
	copy(out.keys[:], keys)
	out.digest = digest
	return out, nil
}

// This is the only canonical digest encoder. Field order, prefix, and compact
// array bytes are the ratified v1 contract; do not hash the record or wire Keyset.
func canonicalKeysetDigest(keys []SelectedKey) (KeysetDigest, error) {
	if len(keys) != 3 {
		return KeysetDigest{}, errors.New("opgate: keyset requires exactly audit, catalog, policy; missing/extra entries or no keys")
	}
	purposes := [3]KeyPurpose{KeyAudit, KeyCatalog, KeyPolicy}
	type entry struct {
		Purpose      KeyPurpose    `json:"purpose"`
		Source       CustodySource `json:"source"`
		PublicSHA256 string        `json:"public_sha256"`
	}
	entries := [3]entry{}
	seen := map[KeyPurpose]bool{}
	for i, k := range keys {
		if seen[k.Purpose] {
			return KeysetDigest{}, fmt.Errorf("opgate: keyset names purpose %q twice", k.Purpose)
		}
		seen[k.Purpose] = true
		if k.Purpose != purposes[i] {
			return KeysetDigest{}, errors.New("opgate: keyset entries must be audit, catalog, policy in canonical purpose order")
		}
		switch k.Source {
		case CustodyLocal, CustodyBYOKEnv, CustodyBYOKFile, CustodyCMEK:
		default:
			return KeysetDigest{}, errors.New("opgate: unsupported custody source")
		}
		if k.PublicSHA256 == (PublicFingerprint{}) {
			return KeysetDigest{}, errors.New("opgate: empty public fingerprint")
		}
		entries[i] = entry{k.Purpose, k.Source, k.PublicSHA256.String()}
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return KeysetDigest{}, err
	}
	return KeysetDigest(sha256.Sum256(append([]byte("olivares.dr.keyset.v1\n"), raw...))), nil
}
func (k Keyset) empty() bool { return k.Format == 0 && k.SHA256 == "" && len(k.Keys) == 0 }
func (k Keyset) MarshalJSON() ([]byte, error) {
	if k.empty() {
		return []byte("null"), nil
	}
	type wire Keyset
	return json.Marshal(wire(k))
}
