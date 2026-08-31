// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package codex

import (
	"encoding/json"
	"strings"
)

// wire.go holds the JSON/JSONL wire shapes the Codex connector reads. Only the
// minimal-data fields the connector maps are declared — token COUNTS, adoption
// metrics, money amounts, and actor/workspace/key IDENTIFIERS and metadata — never
// prompt/diff content or key values (docs/SECURITY-HARDENING.md). The upstream payloads may carry
// more fields; they are ignored.
//
// Two verification tiers (honest, per and the session brief):
//   - VERIFIED OpenAI org APIs (api.openai.com): the Costs API and the Audit Logs API
//     follow the documented org-API conventions (bucketed `data`/`has_more`/`next_page`
//     pages for costs; an `object:"list"` + first_id/last_id cursor for audit logs).
//   - VERIFIED endpoint/params/envelope Codex enterprise governance APIs
//     (api.chatgpt.com, verified 2026-07-04): Analytics /usage and Compliance log-file
//     list/download. Row-level JSON field names beyond the public token vocabulary are
//     UNVERIFIED-FIELDS (2026-07-04: full reference behind ChatGPT admin portal), so all
//     fields are optional and parsing is tolerant.

// money is the {value, currency} amount object the Costs API and the Analytics
// estimated-cost field use. Value is in MAJOR currency units (dollars for USD),
// matching the OpenAI org Costs API; the connector converts it to integer micro-USD.
type money struct {
	Value    float64 `json:"value"`
	Currency string  `json:"currency"`
}

// --- Analytics API (Codex enterprise governance; UNVERIFIED-OFFLINE) -----------

// analyticsResponse is the bucketed Analytics page: page.has_more + page.next_page
// drive pagination. Endpoint/params/envelope verified 2026-07-04.
type analyticsResponse struct {
	Data     []analyticsBucket `json:"data"`
	HasMore  bool              `json:"has_more"`
	NextPage string            `json:"next_page"`
}

// analyticsBucket is one time bucket (daily/weekly) with its per-group results.
// start_time/end_time are Unix seconds (org-API convention).
type analyticsBucket struct {
	StartTime int64             `json:"start_time"`
	EndTime   int64             `json:"end_time"`
	Results   []analyticsResult `json:"results"`
}

// analyticsResult is one grouped Codex usage row. UNVERIFIED-FIELDS (2026-07-04: full
// reference behind ChatGPT admin portal; endpoint/params/envelope verified). The token
// fields match the public Codex token vocabulary and are kept; other row fields are
// optional/tolerant. credits is never converted to USD because no public conversion is
// documented. estimated_cost, when present, remains the only money source.
type analyticsResult struct {
	UserID                string  `json:"user_id"`
	UserEmail             string  `json:"user_email"`
	WorkspaceID           string  `json:"workspace_id"`
	Client                string  `json:"client"`
	Model                 string  `json:"model"`
	Threads               int64   `json:"threads"`
	Turns                 int64   `json:"turns"`
	CodeReviews           int64   `json:"code_reviews"`
	SuggestionsShown      int64   `json:"suggestions_shown"`
	SuggestionsAccepted   int64   `json:"suggestions_accepted"`
	LinesAccepted         int64   `json:"lines_accepted"`
	ActiveUsers           int64   `json:"active_users"`
	InputTokens           int64   `json:"input_tokens"`
	OutputTokens          int64   `json:"output_tokens"`
	CachedInputTokens     int64   `json:"cached_input_tokens"`
	ReasoningOutputTokens int64   `json:"reasoning_output_tokens"`
	Credits               float64 `json:"credits"`
	EstimatedCost         *money  `json:"estimated_cost"`
}

// --- Compliance Logs Platform (file list + JSONL download) ---------------------

type complianceLogFilesResponse struct {
	Files []complianceLogFile
}

func (r *complianceLogFilesResponse) UnmarshalJSON(data []byte) error {
	var env struct {
		Data []complianceLogFile `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err == nil && env.Data != nil {
		r.Files = env.Data
		return nil
	}
	var bare []complianceLogFile
	if err := json.Unmarshal(data, &bare); err != nil {
		return err
	}
	r.Files = bare
	return nil
}

type complianceLogFile struct {
	ID        string `json:"id"`
	LogFileID string `json:"log_file_id"`
	EventType string `json:"event_type"`
	CreatedAt string `json:"created_at"`
}

func (f *complianceLogFile) UnmarshalJSON(data []byte) error {
	type alias complianceLogFile
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*f = complianceLogFile(a)
	if f.ID == "" {
		f.ID = f.LogFileID
	}
	return nil
}

// complianceRecord is one JSONL line of a Compliance Logs Platform file. Metadata
// fields are optional/tolerant; row-level JSON fields beyond id/event metadata are
// UNVERIFIED-FIELDS (2026-07-04: admin portal only). Actor PII (email/ip) is hashed
// into the evidence finding's DetailHash, NEVER surfaced.
type complianceRecord struct {
	ID          string          `json:"id"`
	LogType     string          `json:"log_type"`
	EventType   string          `json:"event_type"`
	Type        string          `json:"type"`
	Timestamp   string          `json:"timestamp"`
	WorkspaceID string          `json:"workspace_id"`
	Actor       complianceActor `json:"actor"`
}

// complianceActor is who the compliance record is about. Every PII field is folded
// into the one-way DetailHash of the evidence finding, never transmitted in the clear.
type complianceActor struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Email     string `json:"email"`
	IPAddress string `json:"ip_address"`
}

// parseComplianceJSONL parses the Compliance Logs Platform export body: one JSON
// record per line (JSONL). Blank lines and lines that do not parse are skipped (an
// immutable audit export should not abort a whole window on one odd line) — the caller
// drops records with no id, so a malformed line never becomes a phantom finding.
func parseComplianceJSONL(body string) []complianceRecord {
	lines := parseComplianceJSONLLines(body)
	out := make([]complianceRecord, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Record)
	}
	return out
}

type parsedComplianceLine struct {
	Record  complianceRecord
	RawLine string
}

func parseComplianceJSONLLines(body string) []parsedComplianceLine {
	var lines []parsedComplianceLine
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec complianceRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		lines = append(lines, parsedComplianceLine{Record: rec, RawLine: line})
	}
	return lines
}

// --- Costs API (api.openai.com/v1/organization/costs; VERIFIED-SHAPE) ----------

// costsResponse is the bucketed Costs page (org-API page convention).
type costsResponse struct {
	Data     []costsBucket `json:"data"`
	HasMore  bool          `json:"has_more"`
	NextPage string        `json:"next_page"`
}

// costsBucket is one daily cost bucket (the Costs API supports 1d granularity only).
type costsBucket struct {
	StartTime int64         `json:"start_time"`
	EndTime   int64         `json:"end_time"`
	Results   []costsResult `json:"results"`
}

// costsResult is one billed cost line. amount is the {value(dollars), currency}
// object; line_item is the billed product line (e.g. a Codex line); project_id is the
// grouping echo when group_by includes it.
type costsResult struct {
	Amount    money  `json:"amount"`
	LineItem  string `json:"line_item"`
	ProjectID string `json:"project_id"`
}

// --- Audit Logs API (api.openai.com/v1/organization/audit_logs; VERIFIED-SHAPE) --

// auditLogsResponse is the org Audit Logs list with first_id/last_id cursor.
type auditLogsResponse struct {
	Data    []auditLogEntry `json:"data"`
	HasMore bool            `json:"has_more"`
	FirstID string          `json:"first_id"`
	LastID  string          `json:"last_id"`
}

// auditLogEntry is one audit-log record. effective_at is Unix seconds; type is the
// event (e.g. "api_key.created", "login.succeeded"); the actor's nested user email /
// ip live under session and are hashed, never surfaced. project carries the workspace.
type auditLogEntry struct {
	ID          string       `json:"id"`
	Type        string       `json:"type"`
	EffectiveAt int64        `json:"effective_at"`
	Actor       auditActor   `json:"actor"`
	Project     auditProject `json:"project"`
}

// auditActor is the audit-log actor union. type is "session" or "api_key"; the
// session carries the acting user (id/email) and ip_address (both hashed).
type auditActor struct {
	Type    string       `json:"type"`
	Session auditSession `json:"session"`
	APIKey  auditAPIKey  `json:"api_key"`
}

type auditSession struct {
	IPAddress string    `json:"ip_address"`
	User      auditUser `json:"user"`
}

type auditUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type auditAPIKey struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type auditProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// --- Org inventory (shared OpenAI org admin API; VERIFIED-SHAPE) ---------------

// projectsResponse is /v1/organization/projects (workspace inventory for Codex
// access-token identity governance). Cursor pagination via last_id + has_more.
type projectsResponse struct {
	Data    []projectEntry `json:"data"`
	HasMore bool           `json:"has_more"`
	LastID  string         `json:"last_id"`
}

type projectEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// adminKeysResponse is /v1/organization/admin_api_keys (automation-identity
// inventory). The API returns only a masked redacted_value, never the secret.
type adminKeysResponse struct {
	Data    []adminKeyEntry `json:"data"`
	HasMore bool            `json:"has_more"`
	LastID  string          `json:"last_id"`
}

type adminKeyEntry struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	RedactedValue string `json:"redacted_value"`
	CreatedAt     int64  `json:"created_at"`
}
