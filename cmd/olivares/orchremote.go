// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/sessions"
)

// remoteWorkStore is the private K5 composition port. ProtocolBinding is the
// durable protocol authority; Get supplies the WorkItem content and revisions
// whose hashes were approved. The connector never imports either AGPL module.
type remoteWorkStore interface {
	sessions.ProtocolBindingStore
	sessions.ProtocolReplayStore
	sessions.ProtocolReplyCommunication
	sessions.ProtocolInterruptCommunication
	GetProtocolBindingSpec(context.Context, model.TenantID, model.ID) (sessions.ProtocolBindingSpec, error)
	Get(context.Context, model.TenantID, sessions.WorkPrincipal, model.ID) (sessions.WorkSnapshot, error)
}

// remoteA2AClient keeps the production adapter on the complete governed
// Delegator while allowing deterministic command-root contract tests without a
// network listener.
type remoteA2AClient interface {
	Test(context.Context, a2a.DelegateSpec) (a2a.DelegationTestResult, error)
	Delegate(context.Context, a2a.DelegateSpec) (a2a.TaskResult, error)
	Reconcile(context.Context, a2a.TaskResult, a2a.TaskRef) (a2a.TaskResult, bool, error)
	CancelTask(context.Context, a2a.TaskRef) (a2a.TaskResult, error)
}

type remoteA2AFactory func(orchRemoteTarget, a2a.DelegationGate) remoteA2AClient

type orchRemoteTarget struct {
	authority     string
	agentRef      string
	name          string
	url           string
	skill         string
	scopes        []string
	trustJWKS     []byte
	headers       map[string]string
	wellKnownPath string
	timeout       time.Duration
	fingerprint   string
	interrupt     sessions.ProtocolInterruptRoute
	policy        protocolRuntimePolicy
}

type orchRemoteExecutor struct {
	store    remoteWorkStore
	approval orchestration.ApprovalGate
	targets  map[string]orchRemoteTarget
	client   remoteA2AFactory
	now      func() time.Time
}

var _ orchestration.RemoteWorkExecutor = (*orchRemoteExecutor)(nil)

// newOrchRemoteExecutor promotes only A2A entries carrying the K5 stable
// authority. Legacy scheduled-fire entries without authority remain available
// to the existing Dispatcher and do not accidentally become remote-work peers.
func newOrchRemoteExecutor(
	cfg orchDispatchConfig,
	store remoteWorkStore,
	approval orchestration.ApprovalGate,
	log *slog.Logger,
) (*orchRemoteExecutor, error) {
	if store == nil {
		return nil, nil
	}
	e := &orchRemoteExecutor{
		store: store, approval: approval,
		targets: make(map[string]orchRemoteTarget),
		now:     time.Now,
	}
	e.client = func(target orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		return a2a.NewDelegator(a2a.DelegatorConfig{
			Emit: a2a.EmitConfig{
				TrustJWKS: target.trustJWKS, Headers: cloneRemoteHeaders(target.headers),
				WellKnownPath: target.wellKnownPath, Timeout: target.timeout,
			},
			Allowlist: a2a.NewAllowlist([]a2a.AllowRule{{
				Agent: target.name, Skill: target.skill, Scopes: append([]string(nil), target.scopes...),
			}}),
			Gate: gate,
		})
	}
	for _, raw := range cfg.A2A.Agents {
		authority := strings.TrimSpace(raw.Authority)
		if authority == "" {
			continue
		}
		agentRef := strings.TrimSpace(raw.SubjectRef)
		if agentRef == "" || strings.TrimSpace(raw.URL) == "" {
			return nil, fmt.Errorf("orch remote: authority %q requires subject_ref and url", authority)
		}
		target := orchRemoteTarget{
			authority: authority, agentRef: agentRef,
			name: orDefaultStr(strings.TrimSpace(raw.Name), agentRef), url: strings.TrimSpace(raw.URL),
			skill: strings.TrimSpace(raw.Skill), scopes: canonicalA2AScopes(raw.Scopes),
			trustJWKS: resolveTrustAnchor(raw, log), headers: cloneRemoteHeaders(raw.Headers),
			wellKnownPath: strings.TrimSpace(raw.WellKnownPath),
			timeout:       time.Duration(raw.TimeoutSeconds) * time.Second,
		}
		interrupt, err := parseProtocolInterruptRoute(
			"orch remote target", raw.InterruptChannelID,
			raw.InterruptSenderUserID, raw.InterruptRecipientUserID,
		)
		if err != nil {
			return nil, fmt.Errorf("orch remote: authority %q: %w", authority, err)
		}
		target.interrupt = interrupt
		policy, err := resolveProtocolRuntimePolicy(
			raw.ProtocolRuleRefs, raw.ProtocolPermissionProfileRef, a2aOutboundRuntimePolicy,
		)
		if err != nil {
			return nil, fmt.Errorf("orch remote: authority %q: %w", authority, err)
		}
		target.policy = policy
		target.fingerprint = remoteTargetFingerprint(target)
		key := remoteTargetKey(authority, agentRef)
		if _, exists := e.targets[key]; exists {
			return nil, fmt.Errorf("orch remote: duplicate A2A authority+agent_ref %q", key)
		}
		e.targets[key] = target
	}
	if len(e.targets) == 0 {
		return nil, nil
	}
	return e, nil
}

func cloneRemoteHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func remoteTargetKey(authority, agentRef string) string {
	return strings.TrimSpace(authority) + "\x00" + strings.TrimSpace(agentRef)
}

func remoteTargetFingerprint(target orchRemoteTarget) string {
	anchor := sha256.Sum256(target.trustJWKS)
	headers, _ := json.Marshal(target.headers)
	headerHash := sha256.Sum256(headers)
	value, _ := json.Marshal(struct {
		Authority       string   `json:"authority"`
		AgentRef        string   `json:"agent_ref"`
		Name            string   `json:"name"`
		URL             string   `json:"url"`
		Skill           string   `json:"skill"`
		Scopes          []string `json:"scopes"`
		WellKnownPath   string   `json:"well_known_path"`
		Timeout         string   `json:"timeout"`
		TrustHash       string   `json:"trust_hash"`
		HeadersHash     string   `json:"headers_hash"`
		ChannelID       model.ID `json:"interrupt_channel_id"`
		SenderUserID    model.ID `json:"interrupt_sender_user_id"`
		RecipientUserID model.ID `json:"interrupt_recipient_user_id"`
		RuleRefs        []string `json:"protocol_rule_refs"`
		PermissionRef   string   `json:"protocol_permission_profile_ref"`
	}{
		Authority: target.authority, AgentRef: target.agentRef, Name: target.name,
		URL: target.url, Skill: target.skill, Scopes: target.scopes,
		WellKnownPath: target.wellKnownPath, Timeout: target.timeout.String(),
		TrustHash:       hex.EncodeToString(anchor[:]),
		HeadersHash:     hex.EncodeToString(headerHash[:]),
		ChannelID:       target.interrupt.ChannelID,
		SenderUserID:    target.interrupt.SenderUserID,
		RecipientUserID: target.interrupt.RecipientUserID,
		RuleRefs:        append([]string(nil), target.policy.ruleRefs...),
		PermissionRef:   target.policy.permissionProfileRef,
	})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func (e *orchRemoteExecutor) resolveTarget(plan orchestration.RemoteWorkPlanRequest) (orchRemoteTarget, error) {
	if strings.TrimSpace(plan.Protocol) != string(sessions.BindingProtocolA2A) ||
		strings.TrimSpace(plan.ProtocolVersion) != a2a.ProtocolVersion {
		return orchRemoteTarget{}, fmt.Errorf("orch remote: unsupported protocol pin %q/%q", plan.Protocol, plan.ProtocolVersion)
	}
	target, ok := e.targets[remoteTargetKey(plan.Authority, plan.AgentRef)]
	if !ok {
		return orchRemoteTarget{}, fmt.Errorf("orch remote: A2A authority+agent_ref is not provisioned")
	}
	if strings.TrimSpace(plan.Skill) != target.skill {
		return orchRemoteTarget{}, fmt.Errorf("orch remote: planned skill does not match operator target")
	}
	allowlist := a2a.NewAllowlist([]a2a.AllowRule{{
		Agent: target.name, Skill: target.skill, Scopes: target.scopes,
	}})
	if !allowlist.Allowed(target.name, plan.Skill, plan.Scope) {
		return orchRemoteTarget{}, fmt.Errorf("orch remote: planned agent/skill/scope is not allowed")
	}
	return target, nil
}

func (e *orchRemoteExecutor) resolveBindingTarget(binding sessions.ProtocolBinding) (orchRemoteTarget, error) {
	target, ok := e.targets[remoteTargetKey(binding.PeerAuthority, binding.RemoteResourceRef)]
	if !ok {
		return orchRemoteTarget{}, fmt.Errorf("orch remote: binding authority+agent_ref is not provisioned")
	}
	return target, nil
}

type preparedRemotePlan struct {
	target   orchRemoteTarget
	work     sessions.WorkSnapshot
	spec     sessions.ProtocolBindingSpec
	mapping  sessions.ProtocolMappingEvaluation
	delegate a2a.DelegateSpec
	planHash string
}

func (e *orchRemoteExecutor) preparePlan(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkPlanRequest,
) (preparedRemotePlan, error) {
	if tenant.IsZero() || request.WorkspaceID.IsZero() || request.WorkItemID.IsZero() ||
		request.BindingSpecID.IsZero() || request.BindingSpecGeneration < 1 ||
		request.OwnerEpoch < 1 || request.LeaseFence < 0 || request.LeaseFence == 1<<63-1 ||
		request.CriteriaRevision < 1 ||
		strings.TrimSpace(request.BriefHash) == "" {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: incomplete remote plan tuple")
	}
	target, err := e.resolveTarget(request)
	if err != nil {
		return preparedRemotePlan{}, err
	}
	principal, err := workflowWorkPrincipal(request.Actor)
	if err != nil {
		return preparedRemotePlan{}, err
	}
	work, err := e.store.Get(ctx, tenant, principal, request.WorkItemID)
	if err != nil {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: read WorkItem: %w", err)
	}
	item := work.Item
	if item.ID != request.WorkItemID || item.WorkspaceID != request.WorkspaceID ||
		item.OwnerEpoch != request.OwnerEpoch || item.BriefHash != request.BriefHash ||
		item.AcceptanceRevision != request.CriteriaRevision || item.Lease == nil ||
		(request.LeaseFence > 0 && (item.Lease.Fence != request.LeaseFence || !item.Leased)) {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: WorkItem tuple changed or has no live approved lease")
	}
	spec, err := e.store.GetProtocolBindingSpec(ctx, tenant, request.BindingSpecID)
	if err != nil {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: read ProtocolBindingSpec: %w", err)
	}
	mapping, err := sessions.EvaluateProtocolBindingMapping(spec, sessions.ProtocolBindingRuntimeExpectation{
		TenantID: tenant, WorkspaceID: request.WorkspaceID, SpecID: request.BindingSpecID,
		Generation: request.BindingSpecGeneration, Protocol: sessions.BindingProtocolA2A,
		ProtocolVersion: request.ProtocolVersion,
		Direction:       sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		PeerAuthority: target.authority, RemoteResourceKind: "agent", RemoteResourceRef: target.agentRef,
		RuleRefs: target.policy.ruleRefs, PermissionProfileRef: target.policy.permissionProfileRef,
	}, protocolWorkMappingSource(item))
	if err != nil {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: evaluate ProtocolBindingSpec: %w", err)
	}
	text, err := requiredProtocolMappedString(mapping, "message.text")
	if err != nil {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: %w", err)
	}
	contextID, err := optionalProtocolMappedString(mapping, "message.context_id")
	if err != nil {
		return preparedRemotePlan{}, fmt.Errorf("orch remote: %w", err)
	}
	paramsHash := remotePlanParamsHash(request, target, item, mapping.EvidenceHash)
	delegate := a2a.DelegateSpec{
		AgentName: target.name, AgentURL: target.url, Skill: request.Skill, Scope: request.Scope,
		Text: text, ContextID: contextID, Tenant: tenant.String(), RequestedBy: request.Actor.Ref,
		Objective: request.RunRef + ":" + request.StepRef, ParamsHash: paramsHash,
	}
	return preparedRemotePlan{
		target: target, work: work, spec: spec, mapping: mapping, delegate: delegate,
		planHash: a2a.DelegationPlanHash(delegate),
	}, nil
}

func remotePlanParamsHash(
	request orchestration.RemoteWorkPlanRequest,
	target orchRemoteTarget,
	item sessions.WorkItem,
	mappingEvidence string,
) string {
	return remoteDigestHex(
		"olivares.remote-work-plan.v1",
		request.WorkspaceID.String(), request.WorkItemID.String(),
		request.BindingSpecID.String(), fmt.Sprint(request.BindingSpecGeneration),
		request.Protocol, request.ProtocolVersion, request.Authority, request.AgentRef,
		request.Skill, request.Scope, fmt.Sprint(request.OwnerEpoch), fmt.Sprint(request.LeaseFence),
		request.BriefHash, fmt.Sprint(request.CriteriaRevision),
		item.WorkKind, item.OwnerKind, item.OwnerRef, target.fingerprint, mappingEvidence,
	)
}

func remoteDigest(parts ...string) []byte {
	h := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	return h.Sum(nil)
}

func remoteDigestHex(parts ...string) string { return hex.EncodeToString(remoteDigest(parts...)) }

func (e *orchRemoteExecutor) Plan(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkPlanRequest,
) (orchestration.RemoteWorkResult, error) {
	prepared, err := e.preparePlan(ctx, tenant, request)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	return orchestration.RemoteWorkResult{
		Outcome: orchestration.RemoteWorkClean, Code: "planned", ObservedAt: e.observedNow(),
		Checks: []orchestration.RemoteWorkCheck{
			{Name: "operator_target", Outcome: orchestration.RemoteWorkClean, EvidenceRef: prepared.target.fingerprint},
			{Name: "work_tuple", Outcome: orchestration.RemoteWorkClean, EvidenceRef: prepared.delegate.ParamsHash},
			{Name: "protocol_mapping", Outcome: orchestration.RemoteWorkClean, EvidenceRef: prepared.mapping.EvidenceHash},
		},
		PlanHash: prepared.planHash, WorkItemID: request.WorkItemID,
		BindingSpecID: request.BindingSpecID, BindingSpecGeneration: request.BindingSpecGeneration,
		OwnerEpoch: request.OwnerEpoch, LeaseFence: request.LeaseFence,
	}, nil
}

func (e *orchRemoteExecutor) Test(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkTestRequest,
) (orchestration.RemoteWorkResult, error) {
	prepared, err := e.preparePlan(ctx, tenant, request.Plan)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	base := orchestration.RemoteWorkResult{
		Outcome: orchestration.RemoteWorkBroken, Code: "preflight_failed", ObservedAt: e.observedNow(),
		PlanHash: prepared.planHash, WorkItemID: request.Plan.WorkItemID,
		BindingSpecID:         request.Plan.BindingSpecID,
		BindingSpecGeneration: request.Plan.BindingSpecGeneration,
		OwnerEpoch:            request.Plan.OwnerEpoch, LeaseFence: request.Plan.LeaseFence,
	}
	if strings.TrimSpace(request.PlanHash) == "" || request.PlanHash != prepared.planHash {
		base.Code, base.Detail = "plan_mismatch", "stored remote plan no longer matches its work tuple"
		return base, nil
	}
	check, err := e.client(prepared.target, nil).Test(ctx, prepared.delegate)
	if err != nil {
		base.Detail = "A2A preflight did not complete"
		return base, nil
	}
	if check.PlanHash != prepared.planHash || check.AgentName != prepared.target.name ||
		check.Skill != request.Plan.Skill || check.Scope != request.Plan.Scope || check.Trust != "verified" {
		base.Code, base.Detail = "preflight_mismatch", "A2A preflight returned a mismatched target or plan"
		return base, nil
	}
	base.Outcome, base.Code, base.Detail = orchestration.RemoteWorkClean, "preflight_verified", "verified A2A peer and capability"
	base.Checks = []orchestration.RemoteWorkCheck{{
		Name: "a2a_preflight", Outcome: orchestration.RemoteWorkClean,
		EvidenceRef: remoteDigestHex(check.PlanHash, check.AgentName, check.Skill, check.Scope, check.Trust),
	}}
	return base, nil
}

func (e *orchRemoteExecutor) Start(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkStartRequest,
) (orchestration.RemoteWorkResult, error) {
	prepared, err := e.preparePlan(ctx, tenant, request.Plan)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || request.PlanHash != prepared.planHash ||
		strings.TrimSpace(request.ApprovalRef) == "" || strings.TrimSpace(request.ApprovalPlanHash) == "" ||
		strings.TrimSpace(request.ApprovalAction) == "" || strings.TrimSpace(request.ApprovalSubjectKind) == "" ||
		strings.TrimSpace(request.ApprovalSubjectRef) == "" {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: start does not match a complete approved plan")
	}

	item := prepared.work.Item
	attemptID := model.ID(workflowSemanticID("remote-a2a-attempt", request.IdempotencyKey))
	dispatchKey := workflowSemanticID("remote-a2a-dispatch", request.IdempotencyKey)
	reserved, err := e.store.ReserveProtocolBinding(ctx, tenant, sessions.ProtocolBindingReservation{
		WorkspaceID:   request.Plan.WorkspaceID,
		BindingSpecID: request.Plan.BindingSpecID, BindingSpecGeneration: request.Plan.BindingSpecGeneration,
		ExpectedDirection: sessions.BindingOutbound,
		WorkItemID:        request.Plan.WorkItemID, AttemptID: attemptID, DispatchKey: dispatchKey,
		ExpectedExternalKind: "task_or_message", Generation: 1,
		OwnerKind: item.OwnerKind, OwnerRef: item.OwnerRef,
		OwnerEpoch: request.Plan.OwnerEpoch, LeaseFence: request.Plan.LeaseFence,
	})
	if err != nil {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: reserve binding: %w", err)
	}
	if err := verifyRemoteReservation(reserved, request.Plan, attemptID, prepared.target); err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	if reserved.Replayed {
		result, err := e.replayedStart(ctx, tenant, request, reserved)
		if err != nil {
			return orchestration.RemoteWorkResult{}, err
		}
		result.PlanHash, result.ApprovalRef = request.PlanHash, request.ApprovalRef
		return result, nil
	}

	gate := remoteStartApprovalGate{
		gate: e.approval, tenant: tenant, start: request,
		target: prepared.target, requestedBy: request.Actor.Ref,
	}
	remote, delegateErr := e.client(prepared.target, gate).Delegate(ctx, prepared.delegate)
	if delegateErr != nil {
		verdict, outcome, code, remoteState, detail := sessions.ProtocolObservationBroken,
			orchestration.RemoteWorkBroken, "dispatch_failed", "not_started", "A2A delegation failed before transmission"
		if errors.Is(delegateErr, a2a.ErrAfterTransmit) {
			verdict, outcome, code, remoteState, detail = sessions.ProtocolObservationUnknown,
				orchestration.RemoteWorkUnknown, "dispatch_ambiguous", "unknown", "A2A delivery is ambiguous and will not be repeated"
		}
		observed, observeErr := e.observeBinding(ctx, tenant, reserved, request.IdempotencyKey+":start-outcome",
			item.Status, remoteState, verdict, code, false, false, remoteDigest("a2a.start.error.v1", code, delegateErr.Error()))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		result := e.projectBinding(observed, outcome, code, detail)
		result.PlanHash, result.ApprovalRef = request.PlanHash, request.ApprovalRef
		return result, nil
	}

	mapped := translateA2AResult(remote, item.Status, false)
	detailHash := remoteTaskHash(remote)
	settlement := sessions.ProtocolBindingSettlement{
		BindingID: reserved.ID, Generation: reserved.Generation, ExpectedVersion: reserved.Version,
		DispatchKey: dispatchKey, ResultKind: mapped.resultKind,
		ExternalID: mapped.externalTaskID, ContextID: remote.ContextID,
		ExternalMessageID: mapped.externalMessageID,
		LocalState:        mapped.localState, RemoteState: mapped.remoteState,
		RemoteRevision: a2a.ProtocolVersion, Verdict: mapped.verdict, Code: mapped.code,
		Observed: true, DetailHash: detailHash, Terminal: mapped.terminal,
	}
	var settled sessions.ProtocolBinding
	if mapped.resultKind == sessions.ProtocolBindingResultMessage {
		replyCommand, commandErr := remoteProtocolReplyCommand(reserved, prepared.target, remote)
		if commandErr != nil {
			return orchestration.RemoteWorkResult{}, commandErr
		}
		replay, replayErr := e.store.ApplyProtocolReplay(ctx, tenant, sessions.ProtocolReplayClaim{
			WorkspaceID: request.Plan.WorkspaceID, Protocol: sessions.BindingProtocolA2A,
			PeerAuthority: prepared.target.authority, Kind: sessions.ProtocolReplayMessageID,
			ReplayID: remote.MessageID, ExpiresAt: e.now().UTC().Add(24 * time.Hour),
			ExpectedBindingID: reserved.ID,
		}, func(joined context.Context) (sessions.ProtocolReplaySettlement, error) {
			var mutationErr error
			settled, mutationErr = e.store.SettleProtocolBinding(joined, tenant, settlement)
			if mutationErr != nil {
				return sessions.ProtocolReplaySettlement{}, mutationErr
			}
			replyCommand.BindingID = settled.ID
			replyCommand.Generation = settled.Generation
			if _, mutationErr = e.store.ProjectProtocolReply(joined, tenant, replyCommand); mutationErr != nil {
				return sessions.ProtocolReplaySettlement{}, mutationErr
			}
			return sessions.ProtocolReplaySettlement{BindingID: settled.ID}, nil
		})
		if replayErr != nil {
			return orchestration.RemoteWorkResult{}, fmt.Errorf(
				"orch remote: atomically settle A2A Message reply: %w", replayErr,
			)
		}
		if replay.Replayed {
			settled, err = e.store.GetProtocolBinding(ctx, tenant, sessions.ProtocolBindingRef{ID: reserved.ID})
			if err == nil {
				_, err = e.store.GetProtocolReply(ctx, tenant, replyCommand.Ref())
			}
			if err != nil {
				return orchestration.RemoteWorkResult{}, fmt.Errorf(
					"orch remote: reload durable A2A Message reply: %w", err,
				)
			}
		}
	} else {
		settled, err = e.store.SettleProtocolBinding(ctx, tenant, settlement)
		if err != nil {
			return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: settle A2A result: %w", err)
		}
		if err := e.recordRemoteInterrupt(ctx, tenant, settled, prepared.target, remote, mapped); err != nil {
			return orchestration.RemoteWorkResult{}, err
		}
	}
	result := e.projectBinding(settled, mapped.outcome, mapped.code, remote.Detail)
	result.PlanHash, result.ApprovalRef = request.PlanHash, request.ApprovalRef
	return result, nil
}

func verifyRemoteReservation(
	binding sessions.ProtocolBinding,
	plan orchestration.RemoteWorkPlanRequest,
	attemptID model.ID,
	target orchRemoteTarget,
) error {
	if binding.ID.IsZero() || binding.Version < 1 || binding.WorkspaceID != plan.WorkspaceID ||
		binding.BindingSpecID != plan.BindingSpecID || binding.BindingSpecGeneration != plan.BindingSpecGeneration ||
		binding.WorkItemID != plan.WorkItemID || binding.AttemptID != attemptID || binding.Generation < 1 ||
		binding.Protocol != sessions.BindingProtocolA2A || binding.ProtocolVersion != a2a.ProtocolVersion ||
		binding.PeerAuthority != target.authority || binding.RemoteResourceRef != plan.AgentRef ||
		binding.OwnerEpoch != plan.OwnerEpoch ||
		binding.LeaseFence != plan.LeaseFence+1 ||
		strings.TrimSpace(binding.SyntheticSID) == "" ||
		binding.LastCommandID.IsZero() || binding.LastEventID.IsZero() || binding.LastEventSeq < 1 {
		return fmt.Errorf("orch remote: binding reservation returned incomplete or mismatched durable evidence")
	}
	return nil
}

func (e *orchRemoteExecutor) replayedStart(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkStartRequest,
	binding sessions.ProtocolBinding,
) (orchestration.RemoteWorkResult, error) {
	if binding.LastObservedAt != nil || binding.ExternalID != "" || binding.ExternalMessageID != "" || binding.Terminal {
		return e.projectBinding(binding, remoteOutcomeFromVerdict(binding.ObservationVerdict),
			binding.ObservationCode, "replayed durable A2A binding receipt"), nil
	}
	observed, err := e.observeBinding(ctx, tenant, binding, request.IdempotencyKey+":replay-ambiguity",
		binding.LocalState, "unknown", sessions.ProtocolObservationUnknown, "dispatch_ambiguous",
		false, false, remoteDigest("a2a.start.replay.v1", binding.ID.String(), fmt.Sprint(binding.Generation)))
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	return e.projectBinding(observed, orchestration.RemoteWorkUnknown, "dispatch_ambiguous",
		"reserved dispatch replay requires reconciliation; A2A SendMessage was not repeated"), nil
}

type remoteStartApprovalGate struct {
	gate        orchestration.ApprovalGate
	tenant      model.TenantID
	start       orchestration.RemoteWorkStartRequest
	target      orchRemoteTarget
	requestedBy string
}

var _ a2a.DelegationGate = remoteStartApprovalGate{}

func (g remoteStartApprovalGate) Authorize(
	ctx context.Context,
	request a2a.DelegationRequest,
) (a2a.GateDecision, error) {
	if g.gate == nil {
		return a2a.GateDecision{ApprovalRef: g.start.ApprovalRef, Status: a2a.StatusNoGate, PlanHash: request.PlanHash}, nil
	}
	if request.Tenant != g.tenant.String() || request.AgentName != g.target.name ||
		request.Skill != g.start.Plan.Skill || request.Scope != g.start.Plan.Scope ||
		request.PlanHash != g.start.PlanHash || request.RequestedBy != g.requestedBy {
		return a2a.GateDecision{}, fmt.Errorf("orch remote: A2A gate request changed the approved delegation")
	}
	decision, err := g.gate.Status(ctx, orchestration.ApprovalCheck{
		Tenant: g.tenant, ApprovalRef: g.start.ApprovalRef, PlanHash: g.start.ApprovalPlanHash,
		Action: g.start.ApprovalAction, SubjectKind: g.start.ApprovalSubjectKind,
		SubjectRef: g.start.ApprovalSubjectRef,
	})
	if err != nil {
		return a2a.GateDecision{}, err
	}
	if decision.PlanHash != "" && decision.PlanHash != g.start.ApprovalPlanHash {
		return a2a.GateDecision{}, fmt.Errorf("orch remote: approval gate returned another workflow plan")
	}
	if decision.ApprovalRef != g.start.ApprovalRef {
		return a2a.GateDecision{}, fmt.Errorf("orch remote: approval gate returned another approval reference")
	}
	return a2a.GateDecision{
		ApprovalRef: decision.ApprovalRef, Status: remoteA2AGateStatus(decision.Status),
		// The workflow approval was checked against ApprovalPlanHash and its exact
		// action/subject above. The A2A PEP independently binds this decision to
		// the connector's remote PlanHash here.
		PlanHash: request.PlanHash,
	}, nil
}

func remoteA2AGateStatus(status orchestration.GateStatus) a2a.GateStatus {
	switch status {
	case orchestration.StatusApproved:
		return a2a.StatusApproved
	case orchestration.StatusPending:
		return a2a.StatusPending
	case orchestration.StatusRejected:
		return a2a.StatusRejected
	case orchestration.StatusExpired:
		return a2a.StatusExpired
	default:
		return a2a.StatusNoGate
	}
}

func (e *orchRemoteExecutor) Observe(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkObserveRequest,
) (orchestration.RemoteWorkResult, error) {
	if tenant.IsZero() || request.BindingID.IsZero() || strings.TrimSpace(request.IdempotencyKey) == "" {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: incomplete observation request")
	}
	binding, err := e.store.GetProtocolBinding(ctx, tenant, sessions.ProtocolBindingRef{ID: request.BindingID})
	if err != nil {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: get binding: %w", err)
	}
	if err := verifyRemoteBinding(binding, request.BindingID); err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	if binding.ExternalID == "" {
		return e.projectBinding(binding, remoteOutcomeFromVerdict(binding.ObservationVerdict),
			binding.ObservationCode, "binding has no remote Task to poll"), nil
	}
	if binding.Terminal {
		return e.projectBinding(binding, remoteOutcomeFromVerdict(binding.ObservationVerdict),
			binding.ObservationCode, "terminal binding returned from durable state"), nil
	}
	target, err := e.resolveBindingTarget(binding)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	prior := bindingTaskResult(binding)
	remote, legal, reconcileErr := e.client(target, nil).Reconcile(ctx, prior, a2a.TaskRef{
		AgentName: target.name, AgentURL: target.url, TaskID: binding.ExternalID,
	})
	if reconcileErr != nil {
		observed, observeErr := e.observeBinding(ctx, tenant, binding, request.IdempotencyKey,
			binding.LocalState, binding.RemoteState, sessions.ProtocolObservationUnknown,
			"observe_unavailable", false, binding.Terminal,
			remoteDigest("a2a.observe.error.v1", binding.ID.String(), reconcileErr.Error()))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		return e.projectBinding(observed, orchestration.RemoteWorkUnknown, "observe_unavailable",
			"A2A Task could not be observed conclusively"), nil
	}
	if remote.TaskID != binding.ExternalID || remote.ResultKind != "task" {
		legal = false
	}
	if !legal {
		observed, observeErr := e.observeBinding(ctx, tenant, binding, request.IdempotencyKey,
			binding.LocalState, binding.RemoteState, sessions.ProtocolObservationBroken,
			"illegal_transition", true, binding.Terminal, remoteTaskHash(remote))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		return e.projectBinding(observed, orchestration.RemoteWorkBroken, "illegal_transition",
			"remote Task returned an illegal lifecycle transition"), nil
	}
	mapped := translateA2AResult(remote, binding.LocalState, binding.CancelRequested)
	observed, err := e.observeRemoteTask(ctx, tenant, binding, request.IdempotencyKey, remote, mapped)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	return e.projectBinding(observed, mapped.outcome, mapped.code, remote.Detail), nil
}

func verifyRemoteBinding(binding sessions.ProtocolBinding, want model.ID) error {
	if binding.ID != want || binding.Version < 1 || binding.Generation < 1 ||
		binding.Protocol != sessions.BindingProtocolA2A || binding.ProtocolVersion != a2a.ProtocolVersion ||
		strings.TrimSpace(binding.PeerAuthority) == "" || strings.TrimSpace(binding.RemoteResourceRef) == "" ||
		binding.AttemptID.IsZero() ||
		strings.TrimSpace(binding.SyntheticSID) == "" || binding.WorkItemID.IsZero() ||
		binding.LastCommandID.IsZero() || binding.LastEventID.IsZero() || binding.LastEventSeq < 1 {
		return fmt.Errorf("orch remote: corrupt or mismatched durable A2A binding")
	}
	return nil
}

func (e *orchRemoteExecutor) Cancel(
	ctx context.Context,
	tenant model.TenantID,
	request orchestration.RemoteWorkCancelRequest,
) (orchestration.RemoteWorkResult, error) {
	if tenant.IsZero() || request.BindingID.IsZero() || request.WorkItemID.IsZero() ||
		strings.TrimSpace(request.IdempotencyKey) == "" {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: incomplete cancellation request")
	}
	binding, err := e.store.GetProtocolBinding(ctx, tenant, sessions.ProtocolBindingRef{ID: request.BindingID})
	if err != nil {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: get binding for cancel: %w", err)
	}
	if err := verifyRemoteBinding(binding, request.BindingID); err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	if binding.WorkItemID != request.WorkItemID {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: cancel WorkItem does not own binding")
	}
	intent, err := e.store.RequestProtocolBindingCancel(ctx, tenant, sessions.ProtocolBindingCancelIntent{
		BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
		SemanticKey: request.IdempotencyKey, ReasonCode: "workflow_cancel",
	})
	if err != nil {
		return orchestration.RemoteWorkResult{}, fmt.Errorf("orch remote: persist cancel intent: %w", err)
	}
	if intent.Replayed {
		outcome, code, detail := orchestration.RemoteWorkUnknown, "cancel_reconcile_required",
			"replayed cancellation intent did not repeat the A2A CancelTask RPC"
		if intent.Terminal {
			outcome, code, detail = remoteOutcomeFromVerdict(intent.ObservationVerdict),
				intent.ObservationCode, "terminal cancellation receipt returned from durable state"
		}
		return e.projectBinding(intent, outcome, code, detail), nil
	}
	if intent.Terminal {
		return e.projectBinding(intent, remoteOutcomeFromVerdict(intent.ObservationVerdict),
			intent.ObservationCode, "terminal binding was not sent another A2A CancelTask request"), nil
	}
	if intent.ExternalID == "" {
		observed, observeErr := e.observeBinding(ctx, tenant, intent, request.IdempotencyKey+":outcome",
			intent.LocalState, intent.RemoteState, sessions.ProtocolObservationBroken,
			"not_cancelable", true, intent.Terminal,
			remoteDigest("a2a.cancel.not-cancelable.v1", intent.ID.String()))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		return e.projectBinding(observed, orchestration.RemoteWorkBroken, "not_cancelable",
			"direct Message binding has no remote Task to cancel"), nil
	}
	target, err := e.resolveBindingTarget(intent)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	remote, cancelErr := e.client(target, nil).CancelTask(ctx, a2a.TaskRef{
		AgentName: target.name, AgentURL: target.url, TaskID: intent.ExternalID,
	})
	if cancelErr != nil {
		verdict, outcome, code, remoteState, detail := sessions.ProtocolObservationBroken,
			orchestration.RemoteWorkBroken, "cancel_failed", intent.RemoteState, "A2A cancellation failed before transmission"
		if errors.Is(cancelErr, a2a.ErrAfterTransmit) {
			verdict, outcome, code, remoteState, detail = sessions.ProtocolObservationUnknown,
				orchestration.RemoteWorkUnknown, "cancel_ambiguous", "unknown", "A2A cancellation is ambiguous and will not be repeated"
		}
		observed, observeErr := e.observeBinding(ctx, tenant, intent, request.IdempotencyKey+":outcome",
			intent.LocalState, remoteState, verdict, code, false, intent.Terminal,
			remoteDigest("a2a.cancel.error.v1", intent.ID.String(), cancelErr.Error()))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		return e.projectBinding(observed, outcome, code, detail), nil
	}
	if remote.TaskID != intent.ExternalID || remote.ResultKind != "task" {
		observed, observeErr := e.observeBinding(ctx, tenant, intent, request.IdempotencyKey+":outcome",
			intent.LocalState, intent.RemoteState, sessions.ProtocolObservationBroken,
			"cancel_identity_mismatch", true, intent.Terminal, remoteTaskHash(remote))
		if observeErr != nil {
			return orchestration.RemoteWorkResult{}, observeErr
		}
		return e.projectBinding(observed, orchestration.RemoteWorkBroken, "cancel_identity_mismatch",
			"A2A cancellation returned another Task"), nil
	}
	mapped := translateA2AResult(remote, intent.LocalState, true)
	observed, err := e.observeRemoteTask(ctx, tenant, intent, request.IdempotencyKey+":outcome", remote, mapped)
	if err != nil {
		return orchestration.RemoteWorkResult{}, err
	}
	return e.projectBinding(observed, mapped.outcome, mapped.code, remote.Detail), nil
}

type translatedA2AResult struct {
	outcome           orchestration.RemoteWorkOutcome
	verdict           sessions.ProtocolObservationVerdict
	code              string
	remoteState       string
	localState        string
	terminal          bool
	resultKind        sessions.ProtocolBindingResultKind
	externalTaskID    string
	externalMessageID string
}

func translateA2AResult(remote a2a.TaskResult, currentLocal string, cancelRequested bool) translatedA2AResult {
	if remote.ResultKind == "message" {
		return translatedA2AResult{
			outcome: orchestration.RemoteWorkClean, verdict: sessions.ProtocolObservationClean,
			code: "remote_message", remoteState: "completed", localState: "review", terminal: true,
			resultKind: sessions.ProtocolBindingResultMessage, externalMessageID: remote.MessageID,
		}
	}
	mapped := translatedA2AResult{
		outcome: orchestration.RemoteWorkClean, verdict: sessions.ProtocolObservationClean,
		localState: currentLocal, resultKind: sessions.ProtocolBindingResultTask,
		externalTaskID: remote.TaskID, terminal: remote.Terminal,
	}
	if cancelRequested {
		// A cooperative cancellation is only an intent until the peer reports the
		// terminal canceled state. Keep the local item blocked while the remote Task
		// remains live; reporting submitted/working must not reopen it as active.
		mapped.localState = "blocked"
	}
	switch remote.State {
	case a2a.TaskStateSubmitted:
		mapped.code, mapped.remoteState = "remote_submitted", "submitted"
		if !cancelRequested {
			mapped.localState = "active"
		}
	case a2a.TaskStateWorking:
		mapped.code, mapped.remoteState = "remote_working", "working"
		if !cancelRequested {
			mapped.localState = "active"
		}
	case a2a.TaskStateInputReq:
		mapped.code, mapped.remoteState, mapped.localState = "remote_input_required", "input_required", "blocked"
	case a2a.TaskStateAuthRequired:
		mapped.code, mapped.remoteState, mapped.localState = "remote_auth_required", "auth_required", "blocked"
	case a2a.TaskStateCompleted:
		mapped.code, mapped.remoteState, mapped.localState, mapped.terminal = "remote_completed", "completed", "review", true
	case a2a.TaskStateCanceled:
		mapped.code, mapped.remoteState, mapped.terminal = "remote_canceled", "canceled", true
		if cancelRequested {
			mapped.localState = "canceled"
		} else {
			mapped.outcome, mapped.verdict, mapped.localState = orchestration.RemoteWorkBroken,
				sessions.ProtocolObservationBroken, "blocked"
			mapped.code = "unexpected_remote_cancel"
		}
	case a2a.TaskStateFailed:
		mapped.outcome, mapped.verdict = orchestration.RemoteWorkBroken, sessions.ProtocolObservationBroken
		mapped.code, mapped.remoteState, mapped.localState, mapped.terminal = "remote_failed", "failed", "blocked", true
	case a2a.TaskStateRejected:
		mapped.outcome, mapped.verdict = orchestration.RemoteWorkBroken, sessions.ProtocolObservationBroken
		mapped.code, mapped.remoteState, mapped.localState, mapped.terminal = "remote_rejected", "rejected", "blocked", true
	default:
		mapped.outcome, mapped.verdict = orchestration.RemoteWorkBroken, sessions.ProtocolObservationBroken
		mapped.code, mapped.remoteState = "remote_state_invalid", "unspecified"
		mapped.terminal = false
	}
	return mapped
}

func remoteTaskHash(result a2a.TaskResult) []byte {
	parts, _ := json.Marshal(result.MessageParts)
	return remoteDigest(
		"a2a.task-result.v1", result.ResultKind, result.TaskID, result.MessageID,
		result.MessageTaskID, result.MessageDigest, result.ContextID,
		string(result.State), fmt.Sprint(result.Interrupt),
		fmt.Sprint(result.Terminal), result.TrustLevel, result.Detail, string(parts),
	)
}

func remoteProtocolReplyCommand(
	binding sessions.ProtocolBinding,
	target orchRemoteTarget,
	result a2a.TaskResult,
) (sessions.ProtocolReplyCommand, error) {
	if result.ResultKind != "message" || strings.TrimSpace(result.MessageID) == "" ||
		strings.TrimSpace(result.ContextID) == "" || len(result.MessageParts) == 0 ||
		strings.TrimSpace(result.MessageDigest) == "" {
		return sessions.ProtocolReplyCommand{}, fmt.Errorf("orch remote: synchronous A2A Message projection is incomplete")
	}
	parts := make([]sessions.ProtocolReplyPart, 0, len(result.MessageParts))
	for _, part := range result.MessageParts {
		parts = append(parts, sessions.ProtocolReplyPart{
			Kind: sessions.ProtocolReplyPartKind(part.Kind), Text: part.Text,
			Reference: part.Reference, Digest: part.Digest,
		})
	}
	return sessions.ProtocolReplyCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: target.interrupt,
		PeerAuthority: binding.PeerAuthority, Kind: sessions.ProtocolReplyMessage,
		ContextID: result.ContextID, MessageID: result.MessageID,
		Parts: parts, SourceDigest: result.MessageDigest,
	}, nil
}

func bindingTaskResult(binding sessions.ProtocolBinding) a2a.TaskResult {
	state := taskStateFromBinding(binding.RemoteState)
	return a2a.TaskResult{
		TaskID: binding.ExternalID, ResultKind: "task", ContextID: binding.ContextID,
		State: state, Interrupt: state == a2a.TaskStateInputReq || state == a2a.TaskStateAuthRequired,
		Terminal: binding.Terminal, TrustLevel: "verified",
	}
}

func taskStateFromBinding(state string) a2a.TaskState {
	switch strings.TrimSpace(state) {
	case "submitted":
		return a2a.TaskStateSubmitted
	case "working":
		return a2a.TaskStateWorking
	case "input_required":
		return a2a.TaskStateInputReq
	case "auth_required":
		return a2a.TaskStateAuthRequired
	case "completed":
		return a2a.TaskStateCompleted
	case "canceled":
		return a2a.TaskStateCanceled
	case "failed":
		return a2a.TaskStateFailed
	case "rejected":
		return a2a.TaskStateRejected
	default:
		return a2a.TaskStateUnspecified
	}
}

func (e *orchRemoteExecutor) observeRemoteTask(
	ctx context.Context,
	tenant model.TenantID,
	binding sessions.ProtocolBinding,
	semanticKey string,
	remote a2a.TaskResult,
	mapped translatedA2AResult,
) (sessions.ProtocolBinding, error) {
	contextID := remote.ContextID
	if contextID == "" {
		contextID = binding.ContextID
	}
	updated, err := e.observeBindingWithIDs(ctx, tenant, binding, semanticKey,
		binding.ExternalID, contextID, binding.ExternalMessageID,
		mapped.localState, mapped.remoteState, mapped.verdict, mapped.code,
		true, mapped.terminal, remoteTaskHash(remote))
	if err != nil {
		return sessions.ProtocolBinding{}, err
	}
	target, err := e.resolveBindingTarget(updated)
	if err != nil {
		return sessions.ProtocolBinding{}, err
	}
	if err := e.recordRemoteInterrupt(ctx, tenant, updated, target, remote, mapped); err != nil {
		return sessions.ProtocolBinding{}, err
	}
	return updated, nil
}

func (e *orchRemoteExecutor) recordRemoteInterrupt(
	ctx context.Context,
	tenant model.TenantID,
	binding sessions.ProtocolBinding,
	target orchRemoteTarget,
	remote a2a.TaskResult,
	mapped translatedA2AResult,
) error {
	if mapped.remoteState != "input_required" && mapped.remoteState != "auth_required" {
		return nil
	}
	request := protocolA2AInterruptRequest(
		binding.ID, binding.Generation, remote.TaskID, remote.ContextID, mapped.remoteState,
	)
	result, err := e.store.RecordProtocolInterrupt(ctx, tenant, sessions.ProtocolInterruptCommand{
		BindingID: binding.ID, Generation: binding.Generation, Route: target.interrupt,
		RemoteState: mapped.remoteState, Requests: []sessions.ProtocolInterruptRequestRef{request},
	})
	if err != nil {
		return fmt.Errorf("orch remote: record actionable remote interrupt: %w", err)
	}
	if result.BindingID != binding.ID || result.Generation != binding.Generation ||
		len(result.Messages) != 1 || result.Messages[0].KeyDigest != request.KeyDigest ||
		result.Messages[0].MessageID.IsZero() || result.Messages[0].DeliveryID.IsZero() {
		return fmt.Errorf("orch remote: interrupt communication returned mismatched durable evidence")
	}
	return nil
}

func protocolA2AInterruptRequest(
	bindingID model.ID,
	generation int64,
	taskID, contextID, remoteState string,
) sessions.ProtocolInterruptRequestRef {
	parts := []string{bindingID.String(), fmt.Sprint(generation), strings.TrimSpace(taskID),
		strings.TrimSpace(contextID), strings.TrimSpace(remoteState)}
	return sessions.ProtocolInterruptRequestRef{
		KeyDigest:     remoteDigestHex(append([]string{"a2a.interrupt.key.v1"}, parts...)...),
		ContentDigest: remoteDigestHex(append([]string{"a2a.interrupt.content.v1"}, parts...)...),
	}
}

func (e *orchRemoteExecutor) observeBinding(
	ctx context.Context,
	tenant model.TenantID,
	binding sessions.ProtocolBinding,
	semanticKey, localState, remoteState string,
	verdict sessions.ProtocolObservationVerdict,
	code string,
	observed, terminal bool,
	detailHash []byte,
) (sessions.ProtocolBinding, error) {
	return e.observeBindingWithIDs(ctx, tenant, binding, semanticKey,
		binding.ExternalID, binding.ContextID, binding.ExternalMessageID,
		localState, remoteState, verdict, code, observed, terminal, detailHash)
}

func (e *orchRemoteExecutor) observeBindingWithIDs(
	ctx context.Context,
	tenant model.TenantID,
	binding sessions.ProtocolBinding,
	semanticKey, externalID, contextID, messageID, localState, remoteState string,
	verdict sessions.ProtocolObservationVerdict,
	code string,
	observed, terminal bool,
	detailHash []byte,
) (sessions.ProtocolBinding, error) {
	updated, err := e.store.ObserveProtocolBinding(ctx, tenant, sessions.ProtocolBindingObservation{
		BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
		SemanticKey: semanticKey, PeerAuthority: binding.PeerAuthority,
		ExternalID: externalID, ContextID: contextID, ExternalMessageID: messageID,
		LocalState: localState, RemoteState: remoteState, RemoteRevision: a2a.ProtocolVersion,
		Verdict: verdict, Code: code, Observed: observed, DetailHash: detailHash, Terminal: terminal,
	})
	if err != nil {
		return sessions.ProtocolBinding{}, fmt.Errorf("orch remote: persist binding observation: %w", err)
	}
	return updated, nil
}

func remoteOutcomeFromVerdict(verdict sessions.ProtocolObservationVerdict) orchestration.RemoteWorkOutcome {
	switch verdict {
	case sessions.ProtocolObservationClean:
		return orchestration.RemoteWorkClean
	case sessions.ProtocolObservationBroken:
		return orchestration.RemoteWorkBroken
	default:
		return orchestration.RemoteWorkUnknown
	}
}

func (e *orchRemoteExecutor) projectBinding(
	binding sessions.ProtocolBinding,
	outcome orchestration.RemoteWorkOutcome,
	code, detail string,
) orchestration.RemoteWorkResult {
	result := orchestration.RemoteWorkResult{
		Outcome: outcome, Code: strings.TrimSpace(code), ObservedAt: e.observedNow(),
		BindingID: binding.ID, BindingSpecID: binding.BindingSpecID,
		BindingSpecGeneration: binding.BindingSpecGeneration, WorkItemID: binding.WorkItemID,
		AttemptID: binding.AttemptID, Generation: binding.Generation, SyntheticSID: binding.SyntheticSID,
		OwnerEpoch: binding.OwnerEpoch, LeaseFence: binding.LeaseFence,
		ExternalContextID: binding.ContextID, ExternalMessageID: binding.ExternalMessageID,
		RemoteState: binding.RemoteState, RemoteRevision: binding.RemoteRevision,
		Terminal: binding.Terminal, WireHash: hex.EncodeToString(binding.DetailHash),
		DetailHash: hex.EncodeToString(binding.DetailHash), CommandID: binding.LastCommandID,
		EventID: binding.LastEventID, EventSeq: binding.LastEventSeq,
		WorkState: binding.LocalState, Detail: clampStr(strings.TrimSpace(detail), 200),
	}
	if binding.LastObservedAt != nil {
		result.ObservedAt = model.NewTimestamp(binding.LastObservedAt.UTC()).String()
	}
	if binding.ExternalMessageID != "" {
		result.ResultKind = orchestration.RemoteWorkResultMessage
	} else if binding.ExternalID != "" {
		result.ResultKind, result.ExternalTaskID = orchestration.RemoteWorkResultTask, binding.ExternalID
	}
	if result.Code == "" {
		result.Code = "binding_observed"
	}
	return result
}

func (e *orchRemoteExecutor) observedNow() string {
	return model.NewTimestamp(e.now().UTC()).String()
}
