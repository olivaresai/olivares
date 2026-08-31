// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type a2aInboundKernel interface {
	sessions.ProtocolReplayStore
	sessions.ProtocolReplyCommunication
	Apply(context.Context, model.TenantID, sessions.WorkPrincipal, sessions.WorkCommand) (sessions.CommandResult, error)
	Get(context.Context, model.TenantID, sessions.WorkPrincipal, model.ID) (sessions.WorkSnapshot, error)
	ReserveProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingReservation) (sessions.ProtocolBinding, error)
	SettleProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingSettlement) (sessions.ProtocolBinding, error)
	ObserveProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingObservation) (sessions.ProtocolBinding, error)
	RequestProtocolBindingCancel(context.Context, model.TenantID, sessions.ProtocolBindingCancelIntent) (sessions.ProtocolBinding, error)
	GetProtocolBinding(context.Context, model.TenantID, sessions.ProtocolBindingRef) (sessions.ProtocolBinding, error)
	GetProtocolBindingSpec(context.Context, model.TenantID, model.ID) (sessions.ProtocolBindingSpec, error)
}

type parsedA2AInboundRoute struct {
	tenant                model.TenantID
	workspace             model.ID
	bindingSpec           model.ID
	bindingSpecGeneration int64
	ownerKind             string
	ownerRef              string
	workKind              string
	priority              string
	policy                protocolRuntimePolicy
	message               sessions.ProtocolInterruptRoute
}

// a2aInboundRouter is the composition-owned translation from an authenticated
// A2A message to the Work/ProtocolBinding authorities. The remote metadata is
// provenance only; every local routing dimension comes from operator config.
type a2aInboundRouter struct {
	kernel a2aInboundKernel
	routes map[string]parsedA2AInboundRoute
}

func buildA2AInboundServer(eng *engine, cfg *a2aInboundConfig) (*a2a.InboundServer, error) {
	if eng == nil || eng.sessionsMod == nil {
		return nil, fmt.Errorf("a2a inbound: sessions kernel is unavailable")
	}
	router, err := newA2AInboundRouter(eng.sessionsMod, cfg.Routes, cfg.AllowedIssuers)
	if err != nil {
		return nil, err
	}
	server, err := a2a.NewInboundServer(a2a.InboundServerConfig{
		Audience:                 cfg.Audience,
		IssuerJWKS:               []byte(cfg.IssuerJWKS),
		JWKSURL:                  cfg.JWKSURL,
		AllowedIssuers:           cfg.AllowedIssuers,
		InterfaceTenant:          cfg.InterfaceTenant,
		RequireClientAttestation: cfg.RequireClientAttestation,
		AttesterJWKS:             []byte(cfg.AttesterJWKS),
		Router:                   router,
		DurableReplay:            true,
	})
	if err != nil {
		return nil, err
	}
	eng.sessionsMod.AddProtocolBindingSpecValidator(sessions.BindingProtocolA2A, router)
	return server, nil
}

func newA2AInboundRouter(
	kernel a2aInboundKernel,
	configs []a2aInboundRouteConfig,
	allowedIssuers []string,
) (*a2aInboundRouter, error) {
	if kernel == nil || len(configs) == 0 {
		return nil, fmt.Errorf("a2a inbound: at least one durable route is required")
	}
	allowed := make(map[string]struct{}, len(allowedIssuers))
	for _, issuer := range allowedIssuers {
		issuer = strings.TrimSpace(issuer)
		if issuer != "" {
			allowed[issuer] = struct{}{}
		}
	}
	routes := make(map[string]parsedA2AInboundRoute, len(configs))
	for i, raw := range configs {
		authority := strings.TrimSpace(raw.PeerAuthority)
		if authority == "" {
			return nil, fmt.Errorf("a2a inbound: route %d has no peer_authority", i)
		}
		if _, trusted := allowed[authority]; !trusted {
			return nil, fmt.Errorf("a2a inbound: route %d peer_authority is not allowlisted", i)
		}
		if _, duplicate := routes[authority]; duplicate {
			return nil, fmt.Errorf("a2a inbound: duplicate peer_authority %q", authority)
		}
		tenant, _, err := parseBusinessTenant("a2a inbound route tenant", raw.Tenant)
		if err != nil || tenant.IsZero() {
			return nil, fmt.Errorf("a2a inbound: route %d has an invalid tenant", i)
		}
		workspace, err := model.ParseID(strings.TrimSpace(raw.WorkspaceID))
		if err != nil || workspace.IsZero() {
			return nil, fmt.Errorf("a2a inbound: route %d has an invalid workspace_id", i)
		}
		specID, err := model.ParseID(strings.TrimSpace(raw.BindingSpecID))
		if err != nil || specID.IsZero() || raw.BindingSpecGeneration < 1 {
			return nil, fmt.Errorf("a2a inbound: route %d has an invalid binding spec", i)
		}
		ownerKind := strings.TrimSpace(raw.OwnerKind)
		if (ownerKind != "user" && ownerKind != "agent" && ownerKind != "session") || strings.TrimSpace(raw.OwnerRef) == "" {
			return nil, fmt.Errorf("a2a inbound: route %d has an invalid owner", i)
		}
		messageRoute, err := parseProtocolInterruptRoute(
			"a2a inbound route", raw.ChannelID, raw.SenderUserID, raw.RecipientUserID,
		)
		if err != nil {
			return nil, fmt.Errorf("a2a inbound: route %d has invalid Message route: %w", i, err)
		}
		workKind := strings.TrimSpace(raw.WorkKind)
		if workKind == "" {
			workKind = "operations"
		}
		priority := strings.TrimSpace(raw.Priority)
		if priority == "" {
			priority = "p2"
		}
		policy, err := resolveProtocolRuntimePolicy(
			raw.ProtocolRuleRefs, raw.ProtocolPermissionProfileRef, a2aInboundRuntimePolicy,
		)
		if err != nil {
			return nil, fmt.Errorf("a2a inbound: route %d has invalid protocol policy: %w", i, err)
		}
		routes[authority] = parsedA2AInboundRoute{
			tenant: tenant, workspace: workspace, bindingSpec: specID,
			bindingSpecGeneration: raw.BindingSpecGeneration,
			ownerKind:             ownerKind, ownerRef: strings.TrimSpace(raw.OwnerRef),
			workKind: workKind, priority: priority, policy: policy, message: messageRoute,
		}
	}
	return &a2aInboundRouter{kernel: kernel, routes: routes}, nil
}

var _ sessions.ProtocolBindingSpecValidator = (*a2aInboundRouter)(nil)
var _ a2a.InboundTaskRouter = (*a2aInboundRouter)(nil)

// ValidateProtocolBindingSpec proves an inbound A2A successor against the
// exact active generation already bound to an authenticated operator route.
// It does not accept a browser-selected peer or create remote/local work.
func (r *a2aInboundRouter) ValidateProtocolBindingSpec(
	ctx context.Context,
	tenant model.TenantID,
	input sessions.ProtocolBindingSpecInput,
) (sessions.ProtocolBindingValidation, error) {
	if r == nil || r.kernel == nil || input.Protocol != sessions.BindingProtocolA2A ||
		input.ProtocolVersion != a2a.ProtocolVersion || input.Direction != sessions.BindingInbound ||
		input.LocalKind != sessions.BindingLocalWorkItem || input.RemoteResourceKind != "agent" {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: A2A inbound spec is outside the supported route shape",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	route, ok := r.routes[input.PeerAuthority]
	if !ok || route.tenant != tenant || route.workspace != input.WorkspaceID ||
		!protocolRuntimePolicyMatches(input.RuleRefs, input.PermissionProfileRef, route.policy) {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: A2A inbound spec is outside the configured route",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	current, err := r.kernel.GetProtocolBindingSpec(ctx, tenant, route.bindingSpec)
	if err != nil {
		return sessions.ProtocolBindingValidation{}, err
	}
	if current.ID != route.bindingSpec || current.TenantID != tenant ||
		current.WorkspaceID != route.workspace || current.BindingKey != input.BindingKey ||
		current.Protocol != sessions.BindingProtocolA2A || current.ProtocolVersion != input.ProtocolVersion ||
		(current.Direction != sessions.BindingInbound && current.Direction != sessions.BindingBidirectional) ||
		current.LocalKind != sessions.BindingLocalWorkItem ||
		current.PeerAuthority != input.PeerAuthority ||
		current.RemoteResourceKind != input.RemoteResourceKind ||
		current.RemoteResourceRef != input.RemoteResourceRef ||
		!protocolConfiguredSpecLineage(current, input, route.bindingSpecGeneration) {
		return sessions.ProtocolBindingValidation{}, fmt.Errorf(
			"%w: A2A inbound spec does not continue the configured active route",
			sessions.ErrProtocolBindingSpecUnsupported,
		)
	}
	return sessions.ProtocolBindingValidation{
		Verdict: sessions.ProtocolObservationClean, Code: "a2a_inbound_capability_validated",
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (r *a2aInboundRouter) RouteInboundA2A(
	ctx context.Context,
	message a2a.InboundMessage,
) (a2a.InboundResult, error) {
	route, ok := r.routes[message.PeerAuthority]
	if !ok {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32004, Message: "peer has no local route"}
	}
	spec, err := r.kernel.GetProtocolBindingSpec(ctx, route.tenant, route.bindingSpec)
	if err != nil {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32005, Message: "binding spec is unavailable"}
	}
	source, err := protocolA2AInboundMappingSource(message)
	if err != nil {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32005, Message: "message cannot be mapped"}
	}
	mapping, err := sessions.EvaluateProtocolBindingMapping(spec, sessions.ProtocolBindingRuntimeExpectation{
		TenantID: route.tenant, WorkspaceID: route.workspace, SpecID: route.bindingSpec,
		Generation: route.bindingSpecGeneration, Protocol: sessions.BindingProtocolA2A,
		ProtocolVersion: a2a.ProtocolVersion, Direction: sessions.BindingInbound,
		LocalKind: sessions.BindingLocalWorkItem, PeerAuthority: message.PeerAuthority,
		RemoteResourceKind: "agent", RemoteResourceRef: spec.RemoteResourceRef,
		RuleRefs: route.policy.ruleRefs, PermissionProfileRef: route.policy.permissionProfileRef,
	}, source)
	if err != nil {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32005, Message: "binding spec cannot map message"}
	}
	projection, err := projectInboundA2AWork(message, mapping)
	if err != nil {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32005, Message: "message cannot be mapped"}
	}
	if strings.TrimSpace(message.ReplayID) == "" || message.ReplayExpiresAt.IsZero() {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32005, Message: "verified replay identity is missing"}
	}
	var projected a2a.InboundResult
	replay, err := r.kernel.ApplyProtocolReplay(ctx, route.tenant, sessions.ProtocolReplayClaim{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: message.PeerAuthority, Kind: sessions.ProtocolReplayJTI,
		ReplayID: message.ReplayID, ExpiresAt: message.ReplayExpiresAt,
	}, func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
		messageReplay, err := r.kernel.ApplyProtocolReplay(
			joinedCtx, route.tenant, sessions.ProtocolReplayClaim{
				WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
				PeerAuthority: message.PeerAuthority, Kind: sessions.ProtocolReplayMessageID,
				ReplayID: message.MessageID, ExpiresAt: message.ReplayExpiresAt,
			}, func(messageCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
				return r.routeInboundA2AMessage(messageCtx, route, message, projection)
			})
		if err != nil {
			return sessions.ProtocolReplaySettlement{}, err
		}
		projected, err = r.reloadInboundA2AMessage(
			joinedCtx, route, message, projection, messageReplay.Guard.BindingID,
		)
		if err != nil {
			return sessions.ProtocolReplaySettlement{}, err
		}
		return sessions.ProtocolReplaySettlement{BindingID: messageReplay.Guard.BindingID}, nil
	})
	if err != nil {
		return a2a.InboundResult{}, normalizeInboundA2AError(err)
	}
	if replay.Replayed {
		if _, reloadErr := r.reloadInboundA2AMessage(
			ctx, route, message, projection, replay.Guard.BindingID,
		); reloadErr != nil {
			return a2a.InboundResult{}, normalizeInboundA2AError(reloadErr)
		}
		return a2a.InboundResult{}, a2a.ErrReplay
	}
	if projected.TaskID == "" || replay.Guard.BindingID.IsZero() {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: durable replay settlement has no task projection")
	}
	return projected, nil
}

func (r *a2aInboundRouter) reloadInboundA2AMessage(
	ctx context.Context,
	route parsedA2AInboundRoute,
	message a2a.InboundMessage,
	projection inboundA2AWorkProjection,
	bindingID model.ID,
) (a2a.InboundResult, error) {
	if bindingID.IsZero() {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: replay has no binding")
	}
	binding, err := r.kernel.GetProtocolBinding(
		ctx, route.tenant, sessions.ProtocolBindingRef{ID: bindingID},
	)
	if err != nil {
		return a2a.InboundResult{}, err
	}
	if binding.ID != bindingID || binding.WorkspaceID != route.workspace ||
		binding.WorkItemID.IsZero() || binding.Protocol != sessions.BindingProtocolA2A ||
		(binding.Direction != sessions.BindingInbound && binding.Direction != sessions.BindingBidirectional) ||
		binding.PeerAuthority != message.PeerAuthority || binding.ExternalKind != "task" ||
		binding.ExternalID == "" || binding.ContextID != message.ContextID {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: binding changed Message lineage")
	}
	command := inboundA2AProtocolMessageCommand(binding, route, message, projection)
	reply, err := r.kernel.GetProtocolReply(ctx, route.tenant, command.Ref())
	if err != nil {
		return a2a.InboundResult{}, err
	}
	if reply.BindingID != binding.ID || reply.Generation != binding.Generation ||
		reply.WorkItemID != binding.WorkItemID || reply.MessageID.IsZero() ||
		reply.DeliveryID.IsZero() || reply.ThreadID.IsZero() || reply.State != sessions.MessagePublished {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: durable K3 Message projection is inconsistent")
	}
	principal := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "a2a-inbound-router",
		Actor: "system:a2a-inbound-router", Admin: true,
	}
	snapshot, err := r.kernel.Get(ctx, route.tenant, principal, binding.WorkItemID)
	if err != nil || snapshot.Item.ID != binding.WorkItemID || snapshot.Item.WorkspaceID != route.workspace {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: durable Work projection is inconsistent")
	}
	state := projectWorkStateToA2A(snapshot.Item)
	if state == a2a.TaskStateUnspecified {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: Work state cannot be projected")
	}
	return a2a.InboundResult{
		ResultKind: "task", TaskID: binding.ExternalID,
		ContextID: binding.ContextID, State: state,
	}, nil
}

func (r *a2aInboundRouter) GetInboundA2ATask(
	ctx context.Context,
	request a2a.InboundTaskRequest,
) (a2a.InboundResult, error) {
	route, binding, err := r.inboundA2ATaskBinding(ctx, request)
	if err != nil {
		return a2a.InboundResult{}, err
	}
	return r.withInboundA2ATaskReplay(ctx, route, request, binding.ID,
		func(joinedCtx context.Context, current sessions.ProtocolBinding) (a2a.InboundResult, error) {
			return r.projectInboundA2ATask(joinedCtx, route, current)
		})
}

func (r *a2aInboundRouter) CancelInboundA2ATask(
	ctx context.Context,
	request a2a.InboundTaskRequest,
) (a2a.InboundResult, error) {
	route, binding, err := r.inboundA2ATaskBinding(ctx, request)
	if err != nil {
		return a2a.InboundResult{}, err
	}
	return r.withInboundA2ATaskReplay(ctx, route, request, binding.ID,
		func(joinedCtx context.Context, current sessions.ProtocolBinding) (a2a.InboundResult, error) {
			if current.Terminal {
				if current.LocalState == "canceled" {
					return r.projectInboundA2ATask(joinedCtx, route, current)
				}
				return a2a.InboundResult{},
					&a2a.InboundRouteError{Code: -32002, Message: "task is not cancelable"}
			}
			requested, err := r.kernel.RequestProtocolBindingCancel(
				joinedCtx, route.tenant, sessions.ProtocolBindingCancelIntent{
					BindingID: current.ID, Generation: current.Generation,
					ExpectedVersion: current.Version,
					SemanticKey:     workflowSemanticID("a2a-inbound-cancel", current.ID.String()),
					ReasonCode:      "a2a_peer_requested_cancel",
				})
			if err != nil {
				return a2a.InboundResult{}, normalizeInboundA2AError(err)
			}
			detail := sha256.Sum256([]byte("olivares.a2a.inbound.cancel.v1\x00" + current.ID.String()))
			observed, err := r.kernel.ObserveProtocolBinding(
				joinedCtx, route.tenant, sessions.ProtocolBindingObservation{
					BindingID: requested.ID, Generation: requested.Generation,
					ExpectedVersion: requested.Version,
					SemanticKey:     workflowSemanticID("a2a-inbound-cancel-observed", current.ID.String()),
					PeerAuthority:   current.PeerAuthority, ExternalID: current.ExternalID,
					ContextID: current.ContextID, ExternalMessageID: current.ExternalMessageID,
					LocalState: "canceled", RemoteState: "canceled",
					Verdict: sessions.ProtocolObservationClean, Code: "remote_cancel_confirmed",
					Observed: true, DetailHash: detail[:], Terminal: true,
				})
			if err != nil {
				return a2a.InboundResult{}, normalizeInboundA2AError(err)
			}
			return r.projectInboundA2ATask(joinedCtx, route, observed)
		})
}

func (r *a2aInboundRouter) inboundA2ATaskBinding(
	ctx context.Context,
	request a2a.InboundTaskRequest,
) (parsedA2AInboundRoute, sessions.ProtocolBinding, error) {
	route, ok := r.routes[request.PeerAuthority]
	if !ok || strings.TrimSpace(request.ReplayID) == "" || request.ReplayExpiresAt.IsZero() {
		return parsedA2AInboundRoute{}, sessions.ProtocolBinding{},
			&a2a.InboundRouteError{Code: -32001, Message: "task not found"}
	}
	bindingID, err := model.ParseID(request.TaskID)
	if err != nil || bindingID.IsZero() {
		return parsedA2AInboundRoute{}, sessions.ProtocolBinding{},
			&a2a.InboundRouteError{Code: -32001, Message: "task not found"}
	}
	binding, err := r.kernel.GetProtocolBinding(ctx, route.tenant, sessions.ProtocolBindingRef{ID: bindingID})
	if err != nil || binding.ID != bindingID || binding.WorkspaceID != route.workspace ||
		binding.Protocol != sessions.BindingProtocolA2A ||
		(binding.Direction != sessions.BindingInbound && binding.Direction != sessions.BindingBidirectional) ||
		binding.PeerAuthority != request.PeerAuthority || binding.ExternalKind != "task" ||
		binding.ExternalID != request.TaskID || binding.WorkItemID.IsZero() {
		return parsedA2AInboundRoute{}, sessions.ProtocolBinding{},
			&a2a.InboundRouteError{Code: -32001, Message: "task not found"}
	}
	return route, binding, nil
}

func (r *a2aInboundRouter) withInboundA2ATaskReplay(
	ctx context.Context,
	route parsedA2AInboundRoute,
	request a2a.InboundTaskRequest,
	bindingID model.ID,
	mutation func(context.Context, sessions.ProtocolBinding) (a2a.InboundResult, error),
) (a2a.InboundResult, error) {
	var projected a2a.InboundResult
	replay, err := r.kernel.ApplyProtocolReplay(ctx, route.tenant, sessions.ProtocolReplayClaim{
		WorkspaceID: route.workspace, Protocol: sessions.BindingProtocolA2A,
		PeerAuthority: request.PeerAuthority, Kind: sessions.ProtocolReplayJTI,
		ReplayID: request.ReplayID, ExpiresAt: request.ReplayExpiresAt,
		ExpectedBindingID: bindingID,
	}, func(joinedCtx context.Context) (sessions.ProtocolReplaySettlement, error) {
		current, err := r.kernel.GetProtocolBinding(
			joinedCtx, route.tenant, sessions.ProtocolBindingRef{ID: bindingID},
		)
		if err != nil || current.ID != bindingID || current.WorkspaceID != route.workspace ||
			current.PeerAuthority != request.PeerAuthority || current.ExternalID != request.TaskID {
			return sessions.ProtocolReplaySettlement{},
				&a2a.InboundRouteError{Code: -32001, Message: "task not found"}
		}
		projected, err = mutation(joinedCtx, current)
		if err != nil {
			return sessions.ProtocolReplaySettlement{}, err
		}
		return sessions.ProtocolReplaySettlement{BindingID: current.ID}, nil
	})
	if err != nil {
		return a2a.InboundResult{}, normalizeInboundA2AError(err)
	}
	if replay.Replayed {
		return a2a.InboundResult{}, a2a.ErrReplay
	}
	if replay.Guard.BindingID != bindingID || projected.TaskID != request.TaskID {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: task lifecycle projection is inconsistent")
	}
	return projected, nil
}

func (r *a2aInboundRouter) projectInboundA2ATask(
	ctx context.Context,
	route parsedA2AInboundRoute,
	binding sessions.ProtocolBinding,
) (a2a.InboundResult, error) {
	principal := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem, ActorRef: "a2a-inbound-router",
		Actor: "system:a2a-inbound-router", Admin: true,
	}
	snapshot, err := r.kernel.Get(ctx, route.tenant, principal, binding.WorkItemID)
	if err != nil || snapshot.Item.ID != binding.WorkItemID || snapshot.Item.WorkspaceID != route.workspace {
		return a2a.InboundResult{}, &a2a.InboundRouteError{Code: -32001, Message: "task not found"}
	}
	state := projectWorkStateToA2A(snapshot.Item)
	if state == a2a.TaskStateUnspecified {
		return a2a.InboundResult{}, fmt.Errorf("a2a inbound: work state cannot be projected")
	}
	return a2a.InboundResult{
		ResultKind: "task", TaskID: binding.ExternalID,
		ContextID: binding.ContextID, State: state,
	}, nil
}

func (r *a2aInboundRouter) routeInboundA2AMessage(
	ctx context.Context,
	route parsedA2AInboundRoute,
	message a2a.InboundMessage,
	projection inboundA2AWorkProjection,
) (sessions.ProtocolReplaySettlement, error) {
	principal := sessions.WorkPrincipal{
		ActorKind: model.ActorSystem,
		ActorRef:  "a2a-inbound-router",
		Actor:     "system:a2a-inbound-router",
		Admin:     true,
	}
	semanticKey := message.PeerAuthority + "\x00" + message.MessageID
	create, err := r.kernel.Apply(ctx, route.tenant, principal, sessions.WorkCommand{
		Command: "item.create", WorkspaceID: route.workspace,
		WorkKind: route.workKind, Title: projection.title, BriefMD: projection.brief,
		ContextRefs: projection.refs, Priority: route.priority,
		OwnerKind: route.ownerKind, OwnerRef: route.ownerRef,
		ProvenanceKind: "a2a", ProvenanceRef: projection.provenanceRef,
		ProvenanceHash: projection.digest,
		Acceptance: []sessions.AcceptanceInput{{
			Key: "remote_result_review", Ordinal: 0,
			Statement: "Review the authenticated remote result against the local work brief.", Required: true,
		}},
		IdempotencyKey: workflowSemanticID("a2a-inbound-create", semanticKey),
		CommandScope:   "a2a.inbound.create:" + projection.digest,
		HTTPMethod:     http.MethodPost,
	})
	if err != nil {
		return sessions.ProtocolReplaySettlement{}, normalizeInboundA2AError(err)
	}
	ready, err := r.kernel.Apply(ctx, route.tenant, principal, sessions.WorkCommand{
		Command: "item.ready", WorkItemID: create.ResultID, WorkspaceID: route.workspace,
		ExpectedVersion: create.Version,
		IdempotencyKey:  workflowSemanticID("a2a-inbound-ready", semanticKey),
		CommandScope:    "a2a.inbound.ready:" + projection.digest,
		HTTPMethod:      http.MethodPost,
	})
	if err != nil {
		return sessions.ProtocolReplaySettlement{}, normalizeInboundA2AError(err)
	}
	ownerDigest := sha256.Sum256([]byte(route.ownerKind + "\x00" + route.ownerRef))
	binding, err := r.kernel.ReserveProtocolBinding(ctx, route.tenant, sessions.ProtocolBindingReservation{
		WorkspaceID: route.workspace, BindingSpecID: route.bindingSpec,
		BindingSpecGeneration: route.bindingSpecGeneration,
		ExpectedDirection:     sessions.BindingInbound,
		WorkItemID:            create.ResultID, DispatchKey: inboundA2ADispatchKey(message),
		ExpectedExternalKind: "task", Generation: 1,
		OwnerKind: route.ownerKind, OwnerRef: route.ownerRef,
		OwnerDigest: ownerDigest[:], OwnerEpoch: ready.OwnerEpoch,
	})
	if err != nil {
		return sessions.ProtocolReplaySettlement{}, normalizeInboundA2AError(err)
	}
	if binding.WorkItemID != create.ResultID || binding.PeerAuthority != message.PeerAuthority ||
		binding.Protocol != sessions.BindingProtocolA2A || binding.Generation != 1 {
		return sessions.ProtocolReplaySettlement{}, fmt.Errorf("a2a inbound: reserved binding does not match its work route")
	}
	if binding.ExternalID == "" {
		binding, err = r.kernel.SettleProtocolBinding(ctx, route.tenant, sessions.ProtocolBindingSettlement{
			BindingID: binding.ID, Generation: binding.Generation,
			ExpectedVersion: binding.Version, DispatchKey: inboundA2ADispatchKey(message),
			ResultKind: sessions.ProtocolBindingResultTask,
			ExternalID: binding.ID.String(), ContextID: message.ContextID,
			LocalState: "active", RemoteState: "submitted",
			Verdict: sessions.ProtocolObservationClean, Code: "inbound_accepted",
			Observed: true, DetailHash: projection.digestBytes, Terminal: false,
		})
		if err != nil {
			return sessions.ProtocolReplaySettlement{}, normalizeInboundA2AError(err)
		}
	}
	reply, err := r.kernel.ProjectProtocolReply(
		ctx, route.tenant, inboundA2AProtocolMessageCommand(binding, route, message, projection),
	)
	if err != nil {
		return sessions.ProtocolReplaySettlement{}, normalizeInboundA2AError(err)
	}
	if reply.BindingID != binding.ID || reply.Generation != binding.Generation ||
		reply.WorkItemID != binding.WorkItemID || reply.MessageID.IsZero() ||
		reply.DeliveryID.IsZero() || reply.ThreadID.IsZero() || reply.State != sessions.MessagePublished {
		return sessions.ProtocolReplaySettlement{}, fmt.Errorf(
			"a2a inbound: K3 Message projection did not settle exact binding lineage",
		)
	}
	return sessions.ProtocolReplaySettlement{BindingID: binding.ID}, nil
}

type inboundA2AWorkProjection struct {
	title         string
	brief         string
	refs          []sessions.ContextRef
	provenanceRef string
	digest        string
	digestBytes   []byte
	messageParts  []sessions.ProtocolReplyPart
}

func projectInboundA2AWork(
	message a2a.InboundMessage,
	mapping sessions.ProtocolMappingEvaluation,
) (inboundA2AWorkProjection, error) {
	if message.Protocol != a2a.ProtocolVersion || strings.TrimSpace(message.MessageID) == "" ||
		len(message.Parts) == 0 || len(message.Parts) > 62 || len(message.MessageID) > 512 ||
		message.ContextID == "" || len(message.ContextID) > 512 {
		return inboundA2AWorkProjection{}, fmt.Errorf("invalid inbound message")
	}
	messageParts, err := projectInboundA2AMessageParts(message.Parts)
	if err != nil {
		return inboundA2AWorkProjection{}, err
	}
	canonical, err := json.Marshal(message)
	if err != nil {
		return inboundA2AWorkProjection{}, err
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	brief, err := requiredProtocolMappedString(mapping, "work.brief")
	if err != nil {
		return inboundA2AWorkProjection{}, err
	}
	if len(brief) == 0 || len(brief) > 64*1024 {
		return inboundA2AWorkProjection{}, fmt.Errorf("brief is out of bounds")
	}
	provenanceRef := message.PeerAuthority + "#" + message.PeerSubject
	if len(provenanceRef) > 512 {
		peer := sha256.Sum256([]byte(provenanceRef))
		provenanceRef = "sha256:" + hex.EncodeToString(peer[:])
	}
	refs := []sessions.ContextRef{
		{Kind: "a2a_message", Ref: message.MessageID, Hash: digest},
		{Kind: "protocol_mapping", Ref: mapping.EvidenceHash, Hash: mapping.MappingHash},
	}
	if message.ContextID != "" {
		contextRef := message.ContextID
		if len(contextRef) > 512 {
			contextHash := sha256.Sum256([]byte(contextRef))
			contextRef = "sha256:" + hex.EncodeToString(contextHash[:])
		}
		refs = append(refs, sessions.ContextRef{Kind: "a2a_context", Ref: contextRef})
	}
	title, err := optionalProtocolMappedString(mapping, "work.title")
	if err != nil {
		return inboundA2AWorkProjection{}, err
	}
	if title == "" {
		title = "Inbound A2A work request"
	}
	return inboundA2AWorkProjection{
		title: title, brief: brief, refs: refs,
		provenanceRef: provenanceRef, digest: digest,
		digestBytes:  append([]byte(nil), sum[:]...),
		messageParts: messageParts,
	}, nil
}

func projectInboundA2AMessageParts(parts []a2a.InboundPart) ([]sessions.ProtocolReplyPart, error) {
	if len(parts) == 0 || len(parts) > 62 {
		return nil, fmt.Errorf("inbound A2A Part count is out of bounds")
	}
	projected := make([]sessions.ProtocolReplyPart, 0, len(parts))
	total := 0
	for _, part := range parts {
		switch {
		case (part.Kind == "" || part.Kind == "text") &&
			part.Text != "" && len(part.Data) == 0 && part.Reference == "":
			text, ok := sanitizeInboundA2AText(part.Text)
			if !ok {
				return nil, fmt.Errorf("inbound A2A text Part is invalid")
			}
			total += len(text)
			digest := sha256.Sum256([]byte(text))
			digestText := hex.EncodeToString(digest[:])
			if part.Kind == "text" {
				if !validInboundA2APartDigest(part.Digest) {
					return nil, fmt.Errorf("inbound A2A text Part has no canonical digest")
				}
				digestText = part.Digest
			}
			projected = append(projected, sessions.ProtocolReplyPart{
				Kind: sessions.ProtocolReplyPartText, Text: text,
				Digest: digestText,
			})
		case (part.Kind == "" || part.Kind == "data") &&
			part.Text == "" && len(part.Data) != 0 && json.Valid(part.Data):
			var value any
			decoder := json.NewDecoder(bytes.NewReader(part.Data))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return nil, fmt.Errorf("inbound A2A data Part is invalid: %w", err)
			}
			canonical, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("canonicalize inbound A2A data Part: %w", err)
			}
			total += len(canonical)
			digest := sha256.Sum256(canonical)
			digestText := hex.EncodeToString(digest[:])
			reference := "a2a-part:" + digestText
			if part.Kind == "data" {
				if !validInboundA2APartDigest(part.Digest) ||
					part.Reference != "a2a-part:"+part.Digest {
					return nil, fmt.Errorf("inbound A2A data Part has no canonical reference")
				}
				digestText, reference = part.Digest, part.Reference
			}
			projected = append(projected, sessions.ProtocolReplyPart{
				Kind:      sessions.ProtocolReplyPartData,
				Reference: reference, Digest: digestText,
			})
		case part.Kind == "file" && part.Text == "" && len(part.Data) == 0 &&
			validInboundA2APartReference(part.Reference) && validInboundA2APartDigest(part.Digest):
			total += len(part.Reference) + len(part.Digest)
			projected = append(projected, sessions.ProtocolReplyPart{
				Kind: sessions.ProtocolReplyPartFile, Reference: part.Reference, Digest: part.Digest,
			})
		default:
			return nil, fmt.Errorf("inbound A2A Part has an unsupported shape")
		}
		if total > 64*1024 {
			return nil, fmt.Errorf("inbound A2A Parts exceed their wire bound")
		}
	}
	return projected, nil
}

func validInboundA2APartDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == value
}

func validInboundA2APartReference(value string) bool {
	return value != "" && len(value) <= 512 && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func sanitizeInboundA2AText(value string) (string, bool) {
	if value == "" || len(value) > 32*1024 || !utf8.ValidString(value) {
		return "", false
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return -1
	}, value)
	return value, strings.TrimSpace(value) != "" && len(value) <= 32*1024
}

func inboundA2AProtocolMessageCommand(
	binding sessions.ProtocolBinding,
	route parsedA2AInboundRoute,
	message a2a.InboundMessage,
	projection inboundA2AWorkProjection,
) sessions.ProtocolReplyCommand {
	return sessions.ProtocolReplyCommand{
		Flow:      sessions.ProtocolReplyFlowInbound,
		BindingID: binding.ID, Generation: binding.Generation, Route: route.message,
		PeerAuthority: binding.PeerAuthority, Kind: sessions.ProtocolReplyMessage,
		TaskID: binding.ExternalID, ContextID: message.ContextID, MessageID: message.MessageID,
		Parts:        append([]sessions.ProtocolReplyPart(nil), projection.messageParts...),
		SourceDigest: projection.digest,
	}
}

func inboundA2ADispatchKey(message a2a.InboundMessage) string {
	sum := sha256.Sum256([]byte("olivares.a2a.inbound.v1\x00" + message.PeerAuthority + "\x00" + message.MessageID))
	return hex.EncodeToString(sum[:])
}

func projectWorkStateToA2A(item sessions.WorkItem) a2a.TaskState {
	switch item.Status {
	case "draft", "ready":
		return a2a.TaskStateSubmitted
	case "active":
		return a2a.TaskStateWorking
	case "blocked":
		switch item.BlockedCode {
		case "input_required":
			return a2a.TaskStateInputReq
		case "auth_required":
			return a2a.TaskStateAuthRequired
		default:
			return a2a.TaskStateWorking
		}
	case "review":
		return a2a.TaskStateWorking
	case "completed":
		return a2a.TaskStateCompleted
	case "failed":
		return a2a.TaskStateFailed
	case "canceled":
		return a2a.TaskStateCanceled
	default:
		return a2a.TaskStateUnspecified
	}
}

func normalizeInboundA2AError(err error) error {
	if errors.Is(err, sessions.ErrProtocolBindingConflict) ||
		errors.Is(err, sessions.ErrProtocolReplayConflict) {
		return &a2a.InboundRouteError{Code: -32006, Message: "message id conflicts with durable work"}
	}
	if errors.Is(err, sessions.ErrInvalidProtocolBinding) ||
		errors.Is(err, sessions.ErrInvalidProtocolReplay) ||
		errors.Is(err, sessions.ErrInvalidCommunicationModel) ||
		errors.Is(err, sessions.ErrInvalidCommunicationTransition) ||
		errors.Is(err, sessions.ErrCommunicationEvidenceUnknown) {
		return &a2a.InboundRouteError{Code: -32005, Message: "message cannot be mapped"}
	}
	return err
}
