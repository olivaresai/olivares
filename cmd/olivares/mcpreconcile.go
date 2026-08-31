// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

// mcpProtocolBindingReconciler is the MCP branch of the protocol-neutral REST
// reconcile surface. It performs one authenticated tasks/get through the real
// composed upstream and commits through the durable task adapter, so REST and
// the MCP Resource Server share one lifecycle authority.
type mcpProtocolBindingReconciler struct {
	store    *mcpDurableTaskStore
	upstream mcpc.Upstream
	now      func() time.Time
}

var _ sessions.ProtocolBindingRemoteReconciler = (*mcpProtocolBindingReconciler)(nil)

func newMCPProtocolBindingReconciler(
	store *mcpDurableTaskStore,
	upstream mcpc.Upstream,
) (*mcpProtocolBindingReconciler, error) {
	if store == nil || upstream == nil {
		return nil, fmt.Errorf("mcp protocol binding reconcile: durable store or upstream is unavailable")
	}
	return &mcpProtocolBindingReconciler{store: store, upstream: upstream, now: time.Now}, nil
}

func (r *mcpProtocolBindingReconciler) TestProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	return r.reconcile(ctx, tenant, request, false)
}

func (r *mcpProtocolBindingReconciler) ReconcileProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	return r.reconcile(ctx, tenant, request, true)
}

func (r *mcpProtocolBindingReconciler) reconcile(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
	apply bool,
) (sessions.ProtocolBindingReconcileResult, error) {
	if r == nil || r.store == nil || r.upstream == nil || tenant != r.store.tenant ||
		tenant.IsZero() || tenant.IsSystem() || request.Binding.ID.IsZero() ||
		request.Binding.Protocol != sessions.BindingProtocolMCP || request.ExpectedVersion < 1 ||
		request.Binding.Version != request.ExpectedVersion ||
		!validProtocolReconcileHash(request.ExpectedPlanHash) ||
		strings.TrimSpace(request.SemanticKey) == "" || len(request.SemanticKey) > 1024 {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"mcp protocol binding reconcile: incomplete request",
		)
	}
	current, err := r.store.exactReconcileBinding(ctx, request.Binding.ID)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	if !sameProtocolReconcileAnchor(request.Binding, current) ||
		current.Version != request.ExpectedVersion {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"mcp protocol binding reconcile: binding changed after its REST plan",
		)
	}
	view, err := r.store.bindingView(current)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	if current.Terminal {
		return protocolReconcileResult(
			nil, current, current.ObservationVerdict, current.ObservationCode,
			mcpProtocolBindingObservedAt(current, r.clock()), false,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "durable_binding", Verdict: current.ObservationVerdict,
				EvidenceRef: current.ID.String(),
			}},
		), nil
	}

	params, err := mcpc.BuildDurableTaskGetParams(current.ExternalID, current.ProtocolVersion)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	operationID := workflowSemanticID(
		"mcp-protocol-binding-reconcile",
		request.SemanticKey+"\x00"+current.ID.String()+"\x00"+strconv.FormatInt(current.Generation, 10),
	)
	scopes := []string(nil)
	if scope := strings.TrimSpace(view.Intent.RequiredScope); scope != "" {
		scopes = []string{scope}
	}
	fence := ""
	if current.LeaseFence > 0 {
		fence = strconv.FormatInt(current.LeaseFence, 10)
	}
	observedAt := r.clock()
	peerResult, peerErr := r.upstream.Forward(ctx, mcpc.UpstreamRequest{
		Method: "tasks/get", Params: params,
		Subject: view.Intent.Owner.Subject, Scopes: scopes,
		OperationID: sdk.OperationID(operationID), FenceToken: fence,
	})
	if peerErr != nil || peerResult.State != mcpc.DispatchCompleted {
		return r.finishObservation(ctx, current, view.Intent.Owner, apply,
			mcpc.DurableTaskObservation{
				TaskID: current.ExternalID, Generation: current.Generation,
				Kind: mcpc.DurableTaskObservationGet, Status: current.RemoteState,
				StatusReason: "upstream_unavailable",
				Verdict:      mcpc.DurableTaskVerdictUnobservable,
				ObservedAt:   observedAt, OperationID: operationID,
				Dispatched: peerResult.State != mcpc.DispatchNotSent &&
					peerResult.State != mcpc.DispatchBlocked,
			},
			remoteDigestHex(
				"olivares.mcp.rest-reconcile.unavailable.v1", current.ID.String(),
			),
		)
	}

	report, reportErr := mcpc.ParseDurableTaskGetResult(current.ExternalID, peerResult.Result)
	if reportErr != nil {
		class := mcpc.DurableTaskResultDefectClass(reportErr)
		return r.finishObservation(ctx, current, view.Intent.Owner, apply,
			mcpc.DurableTaskObservation{
				TaskID: current.ExternalID, Generation: current.Generation,
				Kind: mcpc.DurableTaskObservationGet, Status: current.RemoteState,
				StatusReason: class, Verdict: mcpc.DurableTaskVerdictBroken,
				ObservedAt: observedAt, OperationID: operationID, Dispatched: true,
			},
			remoteDigestHex(
				"olivares.mcp.rest-reconcile.defect.v1", current.ID.String(), class,
			),
		)
	}
	return r.finishObservation(ctx, current, view.Intent.Owner, apply,
		mcpc.DurableTaskObservation{
			TaskID: current.ExternalID, Generation: current.Generation,
			Kind: mcpc.DurableTaskObservationGet, Status: report.Status,
			StatusReason: mcpProtocolBindingStatusReason(report.StatusReason),
			TTLMs:        report.TTLMs, PollIntervalMs: report.PollIntervalMs,
			Verdict: mcpc.DurableTaskVerdictClean, ObservedAt: observedAt,
			ResultDigest: report.ResultDigest, OperationID: operationID,
			Dispatched: true, Terminal: report.Terminal,
			InputRequests: report.InputRequests,
		},
		report.ResultDigest,
	)
}

func (r *mcpProtocolBindingReconciler) finishObservation(
	ctx context.Context,
	before sessions.ProtocolBinding,
	owner mcpc.TaskOwner,
	apply bool,
	observation mcpc.DurableTaskObservation,
	evidenceRef string,
) (sessions.ProtocolBindingReconcileResult, error) {
	verdict, err := mcpObservationVerdict(observation.Verdict)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	const code = "mcp_get"
	if !apply {
		return protocolReconcileResult(
			nil, before, verdict, code, observation.ObservedAt, false,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "peer_lifecycle", Verdict: verdict, EvidenceRef: evidenceRef,
			}},
		), nil
	}
	if err := r.store.UpdateObservation(ctx, owner, observation); err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	updated, err := r.store.exactReconcileBinding(ctx, before.ID)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	if updated.ID != before.ID || updated.Generation != before.Generation ||
		updated.Version < before.Version || updated.ObservationVerdict != verdict ||
		updated.ObservationCode != code || updated.LastObservedAt == nil {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"mcp protocol binding reconcile: committed observation is inconsistent",
		)
	}
	return protocolReconcileResult(
		nil, updated, updated.ObservationVerdict, updated.ObservationCode,
		updated.LastObservedAt.UTC(), updated.Replayed,
		[]sessions.ProtocolBindingRemoteCheck{{
			Name: "peer_lifecycle", Verdict: updated.ObservationVerdict,
			EvidenceRef: evidenceRef,
		}},
	), nil
}

func (r *mcpProtocolBindingReconciler) clock() time.Time {
	if r != nil && r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

func mcpProtocolBindingObservedAt(binding sessions.ProtocolBinding, fallback time.Time) time.Time {
	if binding.LastObservedAt != nil {
		return binding.LastObservedAt.UTC()
	}
	if !binding.UpdatedAt.IsZero() {
		return binding.UpdatedAt.UTC()
	}
	return fallback.UTC()
}

func mcpProtocolBindingStatusReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4096 {
		return ""
	}
	return value
}
