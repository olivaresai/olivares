// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package mcpgateway

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/cmd/olivares/internal/pepkit"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// GateAuditor is the evidence seam adapter of the MCP gateway: it backs the
// connector's GateAuditor with the durable evidence operation journal
// (store.ClaimEvidenceOperation / SettleEvidenceOperation) for the ENFORCED
// surfaces, and keeps the historical best-effort ledger anchor for denials and the
// not-yet-enforced legacy surfaces (zero binding — stages 4-6).
type GateAuditor struct {
	Log    *slog.Logger
	Store  store.Store    // journal + ledger; nil ⇒ enforced allows REFUSE (ledger_unwired)
	Tenant model.TenantID // the RS's single tenant (the enforcement anchor)
}

const (
	mcpDecisionDomain = "olivares.mcp.tool.decision.v1"
	// mcpGatewaySurface names this PEP surface in the evidence operation journal.
	mcpGatewaySurface = "mcp.gateway"
)

// mcpToolCallAction is the journal action verb of one enforced tools/call,
// labeled with the OperationID provenance (design §3: a request_instance entry
// must never read as client-keyed idempotency). The paired ledger events are
// "<action>.claim" / "<action>.settle".
func mcpToolCallAction(idKind string) string {
	switch idKind {
	case "keyed", "request_instance":
		return "mcp.tool.call." + idKind
	default:
		return "mcp.tool.call"
	}
}

// mcpEffectAction resolves the journal action of one enforced claim: the
// connector-supplied EffectAction when present (stage 4 — the task
// surface: mcp.task.get.<kind> / mcp.task.cancel.<kind> / mcp.task.update.<kind>
// / mcp.task.track / mcp.task.cancel.compensation / mcp.task.cancel.sweep),
// else the historical tools/call action. ADDITIVE: an empty EffectAction keeps
// the existing mcp.tool.call.* events byte-identical.
func mcpEffectAction(d mcpc.ToolDecision) string {
	if action := strings.TrimSpace(d.EffectAction); action != "" {
		return action
	}
	return mcpToolCallAction(d.OperationIDKind)
}

// refusedMCPGateRecord builds the refused GateRecord for a fault.
func refusedMCPGateRecord(binding sdk.EvidenceBinding, fault sdk.EvidenceFault, failure sdk.FailureClass) mcpc.GateRecord {
	return mcpc.GateRecord{
		Binding: binding,
		Receipt: sdk.EvidenceReceipt{
			OperationID: binding.OperationID, EffectDigest: binding.EffectDigest, Fault: fault,
		},
		State:        mcpc.GateRecordRefused,
		FailureClass: failure,
	}
}

// enforcedTenant resolves the tenant an ENFORCED operation is journaled under: the
// configured RS tenant, with the decision tenant accepted only when EMPTY (fallback)
// or exactly equal. A malformed or mismatched non-empty decision tenant refuses
// (the design flagged the historical silent fallback as a defect: evidence
// must never be silently attributed to a tenant the decision did not name).
func (a GateAuditor) enforcedTenant(decisionTenant string) (model.TenantID, bool) {
	if a.Tenant.IsZero() {
		return "", false
	}
	tid, present, err := pepkit.ParseBusinessTenant("mcp decision tenant", decisionTenant)
	if err != nil {
		return "", false
	}
	if !present {
		return a.Tenant, true
	}
	if tid != a.Tenant {
		return "", false
	}
	return a.Tenant, true
}

func (a GateAuditor) Record(ctx context.Context, d mcpc.ToolDecision, binding sdk.EvidenceBinding) mcpc.GateRecord {
	a.Log.Info("mcp-gateway: tools/call decision",
		"tool", d.Tool, "subject", d.Subject, "allowed", d.Allowed, "reason", d.Reason,
		"required_scope", d.RequiredScope, "approval_ref", d.ApprovalRef, "task_id", d.TaskID, "mcp", d.MCPTag,
		"policy_decision", string(d.Decision), "policy_id", d.RuleID, "server_name", d.Server, "client_id", d.ClientID,
		"grant_mode", d.GrantMode,
		// SEP-414: the W3C trace id correlating this PEP decision with the
		// gen_ai spans of the same trace (an identifier, never a payload).
		"traceparent", d.TraceParent)

	if !d.Allowed || !binding.Valid() {
		// Denials (best-effort by doctrine — a policy deny NEVER depends on
		// evidence success) and the zero-binding legacy surfaces (stages 4-6).
		a.bestEffortAnchor(ctx, d)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultLedgerUnwired, sdk.FailureEvidenceFault)
	}

	// ENFORCED allow: claim the operation single-use + anchor the evidence in one
	// journal transaction. Every refusal below blocks the effect (deny-closed).
	tenant, ok := a.enforcedTenant(d.Tenant)
	if !ok {
		a.Log.Error("mcp-gateway: decision tenant unresolved for enforced tools/call; effect refused",
			"tool", d.Tool, "subject", d.Subject, "decision_tenant", d.Tenant)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultTenantUnresolved, sdk.FailureEvidenceFault)
	}
	actorKind := model.ActorSystem
	if strings.TrimSpace(d.Subject) != "" {
		actorKind = model.ActorAgent
	}
	outcome, err := store.ClaimEvidenceOperation(ctx, a.Store, tenant, store.EvidenceClaim{
		OperationID:  string(binding.OperationID),
		EffectDigest: string(binding.EffectDigest),
		Surface:      mcpGatewaySurface,
		Action:       mcpEffectAction(d),
		Actor:        pepkit.FirstNonEmpty(d.Subject, model.ActorSystem),
		ActorKind:    actorKind,
	})
	switch {
	case errors.Is(err, store.ErrEvidenceRebind):
		// Same OperationID, different EffectDigest: the single-use claim is bound
		// to another effect (sdk.FailureReplay — the 409/-31011 wire shape).
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultWriteError, sdk.FailureReplay)
	case err != nil:
		a.Log.Error("mcp-gateway: evidence claim failed; effect refused", "tool", d.Tool, "err", err)
		return refusedMCPGateRecord(binding, sdk.EvidenceFaultWriteError, sdk.FailureEvidenceFault)
	case outcome.Receipt.MustRefuse(binding):
		// Evidence fault (unwired/unavailable/spool/degrade/...): the specific
		// fault stays server-side; the caller answers 503/-31010.
		a.Log.Error("mcp-gateway: evidence claim not anchored; effect refused",
			"tool", d.Tool, "fault", string(outcome.Receipt.Fault))
		return mcpc.GateRecord{
			Binding: binding, Receipt: outcome.Receipt,
			State: mcpc.GateRecordRefused, FailureClass: sdk.FailureEvidenceFault,
		}
	case outcome.Fresh:
		return mcpc.GateRecord{
			Binding: binding, Receipt: outcome.Receipt, State: mcpc.GateRecordFresh,
			// The claim's durable leadership epoch is the fence token BeforeEffect
			// re-verifies immediately before dispatch (opaque to the connector).
			FenceToken: strconv.FormatUint(outcome.Op.LeaderEpoch, 10),
		}
	default:
		// Exact replay (same operation, same digest): return the recorded state,
		// never a second effect.
		rec := mcpc.GateRecord{Binding: binding, Receipt: outcome.Receipt, State: mcpc.GateRecordReplayPending}
		if outcome.Op.State.Terminal() {
			rec.State = mcpc.GateRecordReplaySettled
			rec.Recorded = &mcpc.RecordedOutcome{
				State:        mcpc.DispatchState(outcome.Op.State),
				ResultDigest: outcome.Op.ResultDigest,
				OutcomeRef:   outcome.Op.OutcomeEvidenceRef,
			}
		}
		return rec
	}
}

// BeforeEffect re-verifies the claim's durable leadership fence IMMEDIATELY before
// the upstream dispatch (store.EvidenceEpochFence: held lock session + persisted
// epoch). Any failure refuses; the claim stays claimed and is never re-dispatched.
func (a GateAuditor) BeforeEffect(ctx context.Context, rec mcpc.GateRecord) sdk.EvidenceReceipt {
	refuse := func(fault sdk.EvidenceFault) sdk.EvidenceReceipt {
		return sdk.EvidenceReceipt{
			OperationID: rec.Binding.OperationID, EffectDigest: rec.Binding.EffectDigest, Fault: fault,
		}
	}
	if a.Store == nil {
		return refuse(sdk.EvidenceFaultLedgerUnwired)
	}
	epoch, err := strconv.ParseUint(strings.TrimSpace(rec.FenceToken), 10, 64)
	if err != nil {
		// A fresh record always carries the claim's epoch; a malformed token is a
		// caller bug and fails closed, never open.
		return refuse(sdk.EvidenceFaultWriteError)
	}
	if err := store.EvidenceEpochFence(ctx, a.Store.Leader(), epoch); err != nil {
		a.Log.Error("mcp-gateway: pre-effect leadership fence refused the dispatch", "err", err)
		return refuse(sdk.EvidenceFaultLedgerUnavailable)
	}
	return rec.Receipt
}

// Settle durably records the dispatch outcome against the claim. A refusing
// settlement means the outcome did NOT commit — the caller withholds the response
// and the operation remains claimed/ambiguous (status replay only).
func (a GateAuditor) Settle(ctx context.Context, out mcpc.GateOutcome) mcpc.GateSettlement {
	refused := mcpc.GateSettlement{FailureClass: sdk.FailureEvidenceFault}
	if a.Store == nil || a.Tenant.IsZero() {
		return refused
	}
	outcome, err := store.SettleEvidenceOperation(ctx, a.Store, a.Tenant, store.EvidenceSettlement{
		OperationID:  string(out.Record.Binding.OperationID),
		EffectDigest: string(out.Record.Binding.EffectDigest),
		State:        model.EvidenceOperationState(out.State),
		ResultDigest: out.ResultDigest,
		DispatchRef:  out.DispatchRef,
		// The settlement is the GATEWAY's observation of the outcome, not a
		// subject action: system attribution.
		Actor:     model.ActorSystem,
		ActorKind: model.ActorSystem,
	})
	if err != nil {
		a.Log.Error("mcp-gateway: evidence settlement failed; response withheld",
			"operation_state", string(out.State), "err", err)
		return refused
	}
	if outcome.Receipt.MustRefuse(out.Record.Binding) {
		a.Log.Error("mcp-gateway: evidence settlement not anchored; response withheld",
			"operation_state", string(out.State), "fault", string(outcome.Receipt.Fault))
		return refused
	}
	return mcpc.GateSettlement{
		Outcome: mcpc.RecordedOutcome{
			State:        out.State,
			ResultDigest: outcome.Op.ResultDigest,
			OutcomeRef:   outcome.Op.OutcomeEvidenceRef,
		},
		EvidenceRef: outcome.Receipt.EvidenceRef,
	}
}

// bestEffortAnchor is the historical decision anchor, now serving denials and
// the zero-binding legacy surfaces only (evidence-or-loud-gap; the enforced
// tools/call path journals claim/settle events instead). Carries no raw arguments
// or tokens (docs/SECURITY-HARDENING.md).
func (a GateAuditor) bestEffortAnchor(ctx context.Context, d mcpc.ToolDecision) {
	decision := "deny"
	if d.Allowed {
		decision = "allow"
	}
	tenant := a.Tenant
	// Tenant-fallback fix: a MALFORMED non-empty decision tenant is never silently
	// re-attributed to the configured tenant — loud gap. Routes it through the
	// shared policy, which also rejects the reserved system tenant: this branch is
	// reached WITHOUT going through enforcedTenant (see Record), so this is the only
	// check on the path, and anchoring a business decision under the system tenant
	// would file the evidence outside every business boundary.
	tid, present, terr := pepkit.ParseBusinessTenant("mcp decision tenant", d.Tenant)
	if terr != nil {
		// The wording is the operator-facing contract (asserted by
		// TestMCPEvidenceTenantResolution and greppable in logs); the precise reason —
		// unparseable, unset, or the reserved system tenant — rides in `err`.
		a.Log.Error("mcp-gateway: malformed decision tenant; decision NOT anchored (evidence gap)",
			"tool", d.Tool, "subject", d.Subject, "decision_tenant", d.Tenant, "err", terr)
		return
	}
	if present {
		tenant = tid
	}
	if a.Store == nil || tenant.IsZero() {
		a.Log.Error("mcp-gateway: no ledger store/tenant; decision NOT anchored (evidence gap)", "tool", d.Tool, "subject", d.Subject)
		return
	}
	actorKind := model.ActorSystem
	if strings.TrimSpace(d.Subject) != "" {
		actorKind = model.ActorAgent
	}
	ph := DecisionHash(tenant.String(), d.Subject, d.Tool, d.RequiredScope, decision, d.ApprovalRef, d.TaskID, d.MCPTag, d.TokenBinding)
	meta := map[string]any{
		"tool": d.Tool, "subject": d.Subject, "allowed": d.Allowed, "reason": d.Reason,
		"required_scope": d.RequiredScope, "approval_ref": d.ApprovalRef, "task_id": d.TaskID,
		"mcp": d.MCPTag, "token_binding": d.TokenBinding, "decision": decision,
	}
	pepkit.AddDelegationMeta(meta, d.IsDelegated, d.ActAs)
	// The decision-table row, named after docker/mcp-gateway's AuditEvent fields.
	// "decision" above keeps its published allow|deny meaning.
	for key, value := range map[string]string{
		"policy_decision": string(d.Decision), "policy_id": d.RuleID,
		"server_name": d.Server, "client_id": d.ClientID,
	} {
		if value != "" {
			meta[key] = value
		}
	}
	if !d.Allowed {
		// A refused call still needs its terminal evidence after disconnect,
		// interrupt or stop. Detach only this bounded ledger write, never an
		// authorization or effect; keep the caller's scope values.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), pepkit.ReanchorTimeout)
		defer cancel()
	}
	err := a.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		ev, aerr := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:       pepkit.FirstNonEmpty(d.Subject, model.ActorSystem),
			ActorKind:   actorKind,
			Action:      "mcp.tool." + decision,
			TargetKind:  "mcp.tool",
			TargetID:    model.ID(d.Tool),
			PayloadHash: ph,
			Meta:        meta,
		})
		if aerr == nil && ev.Seq == 0 {
			a.Log.Error("mcp-gateway: decision evidence dropped by degrade spool (evidence gap)", "tool", d.Tool)
		}
		return aerr
	})
	if err != nil {
		a.Log.Error("mcp-gateway: ledger anchor failed (evidence gap)", "tool", d.Tool, "subject", d.Subject, "err", err)
	}
}

func DecisionHash(tenant, subject, tool, requiredScope, decision, approvalRef, taskID, mcpTag, tokenBinding string) []byte {
	h := sha256.New()
	pepkit.WriteLenPrefixed(h, []byte(mcpDecisionDomain))
	pepkit.WriteLenPrefixed(h, []byte(tenant))
	pepkit.WriteLenPrefixed(h, []byte(subject))
	pepkit.WriteLenPrefixed(h, []byte(tool))
	pepkit.WriteLenPrefixed(h, []byte(requiredScope))
	pepkit.WriteLenPrefixed(h, []byte(decision))
	pepkit.WriteLenPrefixed(h, []byte(approvalRef))
	pepkit.WriteLenPrefixed(h, []byte(taskID))
	pepkit.WriteLenPrefixed(h, []byte(mcpTag))
	pepkit.WriteLenPrefixed(h, []byte(tokenBinding))
	return h.Sum(nil)
}
