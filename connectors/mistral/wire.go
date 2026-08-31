// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mistral

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// wire.go holds the JSON wire shapes the Mistral connector reads. Only the minimal-data
// fields the connector maps are declared — model catalog metadata and (for the opt-in
// seam) workspace/key inventory METADATA, never a key value, prompt or completion
// (docs/SECURITY-HARDENING.md). Admin API beta wire currency was re-verified against the generated
// docs.mistral.ai reference on 2026-07-04. Two verification tiers (honest, per the
// directory's bar):
//
//   - VERIFIED-SHAPE — Models API (GET https://api.mistral.ai/v1/models): the response is
//     {object:"list", data:[ModelCard]} where each card carries id, object, created (unix),
//     owned_by, a capabilities object of booleans, max_context_length, aliases, deprecation
//     and (fine-tuned cards) job/root/archived. Confirmed against docs.mistral.ai/api and
//     the published OpenAPI spec.
//
//   - UNVERIFIED-OFFLINE — workspace/API-key inventory: Mistral publishes NO concrete REST
//     shape for org/workspace/key listing (the Admin API is documented only narratively).
//     The connector models Mistral's own {object:"list", data:[…]} list convention behind
//     an opt-in flag with operator-overridable paths; a 403/404 degrades to an honest
//     posture finding. There is deliberately NO field on apiKeyEntry that could hold a
//     usable secret.

// --- Models API (VERIFIED-SHAPE) -----------------------------------------------

// modelsResponse is GET /v1/models. The API returns the full list in one data array (no
// pagination). object is "list".
type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelCard `json:"data"`
}

// modelCard is one model the Models API reports. The capabilities object's booleans are
// the authoritative per-model capability flags; max_context_length is the live context
// window. Fine-tuned cards additionally carry job/root/archived (ignored here — an id +
// capabilities is all the catalog needs). deprecation is an ISO date or null/"".
type modelCard struct {
	ID               string            `json:"id"`
	Object           string            `json:"object"`
	Created          int64             `json:"created"`
	OwnedBy          string            `json:"owned_by"`
	Name             string            `json:"name"`
	Description      string            `json:"description"`
	MaxContextLength int64             `json:"max_context_length"`
	Deprecation      string            `json:"deprecation"`
	Capabilities     modelCapabilities `json:"capabilities"`
}

// modelCapabilities is the per-model capability boolean set the Models API reports.
type modelCapabilities struct {
	CompletionChat  bool `json:"completion_chat"`
	CompletionFIM   bool `json:"completion_fim"`
	FunctionCalling bool `json:"function_calling"`
	FineTuning      bool `json:"fine_tuning"`
	Vision          bool `json:"vision"`
	Classification  bool `json:"classification"`
}

// --- Workspace / API-key inventory (UNVERIFIED-OFFLINE) ------------------------

// workspacesResponse is the modeled workspace-list page (Mistral's list convention:
// {object:"list", data:[…], has_more, last_id}). UNVERIFIED-OFFLINE — operator-overridable.
type workspacesResponse struct {
	Data    []workspaceEntry `json:"data"`
	HasMore bool             `json:"has_more"`
	LastID  string           `json:"last_id"`
}

// workspaceEntry is one workspace's inventory metadata (non-sensitive). created_at is RFC3339.
type workspaceEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// apiKeysResponse is the modeled API-key-list page. UNVERIFIED-OFFLINE — operator-overridable.
type apiKeysResponse struct {
	Data    []apiKeyEntry `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

// apiKeyEntry is one API key's inventory metadata. masked is the safe-to-display partial;
// there is deliberately NO field that could carry the usable secret (docs/SECURITY-HARDENING.md). The
// secret is shown ONCE at creation in the Mistral console and is never re-readable.
type apiKeyEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"`
	Masked      string `json:"masked"`
	CreatedAt   string `json:"created_at"`
}

// --- Admin API (BETA-VERIFIED) -------------------------------------------
//
// The Mistral Admin API (beta) now has a published generated reference (verified
// 2026-07-04). It requires a distinct AdminApiKey (different from the regular inference
// API key). A 403/404 from any admin surface degrades to a posture finding rather than
// failing the gather.

// adminAuditLogsResponse is GET /api/admin/audit-logs. The generated Admin API beta
// reference verified 2026-07-04 documents AuditLogOut entries; the connector accepts
// both the pinned {data:[...]} envelope and a direct array.
type adminAuditLogsResponse struct {
	Data []adminAuditLogEntry `json:"data"`
}

func (r *adminAuditLogsResponse) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return json.Unmarshal(data, &r.Data)
	}
	var env struct {
		Data  []adminAuditLogEntry `json:"data"`
		Items []adminAuditLogEntry `json:"items"`
		Logs  []adminAuditLogEntry `json:"audit_logs"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	switch {
	case env.Data != nil:
		r.Data = env.Data
	case env.Items != nil:
		r.Data = env.Items
	default:
		r.Data = env.Logs
	}
	return nil
}

// adminAuditLogEntry is one audit log event from the Admin API. The 2026-07-04
// generated reference documents log_id (int), actor_type, actor_metadata (object),
// event_type, event_metadata (object), target_type, target_metadata (object) and
// created_at; the pinned string metadata fields are retained. Actor/target metadata is
// hashed into DetailHash, never surfaced.
type adminAuditLogEntry struct {
	LogID          flexJSONText `json:"log_id"`
	ID             flexJSONText `json:"id"`
	ActorType      string       `json:"actor_type"`
	ActorMetadata  flexJSONText `json:"actor_metadata"`
	EventType      flexJSONText `json:"event_type"`
	Type           flexJSONText `json:"type"`
	EventMetadata  flexJSONText `json:"event_metadata"`
	TargetType     string       `json:"target_type"`
	TargetMetadata flexJSONText `json:"target_metadata"`
	CreatedAt      string       `json:"created_at"`
}

// adminUsageResponse is GET /api/admin/usage?month=M&year=Y.
type adminUsageResponse struct {
	Data []adminUsageEntry `json:"data"`
}

// adminUsageEntry is one per-model usage row from the Admin API. When currency is
// non-empty, the amount is billed (provenance=billed); otherwise estimated.
type adminUsageEntry struct {
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Amount       float64 `json:"amount"`
	Currency     string  `json:"currency"`
}

// adminUsersResponse is GET /api/admin/users.
type adminUsersResponse struct {
	Data []adminUserEntry `json:"data"`
}

// adminUserEntry is one user from the Admin API. Email is hashed into the finding's
// DetailHash, never surfaced (minimal-data, docs/SECURITY-HARDENING.md).
type adminUserEntry struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// adminWorkspacesResponse is GET /api/admin/workspaces. The generated Admin API beta
// reference verified 2026-07-04 documents uuid/name/description/icon/is_default/
// members_count/raw_roles/spend_limit; the pinned id/name fields are retained.
type adminWorkspacesResponse struct {
	Data []adminWorkspaceEntry `json:"data"`
}

func (r *adminWorkspacesResponse) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return json.Unmarshal(data, &r.Data)
	}
	var env struct {
		Data  []adminWorkspaceEntry `json:"data"`
		Items []adminWorkspaceEntry `json:"items"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	if env.Data != nil {
		r.Data = env.Data
	} else {
		r.Data = env.Items
	}
	return nil
}

// adminWorkspaceEntry is one workspace from the Admin API. uuid is accepted as an
// alternative id field (verified 2026-07-04). spend_limit is tolerant number-or-object and
// is surfaced through the existing spend-limit posture path.
type adminWorkspaceEntry struct {
	ID           string         `json:"id"`
	UUID         string         `json:"uuid"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Icon         string         `json:"icon"`
	IsDefault    bool           `json:"is_default"`
	MembersCount int            `json:"members_count"`
	RawRoles     flexJSONText   `json:"raw_roles"`
	SpendLimit   flexSpendLimit `json:"spend_limit"`
}

// adminAPIKeysResponse is GET /api/admin/api-keys.
type adminAPIKeysResponse struct {
	Data []adminAPIKeyEntry `json:"data"`
}

// adminAPIKeyEntry is one API key's inventory metadata from the Admin API. There is
// deliberately NO field that could hold a usable secret (docs/SECURITY-HARDENING.md).
type adminAPIKeyEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at"`
	WorkspaceID string `json:"workspace_id"`
}

// adminSpendLimitResponse is GET /api/admin/spend-limit. spend_limit is tolerant of the
// pinned numeric field and the object/string variants observed in Admin beta-style
// payloads (verified 2026-07-04).
type adminSpendLimitResponse struct {
	SpendLimit flexSpendLimit `json:"spend_limit"`
	Currency   string         `json:"currency"`
}

// adminRateLimitResponse is GET /api/admin/rate-limit.
type adminRateLimitResponse struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	TokensPerMinute   int `json:"tokens_per_minute"`
}

type flexJSONText string

func (v *flexJSONText) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*v = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		*v = flexJSONText(s)
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		*v = flexJSONText(compact.String())
		return nil
	}
	*v = flexJSONText(string(raw))
	return nil
}

func (v flexJSONText) String() string { return string(v) }

type flexSpendLimit struct {
	Amount   float64
	Currency string
	Present  bool
}

func (v *flexSpendLimit) UnmarshalJSON(data []byte) error {
	raw := bytes.TrimSpace(data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*v = flexSpendLimit{}
		return nil
	}
	if amount, ok := parseFlexAmount(raw); ok {
		*v = flexSpendLimit{Amount: amount, Present: true}
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		*v = flexSpendLimit{}
		return nil
	}
	var amount float64
	var ok bool
	for _, key := range []string{"amount", "value", "limit", "spend_limit"} {
		if rawAmount, present := obj[key]; present {
			if amount, ok = parseFlexAmount(rawAmount); ok {
				break
			}
		}
	}
	var currency string
	if rawCurrency, present := obj["currency"]; present {
		_ = json.Unmarshal(rawCurrency, &currency)
	}
	*v = flexSpendLimit{Amount: amount, Currency: currency, Present: ok}
	return nil
}

func parseFlexAmount(raw []byte) (float64, bool) {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		f, err = strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f, err == nil
	}
	return 0, false
}
