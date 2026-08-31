// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package secretref

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// gcpReader resolves `gcp-secretmanager:<name>` (or `<project>/<name>`, with an
// optional `#<version>`) against Google Secret Manager. The version defaults to
// "latest". It GETs the secret version's :access endpoint with a bearer token and
// base64-decodes payload.data.
//
//	gcp-secretmanager:gdrive-token              (default project, latest)
//	gcp-secretmanager:my-proj/gdrive-token#3    (explicit project and version)
//
// Engine config:
//
//	OLIVARES_SECRETREF_GCP_PROJECT        — default project (optional if the locator carries one)
//	OLIVARES_SECRETREF_GCP_TOKEN[_FILE]   — an OAuth2 access token with secretmanager.versions.access
//	OLIVARES_SECRETREF_GCP_ENDPOINT       — API base override (default https://secretmanager.googleapis.com)
type gcpReader struct {
	project  string
	endpoint string
	token    envToken
	doer     httpx.Doer
}

const gcpSecretManagerEndpoint = "https://secretmanager.googleapis.com"

func newGCPReader(getenv func(string) string, doer httpx.Doer) (Reader, bool) {
	tok, ok := loadEnvToken(getenv, "OLIVARES_SECRETREF_GCP_TOKEN")
	if !ok {
		return nil, false
	}
	endpoint := firstEnv(getenv, "OLIVARES_SECRETREF_GCP_ENDPOINT")
	if endpoint == "" {
		endpoint = gcpSecretManagerEndpoint
	}
	return &gcpReader{
		project:  firstEnv(getenv, "OLIVARES_SECRETREF_GCP_PROJECT"),
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    tok,
		doer:     doer,
	}, true
}

func (r *gcpReader) Resolve(ctx context.Context, locator string) ([]byte, error) {
	body, version, _ := strings.Cut(locator, "#")
	version = strings.TrimSpace(version)
	if version == "" {
		version = "latest"
	}
	project, name := r.project, strings.TrimSpace(body)
	if p, n, ok := strings.Cut(strings.TrimSpace(body), "/"); ok {
		project, name = strings.TrimSpace(p), strings.TrimSpace(n)
	}
	if project == "" {
		return nil, fmt.Errorf("gcp-secretmanager: no project (set OLIVARES_SECRETREF_GCP_PROJECT or use <project>/<name>)")
	}
	if name == "" {
		return nil, fmt.Errorf("gcp-secretmanager: empty secret name")
	}
	tokenVal, err := r.token.value()
	if err != nil {
		return nil, fmt.Errorf("gcp-secretmanager: read token: %w", err)
	}
	client := httpx.New(r.endpoint, r.doer, httpx.Bearer(tokenVal), nil)

	path := fmt.Sprintf("/v1/projects/%s/secrets/%s/versions/%s:access", project, name, version)
	var resp struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := client.GetJSON(ctx, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("gcp-secretmanager: %w", err)
	}
	if resp.Payload.Data == "" {
		return nil, fmt.Errorf("gcp-secretmanager: empty payload for %q", name)
	}
	dec, err := base64.StdEncoding.DecodeString(resp.Payload.Data)
	if err != nil {
		return nil, fmt.Errorf("gcp-secretmanager: decode payload: %w", err)
	}
	return dec, nil
}
