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

// Azure is a KEK wrapper backed by Azure Key Vault / Managed HSM
// wrapKey/unwrapKey on an RSA key, alg RSA-OAEP-256. A 32-byte DEK always fits
// any RSA KEK size. Pure-Go; bearer auth (no cgo).
//
// Verified against learn.microsoft.com (Key Vault keys wrapKey / unwrapKey
// REST): POST {vault}/keys/{name}/{version}/wrapkey?api-version=… {alg,
// value(base64url)} → {kid, value(base64url)}; unwrapkey is the reverse.
//
// Two Azure-specific honesty notes:
//   - RSA-OAEP wrap has NO authenticated-context channel, so the aad map is
//     IGNORED here — the envelope's purpose binding rests on the local AES-GCM
//     AAD alone (core/secure/envelope.go). AWS/GCP additionally bind it at the
//     KMS; Azure cannot.
//   - unwrapKey addressed WITHOUT a key version uses the key's LATEST version,
//     which fails for envelopes wrapped before a KEK rotation. So when no
//     version is configured, this backend resolves and PINS the current version
//     once (one GET, cached) so wrap records — and unwrap targets — an exact
//     version. After rotating the KEK in the vault, point the config at the
//     envelope's recorded version (or rewrap: `keys rewrap`).
type Azure struct {
	vaultURL   string // https://{vault}.vault.azure.net or managed-hsm base
	keyName    string
	keyVersion string // resolved+cached when not configured
	apiVersion string
	token      TokenSource
	doer       Doer
}

// AzureConfig configures a Key Vault / Managed HSM KEK wrapper.
type AzureConfig struct {
	// VaultURL is the vault base URL (https://NAME.vault.azure.net) or Managed
	// HSM base URL (https://NAME.managedhsm.azure.net).
	VaultURL string
	KeyName  string
	// KeyVersion pins the KEK version. Empty = resolve the current version once
	// and pin it (see the type comment for why unwrap must never float).
	KeyVersion string
	// Token yields the AAD bearer token (audience the Key Vault data plane).
	Token TokenSource
	// APIVersion overrides the data-plane api-version (default 7.4).
	APIVersion string
	// Doer overrides the HTTP client (tests); nil uses http.DefaultClient.
	Doer Doer
}

// NewAzure builds a Key Vault / Managed HSM KEK wrapper.
func NewAzure(cfg AzureConfig) (*Azure, error) {
	if strings.TrimSpace(cfg.VaultURL) == "" || strings.TrimSpace(cfg.KeyName) == "" {
		return nil, fmt.Errorf("kmswrap: azure vault url and key name are required")
	}
	if cfg.Token == nil {
		return nil, fmt.Errorf("kmswrap: azure token source is required")
	}
	a := &Azure{
		vaultURL: strings.TrimSuffix(cfg.VaultURL, "/"), keyName: cfg.KeyName,
		keyVersion: cfg.KeyVersion, apiVersion: cfg.APIVersion, token: cfg.Token, doer: cfg.Doer,
	}
	if a.apiVersion == "" {
		a.apiVersion = "7.4"
	}
	if a.doer == nil {
		a.doer = http.DefaultClient
	}
	return a, nil
}

// Provider reports the backend name recorded in envelopes.
func (a *Azure) Provider() string { return ProviderAzure }

// KeyID reports the key identifier (vault/keys/name/version once resolved).
func (a *Azure) KeyID() string {
	if a.keyVersion != "" {
		return a.vaultURL + "/keys/" + a.keyName + "/" + a.keyVersion
	}
	return a.vaultURL + "/keys/" + a.keyName
}

// ParseAzureKeyID splits a recorded Azure key identifier
// ({vault}/keys/{name}/{version}) so a custody loader can re-pin the exact KEK
// version an envelope was wrapped under. version may be empty.
func ParseAzureKeyID(kid string) (vaultURL, name, version string, err error) {
	i := strings.Index(kid, "/keys/")
	if i <= 0 {
		return "", "", "", fmt.Errorf("kmswrap: %q is not an Azure key identifier", kid)
	}
	vaultURL = kid[:i]
	rest := strings.Trim(kid[i+len("/keys/"):], "/")
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return vaultURL, parts[0], "", nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return vaultURL, parts[0], parts[1], nil
	default:
		return "", "", "", fmt.Errorf("kmswrap: %q is not an Azure key identifier", kid)
	}
}

// WrapKey wraps the DEK under the pinned KEK version with RSA-OAEP-256. The aad
// map is ignored (no AAD channel in RSA wrap — see the type comment).
func (a *Azure) WrapKey(ctx context.Context, plaintext []byte, _ map[string]string) ([]byte, error) {
	if err := a.resolveVersion(ctx); err != nil {
		return nil, err
	}
	return a.keyOp(ctx, "wrapkey", plaintext)
}

// UnwrapKey unwraps the DEK under the pinned KEK version.
func (a *Azure) UnwrapKey(ctx context.Context, ciphertext []byte, _ map[string]string) ([]byte, error) {
	if err := a.resolveVersion(ctx); err != nil {
		return nil, err
	}
	return a.keyOp(ctx, "unwrapkey", ciphertext)
}

// resolveVersion pins the KEK to its current version when none was configured:
// one GET on the version-less key URL, whose response kid carries the version.
func (a *Azure) resolveVersion(ctx context.Context) error {
	if a.keyVersion != "" {
		return nil
	}
	var resp struct {
		Key struct {
			Kid string `json:"kid"`
		} `json:"key"`
	}
	url := a.vaultURL + "/keys/" + a.keyName + "?api-version=" + a.apiVersion
	if err := a.call(ctx, http.MethodGet, url, nil, &resp); err != nil {
		return fmt.Errorf("kmswrap: resolve azure key version: %w", err)
	}
	_, _, version, err := ParseAzureKeyID(resp.Key.Kid)
	if err != nil || version == "" {
		return fmt.Errorf("kmswrap: azure key %s/%s did not report a version (kid %q)", a.vaultURL, a.keyName, resp.Key.Kid)
	}
	a.keyVersion = version
	return nil
}

// keyOp performs wrapkey/unwrapkey with RSA-OAEP-256.
func (a *Azure) keyOp(ctx context.Context, op string, value []byte) ([]byte, error) {
	body := mustJSON(map[string]string{
		"alg":   "RSA-OAEP-256",
		"value": base64.RawURLEncoding.EncodeToString(value),
	})
	url := a.vaultURL + "/keys/" + a.keyName + "/" + a.keyVersion + "/" + op + "?api-version=" + a.apiVersion
	var resp struct {
		Value string `json:"value"`
	}
	if err := a.call(ctx, http.MethodPost, url, body, &resp); err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(resp.Value)
	if err != nil {
		// Tolerate standard padding just in case.
		raw, err = base64.StdEncoding.DecodeString(resp.Value)
		if err != nil {
			return nil, fmt.Errorf("kmswrap: decode Azure %s value: %w", op, err)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("kmswrap: Azure %s returned an empty value", op)
	}
	return raw, nil
}

func (a *Azure) call(ctx context.Context, method, url string, body []byte, out any) error {
	tok, err := a.token(ctx)
	if err != nil {
		return fmt.Errorf("kmswrap: azure token: %w", err)
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
