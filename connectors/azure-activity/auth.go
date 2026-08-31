// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This file mints Azure AD (Microsoft Entra) access tokens with the OAuth2
// client-credentials flow, using ONLY the standard library: a POST of
// {grant_type=client_credentials, client_id, client_secret, scope} to the tenant
// token endpoint returns a short-lived bearer token for the Azure Resource
// Manager. Tokens are cached until shortly before expiry, never logged, never
// emitted. The client secret lives only in memory and is sent only to the token
// endpoint.

const (
	// managementScope is the Azure Resource Manager OAuth2 scope. The
	// client-credentials .default scope grants the service principal's assigned
	// (read-only, per doc.go) RBAC roles — Reader covers inventory + Activity Log.
	managementScope = "https://management.azure.com/.default"
	loginEndpoint   = "https://login.microsoftonline.com"
	tokenSkew       = 60 * time.Second
)

// tokenSource yields a bearer access token (refreshing as needed).
type tokenSource interface {
	token(ctx context.Context) (string, error)
}

// staticTokenSource returns a fixed operator-supplied bearer token (e.g. a
// managed-identity/ADC sidecar that refreshes it out of band). It is also how
// tests drive the connector without a client secret.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) token(context.Context) (string, error) { return s.tok, nil }

// ccTokenSource mints and caches access tokens via the client-credentials flow.
type ccTokenSource struct {
	tokenURL     string
	clientID     string
	clientSecret string
	client       *http.Client
	now          func() time.Time

	mu     sync.Mutex
	cached string
	exp    time.Time
}

// newCCTokenSource builds a client-credentials token source. tokenURLOverride (a
// full URL) points the exchange at a test server; otherwise the URL is derived
// from loginEndpoint + tenant.
func newCCTokenSource(tenant, clientID, clientSecret, tokenURLOverride string, client *http.Client) *ccTokenSource {
	url := strings.TrimSpace(tokenURLOverride)
	if url == "" {
		url = loginEndpoint + "/" + tenant + "/oauth2/v2.0/token"
	}
	return &ccTokenSource{
		tokenURL: url, clientID: clientID, clientSecret: clientSecret,
		client: client, now: time.Now,
	}
}

// token returns a cached access token or mints a fresh one when the cache is
// empty or near expiry.
func (s *ccTokenSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" && s.now().Before(s.exp) {
		return s.cached, nil
	}
	tok, ttl, err := s.mint(ctx)
	if err != nil {
		return "", err
	}
	life := ttl - tokenSkew
	if life < ttl/2 {
		life = ttl / 2
	}
	s.cached = tok
	s.exp = s.now().Add(life)
	return tok, nil
}

// mint POSTs the client-credentials request and decodes the token. A non-2xx
// surfaces only the status; the client secret never appears in any error.
func (s *ccTokenSource) mint(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {s.clientID},
		"client_secret": {s.clientSecret},
		"scope":         {managementScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("azure-activity: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("azure-activity: token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("azure-activity: token endpoint status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", 0, fmt.Errorf("azure-activity: token response has no access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return tr.AccessToken, ttl, nil
}
