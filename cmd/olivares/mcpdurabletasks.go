// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

const (
	mcpDurableExternalKind = "task"
	mcpDurablePageSize     = 200
	mcpDurableMaxPages     = 4096
)

// mcpDurableTaskStoreConfig is operator-owned local routing. None of these
// values may be accepted from MCP request metadata.
type mcpDurableTaskStoreConfig struct {
	WorkspaceID   model.ID
	BindingSpecID model.ID
	// Generation is the pinned ProtocolBindingSpec generation, not the external
	// task generation allocated by this adapter.
	Generation int64
	OwnerKind  string
	OwnerRef   string
	// UpstreamDescriptor is the composition-owned effect target fingerprint
	// supplied by the MCP Resource Server. It is distinct from the protocol
	// spec's human/operator-owned PeerAuthority lineage.
	UpstreamDescriptor string
	// InterruptRoute is the operator-owned K3 Message route used when an MCP
	// Task asks for input. Remote task metadata can never select these Users or
	// the Channel.
	InterruptRoute sessions.ProtocolInterruptRoute
	Policy         protocolRuntimePolicy
	// WorkItemResolver is an optional local-authority seam. It may select an
	// existing WorkItem from authenticated/configured control-plane context. The
	// MCP request and its metadata never carry a WorkItem ID or lease fence.
	WorkItemResolver mcpDurableWorkItemResolver
}

// mcpDurableWorkItemResolveRequest contains only the immutable, already
// governed task origin. A resolver is composition-owned and returns a local ID;
// the adapter independently reads the WorkItem and its current lease authority.
type mcpDurableWorkItemResolveRequest struct {
	WorkspaceID        model.ID
	TaskID             string
	Tool               string
	OriginOperationID  string
	OriginEffectDigest string
}

// mcpDurableWorkItemResolver is deliberately not part of the Apache connector
// DTO. That keeps a peer-supplied metadata member from becoming local routing
// authority. Returning found=false preserves the ordinary child-WorkItem path.
type mcpDurableWorkItemResolver interface {
	ResolveMCPDurableWorkItem(
		context.Context,
		model.TenantID,
		mcpDurableWorkItemResolveRequest,
	) (workItemID model.ID, found bool, err error)
}

// mcpDurableTaskStore translates the Apache MCP persistence port into the K5
// WorkKernel and ProtocolBinding authorities at the AGPL composition root.
type mcpDurableTaskStore struct {
	tenant     model.TenantID
	work       sessions.WorkKernel
	bindings   sessions.ProtocolBindingStore
	interrupts sessions.ProtocolInterruptCommunication
	specs      interface {
		GetProtocolBindingSpec(context.Context, model.TenantID, model.ID) (sessions.ProtocolBindingSpec, error)
	}
	config mcpDurableTaskStoreConfig
	actor  sessions.WorkPrincipal
}

var _ mcpc.DurableTaskStore = (*mcpDurableTaskStore)(nil)
var _ mcpc.DurableTaskInterruptStore = (*mcpDurableTaskStore)(nil)
var _ sessions.ProtocolBindingSpecValidator = (*mcpDurableTaskStore)(nil)

// newMCPDurableTaskStore is the production constructor used by mcpgateway.go.
// A sessions.Module supplies both required authorities; the split constructor
// below exists only to keep composition tests narrow.
func newMCPDurableTaskStore(
	tenant model.TenantID,
	kernel *sessions.Module,
	config mcpDurableTaskStoreConfig,
) (*mcpDurableTaskStore, error) {
	if kernel == nil {
		return nil, fmt.Errorf("mcp durable tasks: sessions kernel is unavailable")
	}
	result, err := newMCPDurableTaskStoreWithPorts(tenant, kernel, kernel, kernel, config)
	if err == nil {
		result.specs = kernel
	}
	return result, err
}

// ValidateProtocolBindingSpec proves an MCP successor against the active spec
// that backs this live durable Tasks Resource Server. The request cannot invent
// another peer, protocol version, resource, or binding lineage.
func (s *mcpDurableTaskStore) ValidateProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	input sessions.ProtocolBindingSpecInput,
) (sessions.ProtocolBindingValidation, error) {
	if s == nil || s.specs == nil || tenant != s.tenant ||
		input.WorkspaceID != s.config.WorkspaceID ||
		input.Protocol != sessions.BindingProtocolMCP ||
		input.Direction != sessions.BindingOutbound ||
		input.LocalKind != sessions.BindingLocalWorkItem ||
		!protocolRuntimePolicyMatches(input.RuleRefs, input.PermissionProfileRef, s.config.Policy) {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: mcp durable tasks spec is outside the configured Resource Server route",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	current, err := s.specs.GetProtocolBindingSpec(ctx, tenant, s.config.BindingSpecID)
	if err != nil {
		return sessions.ProtocolBindingValidation{}, err
	}
	if current.ID != s.config.BindingSpecID || current.TenantID != tenant ||
		current.Protocol != sessions.BindingProtocolMCP ||
		current.WorkspaceID != input.WorkspaceID || current.BindingKey != input.BindingKey ||
		current.ProtocolVersion != input.ProtocolVersion || current.Direction != input.Direction ||
		current.PeerAuthority != input.PeerAuthority ||
		current.RemoteResourceKind != input.RemoteResourceKind ||
		current.RemoteResourceRef != input.RemoteResourceRef ||
		!protocolConfiguredSpecLineage(current, input, s.config.Generation) {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: mcp durable tasks spec does not continue the configured active capability",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	return sessions.ProtocolBindingValidation{
		Verdict: sessions.ProtocolObservationClean, Code: "mcp_tasks_capability_validated",
		ObservedAt: time.Now().UTC(),
	}, nil
}

// bindUpstreamDescriptor installs the Resource Server's immutable effect
// target fingerprint before the store is exposed to requests. ProtocolBinding
// peer authority remains the operator-authored spec lineage; this separate
// value prevents a backend/credential re-point from replaying under that
// lineage without requiring the fingerprint itself to be a URI authority.
func (s *mcpDurableTaskStore) bindUpstreamDescriptor(value string) error {
	value = strings.TrimSpace(value)
	if s == nil || !boundedMCPValue(value, 1, 512) {
		return fmt.Errorf("mcp durable tasks: invalid upstream descriptor")
	}
	if s.config.UpstreamDescriptor != "" && s.config.UpstreamDescriptor != value {
		return fmt.Errorf("mcp durable tasks: upstream descriptor is already bound")
	}
	s.config.UpstreamDescriptor = value
	return nil
}

func newMCPDurableTaskStoreWithPorts(
	tenant model.TenantID,
	work sessions.WorkKernel,
	bindings sessions.ProtocolBindingStore,
	interrupts sessions.ProtocolInterruptCommunication,
	config mcpDurableTaskStoreConfig,
) (*mcpDurableTaskStore, error) {
	config.OwnerKind = strings.TrimSpace(config.OwnerKind)
	config.OwnerRef = strings.TrimSpace(config.OwnerRef)
	policy, policyErr := resolveProtocolRuntimePolicy(
		config.Policy.ruleRefs, config.Policy.permissionProfileRef, mcpTaskRuntimePolicy,
	)
	config.Policy = policy
	parsedTenant, tenantErr := model.ParseTenantID(tenant.String())
	parsedWorkspace, workspaceErr := model.ParseID(config.WorkspaceID.String())
	parsedSpec, specErr := model.ParseID(config.BindingSpecID.String())
	if tenantErr != nil || workspaceErr != nil || specErr != nil || policyErr != nil || tenant.IsZero() || tenant.IsSystem() ||
		parsedTenant != tenant || parsedWorkspace != config.WorkspaceID || parsedSpec != config.BindingSpecID ||
		work == nil || bindings == nil || interrupts == nil ||
		config.WorkspaceID.IsZero() || config.BindingSpecID.IsZero() || config.Generation < 1 ||
		(config.OwnerKind != "user" && config.OwnerKind != "agent" && config.OwnerKind != "session") ||
		config.OwnerRef == "" || len(config.OwnerRef) > 512 ||
		!validMCPProtocolInterruptRoute(config.InterruptRoute) {
		return nil, fmt.Errorf("mcp durable tasks: invalid local routing configuration")
	}
	return &mcpDurableTaskStore{
		tenant: tenant, work: work, bindings: bindings, interrupts: interrupts, config: config,
		actor: sessions.WorkPrincipal{
			ActorKind: model.ActorSystem,
			ActorRef:  "mcp-durable-task-adapter",
			Actor:     "system:mcp-durable-task-adapter",
			Admin:     true,
		},
	}, nil
}

func (s *mcpDurableTaskStore) Register(
	ctx context.Context,
	intent mcpc.DurableTaskIntent,
) (mcpc.DurableTaskRef, error) {
	if err := s.validateIntent(intent); err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	mapping, err := s.evaluateRegistrationMapping(ctx, intent)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	projection := mcpTaskProjection(intent)
	projectionJSON, projectionHash, err := mcpTaskProjectionEvidence(projection)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	ownerDigest, err := mcpTaskOwnerDigest(intent.Owner)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}

	semantic := intent.OriginOperationID + "\x00" + intent.OriginEffectDigest
	dispatchKey := workflowSemanticID("mcp-durable-task-dispatch", semantic)
	generation, replay, err := s.registrationGeneration(ctx, intent, projectionHash)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	if replay != nil {
		if replay.ObservationCode != "reserved_before_transmit" {
			return s.registeredTaskRef(ctx, *replay, intent)
		}
		return s.settleTaskRegistration(ctx, *replay, intent, projectionHash, dispatchKey)
	}

	work, err := s.resolveRegistrationWork(ctx, intent, projectionHash, semantic, mapping)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	reservation := sessions.ProtocolBindingReservation{
		WorkspaceID:           s.config.WorkspaceID,
		BindingSpecID:         s.config.BindingSpecID,
		BindingSpecGeneration: s.config.Generation,
		ExpectedDirection:     sessions.BindingOutbound,
		WorkItemID:            work.id,
		AttemptID:             model.ID(workflowSemanticID("mcp-durable-task-attempt", semantic)),
		DispatchKey:           dispatchKey,
		ExpectedExternalKind:  mcpDurableExternalKind,
		ExpectedExternalID:    intent.TaskID,
		Generation:            generation,
		OwnerKind:             s.config.OwnerKind,
		OwnerRef:              s.config.OwnerRef,
		OwnerDigest:           ownerDigest,
		OwnerEpoch:            work.ownerEpoch,
		LeaseFence:            work.leaseFence,
		MCPTask:               &projection,
		ProtocolMetadataJSON:  projectionJSON,
	}
	reserved, err := s.bindings.ReserveProtocolBinding(ctx, s.tenant, reservation)
	if err != nil {
		if errors.Is(err, sessions.ErrProtocolBindingConflict) {
			if work.created {
				if cancelErr := s.cancelConflictingWork(ctx, work, semantic); cancelErr != nil {
					return mcpc.DurableTaskRef{}, fmt.Errorf("%w: local work compensation failed: %v",
						mcpc.ErrDurableTaskConflict, cancelErr)
				}
			}
			return mcpc.DurableTaskRef{}, mcpc.ErrDurableTaskConflict
		}
		return mcpc.DurableTaskRef{}, fmt.Errorf("mcp durable tasks: reserve binding: %w", err)
	}
	if err := s.verifyReservedBinding(ctx, reserved, intent, projectionHash, work.id, generation); err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	if reserved.ExternalID != intent.TaskID {
		return mcpc.DurableTaskRef{}, mcpc.ErrDurableTaskConflict
	}
	// ExpectedExternalID claims the task identifier atomically during Reserve,
	// so a fresh reservation already carries ExternalID. Only a reservation
	// that has advanced beyond the pre-transmit state represents a completed
	// Register replay; a crash after Reserve must still be settled here.
	if reserved.Replayed && reserved.ObservationCode != "reserved_before_transmit" {
		return s.registeredTaskRef(ctx, reserved, intent)
	}
	return s.settleTaskRegistration(ctx, reserved, intent, projectionHash, dispatchKey)
}

func (s *mcpDurableTaskStore) evaluateRegistrationMapping(
	ctx context.Context,
	intent mcpc.DurableTaskIntent,
) (sessions.ProtocolMappingEvaluation, error) {
	if s.specs == nil {
		return sessions.ProtocolMappingEvaluation{}, fmt.Errorf("mcp durable tasks: binding spec reader is unavailable")
	}
	spec, err := s.specs.GetProtocolBindingSpec(ctx, s.tenant, s.config.BindingSpecID)
	if err != nil {
		return sessions.ProtocolMappingEvaluation{}, fmt.Errorf("mcp durable tasks: read binding spec: %w", err)
	}
	evaluation, err := sessions.EvaluateProtocolBindingMapping(spec, sessions.ProtocolBindingRuntimeExpectation{
		TenantID: s.tenant, WorkspaceID: s.config.WorkspaceID, SpecID: s.config.BindingSpecID,
		Generation: s.config.Generation, Protocol: sessions.BindingProtocolMCP,
		ProtocolVersion: intent.ProtocolVersion, Direction: sessions.BindingOutbound,
		LocalKind: sessions.BindingLocalWorkItem, PeerAuthority: spec.PeerAuthority,
		RemoteResourceKind: spec.RemoteResourceKind, RemoteResourceRef: spec.RemoteResourceRef,
		RuleRefs: s.config.Policy.ruleRefs, PermissionProfileRef: s.config.Policy.permissionProfileRef,
	}, protocolMCPTaskMappingSource(intent))
	if err != nil {
		return sessions.ProtocolMappingEvaluation{}, fmt.Errorf("mcp durable tasks: evaluate binding spec: %w", err)
	}
	return evaluation, nil
}

func (s *mcpDurableTaskStore) settleTaskRegistration(
	ctx context.Context,
	reserved sessions.ProtocolBinding,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
	dispatchKey string,
) (mcpc.DurableTaskRef, error) {
	workState, err := s.currentWorkState(ctx, reserved.WorkItemID)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}

	remoteState := strings.TrimSpace(intent.InitialStatus)
	if remoteState == "" {
		remoteState = "working"
	}
	localState, err := mcpTargetWorkState(workState, remoteState)
	if err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	settled, err := s.bindings.SettleProtocolBinding(ctx, s.tenant, sessions.ProtocolBindingSettlement{
		BindingID: reserved.ID, Generation: reserved.Generation,
		ExpectedVersion: reserved.Version, DispatchKey: dispatchKey,
		ResultKind: sessions.ProtocolBindingResultTask,
		ExternalID: intent.TaskID,
		LocalState: localState, RemoteState: remoteState,
		RemoteRevision: intent.ProtocolVersion,
		Verdict:        sessions.ProtocolObservationClean,
		Code:           "mcp_register", Observed: true,
		DetailHash: projectionHash,
		TTLMs:      cloneMCPInt64(intent.TTLMs), PollIntervalMs: cloneMCPInt64(intent.PollIntervalMs),
		Terminal: mcpTaskTerminal(remoteState),
	})
	if err != nil {
		if errors.Is(err, sessions.ErrProtocolBindingConflict) {
			bindings, reloadErr := s.loadBindings(ctx, intent.TaskID)
			if reloadErr == nil {
				current, currentErr := currentMCPBinding(bindings, reserved.Generation)
				if currentErr == nil && current != nil && current.ObservationCode != "reserved_before_transmit" &&
					s.verifySettledBinding(
						ctx, *current, intent, projectionHash, reserved.WorkItemID, reserved.Generation,
					) == nil {
					return s.registeredTaskRef(ctx, *current, intent)
				}
			}
			return mcpc.DurableTaskRef{}, mcpc.ErrDurableTaskConflict
		}
		return mcpc.DurableTaskRef{}, fmt.Errorf("mcp durable tasks: settle binding: %w", err)
	}
	if err := s.verifySettledBinding(
		ctx, settled, intent, projectionHash, reserved.WorkItemID, reserved.Generation,
	); err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	return s.registeredTaskRef(ctx, settled, intent)
}

func (s *mcpDurableTaskStore) registeredTaskRef(
	ctx context.Context,
	binding sessions.ProtocolBinding,
	intent mcpc.DurableTaskIntent,
) (mcpc.DurableTaskRef, error) {
	requests, err := normalizeMCPInputRefs(intent.InitialInputRequests, true)
	if err != nil {
		return mcpc.DurableTaskRef{}, fmt.Errorf("mcp durable tasks: invalid registration input requests: %w", err)
	}
	if err := s.recordProtocolInterrupts(
		ctx, binding, intent.InitialStatus, protocolInterruptRequestRefs(requests),
	); err != nil {
		return mcpc.DurableTaskRef{}, err
	}
	return mcpTaskRef(binding), nil
}

func (s *mcpDurableTaskStore) Get(
	ctx context.Context,
	owner mcpc.TaskOwner,
	taskID string,
	generation int64,
) (mcpc.DurableTaskView, error) {
	if err := s.validateOwner(owner, false); err != nil || !validMCPTaskID(taskID) || generation < 0 {
		return mcpc.DurableTaskView{}, mcpc.ErrDurableTaskNotFound
	}
	bindings, err := s.loadBindings(ctx, taskID)
	if err != nil {
		return mcpc.DurableTaskView{}, err
	}
	current, err := currentMCPBinding(bindings, generation)
	if err != nil {
		return mcpc.DurableTaskView{}, err
	}
	if current == nil || !mcpBindingOwnerMatches(*current, owner) {
		return mcpc.DurableTaskView{}, mcpc.ErrDurableTaskNotFound
	}
	return s.bindingView(*current)
}

func (s *mcpDurableTaskStore) PrepareInputResponses(
	ctx context.Context,
	owner mcpc.TaskOwner,
	batch mcpc.DurableTaskInputResponseBatch,
) error {
	if err := s.validateOwner(owner, false); err != nil || !validMCPTaskID(batch.TaskID) ||
		batch.Generation < 1 || !boundedMCPValue(batch.OperationID, 1, 512) ||
		!boundedMCPValue(batch.EffectDigest, 1, 512) {
		return fmt.Errorf("mcp durable tasks: invalid input response batch")
	}
	responses, err := normalizeMCPInputRefs(batch.Responses, false)
	if err != nil {
		return fmt.Errorf("mcp durable tasks: invalid input response batch: %w", err)
	}
	bindings, err := s.loadBindings(ctx, batch.TaskID)
	if err != nil {
		return err
	}
	binding, err := currentMCPBinding(bindings, batch.Generation)
	if err != nil {
		return err
	}
	if binding == nil || !mcpBindingOwnerMatches(*binding, owner) {
		return mcpc.ErrDurableTaskNotFound
	}
	if binding.Terminal {
		return fmt.Errorf("mcp durable tasks: terminal task cannot accept input responses")
	}
	return s.prepareProtocolInputResponses(
		ctx, *binding, batch.OperationID, batch.EffectDigest,
		protocolInputResponseRefs(responses),
	)
}

func (s *mcpDurableTaskStore) UpdateObservation(
	ctx context.Context,
	owner mcpc.TaskOwner,
	update mcpc.DurableTaskObservation,
) error {
	if err := s.validateOwner(owner, false); err != nil || !validMCPTaskID(update.TaskID) || update.Generation < 1 ||
		!boundedMCPValue(update.OperationID, 1, 512) || update.ObservedAt.IsZero() ||
		!boundedMCPValue(update.Status, 0, 128) || !boundedMCPValue(update.StatusReason, 0, 4096) ||
		!validMCPMillis(update.TTLMs) || !validMCPMillis(update.PollIntervalMs) {
		return fmt.Errorf("mcp durable tasks: invalid observation")
	}
	inputRequests, err := normalizeMCPInputRefs(update.InputRequests, true)
	if err != nil {
		return fmt.Errorf("mcp durable tasks: invalid observation input requests: %w", err)
	}
	bindings, err := s.loadBindings(ctx, update.TaskID)
	if err != nil {
		return err
	}
	binding, err := currentMCPBinding(bindings, update.Generation)
	if err != nil {
		return err
	}
	if binding == nil || !mcpBindingOwnerMatches(*binding, owner) {
		return mcpc.ErrDurableTaskNotFound
	}
	verdict, err := mcpObservationVerdict(update.Verdict)
	if err != nil {
		return err
	}
	kind, code, err := mcpObservationCode(update.Kind)
	if err != nil {
		return err
	}
	if kind == mcpc.DurableTaskObservationRegister {
		return fmt.Errorf("mcp durable tasks: Register observations are created only by Register")
	}
	remoteState := strings.TrimSpace(update.Status)
	if remoteState == "" {
		remoteState = binding.RemoteState
	}
	if len(inputRequests) > 0 && (verdict != sessions.ProtocolObservationClean ||
		remoteState != "input_required" ||
		(kind != mcpc.DurableTaskObservationGet && kind != mcpc.DurableTaskObservationUpdate)) {
		return fmt.Errorf("mcp durable tasks: input requests require a clean input_required task observation")
	}
	authoritativeGet := kind == mcpc.DurableTaskObservationGet && verdict == sessions.ProtocolObservationClean
	unexpectedCancel := authoritativeGet && mcpRemoteCancelled(remoteState) && !binding.CancelRequested
	if unexpectedCancel {
		verdict = sessions.ProtocolObservationBroken
		code = "unexpected_remote_cancel"
	}
	detailHash, err := mcpObservationDetailHash(update)
	if err != nil {
		return err
	}
	if binding.LastObservedAt != nil && kind == mcpc.DurableTaskObservationGet &&
		update.ObservedAt.Before(*binding.LastObservedAt) {
		if mcpObservationAlreadyApplied(*binding, code, verdict, update, detailHash) {
			return s.recordProtocolInterrupts(
				ctx, *binding, remoteState, protocolInterruptRequestRefs(inputRequests),
			)
		}
		return fmt.Errorf("mcp durable tasks: stale task observation")
	}
	workState, err := s.currentWorkState(ctx, binding.WorkItemID)
	if err != nil {
		return err
	}
	localState, err := mcpStableWorkState(workState)
	if err != nil {
		return err
	}
	if authoritativeGet {
		if binding.Terminal && remoteState != binding.RemoteState {
			return fmt.Errorf("mcp durable tasks: terminal task status cannot regress or change")
		}
		if unexpectedCancel || (binding.CancelRequested && !mcpTaskTerminal(remoteState)) {
			localState = "blocked"
		} else {
			localState, err = mcpTargetWorkState(workState, remoteState)
			if err != nil {
				return err
			}
		}
		if update.Terminal != mcpTaskTerminal(remoteState) {
			return fmt.Errorf("mcp durable tasks: terminal observation disagrees with task status")
		}
	} else if update.Terminal {
		return fmt.Errorf("mcp durable tasks: only a clean tasks/get observation can confirm terminal state")
	}
	if kind == mcpc.DurableTaskObservationCancel && update.CancelRequested && !update.Terminal {
		// RequestCancel moves protocol-owned work into a cancel-pending blocked
		// state. The acknowledgement is not terminal proof and must not reactivate
		// the synthetic execution lease before a clean tasks/get confirms it.
		localState = "blocked"
	}
	semanticMaterial := update.OperationID + "\x00" + update.TaskID + "\x00" +
		strconv.FormatInt(update.Generation, 10) + "\x00" + string(kind) + "\x00" + update.ResultDigest
	if update.CancelRequested && !binding.CancelRequested {
		cancelled, cancelErr := s.bindings.RequestProtocolBindingCancel(ctx, s.tenant,
			sessions.ProtocolBindingCancelIntent{
				BindingID: binding.ID, Generation: binding.Generation,
				ExpectedVersion: binding.Version,
				SemanticKey:     workflowSemanticID("mcp-durable-task-cancel", semanticMaterial),
				ReasonCode:      "mcp_cancel_requested",
			})
		if cancelErr != nil {
			if !errors.Is(cancelErr, sessions.ErrProtocolBindingConflict) {
				return fmt.Errorf("mcp durable tasks: request binding cancel: %w", cancelErr)
			}
			reloaded, reloadErr := s.loadBindings(ctx, update.TaskID)
			current, currentErr := currentMCPBinding(reloaded, update.Generation)
			if reloadErr != nil || currentErr != nil || current == nil ||
				!current.CancelRequested || !mcpBindingOwnerMatches(*current, owner) {
				return fmt.Errorf("mcp durable tasks: request binding cancel: %w", cancelErr)
			}
			cancelled = *current
		}
		binding = &cancelled
	}
	ttl, poll := binding.CurrentTTLMs, binding.CurrentPollIntervalMs
	if update.Kind == mcpc.DurableTaskObservationGet {
		ttl, poll = update.TTLMs, update.PollIntervalMs
	}
	observed, err := s.bindings.ObserveProtocolBinding(ctx, s.tenant, sessions.ProtocolBindingObservation{
		BindingID: binding.ID, Generation: binding.Generation,
		ExpectedVersion: binding.Version,
		SemanticKey:     workflowSemanticID("mcp-durable-task-observe", semanticMaterial),
		PeerAuthority:   binding.PeerAuthority,
		ExternalID:      binding.ExternalID,
		LocalState:      localState, RemoteState: remoteState,
		RemoteRevision: binding.RemoteRevision,
		Verdict:        verdict, Code: code,
		Observed:   verdict != sessions.ProtocolObservationUnknown,
		DetailHash: detailHash,
		TTLMs:      cloneMCPInt64(ttl), PollIntervalMs: cloneMCPInt64(poll),
		Terminal: update.Terminal,
	})
	if err != nil {
		if errors.Is(err, sessions.ErrProtocolBindingConflict) {
			reloaded, reloadErr := s.loadBindings(ctx, update.TaskID)
			current, currentErr := currentMCPBinding(reloaded, update.Generation)
			applied := update
			applied.Status = remoteState
			if reloadErr == nil && currentErr == nil && current != nil &&
				mcpBindingOwnerMatches(*current, owner) &&
				mcpObservationAlreadyApplied(*current, code, verdict, applied, detailHash) {
				return s.recordProtocolInterrupts(
					ctx, *current, remoteState, protocolInterruptRequestRefs(inputRequests),
				)
			}
		}
		return fmt.Errorf("mcp durable tasks: observe binding: %w", err)
	}
	return s.recordProtocolInterrupts(
		ctx, observed, remoteState, protocolInterruptRequestRefs(inputRequests),
	)
}

func (s *mcpDurableTaskStore) List(
	ctx context.Context,
	owner mcpc.TaskOwner,
	cursor string,
	limit int,
) (mcpc.DurableTaskPage, error) {
	if err := s.validateOwner(owner, true); err != nil || limit < 1 || limit > 1000 {
		return mcpc.DurableTaskPage{}, fmt.Errorf("mcp durable tasks: invalid inventory selector")
	}
	afterTaskID := ""
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		afterTaskID = string(decoded)
		if err != nil || !validMCPTaskID(afterTaskID) ||
			base64.RawURLEncoding.EncodeToString(decoded) != cursor {
			return mcpc.DurableTaskPage{}, fmt.Errorf("mcp durable tasks: invalid inventory cursor")
		}
	}
	bindings, err := s.loadBindings(ctx, "")
	if err != nil {
		return mcpc.DurableTaskPage{}, err
	}
	current, err := currentMCPInventory(bindings)
	if err != nil {
		return mcpc.DurableTaskPage{}, err
	}
	views := make([]mcpc.DurableTaskView, 0, len(current))
	for _, binding := range current {
		if owner.Issuer != "" && !mcpBindingOwnerMatches(binding, owner) {
			continue
		}
		view, err := s.bindingView(binding)
		if err != nil {
			return mcpc.DurableTaskPage{}, err
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Ref.TaskID != views[j].Ref.TaskID {
			return views[i].Ref.TaskID < views[j].Ref.TaskID
		}
		return views[i].Ref.Generation < views[j].Ref.Generation
	})
	start := sort.Search(len(views), func(i int) bool {
		return views[i].Ref.TaskID > afterTaskID
	})
	if afterTaskID == "" {
		start = 0
	}
	end := start + limit
	if end > len(views) {
		end = len(views)
	}
	page := mcpc.DurableTaskPage{Tasks: append([]mcpc.DurableTaskView(nil), views[start:end]...)}
	if end < len(views) {
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(views[end-1].Ref.TaskID))
	}
	return page, nil
}

func (s *mcpDurableTaskStore) recordProtocolInterrupts(
	ctx context.Context,
	binding sessions.ProtocolBinding,
	remoteState string,
	requests []sessions.ProtocolInterruptRequestRef,
) error {
	if len(requests) == 0 {
		return nil
	}
	result, err := s.interrupts.RecordProtocolInterrupt(ctx, s.tenant, sessions.ProtocolInterruptCommand{
		BindingID: binding.ID, Generation: binding.Generation,
		Route: s.config.InterruptRoute, RemoteState: remoteState,
		Requests: append([]sessions.ProtocolInterruptRequestRef(nil), requests...),
	})
	if err != nil {
		return fmt.Errorf("mcp durable tasks: record protocol interrupt: %w", err)
	}
	if result.BindingID != binding.ID || result.Generation != binding.Generation ||
		len(result.Messages) != len(requests) {
		return fmt.Errorf("mcp durable tasks: protocol interrupt result is incomplete")
	}
	want := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		want[request.KeyDigest] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Messages))
	for _, message := range result.Messages {
		_, expected := want[message.KeyDigest]
		_, duplicate := seen[message.KeyDigest]
		if !expected || duplicate || message.MessageID.IsZero() || message.DeliveryID.IsZero() {
			return fmt.Errorf("mcp durable tasks: protocol interrupt result crossed its request set")
		}
		seen[message.KeyDigest] = struct{}{}
	}
	return nil
}

func (s *mcpDurableTaskStore) prepareProtocolInputResponses(
	ctx context.Context,
	binding sessions.ProtocolBinding,
	operationID string,
	effectDigest string,
	responses []sessions.ProtocolInputResponseRef,
) error {
	result, err := s.interrupts.PrepareProtocolInputResponses(
		ctx, s.tenant, sessions.ProtocolInputResponseCommand{
			BindingID: binding.ID, Generation: binding.Generation,
			Route: s.config.InterruptRoute, OperationID: operationID, EffectDigest: effectDigest,
			Responses: append([]sessions.ProtocolInputResponseRef(nil), responses...),
		},
	)
	if err != nil {
		return fmt.Errorf("mcp durable tasks: prepare protocol input responses: %w", err)
	}
	if result.BindingID != binding.ID || result.Generation != binding.Generation ||
		len(result.Responses) != len(responses) {
		return fmt.Errorf("mcp durable tasks: protocol input response result is incomplete")
	}
	want := make(map[string]struct{}, len(responses))
	for _, response := range responses {
		want[response.KeyDigest] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Responses))
	for _, response := range result.Responses {
		_, expected := want[response.KeyDigest]
		_, duplicate := seen[response.KeyDigest]
		if !expected || duplicate || response.AckID.IsZero() || response.ResponseMessageID.IsZero() {
			return fmt.Errorf("mcp durable tasks: protocol input response result crossed its request set")
		}
		seen[response.KeyDigest] = struct{}{}
	}
	return nil
}

type mcpDurableRegistrationWork struct {
	id         model.ID
	version    int64
	ownerEpoch int64
	leaseFence int64
	created    bool
}

func (s *mcpDurableTaskStore) resolveRegistrationWork(
	ctx context.Context,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
	semantic string,
	mapping sessions.ProtocolMappingEvaluation,
) (mcpDurableRegistrationWork, error) {
	if s.config.WorkItemResolver != nil {
		workItemID, found, err := s.config.WorkItemResolver.ResolveMCPDurableWorkItem(
			ctx, s.tenant, mcpDurableWorkItemResolveRequest{
				WorkspaceID: s.config.WorkspaceID, TaskID: intent.TaskID, Tool: intent.Tool,
				OriginOperationID: intent.OriginOperationID, OriginEffectDigest: intent.OriginEffectDigest,
			},
		)
		if err != nil {
			return mcpDurableRegistrationWork{}, fmt.Errorf(
				"mcp durable tasks: resolve existing WorkItem: %w", err,
			)
		}
		if found {
			parsed, parseErr := model.ParseID(workItemID.String())
			if parseErr != nil || workItemID.IsZero() || parsed != workItemID {
				return mcpDurableRegistrationWork{}, fmt.Errorf(
					"mcp durable tasks: existing WorkItem resolver returned an invalid ID",
				)
			}
			snapshot, readErr := s.work.Get(ctx, s.tenant, s.actor, workItemID)
			if readErr != nil {
				return mcpDurableRegistrationWork{}, fmt.Errorf(
					"mcp durable tasks: read existing WorkItem: %w", readErr,
				)
			}
			item := snapshot.Item
			if item.ID != workItemID || item.WorkspaceID != s.config.WorkspaceID || item.Version < 1 ||
				item.OwnerKind != s.config.OwnerKind || item.OwnerRef != s.config.OwnerRef ||
				(item.Status != "ready" && item.Status != "active") || item.OwnerEpoch < 1 {
				return mcpDurableRegistrationWork{}, fmt.Errorf(
					"mcp durable tasks: existing WorkItem is outside the configured route or lifecycle",
				)
			}
			lease, leaseErr := s.work.GetLease(ctx, s.tenant, s.actor, workItemID)
			if leaseErr != nil {
				return mcpDurableRegistrationWork{}, fmt.Errorf(
					"mcp durable tasks: read existing WorkItem lease: %w", leaseErr,
				)
			}
			if lease.WorkItemID != workItemID || lease.WorkspaceID != s.config.WorkspaceID || lease.Fence < 0 {
				return mcpDurableRegistrationWork{}, fmt.Errorf(
					"mcp durable tasks: existing WorkItem lease is incomplete or mismatched",
				)
			}
			return mcpDurableRegistrationWork{
				id: workItemID, version: item.Version, ownerEpoch: item.OwnerEpoch,
				leaseFence: lease.Fence,
			}, nil
		}
		if !workItemID.IsZero() {
			return mcpDurableRegistrationWork{}, fmt.Errorf(
				"mcp durable tasks: existing WorkItem resolver returned an ID without authority",
			)
		}
	}
	return s.createReadyWork(ctx, projectionHash, semantic, mapping)
}

func (s *mcpDurableTaskStore) createReadyWork(
	ctx context.Context,
	projectionHash []byte,
	semantic string,
	mapping sessions.ProtocolMappingEvaluation,
) (mcpDurableRegistrationWork, error) {
	brief, err := requiredProtocolMappedString(mapping, "work.brief")
	if err != nil {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: %w", err)
	}
	title, err := optionalProtocolMappedString(mapping, "work.title")
	if err != nil {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: %w", err)
	}
	if title == "" {
		title = "MCP asynchronous task"
	}
	created, err := s.work.Apply(ctx, s.tenant, s.actor, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: s.config.WorkspaceID,
		WorkKind: "operations", Title: title, BriefMD: brief,
		ContextRefs: []sessions.ContextRef{{
			Kind: "protocol_mapping", Ref: mapping.EvidenceHash, Hash: mapping.MappingHash,
		}}, Priority: "p2",
		OwnerKind: s.config.OwnerKind, OwnerRef: s.config.OwnerRef,
		ProvenanceKind: "mcp", ProvenanceRef: "mcp-task:" + hex.EncodeToString(projectionHash),
		ProvenanceHash: hex.EncodeToString(projectionHash),
		Acceptance: []sessions.AcceptanceInput{{
			Key: "remote_result_review", Ordinal: 0,
			Statement: "Review the remote MCP task result against the governed tool operation.", Required: true,
		}},
		IdempotencyKey: workflowSemanticID("mcp-durable-task-create", semantic),
		CommandScope:   "mcp:durable-task:create", HTTPMethod: http.MethodPost,
	})
	if err != nil {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: create WorkItem: %w", err)
	}
	if created.ResultID.IsZero() || created.Version < 1 || created.Status != "draft" {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: WorkItem create returned an invalid result")
	}
	ready, err := s.work.Apply(ctx, s.tenant, s.actor, sessions.WorkCommand{
		Command: "item.ready", WorkspaceID: s.config.WorkspaceID, WorkItemID: created.ResultID,
		ExpectedVersion: created.Version,
		IdempotencyKey:  workflowSemanticID("mcp-durable-task-ready", semantic),
		CommandScope:    "mcp:durable-task:ready", HTTPMethod: http.MethodPost,
	})
	if err != nil {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: ready WorkItem: %w", err)
	}
	if ready.ResultID != created.ResultID || ready.Version < created.Version || ready.Status != "ready" || ready.OwnerEpoch < 1 {
		return mcpDurableRegistrationWork{}, fmt.Errorf("mcp durable tasks: WorkItem ready returned an invalid result")
	}
	return mcpDurableRegistrationWork{
		id: ready.ResultID, version: ready.Version, ownerEpoch: ready.OwnerEpoch,
		leaseFence: ready.LeaseFence, created: true,
	}, nil
}

func (s *mcpDurableTaskStore) cancelConflictingWork(
	ctx context.Context,
	work mcpDurableRegistrationWork,
	semantic string,
) error {
	_, err := s.work.Apply(ctx, s.tenant, s.actor, sessions.WorkCommand{
		Command: "item.cancel", WorkspaceID: s.config.WorkspaceID, WorkItemID: work.id,
		ExpectedVersion: work.version,
		Code:            "mcp_task_binding_conflict", Reason: "MCP task identifier is already bound to live work",
		IdempotencyKey: workflowSemanticID("mcp-durable-task-conflict", semantic),
		CommandScope:   "mcp:durable-task:conflict", HTTPMethod: http.MethodPost,
	})
	return err
}

func (s *mcpDurableTaskStore) currentWorkState(ctx context.Context, workItemID model.ID) (string, error) {
	snapshot, err := s.work.Get(ctx, s.tenant, s.actor, workItemID)
	if err != nil {
		return "", fmt.Errorf("mcp durable tasks: read WorkItem: %w", err)
	}
	item := snapshot.Item
	if item.ID != workItemID || item.WorkspaceID != s.config.WorkspaceID || item.Version < 1 ||
		item.OwnerKind != s.config.OwnerKind || item.OwnerRef != s.config.OwnerRef ||
		!validMCPWorkState(item.Status) {
		return "", fmt.Errorf("mcp durable tasks: WorkItem projection is incomplete or mismatched")
	}
	return item.Status, nil
}

func (s *mcpDurableTaskStore) registrationGeneration(
	ctx context.Context,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
) (int64, *sessions.ProtocolBinding, error) {
	bindings, err := s.loadBindings(ctx, intent.TaskID)
	if err != nil {
		return 0, nil, err
	}
	var (
		maxGeneration int64
		replay        *sessions.ProtocolBinding
		liveConflict  bool
	)
	for i := range bindings {
		binding := bindings[i]
		if binding.Generation > maxGeneration {
			maxGeneration = binding.Generation
		}
		if mcpBindingProjectionOrigin(binding, intent) {
			if err := s.verifyBindingProjection(binding, intent, projectionHash); err != nil {
				return 0, nil, err
			}
			if replay != nil && replay.ID != binding.ID {
				return 0, nil, fmt.Errorf("mcp durable tasks: duplicate registration origin")
			}
			bindingCopy := binding
			replay = &bindingCopy
			continue
		}
		if !binding.Terminal {
			liveConflict = true
		}
	}
	if liveConflict {
		return 0, nil, mcpc.ErrDurableTaskConflict
	}
	if replay != nil {
		return replay.Generation, replay, nil
	}
	if maxGeneration == math.MaxInt64 {
		return 0, nil, fmt.Errorf("mcp durable tasks: task generation exhausted")
	}
	return maxGeneration + 1, nil, nil
}

func (s *mcpDurableTaskStore) loadBindings(
	ctx context.Context,
	taskID string,
) ([]sessions.ProtocolBinding, error) {
	lineage, currentSpec, err := s.configuredMCPLineage(ctx)
	if err != nil {
		return nil, err
	}
	query := sessions.ProtocolBindingQuery{
		WorkspaceID: s.config.WorkspaceID,
		Protocol:    sessions.BindingProtocolMCP,
		// Successor generations share the same peer route. Do not constrain this
		// inventory to the currently configured spec ID: live tasks remain pinned
		// to the exact ancestor that created them.
		PeerAuthority: currentSpec.PeerAuthority,
		ExternalKind:  mcpDurableExternalKind,
		ExternalID:    taskID,
		Limit:         mcpDurablePageSize,
	}
	var out []sessions.ProtocolBinding
	seen := map[string]struct{}{"": {}}
	for pageNumber := 0; pageNumber < mcpDurableMaxPages; pageNumber++ {
		page, err := s.bindings.ListProtocolBindings(ctx, s.tenant, query)
		if err != nil {
			return nil, fmt.Errorf("mcp durable tasks: list bindings: %w", err)
		}
		if len(page.Items) > query.Limit {
			return nil, fmt.Errorf("mcp durable tasks: binding page exceeds its limit")
		}
		for _, binding := range page.Items {
			if binding.TenantID != s.tenant || binding.WorkspaceID != s.config.WorkspaceID ||
				binding.Protocol != sessions.BindingProtocolMCP ||
				binding.PeerAuthority != currentSpec.PeerAuthority ||
				binding.ExternalKind != mcpDurableExternalKind ||
				(taskID != "" && binding.ExternalID != taskID) {
				return nil, fmt.Errorf("mcp durable tasks: binding inventory escaped its selector")
			}
			spec, belongs := lineage[binding.BindingSpecID]
			if !belongs {
				// Another MCP route may use the same peer and task identifier. It is
				// outside this configured successor chain and cannot be hydrated here.
				continue
			}
			if err := s.verifyBindingBase(binding); err != nil {
				return nil, err
			}
			if err := verifyMCPBindingSpecPin(binding, spec); err != nil {
				return nil, err
			}
			out = append(out, binding)
		}
		next := strings.TrimSpace(page.NextCursor)
		if next == "" {
			if page.HasMore {
				return nil, fmt.Errorf("mcp durable tasks: binding inventory omitted its continuation cursor")
			}
			return out, nil
		}
		if !page.HasMore {
			return nil, fmt.Errorf("mcp durable tasks: binding inventory returned a spurious continuation cursor")
		}
		if _, duplicate := seen[next]; duplicate {
			return nil, fmt.Errorf("mcp durable tasks: binding inventory repeated a cursor")
		}
		seen[next] = struct{}{}
		query.Cursor = next
	}
	return nil, fmt.Errorf("mcp durable tasks: binding inventory exceeded its page bound")
}

// exactReconcileBinding re-reads and validates one REST-selected binding
// against the exact immutable spec generation it names. A configured successor
// does not invalidate an older live task, but an unrelated spec with the same
// peer authority is never admitted into this Resource Server's lineage.
func (s *mcpDurableTaskStore) exactReconcileBinding(
	ctx context.Context,
	bindingID model.ID,
) (sessions.ProtocolBinding, error) {
	if s == nil || bindingID.IsZero() {
		return sessions.ProtocolBinding{}, fmt.Errorf(
			"mcp durable tasks: invalid reconcile binding selector",
		)
	}
	binding, err := s.bindings.GetProtocolBinding(
		ctx, s.tenant, sessions.ProtocolBindingRef{ID: bindingID},
	)
	if err != nil {
		return sessions.ProtocolBinding{}, fmt.Errorf(
			"mcp durable tasks: read exact reconcile binding: %w", err,
		)
	}
	lineage, _, err := s.configuredMCPLineage(ctx)
	if err != nil {
		return sessions.ProtocolBinding{}, err
	}
	spec, belongs := lineage[binding.BindingSpecID]
	if !belongs {
		return sessions.ProtocolBinding{}, fmt.Errorf(
			"mcp durable tasks: reconcile binding is outside the configured successor lineage",
		)
	}
	if err := s.verifyBindingBase(binding); err != nil {
		return sessions.ProtocolBinding{}, err
	}
	if err := verifyMCPBindingSpecPin(binding, spec); err != nil {
		return sessions.ProtocolBinding{}, err
	}
	return binding, nil
}

func (s *mcpDurableTaskStore) configuredMCPLineage(
	ctx context.Context,
) (map[model.ID]sessions.ProtocolBindingSpec, sessions.ProtocolBindingSpec, error) {
	if s == nil || s.specs == nil {
		return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
			"mcp durable tasks: binding spec reader is unavailable",
		)
	}
	lineage := make(map[model.ID]sessions.ProtocolBindingSpec)
	seen := make(map[model.ID]struct{})
	next := s.config.BindingSpecID
	var configured sessions.ProtocolBindingSpec
	var child *sessions.ProtocolBindingSpec
	for depth := 0; depth < mcpDurableMaxPages; depth++ {
		if next.IsZero() {
			break
		}
		if _, duplicate := seen[next]; duplicate {
			return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
				"mcp durable tasks: binding spec successor chain contains a cycle",
			)
		}
		seen[next] = struct{}{}
		spec, err := s.specs.GetProtocolBindingSpec(ctx, s.tenant, next)
		if err != nil {
			return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
				"mcp durable tasks: read exact binding spec generation: %w", err,
			)
		}
		if err := s.verifyMCPLineageSpec(spec); err != nil {
			return nil, sessions.ProtocolBindingSpec{}, err
		}
		if configured.ID.IsZero() {
			configured = spec
			if spec.ID != s.config.BindingSpecID || spec.Generation != s.config.Generation ||
				spec.State != sessions.ProtocolBindingSpecActive {
				return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
					"mcp durable tasks: configured binding spec is not the exact active generation",
				)
			}
		} else if child == nil || child.SupersedesID != spec.ID ||
			spec.Generation >= child.Generation || spec.State != sessions.ProtocolBindingSpecSuperseded ||
			!sameMCPBindingRoute(configured, spec) {
			return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
				"mcp durable tasks: binding spec ancestor escaped its configured lineage",
			)
		}
		lineage[spec.ID] = spec
		specCopy := spec
		child = &specCopy
		if spec.SupersedesID.IsZero() {
			return lineage, configured, nil
		}
		next = spec.SupersedesID
	}
	return nil, sessions.ProtocolBindingSpec{}, fmt.Errorf(
		"mcp durable tasks: binding spec successor chain exceeded its bound",
	)
}

func (s *mcpDurableTaskStore) verifyMCPLineageSpec(spec sessions.ProtocolBindingSpec) error {
	if spec.ID.IsZero() || spec.TenantID != s.tenant || spec.WorkspaceID != s.config.WorkspaceID ||
		spec.Generation < 1 || spec.Protocol != sessions.BindingProtocolMCP ||
		spec.Direction != sessions.BindingOutbound || spec.LocalKind != sessions.BindingLocalWorkItem ||
		spec.CurrencyPolicy != sessions.BindingCurrencyPinned ||
		(spec.State != sessions.ProtocolBindingSpecActive && spec.State != sessions.ProtocolBindingSpecSuperseded) ||
		!protocolRuntimePolicyMatches(spec.RuleRefs, spec.PermissionProfileRef, s.config.Policy) {
		return fmt.Errorf("mcp durable tasks: binding spec is outside the configured route or policy")
	}
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: spec.WorkspaceID, BindingKey: spec.BindingKey, Generation: spec.Generation,
		Protocol: spec.Protocol, ProtocolVersion: spec.ProtocolVersion,
		Direction: spec.Direction, LocalKind: spec.LocalKind, LocalSelector: spec.LocalSelector,
		PeerAuthority: spec.PeerAuthority, RemoteResourceKind: spec.RemoteResourceKind,
		RemoteResourceRef: spec.RemoteResourceRef, MappingSchema: spec.MappingSchema,
		Mapping: spec.Mapping, KnownLosses: spec.KnownLosses, RuleRefs: spec.RuleRefs,
		PermissionProfileRef: spec.PermissionProfileRef, CurrencyPolicy: spec.CurrencyPolicy,
		Validation: spec.Validation, SupersedesID: spec.SupersedesID,
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(input)
	if err != nil || !bytes.Equal(spec.SpecHash, digests.SpecHash) ||
		!bytes.Equal(spec.MappingHash, digests.MappingHash) ||
		!bytes.Equal(spec.LossesHash, digests.LossesHash) {
		return fmt.Errorf("mcp durable tasks: binding spec digests are inconsistent")
	}
	return nil
}

func sameMCPBindingRoute(left, right sessions.ProtocolBindingSpec) bool {
	return left.BindingKey == right.BindingKey && left.Protocol == right.Protocol &&
		left.ProtocolVersion == right.ProtocolVersion && left.Direction == right.Direction &&
		left.LocalKind == right.LocalKind && left.PeerAuthority == right.PeerAuthority &&
		left.RemoteResourceKind == right.RemoteResourceKind &&
		left.RemoteResourceRef == right.RemoteResourceRef &&
		protocolRuntimePolicyMatches(right.RuleRefs, right.PermissionProfileRef, protocolRuntimePolicy{
			ruleRefs: left.RuleRefs, permissionProfileRef: left.PermissionProfileRef,
		})
}

func verifyMCPBindingSpecPin(
	binding sessions.ProtocolBinding,
	spec sessions.ProtocolBindingSpec,
) error {
	if binding.BindingSpecID != spec.ID || binding.BindingSpecGeneration != spec.Generation ||
		binding.ProtocolVersion != spec.ProtocolVersion || binding.Direction != sessions.BindingOutbound ||
		binding.PeerAuthority != spec.PeerAuthority || binding.RemoteResourceRef != spec.RemoteResourceRef ||
		!bytes.Equal(binding.PinnedSpecHash, spec.SpecHash) ||
		!bytes.Equal(binding.PinnedMappingHash, spec.MappingHash) ||
		!bytes.Equal(binding.PinnedLossesHash, spec.LossesHash) {
		return fmt.Errorf("mcp durable tasks: binding does not match its exact pinned spec generation")
	}
	return nil
}

func (s *mcpDurableTaskStore) bindingView(binding sessions.ProtocolBinding) (mcpc.DurableTaskView, error) {
	if err := s.verifyBindingBase(binding); err != nil {
		return mcpc.DurableTaskView{}, err
	}
	kind, err := mcpBindingObservationKind(binding.ObservationCode)
	if err != nil {
		return mcpc.DurableTaskView{}, err
	}
	verdict, err := mcpBindingVerdict(binding.ObservationVerdict)
	if err != nil {
		return mcpc.DurableTaskView{}, err
	}
	projection := binding.MCPTask
	owner := mcpc.TaskOwner{
		Tenant: s.tenant.String(), Issuer: projection.Owner.Issuer, Subject: projection.Owner.Subject,
		ActAs: projection.Owner.ActAs, ClientID: projection.Owner.ClientID,
		IsDelegated: projection.Owner.IsDelegated,
	}
	view := mcpc.DurableTaskView{
		Ref: mcpTaskRef(binding),
		Intent: mcpc.DurableTaskIntent{
			Owner: owner, TaskID: binding.ExternalID,
			Tool: projection.Tool, RequiredScope: projection.RequiredScope,
			Destructive: projection.Destructive, CreatedAt: projection.CreatedAt,
			TTLMs: cloneMCPInt64(projection.TTLMs), PollIntervalMs: cloneMCPInt64(projection.PollIntervalMs),
			InitialStatus: projection.InitialStatus,
			// Upstream status text is intentionally not reconstructed from durable state.
			InitialStatusReason:  "",
			UpstreamDescriptor:   projection.UpstreamDescriptor,
			ProtocolVersion:      projection.ProtocolRevision,
			OriginOperationID:    projection.OriginOperationID,
			OriginEffectDigest:   projection.OriginEffectDigest,
			InitialInputRequests: durableTaskInputRefs(projection.InitialInputRequests),
		},
		Observation: mcpc.DurableTaskObservation{
			TaskID: binding.ExternalID, Generation: binding.Generation,
			Kind:            kind,
			Status:          binding.RemoteState,
			TTLMs:           cloneMCPInt64(binding.CurrentTTLMs),
			PollIntervalMs:  cloneMCPInt64(binding.CurrentPollIntervalMs),
			Verdict:         verdict,
			ResultDigest:    hex.EncodeToString(binding.DetailHash),
			Dispatched:      binding.LastObservedAt != nil,
			Terminal:        binding.Terminal,
			CancelRequested: binding.CancelRequested,
		},
	}
	if binding.ObservationCode == "reserved_before_transmit" {
		// The upstream task identity was claimed, but Register did not yet commit
		// its observed settlement. Rehydrate from immutable intent without
		// inventing the transport's "unobserved" sentinel as an MCP task status.
		view.Observation.Status = ""
	}
	if err := s.validateIntent(view.Intent); err != nil {
		return mcpc.DurableTaskView{}, fmt.Errorf("mcp durable tasks: invalid durable intent projection: %w", err)
	}
	if binding.LastObservedAt != nil {
		view.Observation.ObservedAt = *binding.LastObservedAt
	}
	view.Observation.Acknowledged = view.Observation.Verdict == mcpc.DurableTaskVerdictClean &&
		(view.Observation.Kind == mcpc.DurableTaskObservationRegister ||
			view.Observation.Kind == mcpc.DurableTaskObservationUpdate ||
			view.Observation.Kind == mcpc.DurableTaskObservationCancel)
	return view, nil
}

func (s *mcpDurableTaskStore) validateIntent(intent mcpc.DurableTaskIntent) error {
	if err := s.validateOwner(intent.Owner, false); err != nil || !validMCPTaskID(intent.TaskID) ||
		!boundedMCPValue(intent.Tool, 1, 256) || !boundedMCPValue(intent.RequiredScope, 0, 256) ||
		intent.CreatedAt.IsZero() || !validMCPMillis(intent.TTLMs) || !validMCPMillis(intent.PollIntervalMs) ||
		!validMCPTaskStatus(intent.InitialStatus) || !boundedMCPValue(intent.UpstreamDescriptor, 1, 512) ||
		!boundedMCPValue(intent.ProtocolVersion, 1, 64) ||
		!boundedMCPValue(intent.OriginOperationID, 1, 512) ||
		!boundedMCPValue(intent.OriginEffectDigest, 1, 512) {
		return fmt.Errorf("mcp durable tasks: invalid registration intent")
	}
	if s.config.UpstreamDescriptor != "" &&
		intent.UpstreamDescriptor != s.config.UpstreamDescriptor {
		return fmt.Errorf("mcp durable tasks: registration changed the composed upstream descriptor")
	}
	requests, err := normalizeMCPInputRefs(intent.InitialInputRequests, true)
	if err != nil || len(requests) > 0 && intent.InitialStatus != "input_required" {
		return fmt.Errorf("mcp durable tasks: invalid registration input requests")
	}
	return nil
}

func (s *mcpDurableTaskStore) validateOwner(owner mcpc.TaskOwner, tenantOnly bool) error {
	if owner.Tenant != s.tenant.String() {
		return fmt.Errorf("mcp durable tasks: owner tenant mismatch")
	}
	if tenantOnly && owner.Issuer == "" && owner.Subject == "" && owner.ActAs == "" && owner.ClientID == "" && !owner.IsDelegated {
		return nil
	}
	if !boundedMCPValue(owner.Issuer, 1, 512) || !boundedMCPValue(owner.Subject, 1, 512) ||
		!boundedMCPValue(owner.ActAs, 0, 512) || !boundedMCPValue(owner.ClientID, 0, 512) ||
		owner.IsDelegated != (owner.ActAs != "") {
		return fmt.Errorf("mcp durable tasks: incomplete owner")
	}
	return nil
}

func (s *mcpDurableTaskStore) verifyReservedBinding(
	ctx context.Context,
	binding sessions.ProtocolBinding,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
	workItemID model.ID,
	generation int64,
) error {
	if err := s.verifyBindingProjection(binding, intent, projectionHash); err != nil {
		return err
	}
	lineage, configured, err := s.configuredMCPLineage(ctx)
	if err != nil {
		return err
	}
	if _, exists := lineage[binding.BindingSpecID]; !exists || binding.BindingSpecID != configured.ID ||
		binding.BindingSpecGeneration != configured.Generation {
		return fmt.Errorf("mcp durable tasks: reserved binding did not use the configured active generation")
	}
	if err := verifyMCPBindingSpecPin(binding, configured); err != nil {
		return err
	}
	if binding.WorkItemID != workItemID || binding.Generation != generation || binding.ExternalKind != mcpDurableExternalKind ||
		binding.ExternalID != "" && binding.ExternalID != intent.TaskID {
		return fmt.Errorf("mcp durable tasks: reserved binding does not match its registration")
	}
	return nil
}

func (s *mcpDurableTaskStore) verifySettledBinding(
	ctx context.Context,
	binding sessions.ProtocolBinding,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
	workItemID model.ID,
	generation int64,
) error {
	if err := s.verifyReservedBinding(ctx, binding, intent, projectionHash, workItemID, generation); err != nil {
		return err
	}
	if binding.ExternalID != intent.TaskID || binding.RemoteState == "" {
		return fmt.Errorf("mcp durable tasks: settled binding is incomplete")
	}
	return nil
}

func (s *mcpDurableTaskStore) verifyBindingProjection(
	binding sessions.ProtocolBinding,
	intent mcpc.DurableTaskIntent,
	projectionHash []byte,
) error {
	if err := s.verifyBindingBase(binding); err != nil {
		return err
	}
	if !bytes.Equal(binding.MCPTaskHash, projectionHash) || !mcpProjectionMatchesIntent(*binding.MCPTask, intent) {
		return fmt.Errorf("mcp durable tasks: binding projection does not match its registration intent")
	}
	return nil
}

func (s *mcpDurableTaskStore) verifyBindingBase(binding sessions.ProtocolBinding) error {
	if binding.ID.IsZero() || binding.Version < 1 || binding.TenantID != s.tenant ||
		binding.WorkspaceID != s.config.WorkspaceID || binding.BindingSpecID.IsZero() ||
		binding.BindingSpecGeneration < 1 || binding.WorkItemID.IsZero() ||
		binding.Protocol != sessions.BindingProtocolMCP || binding.Generation < 1 ||
		strings.TrimSpace(binding.SyntheticSID) == "" || binding.ExternalKind != mcpDurableExternalKind ||
		!validMCPTaskID(binding.ExternalID) || binding.OwnerKind != s.config.OwnerKind ||
		binding.OwnerRef != s.config.OwnerRef || len(binding.OwnerDigest) != sha256.Size ||
		len(binding.PinnedSpecHash) != sha256.Size || len(binding.PinnedMappingHash) != sha256.Size ||
		len(binding.PinnedLossesHash) != sha256.Size || binding.MCPTask == nil ||
		len(binding.MCPTaskHash) != sha256.Size || len(binding.ProtocolMetadataJSON) == 0 {
		return fmt.Errorf("mcp durable tasks: durable binding is incomplete or mismatched")
	}
	if binding.MCPTask.ProtocolRevision != binding.ProtocolVersion ||
		s.config.UpstreamDescriptor != "" &&
			binding.MCPTask.UpstreamDescriptor != s.config.UpstreamDescriptor {
		return fmt.Errorf("mcp durable tasks: durable binding route projection is inconsistent")
	}
	inputRequests := durableTaskInputRefs(binding.MCPTask.InitialInputRequests)
	normalizedRequests, err := normalizeMCPInputRefs(inputRequests, true)
	if err != nil || !equalMCPInputRefs(inputRequests, normalizedRequests) ||
		len(inputRequests) > 0 && binding.MCPTask.InitialStatus != "input_required" {
		return fmt.Errorf("mcp durable tasks: durable binding input requests are inconsistent")
	}
	canonical, hash, err := mcpTaskProjectionEvidence(*binding.MCPTask)
	if err != nil || !bytes.Equal(hash, binding.MCPTaskHash) || !bytes.Equal(canonical, binding.ProtocolMetadataJSON) {
		return fmt.Errorf("mcp durable tasks: durable binding projection is inconsistent")
	}
	ownerDigest, err := mcpTaskOwnerDigest(mcpc.TaskOwner{
		Tenant: s.tenant.String(), Issuer: binding.MCPTask.Owner.Issuer,
		Subject: binding.MCPTask.Owner.Subject, ActAs: binding.MCPTask.Owner.ActAs,
		ClientID: binding.MCPTask.Owner.ClientID, IsDelegated: binding.MCPTask.Owner.IsDelegated,
	})
	if err != nil || !bytes.Equal(ownerDigest, binding.OwnerDigest) {
		return fmt.Errorf("mcp durable tasks: durable binding owner projection is inconsistent")
	}
	return nil
}

func mcpTaskProjection(intent mcpc.DurableTaskIntent) sessions.ProtocolMCPTaskProjection {
	inputRequests, _ := normalizeMCPInputRefs(intent.InitialInputRequests, true)
	return sessions.ProtocolMCPTaskProjection{
		Owner: sessions.ProtocolMCPTaskOwner{
			Subject: intent.Owner.Subject, IsDelegated: intent.Owner.IsDelegated,
			ActAs: intent.Owner.ActAs, Issuer: intent.Owner.Issuer, ClientID: intent.Owner.ClientID,
		},
		Tool: intent.Tool, RequiredScope: intent.RequiredScope, Destructive: intent.Destructive,
		CreatedAt: intent.CreatedAt.UTC(), TTLMs: cloneMCPInt64(intent.TTLMs),
		PollIntervalMs: cloneMCPInt64(intent.PollIntervalMs),
		InitialStatus:  intent.InitialStatus,
		// Persist a stable status and evidence hashes, never upstream status text.
		InitialStatusReason: "",
		UpstreamDescriptor:  intent.UpstreamDescriptor, ProtocolRevision: intent.ProtocolVersion,
		OriginOperationID: intent.OriginOperationID, OriginEffectDigest: intent.OriginEffectDigest,
		InitialInputRequests: protocolInterruptRequestRefs(inputRequests),
	}
}

func mcpTaskProjectionEvidence(projection sessions.ProtocolMCPTaskProjection) (json.RawMessage, []byte, error) {
	raw, err := json.Marshal(projection)
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, nil, err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		return nil, nil, err
	}
	canonical := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	sum := sha256.Sum256(canonical)
	return append(json.RawMessage(nil), canonical...), append([]byte(nil), sum[:]...), nil
}

func mcpTaskOwnerDigest(owner mcpc.TaskOwner) ([]byte, error) {
	canonical, err := json.Marshal(struct {
		Tenant      string `json:"tenant"`
		Issuer      string `json:"issuer"`
		Subject     string `json:"subject"`
		ActAs       string `json:"act_as"`
		ClientID    string `json:"client_id"`
		IsDelegated bool   `json:"is_delegated"`
	}{owner.Tenant, owner.Issuer, owner.Subject, owner.ActAs, owner.ClientID, owner.IsDelegated})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append([]byte("olivares.mcp.task-owner.v1\x00"), canonical...))
	return append([]byte(nil), sum[:]...), nil
}

func mcpBindingOwnerMatches(binding sessions.ProtocolBinding, owner mcpc.TaskOwner) bool {
	if binding.MCPTask == nil {
		return false
	}
	want, err := mcpTaskOwnerDigest(owner)
	if err != nil || !bytes.Equal(want, binding.OwnerDigest) {
		return false
	}
	got := binding.MCPTask.Owner
	return got.Issuer == owner.Issuer && got.Subject == owner.Subject && got.ActAs == owner.ActAs &&
		got.ClientID == owner.ClientID && got.IsDelegated == owner.IsDelegated
}

func mcpBindingProjectionOrigin(binding sessions.ProtocolBinding, intent mcpc.DurableTaskIntent) bool {
	return binding.MCPTask != nil && binding.MCPTask.OriginOperationID == intent.OriginOperationID &&
		binding.MCPTask.OriginEffectDigest == intent.OriginEffectDigest
}

func mcpProjectionMatchesIntent(projection sessions.ProtocolMCPTaskProjection, intent mcpc.DurableTaskIntent) bool {
	inputRequests, err := normalizeMCPInputRefs(intent.InitialInputRequests, true)
	if err != nil || !equalMCPInputRefs(durableTaskInputRefs(projection.InitialInputRequests), inputRequests) {
		return false
	}
	return projection.Owner.Issuer == intent.Owner.Issuer && projection.Owner.Subject == intent.Owner.Subject &&
		projection.Owner.ActAs == intent.Owner.ActAs && projection.Owner.ClientID == intent.Owner.ClientID &&
		projection.Owner.IsDelegated == intent.Owner.IsDelegated && projection.Tool == intent.Tool &&
		projection.RequiredScope == intent.RequiredScope && projection.Destructive == intent.Destructive &&
		projection.CreatedAt.Equal(intent.CreatedAt) && equalMCPInt64(projection.TTLMs, intent.TTLMs) &&
		equalMCPInt64(projection.PollIntervalMs, intent.PollIntervalMs) &&
		projection.InitialStatus == intent.InitialStatus && projection.UpstreamDescriptor == intent.UpstreamDescriptor &&
		projection.ProtocolRevision == intent.ProtocolVersion && projection.OriginOperationID == intent.OriginOperationID &&
		projection.OriginEffectDigest == intent.OriginEffectDigest
}

func currentMCPBinding(bindings []sessions.ProtocolBinding, generation int64) (*sessions.ProtocolBinding, error) {
	var current *sessions.ProtocolBinding
	for i := range bindings {
		binding := bindings[i]
		if generation > 0 && binding.Generation != generation {
			continue
		}
		if current == nil || binding.Generation > current.Generation {
			bindingCopy := binding
			current = &bindingCopy
			continue
		}
		if binding.Generation == current.Generation && binding.ID != current.ID {
			return nil, fmt.Errorf("mcp durable tasks: duplicate binding generation")
		}
	}
	return current, nil
}

func currentMCPInventory(bindings []sessions.ProtocolBinding) ([]sessions.ProtocolBinding, error) {
	byTask := make(map[string]sessions.ProtocolBinding)
	for _, binding := range bindings {
		if !validMCPTaskID(binding.ExternalID) {
			return nil, fmt.Errorf("mcp durable tasks: inventory binding has no task ID")
		}
		current, exists := byTask[binding.ExternalID]
		switch {
		case !exists || binding.Generation > current.Generation:
			byTask[binding.ExternalID] = binding
		case binding.Generation == current.Generation && binding.ID != current.ID:
			return nil, fmt.Errorf("mcp durable tasks: inventory has duplicate current generations")
		}
	}
	out := make([]sessions.ProtocolBinding, 0, len(byTask))
	for _, binding := range byTask {
		out = append(out, binding)
	}
	return out, nil
}

func mcpTaskRef(binding sessions.ProtocolBinding) mcpc.DurableTaskRef {
	return mcpc.DurableTaskRef{
		TaskID: binding.ExternalID, Generation: binding.Generation,
		BindingID: binding.ID.String(), WorkItemID: binding.WorkItemID.String(), SID: binding.SyntheticSID,
	}
}

func mcpObservationVerdict(verdict mcpc.DurableTaskVerdict) (sessions.ProtocolObservationVerdict, error) {
	switch verdict {
	case mcpc.DurableTaskVerdictClean:
		return sessions.ProtocolObservationClean, nil
	case mcpc.DurableTaskVerdictBroken:
		return sessions.ProtocolObservationBroken, nil
	case mcpc.DurableTaskVerdictUnobservable:
		return sessions.ProtocolObservationUnknown, nil
	default:
		return "", fmt.Errorf("mcp durable tasks: unknown observation verdict")
	}
}

func mcpBindingVerdict(verdict sessions.ProtocolObservationVerdict) (mcpc.DurableTaskVerdict, error) {
	switch verdict {
	case sessions.ProtocolObservationClean:
		return mcpc.DurableTaskVerdictClean, nil
	case sessions.ProtocolObservationBroken:
		return mcpc.DurableTaskVerdictBroken, nil
	case sessions.ProtocolObservationUnknown:
		return mcpc.DurableTaskVerdictUnobservable, nil
	}
	return "", fmt.Errorf("mcp durable tasks: durable binding has an unknown observation verdict")
}

func mcpObservationCode(kind mcpc.DurableTaskObservationKind) (mcpc.DurableTaskObservationKind, string, error) {
	switch kind {
	case mcpc.DurableTaskObservationRegister:
		return kind, "mcp_register", nil
	case mcpc.DurableTaskObservationGet:
		return kind, "mcp_get", nil
	case mcpc.DurableTaskObservationUpdate:
		return kind, "mcp_update", nil
	case mcpc.DurableTaskObservationCancel:
		return kind, "mcp_cancel", nil
	default:
		return "", "", fmt.Errorf("mcp durable tasks: unknown observation kind")
	}
}

func mcpBindingObservationKind(code string) (mcpc.DurableTaskObservationKind, error) {
	switch code {
	case "mcp_register", "reserved_before_transmit":
		return mcpc.DurableTaskObservationRegister, nil
	case "mcp_get", "unexpected_remote_cancel":
		return mcpc.DurableTaskObservationGet, nil
	case "mcp_update":
		return mcpc.DurableTaskObservationUpdate, nil
	case "mcp_cancel", "cancel_requested_unobserved":
		return mcpc.DurableTaskObservationCancel, nil
	}
	return "", fmt.Errorf("mcp durable tasks: durable binding has an unknown observation code")
}

func mcpObservationDetailHash(update mcpc.DurableTaskObservation) ([]byte, error) {
	if update.ResultDigest != "" {
		digest, err := hex.DecodeString(update.ResultDigest)
		if err != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf("mcp durable tasks: invalid result digest")
		}
		return digest, nil
	}
	if update.StatusReason == "" {
		return nil, nil
	}
	sum := sha256.Sum256([]byte("olivares.mcp.task-observation-detail.v1\x00" + update.StatusReason))
	return append([]byte(nil), sum[:]...), nil
}

func validMCPTaskID(value string) bool {
	return boundedMCPValue(value, 1, 512) && strings.TrimSpace(value) == value
}

func validMCPSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == value
}

func normalizeMCPInputRefs(
	refs []mcpc.DurableTaskInputRef,
	allowEmpty bool,
) ([]mcpc.DurableTaskInputRef, error) {
	if len(refs) == 0 {
		if allowEmpty {
			return nil, nil
		}
		return nil, fmt.Errorf("input reference set is empty")
	}
	if len(refs) > 64 {
		return nil, fmt.Errorf("input reference set exceeds its bound")
	}
	result := append([]mcpc.DurableTaskInputRef(nil), refs...)
	sort.Slice(result, func(i, j int) bool { return result[i].KeyDigest < result[j].KeyDigest })
	for index := range result {
		if !validMCPSHA256(result[index].KeyDigest) || !validMCPSHA256(result[index].ContentDigest) {
			return nil, fmt.Errorf("input reference is not a canonical SHA-256 pair")
		}
		if index > 0 && result[index-1].KeyDigest == result[index].KeyDigest {
			return nil, fmt.Errorf("input reference key is duplicated")
		}
	}
	return result, nil
}

func protocolInterruptRequestRefs(
	refs []mcpc.DurableTaskInputRef,
) []sessions.ProtocolInterruptRequestRef {
	result := make([]sessions.ProtocolInterruptRequestRef, len(refs))
	for index, ref := range refs {
		result[index] = sessions.ProtocolInterruptRequestRef{
			KeyDigest: ref.KeyDigest, ContentDigest: ref.ContentDigest,
		}
	}
	return result
}

func protocolInputResponseRefs(
	refs []mcpc.DurableTaskInputRef,
) []sessions.ProtocolInputResponseRef {
	result := make([]sessions.ProtocolInputResponseRef, len(refs))
	for index, ref := range refs {
		result[index] = sessions.ProtocolInputResponseRef{
			KeyDigest: ref.KeyDigest, ResponseDigest: ref.ContentDigest,
		}
	}
	return result
}

func durableTaskInputRefs(
	refs []sessions.ProtocolInterruptRequestRef,
) []mcpc.DurableTaskInputRef {
	result := make([]mcpc.DurableTaskInputRef, len(refs))
	for index, ref := range refs {
		result[index] = mcpc.DurableTaskInputRef{
			KeyDigest: ref.KeyDigest, ContentDigest: ref.ContentDigest,
		}
	}
	return result
}

func equalMCPInputRefs(left, right []mcpc.DurableTaskInputRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validMCPProtocolInterruptRoute(route sessions.ProtocolInterruptRoute) bool {
	ids := []model.ID{route.ChannelID, route.SenderUserID, route.RecipientUserID}
	for _, id := range ids {
		parsed, err := model.ParseID(id.String())
		if err != nil || id.IsZero() || parsed != id {
			return false
		}
	}
	return route.SenderUserID != route.RecipientUserID
}

func validMCPTaskStatus(status string) bool {
	switch status {
	case "working", "input_required", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func validMCPWorkState(status string) bool {
	switch status {
	case "draft", "ready", "active", "blocked", "review", "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func mcpTaskTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "canceled"
}

func mcpRemoteCancelled(status string) bool {
	return status == "cancelled" || status == "canceled"
}

func mcpTargetWorkState(current, remote string) (string, error) {
	stable, err := mcpStableWorkState(current)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(remote)) {
	case "working", "submitted":
		return "active", nil
	case "completed":
		if stable == "canceled" {
			return stable, nil
		}
		return "review", nil
	case "failed", "rejected", "input_required", "auth_required", "authorization_required":
		if stable == "canceled" {
			return stable, nil
		}
		return "blocked", nil
	case "cancelled", "canceled":
		return "canceled", nil
	default:
		return "", fmt.Errorf("mcp durable tasks: unknown remote task status")
	}
}

func mcpStableWorkState(status string) (string, error) {
	switch status {
	case "ready", "active":
		return "active", nil
	case "review", "blocked", "canceled":
		return status, nil
	case "completed":
		return "review", nil
	case "failed":
		return "blocked", nil
	default:
		return "", fmt.Errorf("mcp durable tasks: WorkItem is outside the protocol lifecycle")
	}
}

func mcpObservationAlreadyApplied(
	binding sessions.ProtocolBinding,
	code string,
	verdict sessions.ProtocolObservationVerdict,
	update mcpc.DurableTaskObservation,
	detailHash []byte,
) bool {
	if binding.ObservationCode != code || binding.ObservationVerdict != verdict ||
		binding.RemoteState != strings.TrimSpace(update.Status) ||
		!bytes.Equal(binding.DetailHash, detailHash) || binding.Terminal != update.Terminal {
		return false
	}
	if update.Kind == mcpc.DurableTaskObservationGet {
		return equalMCPInt64(binding.CurrentTTLMs, update.TTLMs) &&
			equalMCPInt64(binding.CurrentPollIntervalMs, update.PollIntervalMs)
	}
	return !update.CancelRequested || binding.CancelRequested
}

func boundedMCPValue(value string, minLen, maxLen int) bool {
	return len(value) >= minLen && len(value) <= maxLen && utf8.ValidString(value) && strings.TrimSpace(value) == value
}

func validMCPMillis(value *int64) bool {
	const maxDurableTaskMillis = int64((30 * 24 * time.Hour) / time.Millisecond)
	return value == nil || *value > 0 && *value <= maxDurableTaskMillis
}

func cloneMCPInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	valueCopy := *value
	return &valueCopy
}

func equalMCPInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
