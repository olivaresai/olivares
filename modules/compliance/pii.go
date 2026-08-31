// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the crypto-shredding primitive: tokenize-at-write +
// destroy-the-key, the strategy that reconciles RTBF with an append-only,
// hash-chained, WORM-archived ledger (docs/SECURITY-HARDENING.md). The ledger's bytes can never
// change — the chain hash commits to every stored field (core/internal/store/canon),
// per-event Ed25519 signatures sign the hash, DB triggers block UPDATE/DELETE, and
// archival verifier re-derives every hash from the exact stored bytes. So a
// subject reference that must live in immutable media is written as a pii TOKEN
// (AES-256-GCM under a per-(tenant, subject) DEK held in the mutable
// compliance_subject_key row); erasing the subject HARD-deletes that row, destroying
// the plaintext mapping and the only key that opens the tokens in one atomic act.
// The bytes stay; the meaning is gone — NIST SP 800-88 cryptographic erase.
//
// Honest limits (documented in docs/RIGHT-TO-ERASURE.md): a database backup taken
// BEFORE the shred retains the key row until that backup itself expires — true
// crypto-erasure is bounded by the operator's backup retention window; and a token
// is only as private as the DEK row's storage (the same trust boundary as every
// other tenant-store row the token protects against APPEND-ONLY copies, exports and
// WORM archives — not against a live database compromise).

// piiTokenPrefix is the wire prefix of a sealed subject token:
// "pii:v1:<key-row-id>:<base64(nonce||ciphertext)>".
const piiTokenPrefix = "pii:v1:"

// piiTokenAAD domain-separates the token AEAD from any other AES-GCM use and binds
// the ciphertext to its tenant and key row, so a token can never be opened under a
// different key row or replayed across tenants.
const piiTokenAAD = "olivares.pii.token.v1"

// subjectPayloadAAD domain-separates the data-plane payload stored in
// compliance_subject_key. The payload holds the subject ref + aliases encrypted
// under the subject DEK; subject_ref stores only a lookup digest for new rows.
const subjectPayloadAAD = "olivares.subject.payload.v1"

// erasedTokenDisplay is what a shredded token renders as — the receipt's permanent,
// non-identifying stand-in for the erased subject.
const erasedTokenDisplay = "[ERASED]"

// ErrKeyShredded reports that a token's key row no longer exists: the subject was
// crypto-shredded and the token is permanently unintelligible. It is an expected
// outcome, not a fault.
var ErrKeyShredded = errors.New("compliance: subject key shredded; token is permanently unintelligible")

// subjectKey is the in-memory view of one key-ring row.
type subjectKey struct {
	ID      model.ID
	Kind    string
	Ref     string
	Aliases []string
	dek     []byte
}

type subjectPayload struct {
	Ref     string   `json:"ref"`
	Aliases []string `json:"aliases,omitempty"`
}

// identifiers returns the subject's primary ref plus aliases, deduplicated, in
// declaration order. Every identifier is matched and hold-checked individually.
func (k subjectKey) identifiers() []string {
	out := make([]string, 0, 1+len(k.Aliases))
	seen := map[string]struct{}{}
	for _, ref := range append([]string{k.Ref}, k.Aliases...) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out
}

// mintSubjectKey creates the key-ring row for a subject inside the caller's
// transaction: a fresh 256-bit DEK plus the plaintext identifiers it will erase.
// A second live request for the SAME (kind, ref) collides on the unique index and
// surfaces store.ErrConflict — the caller reuses the existing row instead.
func mintSubjectKey(ctx context.Context, sc store.Scope, kind, ref string, aliases []string, createdBy string) (subjectKey, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return subjectKey{}, fmt.Errorf("compliance: generate subject DEK: %w", err)
	}
	repo, err := sc.Ext(subjectKeyKind)
	if err != nil {
		return subjectKey{}, err
	}
	rec, err := repo.Create(ctx, model.Record{
		colSKSubjectKind: kind,
		colSKSubjectRef:  subjectLookupDigest(sc.Tenant(), kind, ref),
		colSKDEK:         dek,
		colSKCreatedBy:   createdBy,
	})
	if err != nil {
		return subjectKey{}, err
	}
	key := subjectKey{ID: model.ID(rec.String(model.ColID)), Kind: kind, Ref: ref, Aliases: aliases, dek: dek}
	payload, err := sealSubjectPayload(sc.Tenant(), key)
	if err != nil {
		return subjectKey{}, err
	}
	rec[colSKPayload] = payload
	rec[colSKAliases] = nil
	if _, err := repo.Update(ctx, rec); err != nil {
		return subjectKey{}, err
	}
	return key, nil
}

// findSubjectKey loads the live key row for a subject, or ok=false after a shred
// (or when none was ever minted).
func findSubjectKey(ctx context.Context, sc store.Scope, kind, ref string) (subjectKey, bool, error) {
	repo, err := sc.Ext(subjectKeyKind)
	if err != nil {
		return subjectKey{}, false, err
	}
	rec, ok, err := findOne(ctx, repo, eq(colSKSubjectKind, kind), eq(colSKSubjectRef, subjectLookupDigest(sc.Tenant(), kind, ref)))
	if err != nil || !ok {
		if err != nil {
			return subjectKey{}, false, err
		}
		// In-place upgrade compatibility for pre rows that stored the subject
		// reference in plaintext. New rows never take this path.
		rec, ok, err = findOne(ctx, repo, eq(colSKSubjectKind, kind), eq(colSKSubjectRef, ref))
		if err != nil || !ok {
			return subjectKey{}, false, err
		}
	}
	key, err := subjectKeyOf(sc.Tenant(), rec)
	return key, err == nil, err
}

// getSubjectKey loads a key row by id (the token's key reference), or
// ErrKeyShredded when the row is gone.
func getSubjectKey(ctx context.Context, sc store.Scope, id model.ID) (subjectKey, error) {
	repo, err := sc.Ext(subjectKeyKind)
	if err != nil {
		return subjectKey{}, err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return subjectKey{}, ErrKeyShredded
		}
		return subjectKey{}, err
	}
	return subjectKeyOf(sc.Tenant(), rec)
}

// shredSubjectKey HARD-deletes the key row — the crypto-shred. After this commits,
// every token sealed under the row's DEK (in this store, in append-only custody, in
// WORM archives, in exports) is permanently unintelligible, and the plaintext
// subject mapping is gone with it.
func shredSubjectKey(ctx context.Context, sc store.Scope, id model.ID) error {
	repo, err := sc.Ext(subjectKeyKind)
	if err != nil {
		return err
	}
	return repo.Delete(ctx, id)
}

func subjectKeyOf(tenant model.TenantID, rec model.Record) (subjectKey, error) {
	key := subjectKey{
		ID:      model.ID(rec.String(model.ColID)),
		Kind:    rec.String(colSKSubjectKind),
		Ref:     rec.String(colSKSubjectRef),
		Aliases: decodeStrings(rec.String(colSKAliases)),
		dek:     rec.Bytes(colSKDEK),
	}
	if payload := rec.String(colSKPayload); strings.TrimSpace(payload) != "" {
		p, err := openSubjectPayload(tenant, key, payload)
		if err != nil {
			return subjectKey{}, err
		}
		key.Ref = p.Ref
		key.Aliases = p.Aliases
	}
	return key, nil
}

func subjectLookupDigest(tenant model.TenantID, kind, ref string) string {
	return "sha256:" + hashHex("olivares.subject.lookup.v1\x00"+tenant.String()+"\x00"+kind+"\x00"+ref)
}

func subjectPayloadAADFor(tenant model.TenantID, key subjectKey) []byte {
	return []byte(subjectPayloadAAD + "\x00" + tenant.String() + "\x00" + key.ID.String() + "\x00" + key.Kind)
}

func sealSubjectPayload(tenant model.TenantID, key subjectKey) (string, error) {
	if len(key.dek) != 32 {
		return "", fmt.Errorf("compliance: subject key %s has a malformed DEK", key.ID)
	}
	body, err := json.Marshal(subjectPayload{Ref: key.Ref, Aliases: key.Aliases})
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key.dek)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject payload cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject payload GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("compliance: generate subject payload nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, body, subjectPayloadAADFor(tenant, key))
	return base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func openSubjectPayload(tenant model.TenantID, key subjectKey, sealed string) (subjectPayload, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return subjectPayload{}, fmt.Errorf("compliance: malformed subject payload: %w", err)
	}
	block, err := aes.NewCipher(key.dek)
	if err != nil {
		return subjectPayload{}, fmt.Errorf("compliance: init subject payload cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return subjectPayload{}, fmt.Errorf("compliance: init subject payload GCM: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return subjectPayload{}, fmt.Errorf("compliance: malformed subject payload")
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], subjectPayloadAADFor(tenant, key))
	if err != nil {
		return subjectPayload{}, fmt.Errorf("compliance: subject payload failed authentication")
	}
	var out subjectPayload
	if err := json.Unmarshal(pt, &out); err != nil {
		return subjectPayload{}, fmt.Errorf("compliance: malformed subject payload JSON: %w", err)
	}
	return out, nil
}

// tokenAAD binds a token to (tenant, key row): the same plaintext sealed for a
// different tenant or key row authenticates as garbage, by construction.
func tokenAAD(tenant model.TenantID, keyID model.ID) []byte {
	return []byte(piiTokenAAD + "\x00" + tenant.String() + "\x00" + keyID.String())
}

// sealSubjectToken encrypts a subject reference under the key row's DEK and returns
// the wire token. Tokens are non-deterministic (fresh nonce per seal) — equality of
// two tokens never reveals equality of two subjects.
func sealSubjectToken(tenant model.TenantID, key subjectKey, plaintext string) (string, error) {
	if len(key.dek) != 32 {
		return "", fmt.Errorf("compliance: subject key %s has a malformed DEK", key.ID)
	}
	block, err := aes.NewCipher(key.dek)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("compliance: generate token nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), tokenAAD(tenant, key.ID))
	return piiTokenPrefix + key.ID.String() + ":" + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

// parseTokenKeyID extracts the key-row id from a wire token without opening it —
// enough to know WHICH key a token needs (and so whether it is already shredded).
func parseTokenKeyID(token string) (model.ID, string, error) {
	rest, ok := strings.CutPrefix(token, piiTokenPrefix)
	if !ok {
		return "", "", fmt.Errorf("compliance: not a pii token")
	}
	keyID, body, ok := strings.Cut(rest, ":")
	if !ok || keyID == "" || body == "" {
		return "", "", fmt.Errorf("compliance: malformed pii token")
	}
	return model.ID(keyID), body, nil
}

// openSubjectToken decrypts a wire token inside the caller's scope. A missing key
// row returns ErrKeyShredded — the expected post-erasure outcome.
func openSubjectToken(ctx context.Context, sc store.Scope, tenant model.TenantID, token string) (string, error) {
	keyID, body, err := parseTokenKeyID(token)
	if err != nil {
		return "", err
	}
	key, err := getSubjectKey(ctx, sc, keyID)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("compliance: malformed pii token body: %w", err)
	}
	block, err := aes.NewCipher(key.dek)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("compliance: init subject GCM: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("compliance: malformed pii token body")
	}
	pt, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], tokenAAD(tenant, key.ID))
	if err != nil {
		return "", fmt.Errorf("compliance: pii token failed authentication")
	}
	return string(pt), nil
}

// subjectPlanDigest is the PII-free binding of a subject identity into a plan hash:
// an HMAC-SHA256 under the subject's own DEK over the canonical identifier list.
// While the key lives it is deterministic (stable across approval polls); after the
// shred it can never be recomputed for any candidate subject — unlike an unkeyed
// hash, it is not dictionary-attackable from the stored plan (low-entropy refs like
// emails would otherwise be recoverable from a plain SHA-256).
func subjectPlanDigest(key subjectKey) string {
	mac := hmac.New(sha256.New, key.dek)
	mac.Write([]byte("olivares.pii.plan.v1"))
	for _, id := range key.identifiers() {
		var n [4]byte
		l := len(id)
		n[0], n[1], n[2], n[3] = byte(l>>24), byte(l>>16), byte(l>>8), byte(l)
		mac.Write(n[:])
		mac.Write([]byte(id))
	}
	return hex.EncodeToString(mac.Sum(nil))
}
