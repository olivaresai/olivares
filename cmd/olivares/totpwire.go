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
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TOTP seed sealing . The engine holds a 32-byte key — env override for
// HA deployments, or a 0600 data-dir key file minted on first boot — and the
// seeds land in the store as AES-256-GCM envelopes bound to the auth partition
// scope. Same construction as the federation and eventing sealers, with its own
// key and its own AAD purpose so a TOTP seed can never be opened as an SSO
// secret or vice versa.
const (
	totpSeedKeyEnv  = "OLIVARES_TOTP_SEED_KEY"
	totpSeedKeyFile = "totp-seed.key"
)

// newTOTPSeedSealer builds the AES-256-GCM TOTP seed sealer over the
// engine-held key. Fail-closed: a malformed env key or an over-permissive key
// file is an error, never a silent downgrade.
func newTOTPSeedSealer(dataDir string, getenv func(string) string) (auth.TOTPSeedSealer, error) {
	var key []byte
	if raw := strings.TrimSpace(getenv(totpSeedKeyEnv)); raw != "" {
		k, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(k) != 32 {
			return nil, fmt.Errorf("totp: %s must be 32 base64-encoded bytes", totpSeedKeyEnv)
		}
		key = k
	} else {
		// loadOrCreateAEADKey is shared with the eventing and federation sealers.
		k, err := loadOrCreateAEADKey(filepath.Join(dataDir, totpSeedKeyFile))
		if err != nil {
			return nil, err
		}
		key = k
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("totp: sealer cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("totp: sealer aead: %w", err)
	}
	return &totpSeedSealer{aead: aead}, nil
}

// totpSeedSealer seals seeds as "v1:" + base64(nonce||ciphertext), the auth
// partition scope bound as AAD.
type totpSeedSealer struct{ aead cipher.AEAD }

const totpSealPrefix = "v1:"

func (s *totpSeedSealer) aad(scope model.TenantID) []byte {
	return []byte("totp.seed|" + scope.String())
}

func (s *totpSeedSealer) Seal(_ context.Context, scope model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("totp: seal nonce: %w", err)
	}
	ct := s.aead.Seal(nil, nonce, plaintext, s.aad(scope))
	return totpSealPrefix + base64.StdEncoding.EncodeToString(append(nonce, ct...)), nil
}

func (s *totpSeedSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, totpSealPrefix)
	if !ok {
		return nil, fmt.Errorf("totp: unknown sealed-seed version")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) < s.aead.NonceSize() {
		return nil, fmt.Errorf("totp: malformed sealed seed")
	}
	nonce, ct := b[:s.aead.NonceSize()], b[s.aead.NonceSize():]
	pt, err := s.aead.Open(nil, nonce, ct, s.aad(scope))
	if err != nil {
		return nil, fmt.Errorf("totp: sealed seed does not open (wrong key or scope)")
	}
	return pt, nil
}
