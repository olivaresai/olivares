// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// vaultReader resolves `vault:<api-path>#<key>` against a HashiCorp Vault KV
// engine. The locator is the API path AFTER /v1/ and a JSON field selector, e.g.
//
//	vault:secret/data/gdrive#token   (KV v2 — note the explicit /data/ segment)
//	vault:kv/myapp#password           (KV v1)
//
// The reader GETs <addr>/v1/<path> with the X-Vault-Token credential and extracts
// the field: KV v2 nests the secret under data.data, KV v1 under data, so it
// looks in data.data first and falls back to data. With no "#key" it returns the
// sole field when the secret has exactly one, else it errors and names the
// available fields (never their values).
//
// Engine config (read once):
//
//	OLIVARES_SECRETREF_VAULT_ADDR | VAULT_ADDR        — the Vault address
//	OLIVARES_SECRETREF_VAULT_TOKEN[_FILE] | VAULT_TOKEN — the token (file re-read per call)
//	OLIVARES_SECRETREF_VAULT_NAMESPACE | VAULT_NAMESPACE — optional Enterprise namespace
type vaultReader struct {
	addr      string
	namespace string
	token     envToken
	doer      httpx.Doer
}

func newVaultReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	addr := firstEnv(getenv, "OLIVARES_SECRETREF_VAULT_ADDR", "VAULT_ADDR")
	tok, hasTok := loadEnvToken(getenv, "OLIVARES_SECRETREF_VAULT_TOKEN")
	if !hasTok {
		if v := firstEnv(getenv, "VAULT_TOKEN"); v != "" {
			tok, hasTok = envToken{static: v}, true
		}
	}
	if addr == "" || !hasTok {
		return nil, false
	}
	return &vaultReader{
		addr:      strings.TrimRight(addr, "/"),
		namespace: firstEnv(getenv, "OLIVARES_SECRETREF_VAULT_NAMESPACE", "VAULT_NAMESPACE"),
		token:     tok,
		doer:      doer,
	}, true
}

func (r *vaultReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	path, key, hasKey := strings.Cut(locator, "#")
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path == "" {
		return nil, fmt.Errorf("vault: empty secret path")
	}
	tokenVal, err := r.token.value()
	if err != nil {
		return nil, fmt.Errorf("vault: read token: %w", err)
	}
	var headers map[string]string
	if r.namespace != "" {
		headers = map[string]string{"X-Vault-Namespace": r.namespace}
	}
	client := httpx.New(r.addr, r.doer, httpx.Header("X-Vault-Token", tokenVal, tokenVal), headers)

	var resp struct {
		Data json.RawMessage `json:"data"`
	}
	if err := client.GetJSON(ctx, "/v1/"+path, nil, &resp); err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	fields, err := vaultFields(resp.Data)
	if err != nil {
		return nil, err
	}
	return selectField(fields, key, hasKey, "vault")
}

// vaultFields returns the secret's field map: KV v2 nests it under data.data, KV
// v1 stores it directly under data. The two are distinguished by the v2 envelope's
// REQUIRED sibling: a KV v2 read response always carries data.metadata alongside
// data.data, whereas KV v1 stores the operator's own fields directly. Requiring
// the metadata sibling (not just the shape of a "data" field) means a KV v1 secret
// that happens to have a field literally named "data" — even one holding an object
// — is correctly read as v1, never silently mis-selected as a v2 envelope.
func vaultFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("vault: decode secret data: %w", err)
	}
	inner, hasData := top["data"]
	if _, hasMeta := top["metadata"]; hasData && hasMeta {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(inner, &nested); err == nil && nested != nil {
			return nested, nil // KV v2 (data + metadata envelope)
		}
	}
	return top, nil // KV v1 (operator fields stored directly)
}

// selectField extracts a named field (or the sole field when none is named) from a
// secret's field map and coerces it to a string value. It never includes a value
// in an error.
func selectField(fields map[string]json.RawMessage, key string, hasKey bool, backend string) ([]byte, error) {
	if !hasKey || strings.TrimSpace(key) == "" {
		if len(fields) == 1 {
			for _, v := range fields {
				return jsonScalar(v, backend)
			}
		}
		names := make([]string, 0, len(fields))
		for k := range fields {
			names = append(names, k)
		}
		return nil, fmt.Errorf("%s: secret has %d fields (%s); name one with #<key>", backend, len(fields), strings.Join(names, ", "))
	}
	raw, ok := fields[strings.TrimSpace(key)]
	if !ok {
		return nil, fmt.Errorf("%s: secret has no field %q", backend, key)
	}
	return jsonScalar(raw, backend)
}

// jsonScalar coerces a JSON value to its string/secret bytes: a JSON string is
// unquoted; a number/bool is its literal text. An object/array is refused (a
// secret field must be a scalar).
func jsonScalar(raw json.RawMessage, backend string) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, fmt.Errorf("%s: secret field is null/empty", backend)
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return nil, fmt.Errorf("%s: secret field is not a scalar value", backend)
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%s: decode secret field: %w", backend, err)
		}
		return []byte(s), nil
	}
	return []byte(trimmed), nil
}
