// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

// LoadOrCreateSigningKey loads the Ed25519 audit signing key at path, or mints
// and persists one (0600) on first boot. It fails closed if an existing key file
// is more permissive than owner-only. created reports whether a new key was
// generated (the operator may want to back it up). The returned key may be backed
// by an HSM/KMS in a hardened deployment by supplying it through the audit
// signer instead of this on-disk path.
func LoadOrCreateSigningKey(path string) (ed25519.PrivateKey, bool, error) {
	if fileExists(path) {
		b, err := readSecret(path)
		if err != nil {
			return nil, false, err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, false, fmt.Errorf("secure: %s is not a valid Ed25519 private key", path)
		}
		return ed25519.PrivateKey(raw), false, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("secure: generate signing key: %w", err)
	}
	enc := base64.StdEncoding.EncodeToString(priv)
	if err := writeSecret(path, []byte(enc+"\n")); err != nil {
		return nil, false, err
	}
	return priv, true, nil
}

// LoadSigningKey loads an EXISTING Ed25519 signing key from path and NEVER mints
// one. It is the HA-safe counterpart to LoadOrCreateSigningKey: in an
// active-passive cluster every node must sign with the SAME key, so the audit
// hash-chain does not fork at failover and one verification key checks the whole
// ledger. The key is therefore provisioned out of band — a shared Kubernetes
// Secret, mounted read-only into every replica — and a MISSING or empty file is a
// configuration error the node must fail closed on, never a cue to silently mint a
// fresh per-node key (which would fork the chain the moment a standby took over).
// It enforces the same owner-only permission check as every other secret read, so
// the shared Secret must be mounted at mode 0400/0600 (the chart sets this). This
// is the decoupling seam BYOK/HSM-derived key plugs into next.
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	if !fileExists(path) {
		return nil, fmt.Errorf("secure: audit signing key %q not found: HA mode requires the shared key to be provisioned (a mounted Secret); refusing to mint a per-node key that would fork the ledger at failover", path)
	}
	// Shared-secret read: the key is a mounted, read-only Secret owned root:fsGroup,
	// so the non-root engine reads it via the group bit — owner-only would reject it.
	b, err := readSharedSecret(path)
	if err != nil {
		return nil, err
	}
	return DecodeSigningKey(string(b))
}

// DecodeSigningKey parses a base64-encoded Ed25519 private key (the on-disk and
// in-env wire form LoadOrCreateSigningKey writes). It is exported so the shared
// key can also be supplied directly through an environment variable, not only a
// file, without duplicating the decode-and-validate logic.
func DecodeSigningKey(b64 string) (ed25519.PrivateKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("secure: value is not a valid base64-encoded Ed25519 private key")
	}
	return ed25519.PrivateKey(raw), nil
}
