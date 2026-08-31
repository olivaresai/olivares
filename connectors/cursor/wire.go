// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// wire.go holds the JSON shapes the Cursor Admin API returns and the read-only HTTP
// client the connector reads them through. Only the minimal-data fields the connector
// maps are declared — token COUNTS, billed money, model ids, role/actor IDENTIFIERS and
// audit metadata — never prompt/diff content or key values (docs/SECURITY-HARDENING.md). The upstream
// payloads carry more fields; unknown keys are ignored.
//
// VERIFIED (primary source, jun-2026, https://cursor.com/docs/account/teams/admin-api):
//   - base https://api.cursor.com; auth = HTTP Basic with the API key as the USERNAME
//     and an EMPTY password (NOT Bearer — Bearer is only the separate Cloud Agents API).
//   - the read endpoints used here are GET /teams/members + GET /teams/audit-logs and the
//     query-with-body reads POST /teams/filtered-usage-events / /teams/spend. The Admin
//     API's mutating endpoints (user-spend-limit, remove-member, group writes) are NEVER
//     constructed by this client — read-only by construction, like the modelprovider one.

// Doer is the minimal HTTP capability the connector needs (satisfied by *http.Client). A
// test injects a stub returning recorded fixture bytes, so no live call is made.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// maxErrBody bounds how much of a non-2xx body is surfaced for diagnostics (never the
// credential — Basic auth lives in the Authorization header, never echoed into an error).
const maxErrBody = 2 << 10

// client is the read-only Cursor Admin API client. Every method targets exactly one
// documented READ endpoint; there is no generic "do any request", so the connector
// cannot reach a mutating endpoint by construction (the read-first guarantee, docs/SECURITY-HARDENING.md).
// The Admin API authenticates with the key as the Basic-auth username and an empty
// password — set once per request, never logged, never placed in a URL.
type client struct {
	base string
	key  string
	doer Doer
}

func newClient(base, key string, doer Doer) *client {
	if doer == nil {
		doer = http.DefaultClient
	}
	return &client{base: strings.TrimRight(base, "/"), key: key, doer: doer}
}

// getJSON issues a read-only GET with Basic auth and decodes the JSON body into out.
func (c *client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("cursor: build GET %s: %w", path, err)
	}
	return c.do(req, path, out)
}

// postJSON issues a query-with-body POST READ with Basic auth and decodes the response.
// The Cursor Admin API serves several reads (usage events, spend, daily usage) as POST
// with a JSON filter body; this is still a read — the connector never POSTs to a
// mutating endpoint (it has no method that names one).
func (c *client) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("cursor: encode body for %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("cursor: build POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, path, out)
}

// do applies Basic auth, executes the request and decodes a 2xx body into out. A non-2xx
// status is returned as an error carrying the status code (so isUnavailable can degrade a
// plan-gated 403/404) and a bounded body excerpt — never the credential.
func (c *client) do(req *http.Request, path string, out any) error {
	req.SetBasicAuth(c.key, "") // key as username, empty password (VERIFIED Cursor Admin API)
	req.Header.Set("Accept", "application/json")

	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("cursor: %s %s: %w", req.Method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return fmt.Errorf("cursor: %s %s: status %d: %s", req.Method, path, resp.StatusCode, strings.TrimSpace(string(excerpt)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("cursor: decode %s: %w", path, err)
	}
	return nil
}

// --- /teams/members (GET; inventory + actor attribution) -----------------------

// membersResponse is GET /teams/members.
type membersResponse struct {
	TeamMembers []memberEntry `json:"teamMembers"`
}

// memberEntry is one team member. id is the stable non-PII identifier used for cost
// attribution and as the inventory SubjectRef; email/name are PII folded into a hash.
type memberEntry struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	IsRemoved bool   `json:"isRemoved"`
}

// --- /teams/filtered-usage-events (POST read; billed cost + tokens) ------------

// usageRequest is the filter body: an inclusive epoch-millis window plus pagination.
type usageRequest struct {
	StartDate int64 `json:"startDate"`
	EndDate   int64 `json:"endDate"`
	Page      int   `json:"page"`
	PageSize  int   `json:"pageSize"`
}

// usageResponse is one page of usage events.
type usageResponse struct {
	UsageEvents []usageEvent `json:"usageEvents"`
	Pagination  pagination   `json:"pagination"`
}

// usageEvent is one billable Cursor agent event. chargedCents is the AUTHORITATIVE billed
// amount (model cost + Cursor token rate, in cents — VERIFIED: "Use this field to reconcile
// event-level costs with /teams/spend totals."); tokenUsage.totalCents is model-cost-only
// and is NOT used as the billed figure. timestamp is epoch millis (a string per the API).
type usageEvent struct {
	Timestamp        string      `json:"timestamp"`
	UserEmail        string      `json:"userEmail"`
	ServiceAccountID string      `json:"serviceAccountId"`
	Model            string      `json:"model"`
	Kind             string      `json:"kind"`
	MaxMode          bool        `json:"maxMode"`
	IsTokenBasedCall bool        `json:"isTokenBasedCall"`
	IsChargeable     bool        `json:"isChargeable"`
	IsHeadless       bool        `json:"isHeadless"`
	ChargedCents     float64     `json:"chargedCents"`
	TokenUsage       *tokenUsage `json:"tokenUsage"`
}

// tokenUsage is the per-event token breakdown (present when isTokenBasedCall=true).
type tokenUsage struct {
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	TotalCents       float64 `json:"totalCents"`
}

// --- /teams/spend (POST read; budget posture) ----------------------------------

type spendRequest struct {
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}

type spendResponse struct {
	TeamMemberSpend []spendEntry `json:"teamMemberSpend"`
	TotalPages      int          `json:"totalPages"`
}

// spendEntry is one member's current-cycle spend. spendCents is on-demand spend (excl.
// included usage); overallSpendCents is total (incl. included). monthlyLimitDollars is
// NULLABLE (a member with no cap) — a *float64 so "no limit" is distinguishable from $0.
type spendEntry struct {
	UserID              string   `json:"userId"`
	Name                string   `json:"name"`
	Email               string   `json:"email"`
	Role                string   `json:"role"`
	SpendCents          float64  `json:"spendCents"`
	OverallSpendCents   float64  `json:"overallSpendCents"`
	MonthlyLimitDollars *float64 `json:"monthlyLimitDollars"`
}

// --- /teams/audit-logs (GET; external_activity evidence) -----------------------

type auditResponse struct {
	Events     []auditEvent `json:"events"`
	Pagination pagination   `json:"pagination"`
}

// auditEvent is one audit record. event_id is the non-sensitive handle (SubjectRef); the
// user_email / ip_address / event_data are folded into the one-way DetailHash, never
// surfaced. timestamp is epoch millis (a string).
type auditEvent struct {
	EventID   string          `json:"event_id"`
	Timestamp string          `json:"timestamp"`
	IPAddress string          `json:"ip_address"`
	UserEmail string          `json:"user_email"`
	EventType string          `json:"event_type"`
	EventData json.RawMessage `json:"event_data"`
}

// pagination is the shared page cursor (filtered-usage-events / audit-logs). When the API
// omits hasNextPage (a zero value), the caller stops on an empty page instead.
type pagination struct {
	NumPages    int  `json:"numPages"`
	CurrentPage int  `json:"currentPage"`
	PageSize    int  `json:"pageSize"`
	HasNextPage bool `json:"hasNextPage"`
}
