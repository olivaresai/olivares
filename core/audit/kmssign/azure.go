// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
)

// Azure is an off-box checkpoint signer backed by Azure Key Vault / Managed HSM. It
// signs via the Sign REST op and returns an X9.62 ASN.1-DER ECDSA signature.
//
// Verified against learn.microsoft.com (Key Vault keys Sign / Get Key REST): POST
// {vaultBaseUrl}/keys/{name}/{version}/sign?api-version=… {alg, value(base64url
// digest)} → {kid, value(base64url signature)}; GET …/{name}/{version} → JsonWebKey.
//
// Two conversions make Azure uniform with AWS/GCP for the off-box verifier: (1) the
// JWA ES256/ES384 signature `value` is the raw r‖s pair (JWS form), which this
// backend re-encodes to ASN.1-DER so ecdsa.VerifyASN1 checks it; (2) the public key
// arrives as a JWK (kty EC, crv P-256/384, x, y), which this backend converts to
// DER SubjectPublicKeyInfo for the auditor to pin. Pure-Go; bearer auth (no cgo).
type Azure struct {
	vaultURL   string // https://{vault}.vault.azure.net or managed-hsm base
	keyName    string
	keyVersion string
	apiVersion string
	jwaAlg     string // ES256 | ES384
	alg        audit.SigAlg
	curve      elliptic.Curve
	coordLen   int
	token      TokenSource
	doer       Doer
	now        func() time.Time

	pubDER []byte
}

// AzureConfig configures a Key Vault / Managed HSM checkpoint signer.
type AzureConfig struct {
	// VaultURL is the vault base URL (https://NAME.vault.azure.net) or Managed HSM
	// base URL (https://NAME.managedhsm.azure.net).
	VaultURL   string
	KeyName    string
	KeyVersion string
	// Token yields the AAD bearer token (audience the Key Vault data plane).
	Token TokenSource
	// Algorithm selects ES256 (P-256) [default] or ES384 (P-384).
	Algorithm audit.SigAlg
	// APIVersion overrides the data-plane api-version (default 7.4).
	APIVersion string
	Doer       Doer
	Now        func() time.Time
}

// NewAzure builds a Key Vault / Managed HSM off-box checkpoint signer.
func NewAzure(cfg AzureConfig) (*Azure, error) {
	if strings.TrimSpace(cfg.VaultURL) == "" || strings.TrimSpace(cfg.KeyName) == "" {
		return nil, fmt.Errorf("kmssign: azure vault url and key name are required")
	}
	if cfg.Token == nil {
		return nil, fmt.Errorf("kmssign: azure token source is required")
	}
	a := &Azure{
		vaultURL: strings.TrimSuffix(cfg.VaultURL, "/"), keyName: cfg.KeyName, keyVersion: cfg.KeyVersion,
		apiVersion: cfg.APIVersion, token: cfg.Token, doer: cfg.Doer, now: cfg.Now,
	}
	if a.apiVersion == "" {
		a.apiVersion = "7.4"
	}
	if a.doer == nil {
		a.doer = http.DefaultClient
	}
	switch cfg.Algorithm {
	case "", audit.AlgECDSAP256SHA256:
		a.alg, a.jwaAlg, a.curve, a.coordLen = audit.AlgECDSAP256SHA256, "ES256", elliptic.P256(), 32
	case audit.AlgECDSAP384SHA384:
		a.alg, a.jwaAlg, a.curve, a.coordLen = audit.AlgECDSAP384SHA384, "ES384", elliptic.P384(), 48
	default:
		return nil, fmt.Errorf("kmssign: unsupported Azure algorithm %q (use ECDSA P-256/P-384)", cfg.Algorithm)
	}
	return a, nil
}

// Algorithm reports the off-box signature scheme.
func (a *Azure) Algorithm() audit.SigAlg { return a.alg }

// KeyID reports the key identifier (vault/name[/version]).
func (a *Azure) KeyID() string {
	if a.keyVersion != "" {
		return a.vaultURL + "/keys/" + a.keyName + "/" + a.keyVersion
	}
	return a.vaultURL + "/keys/" + a.keyName
}

// SignCheckpoint signs the digest and returns the ASN.1-DER ECDSA signature
// (converted from Azure's raw r‖s JWS form).
func (a *Azure) SignCheckpoint(ctx context.Context, preimage []byte) ([]byte, error) {
	digest := hashFor(a.alg, preimage)
	body := mustJSON(map[string]string{
		"alg":   a.jwaAlg,
		"value": base64.RawURLEncoding.EncodeToString(digest),
	})
	var resp struct {
		Value string `json:"value"`
	}
	if err := a.call(ctx, http.MethodPost, a.keyOpURL("sign"), body, &resp); err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(resp.Value)
	if err != nil {
		// Tolerate standard padding just in case.
		raw, err = base64.StdEncoding.DecodeString(resp.Value)
		if err != nil {
			return nil, fmt.Errorf("kmssign: decode Azure signature: %w", err)
		}
	}
	if len(raw) != 2*a.coordLen {
		return nil, fmt.Errorf("kmssign: Azure %s signature is %d bytes, want %d (r||s)", a.jwaAlg, len(raw), 2*a.coordLen)
	}
	r := new(big.Int).SetBytes(raw[:a.coordLen])
	s := new(big.Int).SetBytes(raw[a.coordLen:])
	der, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	if err != nil {
		return nil, fmt.Errorf("kmssign: encode Azure signature: %w", err)
	}
	return der, nil
}

// PublicKey fetches the JWK and returns it as DER SubjectPublicKeyInfo.
func (a *Azure) PublicKey(ctx context.Context) ([]byte, error) {
	if a.pubDER != nil {
		return a.pubDER, nil
	}
	var resp struct {
		Key struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"key"`
	}
	if err := a.call(ctx, http.MethodGet, a.keyURL(), nil, &resp); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resp.Key.Kty, "EC") {
		return nil, fmt.Errorf("kmssign: Azure key type %q is not EC (RSA keys are a documented seam)", resp.Key.Kty)
	}
	x, err := base64.RawURLEncoding.DecodeString(resp.Key.X)
	if err != nil {
		return nil, fmt.Errorf("kmssign: decode Azure key x: %w", err)
	}
	y, err := base64.RawURLEncoding.DecodeString(resp.Key.Y)
	if err != nil {
		return nil, fmt.Errorf("kmssign: decode Azure key y: %w", err)
	}
	pub := &ecdsa.PublicKey{Curve: a.curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("kmssign: marshal Azure public key: %w", err)
	}
	a.pubDER = der
	return der, nil
}

func (a *Azure) keyURL() string {
	u := a.vaultURL + "/keys/" + a.keyName
	if a.keyVersion != "" {
		u += "/" + a.keyVersion
	}
	return u + "?api-version=" + a.apiVersion
}

func (a *Azure) keyOpURL(op string) string {
	u := a.vaultURL + "/keys/" + a.keyName
	if a.keyVersion != "" {
		u += "/" + a.keyVersion
	}
	return u + "/" + op + "?api-version=" + a.apiVersion
}

func (a *Azure) call(ctx context.Context, method, url string, body []byte, out any) error {
	tok, err := a.token(ctx)
	if err != nil {
		return fmt.Errorf("kmssign: azure token: %w", err)
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
	return doJSON(ctx, a.doer, req, out)
}
