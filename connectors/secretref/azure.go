// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// azureReader resolves `azure-keyvault:<vault>/<name>` (optional `/<version>`)
// against Azure Key Vault. The vault may be a short name (expanded to
// https://<vault>.vault.azure.net) or a full https URL; when the locator carries
// only a name, the default vault is used.
//
//	azure-keyvault:mykv/gdrive-token            (vault "mykv", current version)
//	azure-keyvault:gdrive-token                 (default vault)
//	azure-keyvault:mykv/gdrive-token/abc123     (explicit version)
//
// Engine config:
//
//	OLIVARES_SECRETREF_AZURE_TOKEN[_FILE] — an AAD bearer token for https://vault.azure.net/.default
//	OLIVARES_SECRETREF_AZURE_VAULT_URL    — default vault (name or URL), optional
//	OLIVARES_SECRETREF_AZURE_API_VERSION  — default 7.4
type azureReader struct {
	defaultVault string
	apiVersion   string
	token        envToken
	doer         httpx.Doer
}

const azureDefaultAPIVersion = "7.4"

func newAzureReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	tok, ok := loadEnvToken(getenv, "OLIVARES_SECRETREF_AZURE_TOKEN")
	if !ok {
		return nil, false
	}
	ver := firstEnv(getenv, "OLIVARES_SECRETREF_AZURE_API_VERSION")
	if ver == "" {
		ver = azureDefaultAPIVersion
	}
	return &azureReader{
		defaultVault: firstEnv(getenv, "OLIVARES_SECRETREF_AZURE_VAULT_URL"),
		apiVersion:   ver,
		token:        tok,
		doer:         doer,
	}, true
}

func (r *azureReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	vault, name, version, err := r.parseLocator(locator)
	if err != nil {
		return nil, err
	}
	tokenVal, terr := r.token.value()
	if terr != nil {
		return nil, fmt.Errorf("azure-keyvault: read token: %w", terr)
	}
	client := httpx.New(vault, r.doer, httpx.Bearer(tokenVal), nil)

	path := "/secrets/" + url.PathEscape(name)
	if version != "" {
		path += "/" + url.PathEscape(version)
	}
	var resp struct {
		Value string `json:"value"`
	}
	if err := client.GetJSON(ctx, path, url.Values{"api-version": {r.apiVersion}}, &resp); err != nil {
		return nil, fmt.Errorf("azure-keyvault: %w", err)
	}
	if resp.Value == "" {
		return nil, fmt.Errorf("azure-keyvault: empty value for %q", name)
	}
	return []byte(resp.Value), nil
}

// parseLocator splits "<vault>/<name>[/<version>]" or "<name>" (default vault),
// returning the vault base URL, the secret name and an optional version.
func (r *azureReader) parseLocator(locator string) (vaultURL, name, version string, err error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(locator), "/"), "/")
	switch len(parts) {
	case 1:
		if r.defaultVault == "" {
			return "", "", "", fmt.Errorf("azure-keyvault: no vault (set OLIVARES_SECRETREF_AZURE_VAULT_URL or use <vault>/<name>)")
		}
		return azureVaultURL(r.defaultVault), parts[0], "", nil
	case 2:
		return azureVaultURL(parts[0]), parts[1], "", nil
	case 3:
		return azureVaultURL(parts[0]), parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("azure-keyvault: locator must be <vault>/<name>[/<version>]")
	}
}

// azureVaultURL expands a short vault name to its DNS URL; a value already
// carrying a scheme is used verbatim.
func azureVaultURL(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://") {
		return v
	}
	return "https://" + v + ".vault.azure.net"
}
