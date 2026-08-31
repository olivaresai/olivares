// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agent365

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

// token performs the OAuth2 client-credentials grant (the connector's only
// non-GET call) against the token endpoint using the same injected transport.
// The access token is held only in memory; a non-2xx is an error carrying the
// status and a bounded 2KiB body excerpt, never the client secret request form.
func (s *Source) token(ctx context.Context) (string, error) {
	tokenURL := s.oauthTokenURL
	if tokenURL == "" {
		tokenURL = "https://login.microsoftonline.com/" + url.PathEscape(s.tenantID) + "/oauth2/v2.0/token"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("agent365: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("agent365: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		msg := strings.TrimSpace(string(excerpt))
		if s.clientSecret != "" {
			msg = strings.ReplaceAll(msg, s.clientSecret, "[REDACTED]")
		}
		return "", fmt.Errorf("agent365: token endpoint status %d: %s", resp.StatusCode, msg)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("agent365: decode token response: %w", err)
	}
	return tr.AccessToken, nil
}

// graphClient resolves effective auth and builds the read-only Graph client. An
// operator-supplied delegated token takes precedence over client-credentials;
// otherwise a fresh client-credentials token is minted for this call.
func (s *Source) graphClient(ctx context.Context) (*httpx.Client, error) {
	if s.accessToken != "" {
		return s.graphClientFromToken(s.accessToken)
	}
	tok, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	return s.graphClientFromToken(tok)
}

func (s *Source) graphClientFromToken(tok string) (*httpx.Client, error) {
	if tok == "" {
		return nil, fmt.Errorf("agent365: token endpoint returned an empty access token")
	}
	return httpx.New(s.baseURL, s.transport(), httpx.Bearer(tok), nil), nil
}
