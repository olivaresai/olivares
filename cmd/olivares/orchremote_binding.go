// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

var _ sessions.ProtocolBindingRemoteReconciler = (*orchRemoteExecutor)(nil)

// TestProtocolBinding performs the same authenticated A2A observation as the
// workflow executor without changing the durable binding or its WorkItem. The
// REST boundary returns this evidence as a test projection only.
func (e *orchRemoteExecutor) TestProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	return e.reconcileProtocolBindingRequest(ctx, tenant, request, false)
}

// ReconcileProtocolBinding observes and commits one exact binding generation.
// The current row is re-read before the peer call and its version is then used
// by ObserveProtocolBinding as the post-read CAS.
func (e *orchRemoteExecutor) ReconcileProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	return e.reconcileProtocolBindingRequest(ctx, tenant, request, true)
}

func (e *orchRemoteExecutor) reconcileProtocolBindingRequest(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
	apply bool,
) (sessions.ProtocolBindingReconcileResult, error) {
	if e == nil || e.store == nil || tenant.IsZero() || tenant.IsSystem() ||
		request.Binding.ID.IsZero() || request.ExpectedVersion < 1 ||
		request.Binding.Version != request.ExpectedVersion ||
		!validProtocolReconcileHash(request.ExpectedPlanHash) ||
		strings.TrimSpace(request.SemanticKey) == "" || len(request.SemanticKey) > 1024 {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"orch remote: incomplete protocol binding reconcile request",
		)
	}
	current, err := e.store.GetProtocolBinding(
		ctx, tenant, sessions.ProtocolBindingRef{ID: request.Binding.ID},
	)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"orch remote: get binding for REST reconcile: %w", err,
		)
	}
	if !sameProtocolReconcileAnchor(request.Binding, current) ||
		current.Version != request.ExpectedVersion {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"orch remote: protocol binding changed after its REST plan",
		)
	}
	if current.Protocol != sessions.BindingProtocolA2A {
		return sessions.ProtocolBindingReconcileResult{}, fmt.Errorf(
			"orch remote: protocol %q has no operator reconciliation adapter", current.Protocol,
		)
	}
	if err := verifyRemoteBinding(current, request.Binding.ID); err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}

	if current.Terminal || current.ExternalID == "" {
		return protocolReconcileResult(
			e, current, current.ObservationVerdict, current.ObservationCode,
			protocolBindingObservedAt(e, current), true,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "durable_binding", Verdict: current.ObservationVerdict,
				EvidenceRef: current.ID.String(),
			}},
		), nil
	}

	target, err := e.resolveBindingTarget(current)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	observedAt := e.now().UTC()
	remote, legal, observeErr := e.client(target, nil).Reconcile(
		ctx,
		bindingTaskResult(current),
		a2a.TaskRef{AgentName: target.name, AgentURL: target.url, TaskID: current.ExternalID},
	)
	if observeErr != nil {
		if !apply {
			return protocolReconcileResult(
				e, current, sessions.ProtocolObservationUnknown, "observe_unavailable",
				observedAt, false,
				[]sessions.ProtocolBindingRemoteCheck{{
					Name: "peer_observation", Verdict: sessions.ProtocolObservationUnknown,
					EvidenceRef: remoteDigestHex("a2a.rest-reconcile.test.v1", current.ID.String()),
				}},
			), nil
		}
		updated, err := e.observeBinding(
			ctx, tenant, current, request.SemanticKey,
			current.LocalState, current.RemoteState,
			sessions.ProtocolObservationUnknown, "observe_unavailable",
			false, current.Terminal,
			remoteDigest("a2a.rest-reconcile.error.v1", current.ID.String(), observeErr.Error()),
		)
		if err != nil {
			return sessions.ProtocolBindingReconcileResult{}, err
		}
		return protocolReconcileResult(
			e, updated, updated.ObservationVerdict, updated.ObservationCode,
			protocolBindingObservedAt(e, updated), updated.Replayed,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "peer_observation", Verdict: sessions.ProtocolObservationUnknown,
				EvidenceRef: updated.ID.String(),
			}},
		), nil
	}

	if remote.TaskID != current.ExternalID || remote.ResultKind != "task" {
		legal = false
	}
	if !legal {
		if !apply {
			return protocolReconcileResult(
				e, current, sessions.ProtocolObservationBroken, "illegal_transition",
				observedAt, false,
				[]sessions.ProtocolBindingRemoteCheck{{
					Name: "peer_lifecycle", Verdict: sessions.ProtocolObservationBroken,
					EvidenceRef: hex.EncodeToString(remoteTaskHash(remote)),
				}},
			), nil
		}
		updated, err := e.observeBinding(
			ctx, tenant, current, request.SemanticKey,
			current.LocalState, current.RemoteState,
			sessions.ProtocolObservationBroken, "illegal_transition",
			true, current.Terminal, remoteTaskHash(remote),
		)
		if err != nil {
			return sessions.ProtocolBindingReconcileResult{}, err
		}
		return protocolReconcileResult(
			e, updated, updated.ObservationVerdict, updated.ObservationCode,
			protocolBindingObservedAt(e, updated), updated.Replayed,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "peer_lifecycle", Verdict: sessions.ProtocolObservationBroken,
				EvidenceRef: hex.EncodeToString(updated.DetailHash),
			}},
		), nil
	}

	mapped := translateA2AResult(remote, current.LocalState, current.CancelRequested)
	if !apply {
		return protocolReconcileResult(
			e, current, mapped.verdict, mapped.code, observedAt, false,
			[]sessions.ProtocolBindingRemoteCheck{{
				Name: "peer_lifecycle", Verdict: mapped.verdict,
				EvidenceRef: hex.EncodeToString(remoteTaskHash(remote)),
			}},
		), nil
	}
	updated, err := e.observeRemoteTask(
		ctx, tenant, current, request.SemanticKey, remote, mapped,
	)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	return protocolReconcileResult(
		e, updated, updated.ObservationVerdict, updated.ObservationCode,
		protocolBindingObservedAt(e, updated), updated.Replayed,
		[]sessions.ProtocolBindingRemoteCheck{{
			Name: "peer_lifecycle", Verdict: updated.ObservationVerdict,
			EvidenceRef: hex.EncodeToString(updated.DetailHash),
		}},
	), nil
}

func validProtocolReconcileHash(value string) bool {
	value = strings.TrimSpace(value)
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func sameProtocolReconcileAnchor(left, right sessions.ProtocolBinding) bool {
	return left.ID == right.ID && left.TenantID == right.TenantID &&
		left.WorkspaceID == right.WorkspaceID && left.Version == right.Version &&
		left.BindingSpecID == right.BindingSpecID &&
		left.BindingSpecGeneration == right.BindingSpecGeneration &&
		left.WorkItemID == right.WorkItemID && left.AttemptID == right.AttemptID &&
		left.Protocol == right.Protocol && left.ProtocolVersion == right.ProtocolVersion &&
		left.PeerAuthority == right.PeerAuthority &&
		left.RemoteResourceRef == right.RemoteResourceRef &&
		left.Generation == right.Generation && left.SyntheticSID == right.SyntheticSID &&
		left.ExternalKind == right.ExternalKind && left.ExternalID == right.ExternalID &&
		left.ContextID == right.ContextID && left.ExternalMessageID == right.ExternalMessageID &&
		left.LocalState == right.LocalState && left.RemoteState == right.RemoteState &&
		left.RemoteRevision == right.RemoteRevision && left.Terminal == right.Terminal &&
		left.CancelRequested == right.CancelRequested
}

func protocolBindingObservedAt(e *orchRemoteExecutor, binding sessions.ProtocolBinding) time.Time {
	if binding.LastObservedAt != nil {
		return binding.LastObservedAt.UTC()
	}
	if !binding.UpdatedAt.IsZero() {
		return binding.UpdatedAt.UTC()
	}
	return e.now().UTC()
}

func protocolReconcileResult(
	_ *orchRemoteExecutor,
	binding sessions.ProtocolBinding,
	verdict sessions.ProtocolObservationVerdict,
	code string,
	observedAt time.Time,
	replayed bool,
	checks []sessions.ProtocolBindingRemoteCheck,
) sessions.ProtocolBindingReconcileResult {
	return sessions.ProtocolBindingReconcileResult{
		Verdict: verdict, Code: strings.TrimSpace(code), ObservedAt: observedAt.UTC(),
		Checks:  append([]sessions.ProtocolBindingRemoteCheck(nil), checks...),
		Binding: binding, Replayed: replayed,
	}
}
