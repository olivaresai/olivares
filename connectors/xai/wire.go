// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package xai

import (
	"strconv"
	"strings"
)

// wire.go holds the JSON wire shapes the xAI connector reads. All shapes are VERIFIED-SHAPE
// against the official docs.x.ai REST reference (primary-source verified 2026-06-20 and
// audit/key currency re-verified 2026-07-04). Only the minimal-data fields the connector
// maps are declared — key/ACL inventory METADATA, the masked key hint, billed money and
// catalog metadata — never a key value, prompt or completion (docs/SECURITY-HARDENING.md). The upstream
// payloads carry more fields; they are ignored.
//
// CASING AMBIGUITY (documented, handled here): the Management API is INCONSISTENT across
// its own examples — the api-keys LIST response shows snake_case "acl_strings" and a
// QUOTED-string "disabled":"false"/tpm, while the create-response and validation examples
// show camelCase "aclStrings"/"acls" and a boolean "disabled". This connector reads ALL
// THREE ACL field names (merged) and decodes "disabled" with flexBool (string-or-bool), so
// it is correct regardless of which casing a given tenant/endpoint emits.

// --- Management API: key validation + key inventory (VERIFIED-SHAPE) ------------

// validationResponse is GET /auth/management-keys/validation — introspects the calling
// management key to discover its team/scope. teamId is deprecated in favor of scopeId for
// org-scoped keys; the connector falls back to scopeId when teamId is empty.
type validationResponse struct {
	APIKeyID string `json:"apiKeyId"`
	TeamID   string `json:"teamId"`
	Scope    string `json:"scope"`
	ScopeID  string `json:"scopeId"`
	Name     string `json:"name"`
}

// apiKeysResponse is GET /auth/teams/{teamId}/api-keys. Cursor pagination via
// paginationToken (absent/empty => last page). Shape re-verified 2026-07-04.
type apiKeysResponse struct {
	APIKeys         []xaiAPIKey `json:"apiKeys"`
	PaginationToken string      `json:"paginationToken"`
}

// xaiAPIKey is one API key's inventory metadata. redactedApiKey is the safe-to-display
// masked hint (e.g. "xai-a**b"); there is deliberately NO field that could carry the
// usable secret (the full apiKey is returned ONLY by create/rotate, which this read-only
// connector never calls). The ACL strings are read from all three documented field-name
// spellings; disabled is decoded string-or-bool. Quota metadata tpm/qps/qpm is optional
// and parsed string-or-number (docs.x.ai re-verified 2026-07-04).
type xaiAPIKey struct {
	APIKeyID    string   `json:"apiKeyId"`
	RedactedKey string   `json:"redactedApiKey"`
	Name        string   `json:"name"`
	UserID      string   `json:"userId"`
	TeamID      string   `json:"teamId"`
	CreateTime  string   `json:"createTime"`
	ModifyTime  string   `json:"modifyTime"`
	ExpireTime  string   `json:"expireTime"`
	Disabled    flexBool `json:"disabled"`
	ACLSnake    []string `json:"acl_strings"`
	ACLCamel    []string `json:"aclStrings"`
	ACLPlain    []string `json:"acls"`
	TPM         flexInt  `json:"tpm"`
	QPS         flexInt  `json:"qps"`
	QPM         flexInt  `json:"qpm"`
}

// acls returns the key's ACL strings, merged across the three documented field spellings
// (acl_strings / aclStrings / acls) and de-duplicated, preserving first-seen order.
func (k xaiAPIKey) acls() []string {
	seen := make(map[string]bool)
	var out []string
	for _, group := range [][]string{k.ACLSnake, k.ACLCamel, k.ACLPlain} {
		for _, a := range group {
			if a != "" && !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// flexBool decodes a JSON boolean that the xAI Management API sometimes emits as a bare
// boolean (true/false) and sometimes as a quoted string ("true"/"false"). Any unparseable
// value decodes to false (a key is treated as enabled unless it explicitly says disabled),
// never an error that would abort the whole key page.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	v, err := strconv.ParseBool(s)
	if err != nil {
		*b = false
		return nil
	}
	*b = flexBool(v)
	return nil
}

// flexInt decodes optional xAI quota integers (tpm/qps/qpm), which docs and examples
// have emitted as both JSON numbers and quoted decimal strings; re-verified 2026-07-04.
type flexInt int

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		*n = 0
		return nil
	}
	*n = flexInt(v)
	return nil
}

// --- Management API: billing (VERIFIED-SHAPE) ----------------------------------

// moneyVal is the { "val": "<decimal-string>" } money wrapper the balance / spending-limit
// endpoints use. Amounts are decimal STRINGS (arbitrary precision), never floats on the wire.
type moneyVal struct {
	Val string `json:"val"`
}

// invoicesResponse is GET /v1/billing/teams/{teamId}/invoices — finalized historical spend.
type invoicesResponse struct {
	Invoices []invoice `json:"invoices"`
}

// invoice is one billing-cycle invoice. lines[].amount and total are plain decimal strings.
type invoice struct {
	TeamID        string        `json:"teamId"`
	InvoiceID     string        `json:"invoiceId"`
	InvoiceNumber string        `json:"invoiceNumber"`
	CreateTime    string        `json:"createTime"`
	InvoiceStatus string        `json:"invoiceStatus"`
	Lines         []invoiceLine `json:"lines"`
	Total         string        `json:"total"`
}

// invoiceLine is one billed line, keyed by cluster + description (not a clean model id).
type invoiceLine struct {
	ClusterName string `json:"clusterName"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
}

// previewResponse is GET /v1/billing/teams/{teamId}/postpaid/invoice/preview — the
// current (unfinalized) billing cycle's spend so far for a postpaid team.
type previewResponse struct {
	CoreInvoice coreInvoice `json:"coreInvoice"`
}

type coreInvoice struct {
	Lines          []invoiceLine `json:"lines"`
	AmountAfterVat string        `json:"amountAfterVat"`
}

// balanceResponse is GET /v1/billing/teams/{teamId}/prepaid/balance — the remaining
// prepaid credit (total.val). 404 on a postpaid team (the connector skips it honestly).
type balanceResponse struct {
	Total moneyVal `json:"total"`
}

// spendingLimitsResponse is GET /v1/billing/teams/{teamId}/postpaid/spending-limits — the
// effective monthly soft/hard spend ceiling. 404 on a prepaid team (skipped honestly).
type spendingLimitsResponse struct {
	SpendingLimits spendingLimits `json:"spendingLimits"`
}

type spendingLimits struct {
	EffectiveSl     moneyVal `json:"effectiveSl"`
	EffectiveHardSl moneyVal `json:"effectiveHardSl"`
}

// --- Management API: audit events (VERIFIED-SHAPE + 2026-07-04 currency) --

// auditEventsResponse is GET /audit/teams/{teamId}/events — the admin action stream.
// Cursor pagination tolerates the pinned has_more+cursor shape and the live
// nextPageToken shape, with nextPageToken preferred when both are present (verified
// 2026-07-04). The live endpoint also documents eventFilter.* and orderBy query params,
// but this read-only evidence connector deliberately pulls the full time window without
// server-side filtering so semantics are not lost in the free-text description field.
type auditEventsResponse struct {
	Events          []auditEvent `json:"events"`
	HasMore         bool         `json:"has_more"`
	Cursor          string       `json:"cursor"`
	NextPageToken   string       `json:"nextPageToken"`
	PaginationToken string       `json:"paginationToken"`
}

// auditEvent is one admin action event. The live docs.x.ai shape verified 2026-07-04 is
// eventTime/eventId/description/user{...}; older pinned aliases are retained
// additively. There is NO structured eventType field — action semantics live in
// description — so titles use description and user identity fields are folded only into
// the one-way DetailHash, never the title or SubjectRef.
type auditEvent struct {
	EventID     string    `json:"eventId"`
	ID          string    `json:"id"`
	EventTime   string    `json:"eventTime"`
	Time        string    `json:"time"`
	Timestamp   string    `json:"timestamp"`
	Description string    `json:"description"`
	Message     string    `json:"message"`
	Action      string    `json:"action"`
	User        auditUser `json:"user"`
	Actor       auditUser `json:"actor"`
}

// auditUser is the actor identity on an audit event. The live docs.x.ai user fields
// verified 2026-07-04 include userId, email, givenName, familyName, profileImage and
// profileImageUrl. These identity fields are never exposed — only hashed into the
// DetailHash.
type auditUser struct {
	UserID          string `json:"userId"`
	ID              string `json:"id"`
	Email           string `json:"email"`
	GivenName       string `json:"givenName"`
	FamilyName      string `json:"familyName"`
	Name            string `json:"name"`
	ProfileImage    string `json:"profileImage"`
	ProfileImageURL string `json:"profileImageUrl"`
}

// --- Inference API: live catalog (VERIFIED-SHAPE) ------------------------------

// languageModelsResponse is GET /v1/language-models — the rich model list (wrapper key is
// "models", NOT the OpenAI-style {object:"list",data:[…]}). Prices are integers in USD
// CENTS per 100,000,000 (1e8) tokens; USD per 1M tokens = field / 10000.
type languageModelsResponse struct {
	Models []languageModel `json:"models"`
}

type languageModel struct {
	ID                       string   `json:"id"`
	Created                  int64    `json:"created"`
	OwnedBy                  string   `json:"owned_by"`
	Version                  string   `json:"version"`
	Aliases                  []string `json:"aliases"`
	InputModalities          []string `json:"input_modalities"`
	OutputModalities         []string `json:"output_modalities"`
	PromptTextTokenPrice     int64    `json:"prompt_text_token_price"`
	CachedPromptTextPrice    int64    `json:"cached_prompt_text_token_price"`
	CompletionTextTokenPrice int64    `json:"completion_text_token_price"`
	LongContextThreshold     int64    `json:"long_context_threshold"`
}
