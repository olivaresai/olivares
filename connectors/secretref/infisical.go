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

// infisicalReader resolves `infisical:<workspaceId>/<environment>/<secretName>`
// (optionally `#<secretPath>`, default "/") against the Infisical REST API. When
// a default workspace and environment are configured, the locator may be just
// `<secretName>`.
//
//	infisical:64a.../prod/GDRIVE_TOKEN          (explicit workspace + env)
//	infisical:GDRIVE_TOKEN                       (default workspace + env)
//	infisical:64a.../prod/GDRIVE_TOKEN#/data     (explicit secret path)
//
// Engine config:
//
//	OLIVARES_SECRETREF_INFISICAL_TOKEN[_FILE] — a service-token / access token (bearer)
//	OLIVARES_SECRETREF_INFISICAL_URL          — API base (default https://app.infisical.com)
//	OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID — default workspace, optional
//	OLIVARES_SECRETREF_INFISICAL_ENV          — default environment, optional
type infisicalReader struct {
	base       string
	defaultWS  string
	defaultEnv string
	token      envToken
	doer       httpx.Doer
}

const infisicalDefaultBase = "https://app.infisical.com"

func newInfisicalReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	tok, ok := loadEnvToken(getenv, "OLIVARES_SECRETREF_INFISICAL_TOKEN")
	if !ok {
		return nil, false
	}
	base := firstEnv(getenv, "OLIVARES_SECRETREF_INFISICAL_URL")
	if base == "" {
		base = infisicalDefaultBase
	}
	return &infisicalReader{
		base:       strings.TrimRight(base, "/"),
		defaultWS:  firstEnv(getenv, "OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID"),
		defaultEnv: firstEnv(getenv, "OLIVARES_SECRETREF_INFISICAL_ENV"),
		token:      tok,
		doer:       doer,
	}, true
}

func (r *infisicalReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	body, secretPath, hasPath := strings.Cut(locator, "#")
	if !hasPath || strings.TrimSpace(secretPath) == "" {
		secretPath = "/"
	}
	ws, env, name, err := r.parseLocator(strings.TrimSpace(body))
	if err != nil {
		return nil, err
	}
	tokenVal, terr := r.token.value()
	if terr != nil {
		return nil, fmt.Errorf("infisical: read token: %w", terr)
	}
	client := httpx.New(r.base, r.doer, httpx.Bearer(tokenVal), nil)

	q := url.Values{"workspaceId": {ws}, "environment": {env}, "secretPath": {secretPath}}
	var resp struct {
		Secret struct {
			SecretValue string `json:"secretValue"`
		} `json:"secret"`
	}
	if err := client.GetJSON(ctx, "/api/v3/secrets/raw/"+url.PathEscape(name), q, &resp); err != nil {
		return nil, fmt.Errorf("infisical: %w", err)
	}
	if resp.Secret.SecretValue == "" {
		return nil, fmt.Errorf("infisical: empty value for %q", name)
	}
	return []byte(resp.Secret.SecretValue), nil
}

func (r *infisicalReader) parseLocator(body string) (ws, env, name string, err error) {
	parts := strings.Split(strings.Trim(body, "/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		if r.defaultWS == "" || r.defaultEnv == "" {
			return "", "", "", fmt.Errorf("infisical: no default workspace/environment (set OLIVARES_SECRETREF_INFISICAL_WORKSPACE_ID and _ENV, or use <workspaceId>/<environment>/<name>)")
		}
		return r.defaultWS, r.defaultEnv, parts[0], nil
	}
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2], nil
	}
	return "", "", "", fmt.Errorf("infisical: locator must be <workspaceId>/<environment>/<name> or <name> with defaults set")
}
