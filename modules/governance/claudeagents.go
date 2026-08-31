// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// AgentsConsole is the D Managed-Agents tool-confirmation backend the console
// calls at /v1/m/claude-agents/sessions/{id}/{events,tool-confirmation}. It serves the
// thread events that carry the concrete tool to confirm, and emits the human
// user.tool_confirmation decision — a privileged, audited action BOUND to the
// approval state machine (it consumes approvals.go's entities; it does not rewrite the
// machine). MINIMAL DATA (docs/SECURITY-HARDENING.md): the decision is recorded against a REDACTED
// fingerprint of (session, tool_use_id) — never the tool payload.
type AgentsConsole struct {
	log     *slog.Logger
	data    api.ModuleData
	clock   model.Clock
	threads ThreadEventProvider
}

var (
	_ sdk.Module       = (*AgentsConsole)(nil)
	_ api.Module       = (*AgentsConsole)(nil)
	_ api.DataConsumer = (*AgentsConsole)(nil)
)

// AgentsConsoleNamespace mounts the console at /v1/m/claude-agents/.
const AgentsConsoleNamespace = "claude-agents"

// subjMgdAgent is the approval subject_kind for a managed-agent tool confirmation
// (the same subject the REAL pending-confirmation findings carry).
const subjMgdAgent = "anthropic.managed_agent"

// actionToolUse is the approval action for a tool confirmation.
const actionToolUse = "tool_use"

// ThreadEvent is one event of a managed-agent thread carrying the tool to confirm.
//
// HONESTY (contract:85): the "primary thread only sees sub-agent start/end, so the
// tool detail lives on the per-thread path" detail is CONNECTOR-MODELED, NOT an
// Anthropic-documented guarantee. The live source is the claude-managed-agents
// read API (a dedicated request-time reader wired by the composition root —);
// this console serves that signal — it never fabricates events.
type ThreadEvent struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	AgentRef  string `json:"agent_ref,omitempty"`
	PeerRef   string `json:"peer_ref,omitempty"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ThreadEventProvider supplies a session's thread events. The triple return keeps
// every absence honest: nil provider or ok=false ⇒ no live source is wired for
// this tenant (an honest empty list, never fabricated events); err != nil ⇒ a
// WIRED source could not answer (the console reports 502 — an upstream outage
// must never read as "no events").
type ThreadEventProvider interface {
	ThreadEvents(ctx context.Context, tenant model.TenantID, sessionID string) ([]ThreadEvent, bool, error)
}

// AgentsConsoleOption configures an AgentsConsole.
type AgentsConsoleOption func(*AgentsConsole)

// WithThreadEventProvider wires the thread-event source.
func WithThreadEventProvider(p ThreadEventProvider) AgentsConsoleOption {
	return func(c *AgentsConsole) { c.threads = p }
}

// WithAgentsConsoleClock overrides the clock (tests inject a deterministic clock).
func WithAgentsConsoleClock(clk model.Clock) AgentsConsoleOption {
	return func(c *AgentsConsole) { c.clock = clk }
}

// NewAgentsConsole constructs the Managed-Agents tool-confirmation console.
func NewAgentsConsole(opts ...AgentsConsoleOption) *AgentsConsole {
	c := &AgentsConsole{clock: model.SystemClock{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *AgentsConsole) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: "olivares.claude-agents", Version: "0.1.0", APIVersion: sdk.APIVersion,
		Type: sdk.TypeModule, Title: "Managed-Agents tool confirmation (HITL)",
		Description: "Serves managed-agent thread events and emits the human user.tool_confirmation decision, bound to the approval state machine and audited with a redacted fingerprint (never the tool payload).",
	}
}

func (c *AgentsConsole) UseData(d api.ModuleData) { c.data = d }
func (c *AgentsConsole) Init(_ context.Context, host sdk.Host) error {
	c.log = host.Logger()
	return nil
}
func (c *AgentsConsole) Start(context.Context) error { return nil }
func (c *AgentsConsole) Stop(context.Context) error  { return nil }
func (c *AgentsConsole) APINamespace() string        { return AgentsConsoleNamespace }

// Permissions: read gates viewing the thread events; admin gates the confirmation.
func (c *AgentsConsole) Permissions() []auth.Permission {
	return []auth.Permission{permApprovalRead, permApprovalAdmin}
}

func (c *AgentsConsole) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/sessions/{id}/events", permApprovalRead, c.handleThreadEvents)
	reg.Handle("POST", "/sessions/{id}/tool-confirmation", permApprovalAdmin, c.handleToolConfirmation)
}

// handleThreadEvents serves a session's thread events from the wired events
// reader. With no live source wired it returns an honest empty list (the HITL
// queue's pending confirmations come from the REAL findings stream); a wired
// source that cannot answer is a 502 — an upstream outage must never read as "no
// events". It never fabricates events.
func (c *AgentsConsole) handleThreadEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session id is required"))
		return
	}
	out := listResponse[ThreadEvent]{Items: []ThreadEvent{}}
	if c.threads != nil {
		evs, ok, err := c.threads.ThreadEvents(r.Context(), mc.Tenant, sessionID)
		if err != nil {
			if c.log != nil {
				c.log.Warn("claude-agents: thread-event source failed", "err", err)
			}
			writeJSON(w, http.StatusBadGateway, errorBody("the thread-event source could not answer — events are UNKNOWN right now, not absent"))
			return
		}
		if ok {
			out.Items = append(out.Items, evs...)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// toolConfirmationInput is the human decision on a managed-agent tool use.
type toolConfirmationInput struct {
	ToolUseID   string `json:"tool_use_id"`
	Result      string `json:"result"` // allow | deny
	DenyMessage string `json:"deny_message,omitempty"`
}

// handleToolConfirmation emits user.tool_confirmation, bound to the approval state
// machine and AUDITED with a redacted fingerprint. Admin tier. A stable user identity is
// required (a system token cannot confirm). DENY-CLOSED: any validation/persist/audit
// error records NOTHING and returns an explicit failure.
func (c *AgentsConsole) handleToolConfirmation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	sessionID := strings.TrimSpace(chi.URLParam(r, "id"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("session id is required"))
		return
	}
	var in toolConfirmationInput
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ToolUseID = strings.TrimSpace(in.ToolUseID)
	result := strings.ToLower(strings.TrimSpace(in.Result))
	if in.ToolUseID == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("tool_use_id is required"))
		return
	}
	if result != "allow" && result != "deny" {
		writeJSON(w, http.StatusBadRequest, errorBody("result must be allow or deny"))
		return
	}
	if len(in.DenyMessage) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("deny_message too long"))
		return
	}
	if containsInlineCredential(in.DenyMessage) {
		writeJSON(w, http.StatusBadRequest, errorBody("deny_message must not contain a credential"))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to confirm a tool; a system token cannot confirm"))
		return
	}

	// Redacted fingerprint of (session, tool_use_id) — the binding the decision is
	// recorded against. The raw session/tool payload NEVER reaches the store (docs/SECURITY-HARDENING.md).
	fp := toolFingerprint(sessionID, in.ToolUseID)
	deciderUser := mc.Principal.UserID.String()
	decision := decisionApprove
	if result == "deny" {
		decision = decisionReject
	}
	now := c.clock.Now()

	var (
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		appRepo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}
		decRepo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		// Bind to an existing pending managed-agent approval for this fingerprint, or
		// open one (requested by the connector, so separation-of-duty never blocks the
		// human decider). required_approvals = 1 (a single human confirmation).
		rec, found, err := findOne(r.Context(), appRepo, eq(colSubjectKind, subjMgdAgent), eq(colSubjectRef, fp))
		if err != nil {
			return err
		}
		var approvalID model.ID
		if found {
			if eff := effectiveStatus(rec, now); eff != statusPending {
				clientErr, clientCode = "this tool confirmation is already "+eff, http.StatusConflict
				return nil
			}
			approvalID = model.ID(rec.String(model.ColID))
		} else {
			created, e := appRepo.Create(r.Context(), model.Record{
				colSubjectKind: subjMgdAgent, colSubjectRef: fp, colAction: actionToolUse,
				colRequestedBy: "connector:olivares.managed-agents", colRequestedByUser: "",
				colStatus: statusPending, colRequiredApproval: int64(1),
				colApproveCount: int64(0), colRejectCount: int64(0),
			})
			if e != nil {
				return e
			}
			rec = created
			approvalID = model.ID(created.String(model.ColID))
		}
		// Append the immutable decision (the append-only trail backs the user.tool_confirmation).
		if _, e := decRepo.Create(r.Context(), model.Record{
			colApprovalID: approvalID.String(), colDecision: decision,
			colDecider: mc.Principal.Actor(), colDeciderUser: deciderUser,
			colNote: in.DenyMessage, colDecidedAt: now.String(),
		}); e != nil {
			return e
		}
		// Resolve the approval terminally.
		newStatus := statusApproved
		rec[colApproveCount] = int64(1)
		if decision == decisionReject {
			newStatus, rec[colApproveCount], rec[colRejectCount] = statusRejected, int64(0), int64(1)
		}
		rec[colStatus], rec[colDecidedAt] = newStatus, now.String()
		if _, e := appRepo.Update(r.Context(), rec); e != nil {
			return e
		}
		// Self-audit with the REDACTED fingerprint — never the payload (docs/SECURITY-HARDENING.md).
		return auditEvent(r.Context(), sc, mc, "governance.agent.tool_confirmation", approvalKind, approvalID, map[string]any{
			"result": result, "tool_hash": fp,
		})
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result, "tool_hash": fp})
}

// toolFingerprint is the redacted SHA-256 of (session|tool_use_id): the binding the
// confirmation is recorded against, so the raw session/tool payload never reaches the
// store (the module cannot import connectors/internal/redact, so it hashes directly).
func toolFingerprint(sessionID, toolUseID string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + toolUseID))
	return hex.EncodeToString(sum[:])
}
