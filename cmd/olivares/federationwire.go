// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	corefederation "github.com/olivaresai/olivares/core/auth/federation"
	"github.com/olivaresai/olivares/core/model"
)

// newFederation builds the OPEN-CORE single-IdP OIDC/SAML provider from the
// environment. It is wired build-INDEPENDENTLY — the same in the default
// AGPL build and under -tags enterprise — because single-IdP SSO is open-core; the
// default artifact therefore links go-oidc/crewjam. It fails CLOSED: any missing
// or invalid configuration yields auth.NoFederation (SSO requests answer 501)
// rather than a half-configured provider. This env provider is only the FALLBACK
// the FederationService uses when no managed config row exists.
func newFederation(getenv func(string) string, log *slog.Logger) auth.Federation {
	fed, err := corefederation.FromEnv(getenv)
	if err != nil {
		log.Info("env SSO not configured; SSO defers to managed config or answers 501", "err", err)
		return auth.NoFederation{}
	}
	log.Info("env SSO federation enabled (single-IdP, open-core)", "protocol", fed.Protocol())
	return fed
}

// newFederationBuilder returns the OPEN-CORE single-IdP managed-config provider
// builder: the console can configure SSO from a store-backed, sealed
// config in BOTH builds (it was enterprise-only before). The reserved
// multi-IdP line is gated separately via newFederationMultiIDP, not here.
func newFederationBuilder() auth.FederationBuilder {
	return corefederation.FromConfig
}

// federationwire.go is the composition-root half of the managed SSO config:
// the AES-256-GCM SecretSealer that encrypts the OIDC client secret / SAML SP
// private key at rest under an ENGINE-HELD key — the core's FederationService
// stores only sealed strings and never sees key material. It mirrors the eventing
// sealer (eventingwire.go); a dedicated key file keeps SSO custody independent of
// eventing custody.
const (
	// federationSecretKeyEnv supplies the 32-byte base64 sealer key directly (the
	// HA path: every node must seal/open with the SAME key). Unset => a per-node
	// key file in the data dir.
	federationSecretKeyEnv = "OLIVARES_SSO_SECRET_KEY"
	// federationSecretKeyFile is the on-disk key minted on first boot (0600,
	// fail-closed on wider permissions).
	federationSecretKeyFile = "sso-secret.key"
)

// newFederationSealer builds the AES-256-GCM SSO secret sealer over the
// engine-held key (env HA-shared key or a 0600 data-dir key file minted on first
// boot). Fail-closed: a malformed env key or an over-permissive key file is an
// error, never a silent downgrade.
func newFederationSealer(dataDir string, getenv func(string) string) (auth.FederationSealer, error) {
	var key []byte
	if raw := strings.TrimSpace(getenv(federationSecretKeyEnv)); raw != "" {
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("federation: %s must be 32 base64-encoded bytes", federationSecretKeyEnv)
		}
		key = k
	} else {
		// loadOrCreateAEADKey is shared with the eventing sealer (eventingwire.go).
		k, err := loadOrCreateAEADKey(filepath.Join(dataDir, federationSecretKeyFile))
		if err != nil {
			return nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("federation: sealer cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("federation: sealer aead: %w", err)
	}
	return &federationSealer{aead: aead}, nil
}

// federationSealer is AES-256-GCM with the config scope bound as AAD, so a sealed
// SSO secret cannot be replayed across scopes. The sealed form is versioned
// ("v1:" + base64(nonce||ciphertext)).
type federationSealer struct{ aead cipher.AEAD }

const federationSealPrefix = "v1:"

func (s *federationSealer) aad(scope model.TenantID) []byte {
	return []byte("federation.config.secret|" + scope.String())
}

func (s *federationSealer) Seal(_ context.Context, scope model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("federation: seal nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, s.aad(scope))
	return federationSealPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (s *federationSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, federationSealPrefix)
	if !ok {
		return nil, fmt.Errorf("federation: unknown sealed-secret version")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) < s.aead.NonceSize() {
		return nil, fmt.Errorf("federation: malformed sealed secret")
	}
	nonce, ct := b[:s.aead.NonceSize()], b[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, s.aad(scope))
	if err != nil {
		return nil, fmt.Errorf("federation: sealed secret does not open (wrong key or scope)")
	}
	return pt, nil
}
