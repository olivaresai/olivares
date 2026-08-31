// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This file mints Google OAuth2 access tokens from a service-account key with the
// standard two-legged JWT-bearer flow (RFC 7523 / Google's documented service-
// account flow), using ONLY the standard library (crypto/rsa + crypto/sha256 +
// encoding/base64): a signed RS256 assertion JWT is exchanged at the token URI for
// a short-lived access token. It mirrors connectors/gcp-audit's token source (replicate the pattern rather than extract a shared helper) so the connector
// stays dependency-free (no golang.org/x/oauth2, no go-jose) and boundary-clean.
// Tokens are cached until shortly before expiry, never logged, and never emitted.

const (
	// readOnlyScope is the single least-privilege read scope this connector needs.
	// cloud-platform.read-only grants read access across GCP — the Vertex AI publisher
	// model catalog (aiplatform), Cloud Monitoring time series and Model Armor template
	// reads — without any write capability. The operator still binds read-only IAM roles
	// to the SA (AI Platform Viewer, Monitoring Viewer, Model Armor Viewer); the scope is
	// the upper bound, the roles the actual grant.
	readOnlyScope = "https://www.googleapis.com/auth/cloud-platform.read-only"
	// defaultTokenURI is Google's OAuth2 token endpoint, used when the service-account
	// key does not carry its own token_uri.
	defaultTokenURI = "https://oauth2.googleapis.com/token"
	jwtBearerGrant  = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	tokenSkew       = 60 * time.Second
)

// serviceAccount is the subset of a Google service-account JSON key this connector
// reads. The private key never leaves memory and is never logged.
type serviceAccount struct {
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// tokenSource yields a bearer access token (refreshing as needed).
type tokenSource interface {
	token(ctx context.Context) (string, error)
}

// staticTokenSource returns a fixed operator-supplied bearer token (e.g. injected by
// a Workload Identity Federation sidecar or ADC helper that refreshes it out of
// band). It is also how tests drive the connector without a private key.
type staticTokenSource struct{ tok string }

func (s staticTokenSource) token(context.Context) (string, error) { return s.tok, nil }

// saTokenSource mints and caches access tokens from a service-account key.
type saTokenSource struct {
	email    string
	kid      string
	tokenURI string
	key      *rsa.PrivateKey
	client   *http.Client
	now      func() time.Time

	mu     sync.Mutex
	cached string
	exp    time.Time
}

// newSATokenSource builds a token source from the bytes of a service-account JSON
// key. The optional tokenURIOverride points the exchange at a test server.
func newSATokenSource(saJSON []byte, client *http.Client, tokenURIOverride string) (*saTokenSource, error) {
	var sa serviceAccount
	if err := json.Unmarshal(saJSON, &sa); err != nil {
		// Never wrap the raw error: a *json.SyntaxError can quote a byte of the
		// credentials document. Only a static message survives.
		return nil, fmt.Errorf("vertex: credentials are not valid JSON")
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, fmt.Errorf("vertex: credentials JSON must carry client_email and private_key")
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	uri := firstNonEmpty(strings.TrimSpace(tokenURIOverride), strings.TrimSpace(sa.TokenURI), defaultTokenURI)
	return &saTokenSource{
		email: sa.ClientEmail, kid: sa.PrivateKeyID, tokenURI: uri,
		key: key, client: client, now: time.Now,
	}, nil
}

// token returns a cached access token or mints a fresh one when the cache is empty
// or near expiry.
func (s *saTokenSource) token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != "" && s.now().Before(s.exp) {
		return s.cached, nil
	}
	tok, ttl, err := s.mint(ctx)
	if err != nil {
		return "", err
	}
	// Refresh tokenSkew before expiry, but never cache for less than half the token
	// life so a short-lived token does not re-mint on every call.
	life := ttl - tokenSkew
	if life < ttl/2 {
		life = ttl / 2
	}
	s.cached = tok
	s.exp = s.now().Add(life)
	return tok, nil
}

// mint signs a fresh assertion and exchanges it for an access token. The only thing
// POSTed is grant_type + the short-lived signed assertion — never the private key. A
// non-2xx surfaces only the status; the credential never appears in any error.
func (s *saTokenSource) mint(ctx context.Context) (string, time.Duration, error) {
	assertion, err := s.assertion()
	if err != nil {
		return "", 0, err
	}
	form := url.Values{"grant_type": {jwtBearerGrant}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("vertex: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("vertex: token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("vertex: token endpoint status %d", resp.StatusCode)
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", 0, fmt.Errorf("vertex: token response has no access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return tr.AccessToken, ttl, nil
}

// assertion builds and signs the RS256 JWT-bearer assertion {iss, scope, aud, iat,
// exp}. iss is the SA email; aud is the token URI; the scope is the single read-only
// scope.
func (s *saTokenSource) assertion() (string, error) {
	now := s.now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if s.kid != "" {
		header["kid"] = s.kid
	}
	claims := map[string]any{
		"iss":   s.email,
		"scope": readOnlyScope,
		"aud":   s.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("vertex: marshal assertion header: %w", err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("vertex: marshal assertion claims: %w", err)
	}
	signingInput := b64url(hb) + "." + b64url(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("vertex: sign assertion: %w", err)
	}
	return signingInput + "." + b64url(sig), nil
}

// parseRSAPrivateKey decodes a PEM RSA private key, accepting PKCS#8 (the format
// Google service-account keys use) and PKCS#1. Error messages are static — they
// never embed any part of the key.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("vertex: private_key is not valid PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("vertex: private_key is not an RSA key")
		}
		return rk, nil
	}
	rk, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("vertex: parse private_key: %w", err)
	}
	return rk, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
