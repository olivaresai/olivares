// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmswrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// GCP is a KEK wrapper backed by Google Cloud KMS encrypt/decrypt on a
// SYMMETRIC (ENCRYPT_DECRYPT) cryptoKey. The wrap context travels as
// additionalAuthenticatedData (canonicalized bytes), which Cloud KMS
// authenticates. Pure-Go; bearer auth via a TokenSource (no cgo).
//
// Verified against cloud.google.com (Cloud KMS REST encrypt / decrypt): POST
// .../cryptoKeys/K:encrypt {plaintext: base64, additionalAuthenticatedData:
// base64} → {ciphertext: base64}; :decrypt is the reverse. NOTE the scope
// difference from the ledger signer: sign keys are cryptoKeyVersion-scoped, but
// encrypt targets a cryptoKey (its primary version) and decrypt AUTO-DETECTS
// the version from the ciphertext — so KEK rotation in Cloud KMS is transparent
// to existing envelopes, and a cryptoKeyVersion path here is a config error.
type GCP struct {
	name  string // projects/*/locations/*/keyRings/*/cryptoKeys/*
	token TokenSource
	doer  Doer
	base  string // override (tests); default https://cloudkms.googleapis.com/v1/
}

// GCPConfig configures a Cloud KMS KEK wrapper.
type GCPConfig struct {
	// KeyName is the full cryptoKey resource name (NOT a cryptoKeyVersion).
	KeyName string
	// Token yields the OAuth2 bearer token (scope cloud-platform / cloudkms).
	Token TokenSource
	// Doer overrides the HTTP client (tests); nil uses http.DefaultClient.
	Doer Doer
	// BaseURL overrides the API base (tests).
	BaseURL string
}

// NewGCP builds a Cloud KMS KEK wrapper.
func NewGCP(cfg GCPConfig) (*GCP, error) {
	name := strings.TrimSpace(cfg.KeyName)
	if name == "" {
		return nil, fmt.Errorf("kmswrap: gcp crypto key name is required")
	}
	if strings.Contains(name, "/cryptoKeyVersions/") {
		return nil, fmt.Errorf("kmswrap: gcp KEK must be a cryptoKey resource (encrypt/decrypt are key-scoped; decrypt auto-detects the version), got a cryptoKeyVersion: %s", name)
	}
	if !strings.Contains(name, "/cryptoKeys/") {
		return nil, fmt.Errorf("kmswrap: gcp KEK %q is not a cryptoKeys resource name", name)
	}
	if cfg.Token == nil {
		return nil, fmt.Errorf("kmswrap: gcp token source is required")
	}
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://cloudkms.googleapis.com/v1/"
	}
	return &GCP{name: name, token: cfg.Token, doer: doer, base: strings.TrimSuffix(base, "/") + "/"}, nil
}

// Provider reports the backend name recorded in envelopes.
func (g *GCP) Provider() string { return ProviderGCP }

// KeyID reports the cryptoKey resource name.
func (g *GCP) KeyID() string { return g.name }

// WrapKey encrypts the DEK under the cryptoKey's primary version with the
// context bound as AAD.
func (g *GCP) WrapKey(ctx context.Context, plaintext []byte, aad map[string]string) ([]byte, error) {
	body := map[string]string{
		"plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}
	if c := canonicalAAD(aad); len(c) > 0 {
		body["additionalAuthenticatedData"] = base64.StdEncoding.EncodeToString(c)
	}
	var resp struct {
		Ciphertext string `json:"ciphertext"`
	}
	if err := g.call(ctx, g.base+g.name+":encrypt", mustJSON(body), &resp); err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(resp.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("kmswrap: decode GCP ciphertext: %w", err)
	}
	if len(ct) == 0 {
		return nil, fmt.Errorf("kmswrap: GCP KMS returned an empty ciphertext")
	}
	return ct, nil
}

// UnwrapKey decrypts the DEK (Cloud KMS picks the right key version from the
// ciphertext), requiring the same AAD it was wrapped under.
func (g *GCP) UnwrapKey(ctx context.Context, ciphertext []byte, aad map[string]string) ([]byte, error) {
	body := map[string]string{
		"ciphertext": base64.StdEncoding.EncodeToString(ciphertext),
	}
	if c := canonicalAAD(aad); len(c) > 0 {
		body["additionalAuthenticatedData"] = base64.StdEncoding.EncodeToString(c)
	}
	var resp struct {
		Plaintext string `json:"plaintext"`
	}
	if err := g.call(ctx, g.base+g.name+":decrypt", mustJSON(body), &resp); err != nil {
		return nil, err
	}
	pt, err := base64.StdEncoding.DecodeString(resp.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("kmswrap: decode GCP plaintext: %w", err)
	}
	if len(pt) == 0 {
		return nil, fmt.Errorf("kmswrap: GCP KMS returned an empty plaintext")
	}
	return pt, nil
}

func (g *GCP) call(ctx context.Context, url string, body []byte, out any) error {
	tok, err := g.token(ctx)
	if err != nil {
		return fmt.Errorf("kmswrap: gcp token: %w", err)
	}
	req, _, err := newJSONPost(url, "application/json", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return doJSON(ctx, g.doer, req, out)
}
