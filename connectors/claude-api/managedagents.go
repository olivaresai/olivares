// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the Managed Agents surface (ANT2-14): multi-agent sessions, the
// context-isolated thread tree, A2A inter-agent messages, the HITL tool-confirmation
// gate, third-party-credential VAULTS, and the SIGNED WEBHOOKS the platform pushes.
// It is OBSERVATION/INGEST only (docs/SECURITY-HARDENING.md): the connector reads forensic detail and
// verifies webhook signatures — it never orchestrates an agent, mints a vault
// credential, or interposes on the data path. Three things matter for the control
// plane:
//
//   - The PRIMARY thread only sees a subagent's start/end. Subagent forensic detail
//     lives in the per-thread event stream — so an auditor MUST read
//     /v1/sessions/:id/threads/:tid/events or the subagent's actions are invisible.
//   - VAULTS hold third-party credentials (mcp_oauth/static_bearer) and are
//     WORKSPACE-SCOPED → a lateral-movement risk worth a posture finding. The connector
//     ingests vlt_/vcrd_ REFERENCES only — NEVER the credential material (docs/SECURITY-HARDENING.md).
//   - WEBHOOKS (session.*/vault.*) are SIGNED (X-Webhook-Signature, whsec_ secret). The
//     receiver VERIFIES the HMAC before trusting a payload; the platform auto-disables a
//     webhook whose target is a private IP or a redirect (a documented anti-SSRF guard).
//
// The HITL user.tool_confirmation gate (stop_reason:requires_action) is MODELED here as
// a pending-approval finding; the approval UI is (out of scope). Multi-agent caps
// (coordinator + roster ≤ 20, ≤ 25 context-isolated threads) are recorded as posture.
//
// Authority (verbatim, jun-2026): platform.claude.com/docs/en/managed-agents/* (ANT2-14).
package claudeapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Managed Agents constants (verified jun-2026).
const (
	// AgentToolsetBeta is the multi-agent toolset beta header value.
	AgentToolsetBeta = "agent_toolset_20260401"
	// MaxRoster is the documented cap on a coordinator's subagent roster.
	MaxRoster = 20
	// MaxThreads is the documented cap on context-isolated threads per session.
	MaxThreads = 25
	// StopRequiresAction is the stop_reason that signals a HITL tool-confirmation gate
	// (user.tool_confirmation) is awaiting an allow/deny decision.
	StopRequiresAction = "requires_action"

	// Vault id prefixes — REFERENCES, never material.
	prefixVault     = "vlt_"
	prefixVaultCred = "vcrd_"

	// webhookSigHeader is the request header carrying the HMAC of a signed webhook
	// (the secret has the documented whsec_ prefix).
	webhookSigHeader = "X-Webhook-Signature"

	// Resource kinds for Managed-Agents edges.
	resAgentThread = "anthropic.agent_thread" // A2A / delegation target
	resVault       = "anthropic.vault"        // an agent's bound vault

	managedAgentSubject = "anthropic.managed_agent"
)

// ThreadEvent is one event in a subagent thread's stream (/v1/sessions/:id/threads/
// :tid/events). The primary thread sees only start/end, so this stream is the ONLY
// forensic record of what a subagent did — the connector reads it read-only and maps
// structural attribution (agent ref, type, time), never message content.
type ThreadEvent struct {
	Type      string `json:"type"`      // e.g. agent.thread_message, tool_use, agent.thread_end
	AgentRef  string `json:"agent_ref"` // the subagent that emitted it
	PeerRef   string `json:"peer_ref"`  // for A2A messages, the recipient agent
	ToolName  string `json:"tool_name"` // for tool_use events
	CreatedAt string `json:"created_at"`
}

// threadEventsResponse is the per-thread event stream page.
type threadEventsResponse struct {
	Data    []ThreadEvent `json:"data"`
	HasMore bool          `json:"has_more"`
	LastID  string        `json:"last_id"`
}

// FetchThreadEvents reads a subagent thread's event stream read-only (ANT2-14). It is
// the source an auditor needs because the primary thread only records start/end; a
// caller pages the whole stream so subagent actions are not lost.
func (s *Source) FetchThreadEvents(ctx context.Context, sessionID, threadID string) ([]ThreadEvent, error) {
	if s.client == nil {
		return nil, nil
	}
	path := "/v1/sessions/" + url.PathEscape(sessionID) + "/threads/" + url.PathEscape(threadID) + "/events"
	var out []ThreadEvent
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp threadEventsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, path, q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// ThreadEventEdge maps an A2A thread message to a PERMITTED-less OBSERVED edge
// (agent → peer agent), so module IV/III see the inter-agent communication the
// primary thread hides. A non-A2A event returns ok=false (no edge). Source is OTEL-ish
// observation; here it is a real observed communication, attributed to the agent.
func ThreadEventEdge(sessionID string, ev ThreadEvent, at time.Time) (model.EdgeObservation, bool) {
	if !strings.HasPrefix(ev.Type, "agent.thread_message") || ev.AgentRef == "" || ev.PeerRef == "" {
		return model.EdgeObservation{}, false
	}
	when := at
	if t := parseTime(ev.CreatedAt); !t.IsZero() {
		when = t
	}
	return model.EdgeObservation{
		OriginKind:   "agent",
		OriginRef:    ev.AgentRef,
		ResourceKind: resAgentThread,
		ResourceRef:  ev.PeerRef, // the recipient subagent
		Mode:         model.ModeReadWrite,
		Source:       model.SignalA2A, // an observed agent↔agent communication
		Confidence:   model.ConfidenceAttributed,
		ToolRef:      sessionID,
		ObservedAt:   when,
	}, true
}

// Vault is a Managed-Agents credential vault (ANT2-14). It holds THIRD-PARTY
// credentials and is WORKSPACE-SCOPED, so any agent in the workspace can use them — a
// lateral-movement risk. The connector ingests the vlt_ REFERENCE and metadata only.
type Vault struct {
	ID           string `json:"id"` // vlt_…
	Name         string `json:"name"`
	WorkspaceRef string `json:"workspace_id"`
	Archived     bool   `json:"archived"` // archive purges material but RETAINS the record
}

// VaultCredential is one third-party credential in a vault (ANT2-14). It is WRITE-ONLY
// at the platform; the connector reads the vcrd_ REFERENCE and the credential TYPE
// (mcp_oauth|static_bearer) — NEVER the token/bearer value (docs/SECURITY-HARDENING.md).
type VaultCredential struct {
	ID       string `json:"id"` // vcrd_…
	VaultRef string `json:"vault_id"`
	Type     string `json:"type"` // mcp_oauth | static_bearer
	Archived bool   `json:"archived"`
}

// vaultsResponse / vaultCredsResponse are the inventory pages.
type vaultsResponse struct {
	Data    []Vault `json:"data"`
	HasMore bool    `json:"has_more"`
	LastID  string  `json:"last_id"`
}

// FetchVaults lists the workspace vaults as inventory (ANT2-14), refs only.
func (s *Source) FetchVaults(ctx context.Context) ([]Vault, error) {
	if s.client == nil {
		return nil, nil
	}
	var out []Vault
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var resp vaultsResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after_id", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/vaults", q, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// VaultLateralRiskFinding returns the posture finding for a workspace-scoped vault
// (ANT2-14): any agent in the workspace can use its third-party credentials, so it is
// a lateral-movement surface. It carries the vlt_ ref only, never material.
func VaultLateralRiskFinding(v Vault, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityMedium,
		SubjectKind: resVault,
		SubjectRef:  v.ID,
		Title:       "Workspace-scoped credential vault is a lateral-movement surface",
		DetailHash:  redact.Hash("vault " + v.ID + " workspace=" + v.WorkspaceRef + " is workspace-scoped; any agent in the workspace can use its third-party credentials (ANT2-14)"),
		OccurredAt:  at,
	}
}

// ---- Signed webhooks (session.*/vault.*) ------------------------------------------

// WebhookEvent is the parsed body of a Managed-Agents webhook. Only structural fields
// are read; no message content. session.* and vault.* are the documented event families.
type WebhookEvent struct {
	Type         string `json:"type"` // session.completed, vault.credential.archived, …
	SessionID    string `json:"session_id"`
	VaultID      string `json:"vault_id"`
	WorkspaceRef string `json:"workspace_id"`
	CreatedAt    string `json:"created_at"`
}

// VerifyWebhookSignature verifies the X-Webhook-Signature of a Managed-Agents webhook
// (ANT2-14). The signature is the HMAC-SHA256 of the raw request body keyed by the
// whsec_ secret, hex-encoded; an optional "sha256=" prefix is tolerated. The compare
// is constant-time. An empty secret or signature fails closed (never trusts an
// unsigned payload). The exact canonicalization follows the webhooks doc; this models
// the documented HMAC-SHA256 scheme.
func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	sig := strings.TrimSpace(signature)
	sig = strings.TrimPrefix(sig, "sha256=")
	want, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := mac.Sum(nil)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// signWebhook is the test/helper inverse of VerifyWebhookSignature.
func signWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ParseWebhookEvent decodes a webhook body into a WebhookEvent (structural only). It
// returns ok=false on malformed JSON or an unrecognized (non session./vault.) type, so
// a junk payload is dropped rather than mapped.
func ParseWebhookEvent(body []byte) (WebhookEvent, bool) {
	var ev WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return WebhookEvent{}, false
	}
	if !strings.HasPrefix(ev.Type, "session.") && !strings.HasPrefix(ev.Type, "vault.") {
		return WebhookEvent{}, false
	}
	return ev, true
}

// WebhookObservations maps a verified webhook event to observations (ANT2-14): a
// session.* event is an agent-session liveness finding; a vault.* event (credential
// added/archived) is a governance finding on the vault ref. It never carries content.
func WebhookObservations(ev WebhookEvent, at time.Time) []model.Observation {
	when := at
	if t := parseTime(ev.CreatedAt); !t.IsZero() {
		when = t
	}
	switch {
	case strings.HasPrefix(ev.Type, "vault."):
		return []model.Observation{model.FindingReport{
			Kind:        "governance",
			Severity:    model.SeverityInfo,
			SubjectKind: resVault,
			SubjectRef:  ev.VaultID,
			Title:       "Vault webhook: " + ev.Type,
			DetailHash:  redact.Hash("vault event " + ev.Type + " vault=" + ev.VaultID + " ws=" + ev.WorkspaceRef),
			OccurredAt:  when,
		}}
	case strings.HasPrefix(ev.Type, "session."):
		return []model.Observation{model.FindingReport{
			Kind:        "forensic",
			Severity:    model.SeverityInfo,
			SubjectKind: managedAgentSubject,
			SubjectRef:  ev.SessionID,
			Title:       "Managed-agent session webhook: " + ev.Type,
			DetailHash:  redact.Hash("session event " + ev.Type + " session=" + ev.SessionID),
			OccurredAt:  when,
		}}
	default:
		return nil
	}
}

// WebhookHandler returns an http.Handler that verifies the webhook signature and, on
// success, emits the mapped observations to sink (ANT2-14). It is read-first: an
// unsigned or wrongly-signed request is REJECTED with 401 and emits nothing (the
// platform's anti-SSRF auto-disable for private-IP/redirect targets is the platform's
// guard; the receiver's guard is the signature). It is the unit a composition root
// mounts; the connector itself does not own the listener (wiring).
func WebhookHandler(secret string, sink func(model.Observation), now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		if !VerifyWebhookSignature(secret, body, r.Header.Get(webhookSigHeader)) {
			http.Error(w, "invalid signature", http.StatusUnauthorized) // fail closed
			return
		}
		ev, ok := ParseWebhookEvent(body)
		if !ok {
			http.Error(w, "unrecognized event", http.StatusBadRequest)
			return
		}
		at := time.Now().UTC()
		if now != nil {
			at = now()
		}
		for _, o := range WebhookObservations(ev, at) {
			sink(o)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// HITLPendingFinding models the user.tool_confirmation HITL gate (ANT2-14): when a
// managed-agent response stops with stop_reason:requires_action, a human must
// allow/deny a tool call. The connector records the pending approval as a finding; the
// approval UI is. ok is false when the response is not awaiting action.
func (r MessageResponse) HITLPendingFinding(sessionRef string, at time.Time) (model.FindingReport, bool) {
	if r.StopReason != StopRequiresAction {
		return model.FindingReport{}, false
	}
	return model.FindingReport{
		Kind:        "governance",
		Severity:    model.SeverityInfo,
		SubjectKind: managedAgentSubject,
		SubjectRef:  refOrSession(sessionRef, r.ID),
		Title:       "Managed-agent tool call awaiting human confirmation (HITL)",
		DetailHash:  redact.Hash(fmt.Sprintf("stop_reason=requires_action session=%s model=%s; user.tool_confirmation pending (ANT2-14)", sessionRef, r.Model)),
		OccurredAt:  at,
	}, true
}
