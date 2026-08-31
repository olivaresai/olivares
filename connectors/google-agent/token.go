// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleagent

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// The standard Google service-account jwt-bearer flow (VERIFIED against the
// Google OAuth2 server-to-server docs, 2026-06-11).
const (
	defaultTokenURL    = "https://oauth2.googleapis.com/token"
	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	jwtBearerGrant     = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	assertionLifetime  = time.Hour
)

// saKey is the slice of a service-account key JSON the connector uses. The
// private key is the operator credential: it lives only in this in-memory
// struct, is used solely to SIGN the jwt-bearer assertion, and never travels in
// a request body, a log line, an error or any emitted Graph field.
type saKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`

	rsa *rsa.PrivateKey // parsed from PrivateKey at Open
}

// loadSAKey resolves the credential from the inline JSON (preferred) or the
// file path. Both empty means offline (nil key, nil error — a MISSING credential
// never fails Open). A configured-but-unreadable file, unparseable JSON, a key
// missing client_email/private_key, or a malformed private key is malformed
// configuration and errors; no error ever embeds key material.
func loadSAKey(inlineJSON, file string) (*saKey, error) {
	raw := []byte(strings.TrimSpace(inlineJSON))
	if len(raw) == 0 && strings.TrimSpace(file) != "" {
		b, err := os.ReadFile(strings.TrimSpace(file))
		if err != nil {
			return nil, fmt.Errorf("googleagent: read credentials_file: %w", err)
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil // offline
	}
	var key saKey
	if err := json.Unmarshal(raw, &key); err != nil {
		// Never wrap the raw error: a *json.SyntaxError's message can quote a byte
		// of the credentials document. Only the safe offset survives.
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("googleagent: credentials are not valid JSON (syntax error at offset %d)", se.Offset)
		}
		return nil, errors.New("googleagent: credentials are not valid JSON")
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, fmt.Errorf("googleagent: credentials JSON must carry client_email and private_key")
	}
	rk, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		return nil, err
	}
	key.rsa = rk
	return &key, nil
}

// parseRSAPrivateKey decodes the key PEM and parses it as PKCS#8 (Google's
// current key format, BEGIN PRIVATE KEY) or PKCS#1 (BEGIN RSA PRIVATE KEY).
// Error messages are static — they never embed any part of the key.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("googleagent: private_key is not PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("googleagent: private_key is not an RSA key")
		}
		return rk, nil
	}
	if rk, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return rk, nil
	}
	return nil, fmt.Errorf("googleagent: private_key PEM is neither PKCS#8 nor PKCS#1 RSA")
}

// saClaims is the jwt-bearer assertion claim set: {iss: client_email, scope,
// aud: token_url, iat: now, exp: now+1h}.
type saClaims struct {
	Iss   string `json:"iss"`
	Scope string `json:"scope"`
	Aud   string `json:"aud"`
	Iat   int64  `json:"iat"`
	Exp   int64  `json:"exp"`
}

// mintAssertion builds and RS256-signs the jwt-bearer assertion with the
// service-account private key (go-jose; the key never leaves memory).
func (s *Source) mintAssertion(now time.Time) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: s.key.rsa},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("googleagent: build signer: %w", err)
	}
	payload, err := json.Marshal(saClaims{
		Iss:   s.key.ClientEmail,
		Scope: cloudPlatformScope,
		Aud:   s.tokenURL,
		Iat:   now.Unix(),
		Exp:   now.Add(assertionLifetime).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("googleagent: marshal claims: %w", err)
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("googleagent: sign assertion: %w", err)
	}
	compact, err := jws.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("googleagent: serialize assertion: %w", err)
	}
	return compact, nil
}

// accessToken exchanges the signed assertion for a bearer access token at the
// token endpoint, through the SAME injected transport so a test stubs it. The
// only thing POSTed is grant_type + the short-lived signed assertion — never the
// private key. A non-2xx surfaces the status and a bounded body excerpt; the
// credential never appears in any error.
func (s *Source) accessToken(ctx context.Context) (string, error) {
	assertion, err := s.mintAssertion(s.clock().UTC())
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {jwtBearerGrant},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("googleagent: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.transport().Do(req)
	if err != nil {
		return "", fmt.Errorf("googleagent: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		// The excerpt is the provider's error body; the request form is never included.
		return "", fmt.Errorf("googleagent: token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return "", fmt.Errorf("googleagent: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("googleagent: token endpoint returned no access_token")
	}
	return tr.AccessToken, nil
}
