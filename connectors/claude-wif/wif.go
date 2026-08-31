// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// IDN-01 Workload Identity Federation exchange constants, verified against the
// Anthropic WIF reference (platform.claude.com/docs/en/manage-claude/wif-reference).
const (
	// grantJWTBearer is the RFC 7523 §2.1 JWT bearer grant — NOT RFC 8693
	// token-exchange. The assertion carries the JWT-SVID/OIDC token.
	grantJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	// oauthTokenPath is the WIF exchange endpoint (POST, JSON body).
	oauthTokenPath = "/v1/oauth/token"
	// tokenPrefixOAT is the documented prefix of a minted access token.
	tokenPrefixOAT = "sk-ant-oat"
	// maxOAuthBody caps the token response body.
	maxOAuthBody = 1 << 20
)

// ExchangeParams identifies the federation rule and target for one exchange. The
// rule (fdrl_), service account (svac_) and organization (UUID) are required; the
// workspace (wrkspc_ or "default") is required only when the rule spans more than
// one workspace, otherwise the server selects the rule's sole workspace.
type ExchangeParams struct {
	FederationRuleID string
	OrganizationID   string
	ServiceAccountID string
	WorkspaceID      string
}

// validate checks the required ids and their prefixes before any network call.
func (p ExchangeParams) validate() error {
	if !strings.HasPrefix(p.FederationRuleID, prefixRule) {
		return fmt.Errorf("federation_rule_id %q must start with %q", p.FederationRuleID, prefixRule)
	}
	if !strings.HasPrefix(p.ServiceAccountID, prefixServiceAccount) {
		return fmt.Errorf("service_account_id %q must start with %q", p.ServiceAccountID, prefixServiceAccount)
	}
	if strings.TrimSpace(p.OrganizationID) == "" {
		return fmt.Errorf("organization_id is required")
	}
	if p.WorkspaceID != "" && p.WorkspaceID != workspaceDefault && !strings.HasPrefix(p.WorkspaceID, prefixWorkspace) {
		return fmt.Errorf("workspace_id %q must be a %q id or the literal %q", p.WorkspaceID, prefixWorkspace, workspaceDefault)
	}
	return nil
}

// MintedToken is the short-lived credential the exchange returns. AccessToken is a
// SECRET (sk-ant-oat…) and MUST NOT be logged or persisted — the helper never does,
// and the host must treat it likewise, using it transiently and re-running the
// exchange before expiry (there is no refresh token for the WIF path). Use Audit()
// for anything written to the ledger or a log.
type MintedToken struct {
	AccessToken string // SECRET — never log or persist
	TokenType   string // "Bearer"
	Scope       string
	ExpiresIn   int // seconds
	ObtainedAt  time.Time

	rule, org, serviceAccount, workspace string // exchange provenance (non-secret)
}

// ExpiresAt is the absolute expiry derived from the obtained time and lifetime.
func (t MintedToken) ExpiresAt() time.Time {
	return t.ObtainedAt.Add(time.Duration(t.ExpiresIn) * time.Second)
}

// ExchangeAudit is the non-secret record of an exchange, suitable for the ledger
// (docs/SECURITY-HARDENING.md). It deliberately carries NO token.
type ExchangeAudit struct {
	FederationRuleID string
	OrganizationID   string
	ServiceAccountID string
	WorkspaceID      string
	Scope            string
	TokenType        string
	ExpiresAt        time.Time
}

// Audit returns the non-secret audit record the host writes to the ledger.
func (t MintedToken) Audit() ExchangeAudit {
	return ExchangeAudit{
		FederationRuleID: t.rule, OrganizationID: t.org, ServiceAccountID: t.serviceAccount,
		WorkspaceID: t.workspace, Scope: t.Scope, TokenType: t.TokenType, ExpiresAt: t.ExpiresAt(),
	}
}

// Exchanger performs the IDN-01 Workload Identity Federation exchange: it presents
// an attested assertion (a verified JWT-SVID from connectors/spiffe, or an OIDC
// token) and mints a short-lived sk-ant-oat token, so a workload never carries a
// static sk-ant- key. It is the only credential-emitting primitive in the product:
// opt-in (the host calls it explicitly), attested (the assertion is verified
// upstream) and audited (the caller logs Audit()). It holds no state and persists
// nothing.
type Exchanger struct {
	baseURL string
	doer    modelprovider.Doer
	now     func() time.Time
}

// NewExchanger builds an Exchanger against baseURL (empty => the Anthropic API). A
// nil doer uses the default HTTP client.
func NewExchanger(baseURL string, doer modelprovider.Doer) *Exchanger {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if doer == nil {
		doer = &http.Client{}
	}
	return &Exchanger{baseURL: strings.TrimRight(baseURL, "/"), doer: doer}
}

// exchangeRequest is the JSON body of the RFC 7523 exchange.
type exchangeRequest struct {
	GrantType        string `json:"grant_type"`
	Assertion        string `json:"assertion"`
	FederationRuleID string `json:"federation_rule_id"`
	OrganizationID   string `json:"organization_id"`
	ServiceAccountID string `json:"service_account_id"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
}

// exchangeResponse is the RFC 6749 §5.1 token response.
type exchangeResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// Exchange presents assertion under the federation rule and mints a short-lived
// token. assertion is the JWT (a verified JWT-SVID or OIDC token) — it is sent, not
// stored. On a non-2xx response it returns an error carrying the HTTP status and the
// Anthropic request id (for support correlation) but never the assertion or any
// secret. The minted token is returned to the caller and is never logged here.
func (e *Exchanger) Exchange(ctx context.Context, assertion string, p ExchangeParams) (MintedToken, error) {
	if strings.TrimSpace(assertion) == "" {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: empty assertion")
	}
	if err := p.validate(); err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: %w", err)
	}

	body, err := json.Marshal(exchangeRequest{
		GrantType: grantJWTBearer, Assertion: assertion,
		FederationRuleID: p.FederationRuleID, OrganizationID: p.OrganizationID,
		ServiceAccountID: p.ServiceAccountID, WorkspaceID: p.WorkspaceID,
	})
	if err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: encode: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+oauthTokenPath, bytes.NewReader(body))
	if err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")

	resp, err := e.doer.Do(req)
	if err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: post: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return MintedToken{}, exchangeError(resp, raw)
	}

	var out exchangeResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: decode response: %w", err)
	}
	if out.AccessToken == "" {
		return MintedToken{}, fmt.Errorf("claude-wif: exchange: response carried no access_token")
	}

	return MintedToken{
		AccessToken: out.AccessToken,
		TokenType:   out.TokenType,
		Scope:       out.Scope,
		ExpiresIn:   out.ExpiresIn,
		ObtainedAt:  e.clock(),
		rule:        p.FederationRuleID, org: p.OrganizationID,
		serviceAccount: p.ServiceAccountID, workspace: p.WorkspaceID,
	}, nil
}

// exchangeError builds a non-sensitive error from a failed exchange: the HTTP
// status, the OAuth error code and the Anthropic request id when present. It never
// echoes the assertion. The specific invalid_grant cause is intentionally opaque
// server-side, so the error surfaces the request id for support correlation.
func exchangeError(resp *http.Response, raw []byte) error {
	reqID := resp.Header.Get("request-id")
	// "error" is a string in the OAuth error shape ({"error":"invalid_grant"}) and an
	// object in the Anthropic API error shape ({"error":{"type":...}}), so decode it
	// as raw and try both forms.
	var parsed struct {
		Error     json.RawMessage `json:"error"`
		RequestID string          `json:"request_id"`
	}
	_ = json.Unmarshal(raw, &parsed)
	if parsed.RequestID != "" {
		reqID = parsed.RequestID
	}
	code := ""
	var asString string
	if err := json.Unmarshal(parsed.Error, &asString); err == nil {
		code = asString
	} else {
		var asObj struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(parsed.Error, &asObj)
		code = asObj.Type
	}
	if code == "" {
		code = "unknown"
	}
	if reqID == "" {
		reqID = "none"
	}
	return fmt.Errorf("claude-wif: exchange rejected: http %d, error %q, request_id %s", resp.StatusCode, code, reqID)
}

// hasOATPrefix reports whether tok carries the documented minted-token prefix; used
// by callers/tests to sanity-check a minted token without logging it.
func hasOATPrefix(tok string) bool { return strings.HasPrefix(tok, tokenPrefixOAT) }

// clock returns the exchanger's time source (injectable for tests).
func (e *Exchanger) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}
