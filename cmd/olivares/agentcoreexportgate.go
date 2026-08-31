// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/connectors/agentcore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// agentcoreexportgate.go wires the governed AgentCore exporter's HITL seam
// (agentcore.ExportGate) to the approval bridge, mirroring
// adminactiongate.go. Normal create/update plans are recoverable and use gateOnce
// (break-glass allowed); weakening plans (delete or ACTIVE→LOG_ONLY downgrade) use
// gateOnceNoBreakGlass and the default CRITICAL action classification, so the
// engine applies the AC-3(2) two-person floor and the connector re-verifies the
// distinct approvers.

const (
	agentCoreExportCapApply          = "agentcore.export.apply"
	agentCoreExportCapApplyWeakening = "agentcore.export.apply_weakening"
)

type agentCoreExportApprovalAdapter struct {
	b       *approvalBridge
	tenant  model.TenantID
	mu      sync.Mutex
	pending map[string]string // plan hash -> approval ref
}

var _ agentcore.ExportGate = (*agentCoreExportApprovalAdapter)(nil)

func (a *agentCoreExportApprovalAdapter) Authorize(ctx context.Context, req agentcore.ExportGateRequest) (agentcore.ExportGateDecision, error) {
	if a == nil || a.b == nil {
		return agentCoreExportNoGate(req.PlanHash), nil
	}
	tid, present, err := parseBusinessTenant("agentcore export request: tenant", req.Tenant)
	if err != nil || !present || tid != a.tenant {
		return agentCoreExportNoGate(req.PlanHash), nil
	}
	capability := agentCoreExportCapApply
	reason := "governed AgentCore Cedar export apply"
	if req.Weakens {
		capability = agentCoreExportCapApplyWeakening
		reason = "governed AgentCore Cedar export apply (weakens remote enforcement)"
	}
	subjectKind := "agentcore.policy_engine"
	subjectRef := strings.TrimSpace(req.EngineID)
	var ref, status, boundHash string
	if req.Weakens {
		ref, status, boundHash, err = a.b.gateOnceNoBreakGlass(ctx, tid, capability, subjectKind, subjectRef, req.PlanHash, reason, req.RequestedBy)
	} else {
		ref, status, boundHash, err = a.b.gateOnce(ctx, tid, capability, subjectKind, subjectRef, req.PlanHash, reason, req.RequestedBy)
	}
	if err != nil {
		return agentcore.ExportGateDecision{}, err
	}
	a.rememberPending(boundHash, ref, status)
	dec := agentcore.ExportGateDecision{ApprovalRef: ref, Status: agentCoreExportStatusOf(status), PlanHash: boundHash}
	if status == nbApproved && req.Weakens {
		// Credentials for provenance, distinct PEOPLE for the quorum the connector
		// re-checks; a read failure degrades to zero of both (deny-closed).
		if cred, ok := a.b.cred(tid); ok {
			ev := a.b.approvalApproverEvidence(ctx, cred, ref)
			dec.Approvers, dec.ApproverPersons = ev.Actors, ev.Persons
		}
	}
	return dec, nil
}

func (a *agentCoreExportApprovalAdapter) rememberPending(planHash, ref, status string) {
	if a == nil || planHash == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]string{}
	}
	if status == nbPending {
		a.pending[planHash] = ref
		return
	}
	delete(a.pending, planHash)
}

func (a *agentCoreExportApprovalAdapter) pendingRef(planHash string) string {
	if a == nil || planHash == "" {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pending[planHash]
}

func agentCoreExportNoGate(planHash string) agentcore.ExportGateDecision {
	return agentcore.ExportGateDecision{ApprovalRef: noGateRefPrefix + planHash, Status: agentcore.ExportNoGate, PlanHash: planHash}
}

func agentCoreExportStatusOf(neutral string) agentcore.ExportGateStatus {
	switch neutral {
	case nbApproved, nbBreakGlass:
		return agentcore.ExportApproved
	case nbPending:
		return agentcore.ExportPending
	case nbRejected, nbCanceled:
		return agentcore.ExportRejected
	case nbExpired:
		return agentcore.ExportExpired
	default:
		return agentcore.ExportNoGate
	}
}

type agentCoreExportRuntime struct {
	exporter *agentcore.Exporter
	gate     *agentCoreExportApprovalAdapter
}

var _ governance.AgentCoreExporter = (*agentCoreExportRuntime)(nil)

func (r *agentCoreExportRuntime) Plan(ctx context.Context, engineID, tenant string, desired []agentcore.RenderedPolicy) (agentcore.ExportPlan, error) {
	return r.exporter.Plan(ctx, engineID, tenant, desired)
}

func (r *agentCoreExportRuntime) Apply(ctx context.Context, plan agentcore.ExportPlan, spec agentcore.ExportSpec) ([]agentcore.ExportResult, error) {
	results, err := r.exporter.Apply(ctx, plan, spec)
	if err == nil {
		return results, nil
	}
	var deny *agentcore.ExportDenyError
	if !errors.As(err, &deny) || !strings.Contains(deny.Reason, "export not approved by governance (pending)") {
		return results, err
	}
	if ref := r.gate.pendingRef(deny.PlanHash); ref != "" {
		return results, &governance.AgentCoreExportPendingError{Err: deny, ApprovalRef: ref}
	}
	return results, err
}
