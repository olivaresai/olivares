// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// trustdoc.go — the administrative license trust document, `olivares.license.trust.v1`.
//
// It is the ratified "administrative configuration" source of new license public keys (connect-v1
// contract §6). A deployment's operator writes it; it adds keys to, or changes the state of keys
// in, the build's embedded anchor. It carries PUBLIC keys only. A signed rotation document
// authenticated by already-installed trust is a different source with no ratified format, and
// this codec does not accept one.
//
// The codec is strict for the same reason the v3 reader is: duplicate fields, unknown fields,
// non-canonical numbers, non-UTC instants and a `kid` that is not derived from its public key
// are refused, never repaired.

package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// TrustDocumentSchema is the only schema the trust document codec accepts.
const TrustDocumentSchema = "olivares.license.trust.v1"

// MaxTrustDocumentBytes bounds a trust document before it is parsed.
const MaxTrustDocumentBytes = 64 << 10

// TrustDocument is a parsed, validated administrative trust document.
type TrustDocument struct {
	// MinKeyEpoch is the keyring-wide key_epoch fence. Zero means none.
	MinKeyEpoch int
	Keys        []TrustDocumentKey
}

// TrustDocumentKey is one administratively configured key.
type TrustDocumentKey struct {
	PublicKey   ed25519.PublicKey
	State       KeyState
	Epoch       int
	RetiredAt   time.Time
	VerifyUntil time.Time
}

// KID derives the entry's key id. It returns "" for a malformed key.
func (k TrustDocumentKey) KID() string {
	kid, err := KeyID(k.PublicKey)
	if err != nil {
		return ""
	}
	return kid
}

type trustDocWire struct {
	Schema      string         `json:"schema"`
	MinKeyEpoch int            `json:"min_key_epoch,omitempty"`
	Keys        []trustKeyWire `json:"keys"`
}

type trustKeyWire struct {
	KID         string  `json:"kid"`
	PublicKey   string  `json:"public_key"`
	State       string  `json:"state"`
	Epoch       int     `json:"epoch,omitempty"`
	RetiredAt   *string `json:"retired_at,omitempty"`
	VerifyUntil *string `json:"verify_until,omitempty"`
}

// DecodeTrustDocument parses and validates data.
func DecodeTrustDocument(data []byte) (TrustDocument, error) {
	if len(data) > MaxTrustDocumentBytes {
		return TrustDocument{}, fmt.Errorf("%w: the trust document exceeds %d bytes", ErrKeyringInvalid, MaxTrustDocumentBytes)
	}
	if err := rejectDuplicateKeysIn(data, "trust document"); err != nil {
		return TrustDocument{}, fmt.Errorf("%w: %v", ErrKeyringInvalid, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w trustDocWire
	if err := dec.Decode(&w); err != nil {
		return TrustDocument{}, fmt.Errorf("%w: the trust document does not decode: %v", ErrKeyringInvalid, err)
	}
	if err := dec.Decode(new(struct{})); err != io.EOF {
		return TrustDocument{}, fmt.Errorf("%w: trailing data after the trust document", ErrKeyringInvalid)
	}
	if w.Schema != TrustDocumentSchema {
		return TrustDocument{}, fmt.Errorf("%w: schema %q is not %q", ErrKeyringInvalid, w.Schema, TrustDocumentSchema)
	}
	if w.Keys == nil {
		return TrustDocument{}, fmt.Errorf("%w: the trust document has no keys list", ErrKeyringInvalid)
	}
	doc := TrustDocument{MinKeyEpoch: w.MinKeyEpoch}
	for i, kw := range w.Keys {
		raw, err := base64.StdEncoding.DecodeString(kw.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return TrustDocument{}, fmt.Errorf("%w: key %d public_key is not a base64 Ed25519 public key", ErrKeyringInvalid, i)
		}
		k := TrustDocumentKey{PublicKey: ed25519.PublicKey(raw), State: KeyState(kw.State), Epoch: kw.Epoch}
		if derived := k.KID(); kw.KID != derived {
			return TrustDocument{}, fmt.Errorf("%w: key %d kid %q is not the key id derived from its public key (%s)", ErrKeyringInvalid, i, kw.KID, derived)
		}
		if k.RetiredAt, err = optionalUTC("retired_at", kw.RetiredAt); err != nil {
			return TrustDocument{}, fmt.Errorf("%w: key %d: %v", ErrKeyringInvalid, i, err)
		}
		if k.VerifyUntil, err = optionalUTC("verify_until", kw.VerifyUntil); err != nil {
			return TrustDocument{}, fmt.Errorf("%w: key %d: %v", ErrKeyringInvalid, i, err)
		}
		doc.Keys = append(doc.Keys, k)
	}
	if _, err := doc.Apply(nil); err != nil {
		return TrustDocument{}, err
	}
	return doc, nil
}

// EncodeTrustDocument validates doc and renders it deterministically.
func EncodeTrustDocument(doc TrustDocument) ([]byte, error) {
	if _, err := doc.Apply(nil); err != nil {
		return nil, err
	}
	w := trustDocWire{Schema: TrustDocumentSchema, MinKeyEpoch: doc.MinKeyEpoch, Keys: []trustKeyWire{}}
	for _, k := range doc.Keys {
		kw := trustKeyWire{KID: k.KID(), PublicKey: base64.StdEncoding.EncodeToString(k.PublicKey), State: string(k.State), Epoch: k.Epoch}
		if !k.RetiredAt.IsZero() {
			s := k.RetiredAt.UTC().Format(time.RFC3339)
			kw.RetiredAt = &s
		}
		if !k.VerifyUntil.IsZero() {
			s := k.VerifyUntil.UTC().Format(time.RFC3339)
			kw.VerifyUntil = &s
		}
		w.Keys = append(w.Keys, kw)
	}
	out, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Apply merges the document over base and returns the resulting keyring. A document entry with
// the KID of a base key replaces that key's state, epoch and window; other entries are added.
// Entries taken from the document are labelled with source "data-dir".
func (d TrustDocument) Apply(base []TrustedKey) (Keyring, error) {
	byKID := make(map[string]TrustDocumentKey, len(d.Keys))
	for i, k := range d.Keys {
		kid := k.KID()
		if kid == "" {
			return Keyring{}, fmt.Errorf("%w: key %d has no valid public key", ErrKeyringInvalid, i)
		}
		if _, dup := byKID[kid]; dup {
			return Keyring{}, fmt.Errorf("%w: key %s appears twice in the trust document", ErrKeyringInvalid, kid)
		}
		byKID[kid] = k
	}
	keys := make([]TrustedKey, 0, len(base)+len(d.Keys))
	used := make(map[string]struct{}, len(d.Keys))
	for _, b := range base {
		kid, err := KeyID(b.PublicKey)
		if err != nil {
			return Keyring{}, err
		}
		if k, ok := byKID[kid]; ok {
			keys = append(keys, k.trusted("data-dir"))
			used[kid] = struct{}{}
			continue
		}
		keys = append(keys, b)
	}
	for _, k := range d.Keys {
		if _, ok := used[k.KID()]; ok {
			continue
		}
		keys = append(keys, k.trusted("data-dir"))
	}
	kr, err := NewKeyring(keys, d.MinKeyEpoch)
	if err != nil {
		if errors.Is(err, ErrKeyringInvalid) {
			return Keyring{}, err
		}
		return Keyring{}, fmt.Errorf("%w: %v", ErrKeyringInvalid, err)
	}
	return kr, nil
}

func (k TrustDocumentKey) trusted(source string) TrustedKey {
	return TrustedKey{PublicKey: k.PublicKey, State: k.State, Epoch: k.Epoch, RetiredAt: k.RetiredAt, VerifyUntil: k.VerifyUntil, Source: source}
}

// ParseKeyState accepts the three state names exactly.
func ParseKeyState(s string) (KeyState, error) {
	st := KeyState(strings.TrimSpace(s))
	if !st.valid() || string(st) != s {
		return "", fmt.Errorf("%w: state %q is not current, verify_only or revoked", ErrKeyringInvalid, s)
	}
	return st, nil
}
