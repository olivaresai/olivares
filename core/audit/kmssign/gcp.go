// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
)

// TokenSource yields a fresh bearer access token for GCP/Azure. It is called per
// request so a short-lived token is refreshed by the operator's chosen mechanism
// (workload-identity, metadata server, gcloud/az token). Returning the token,
// never storing it here, keeps credential lifetime out of this package.
type TokenSource func(ctx context.Context) (string, error)

// StaticToken is a TokenSource for a fixed token (tests / a short-lived run). For a
// long-lived engine prefer a refreshing source.
func StaticToken(tok string) TokenSource {
	return func(context.Context) (string, error) { return tok, nil }
}

// GCP is an off-box checkpoint signer backed by Google Cloud KMS. It signs via
// AsymmetricSign in DIGEST mode and returns the X9.62 ASN.1-DER ECDSA signature.
// Verified against cloud.google.com (Cloud KMS REST AsymmetricSign / GetPublicKey):
// POST .../cryptoKeyVersions/V:asymmetricSign {digest:{sha256:base64}} →
// {signature:base64}; GET .../publicKey → {pem}. Pure-Go; bearer auth via a
// TokenSource (no cgo).
type GCP struct {
	name     string // projects/*/locations/*/keyRings/*/cryptoKeys/*/cryptoKeyVersions/*
	alg      audit.SigAlg
	token    TokenSource
	doer     Doer
	now      func() time.Time
	base     string // override (tests); default https://cloudkms.googleapis.com/v1/
	digestFn string // "sha256" | "sha384"

	pubDER []byte
}

// GCPConfig configures a Cloud KMS checkpoint signer.
type GCPConfig struct {
	// KeyVersionName is the full cryptoKeyVersion resource name.
	KeyVersionName string
	// Token yields the OAuth2 bearer token (scope cloud-platform / cloudkms).
	Token TokenSource
	// Algorithm selects the hash; empty defaults to ECDSA P-256 / SHA-256.
	Algorithm audit.SigAlg
	Doer      Doer
	BaseURL   string // override (tests)
	Now       func() time.Time
}

// NewGCP builds a Cloud KMS off-box checkpoint signer.
func NewGCP(cfg GCPConfig) (*GCP, error) {
	if strings.TrimSpace(cfg.KeyVersionName) == "" {
		return nil, fmt.Errorf("kmssign: gcp key version name is required")
	}
	if cfg.Token == nil {
		return nil, fmt.Errorf("kmssign: gcp token source is required")
	}
	alg := cfg.Algorithm
	if alg == "" {
		alg = audit.AlgECDSAP256SHA256
	}
	var digestFn string
	switch alg {
	case audit.AlgECDSAP256SHA256:
		digestFn = "sha256"
	case audit.AlgECDSAP384SHA384:
		digestFn = "sha384"
	default:
		return nil, fmt.Errorf("kmssign: unsupported GCP algorithm %q (use ECDSA P-256/P-384)", alg)
	}
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	base := cfg.BaseURL
	if base == "" {
		base = "https://cloudkms.googleapis.com/v1/"
	}
	return &GCP{name: cfg.KeyVersionName, alg: alg, token: cfg.Token, doer: doer, now: cfg.Now, base: strings.TrimSuffix(base, "/") + "/", digestFn: digestFn}, nil
}

// Algorithm reports the off-box signature scheme.
func (g *GCP) Algorithm() audit.SigAlg { return g.alg }

// KeyID reports the cryptoKeyVersion resource name.
func (g *GCP) KeyID() string { return g.name }

// SignCheckpoint signs SHA-256(preimage) via AsymmetricSign and returns the
// ASN.1-DER ECDSA signature.
func (g *GCP) SignCheckpoint(ctx context.Context, preimage []byte) ([]byte, error) {
	digest := hashFor(g.alg, preimage)
	body := mustJSON(map[string]any{
		"digest": map[string]string{g.digestFn: base64.StdEncoding.EncodeToString(digest)},
	})
	var resp struct {
		Signature string `json:"signature"`
	}
	if err := g.call(ctx, http.MethodPost, g.base+g.name+":asymmetricSign", body, &resp); err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(resp.Signature)
	if err != nil {
		return nil, fmt.Errorf("kmssign: decode GCP signature: %w", err)
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("kmssign: GCP KMS returned an empty signature")
	}
	return sig, nil
}

// PublicKey fetches (and caches) the public key, returning DER SubjectPublicKeyInfo
// parsed from the PEM the API returns.
func (g *GCP) PublicKey(ctx context.Context) ([]byte, error) {
	if g.pubDER != nil {
		return g.pubDER, nil
	}
	var resp struct {
		PEM string `json:"pem"`
	}
	if err := g.call(ctx, http.MethodGet, g.base+g.name+"/publicKey", nil, &resp); err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(resp.PEM))
	if block == nil {
		return nil, fmt.Errorf("kmssign: GCP public key is not valid PEM")
	}
	// Validate it parses as a public key; return the DER bytes the verifier pins.
	if _, err := x509.ParsePKIXPublicKey(block.Bytes); err != nil {
		return nil, fmt.Errorf("kmssign: GCP public key not parseable: %w", err)
	}
	g.pubDER = block.Bytes
	return g.pubDER, nil
}

func (g *GCP) call(ctx context.Context, method, url string, body []byte, out any) error {
	tok, err := g.token(ctx)
	if err != nil {
		return fmt.Errorf("kmssign: gcp token: %w", err)
	}
	var req *http.Request
	if method == http.MethodPost {
		req, _, err = newJSONPost(url, "application/json", body)
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return doJSON(ctx, g.doer, req, out)
}
