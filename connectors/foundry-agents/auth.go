// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

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

const (
	managementScope = "https://management.azure.com/.default"
	dataPlaneScope  = "https://ai.azure.com/.default"
	loginEndpoint   = "https://login.microsoftonline.com"
)

func (s *Source) token(ctx context.Context, scope string) (string, error) {
	tokenURL := strings.TrimSpace(s.oauthTokenURL)
	if tokenURL == "" {
		tokenURL = loginEndpoint + "/" + url.PathEscape(s.tenantID) + "/oauth2/v2.0/token"
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"scope":         {scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("foundry-agents: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("foundry-agents: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		msg := strings.TrimSpace(string(excerpt))
		if s.clientSecret != "" {
			msg = strings.ReplaceAll(msg, s.clientSecret, "[REDACTED]")
		}
		return "", fmt.Errorf("foundry-agents: token endpoint status %d: %s", resp.StatusCode, msg)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("foundry-agents: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("foundry-agents: token endpoint returned an empty access token")
	}
	return tr.AccessToken, nil
}

func (s *Source) armClient(ctx context.Context) (*httpx.Client, error) {
	tok, err := s.token(ctx, managementScope)
	if err != nil {
		return nil, err
	}
	return httpx.New(s.managementEndpoint, s.transport(), httpx.Bearer(tok), nil), nil
}

func (s *Source) dataPlaneToken(ctx context.Context) (string, error) {
	return s.token(ctx, dataPlaneScope)
}

func (s *Source) dataPlaneClient(base, tok string) *httpx.Client {
	return httpx.New(base, s.transport(), httpx.Bearer(tok), nil)
}
