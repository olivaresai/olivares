// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type recordingA2AInboundKernel struct {
	tenant        model.TenantID
	workspace     model.ID
	workID        model.ID
	bindingID     model.ID
	peer          string
	apply         []sessions.WorkCommand
	reservations  []sessions.ProtocolBindingReservation
	settlements   []sessions.ProtocolBindingSettlement
	cancels       []sessions.ProtocolBindingCancelIntent
	observations  []sessions.ProtocolBindingObservation
	binding       sessions.ProtocolBinding
	spec          sessions.ProtocolBindingSpec
	item          sessions.WorkItem
	replayGuards  map[string]sessions.ProtocolReplayGuard
	replyCommand  sessions.ProtocolReplyCommand
	replyResult   sessions.ProtocolReplyResult
	replyProjects int
	replyReloads  int
	replySettled  int
}

func a2aInboundSpecForTest(
	t *testing.T,
	tenant model.TenantID,
	workspace model.ID,
	specID model.ID,
	peer string,
	generation int64,
	state sessions.ProtocolBindingSpecState,
) sessions.ProtocolBindingSpec {
	t.Helper()
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: "a2a-inbound", Generation: generation,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingInbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: peer, RemoteResourceKind: "agent", RemoteResourceRef: "agent:peer",
		MappingSchema: sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "message.text", Target: "work.brief",
			Cardinality: sessions.ProtocolMappingOneToOne, Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{}, RuleRefs: []string{"rule:a2a-inbound"},
		PermissionProfileRef: "permission:a2a-inbound", CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{Verdict: sessions.ProtocolObservationClean, Code: "validated"},
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(input)
	if err != nil {
		t.Fatalf("inbound spec digests: %v", err)
	}
	return sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{CommunicationEntity: sessions.CommunicationEntity{
			ID: specID, TenantID: tenant, WorkspaceID: workspace, Version: 2,
		}},
		BindingKey: input.BindingKey, Generation: input.Generation,
		Protocol: input.Protocol, ProtocolVersion: input.ProtocolVersion,
		Direction: input.Direction, LocalKind: input.LocalKind, LocalSelector: input.LocalSelector,
		PeerAuthority: input.PeerAuthority, RemoteResourceKind: input.RemoteResourceKind,
		RemoteResourceRef: input.RemoteResourceRef, MappingSchema: input.MappingSchema,
		Mapping: input.Mapping, MappingHash: digests.MappingHash,
		KnownLosses: input.KnownLosses, LossesHash: digests.LossesHash,
		RuleRefs: input.RuleRefs, PermissionProfileRef: input.PermissionProfileRef,
		CurrencyPolicy: input.CurrencyPolicy, Validation: input.Validation,
		State: state, SpecHash: digests.SpecHash,
	}
}

func (k *recordingA2AInboundKernel) ApplyProtocolReplay(
	ctx context.Context,
	tenant model.TenantID,
	claim sessions.ProtocolReplayClaim,
	mutation sessions.ProtocolReplayMutation,
) (sessions.ProtocolReplayResult, error) {
	if k.replayGuards == nil {
		k.replayGuards = make(map[string]sessions.ProtocolReplayGuard)
	}
	key := string(claim.Protocol) + "\x00" + claim.PeerAuthority + "\x00" +
		string(claim.Kind) + "\x00" + claim.ReplayID
	if guard, ok := k.replayGuards[key]; ok {
		return sessions.ProtocolReplayResult{Guard: guard, Replayed: true}, nil
	}
	settlement, err := mutation(ctx)
	if err != nil {
		return sessions.ProtocolReplayResult{}, err
	}
	guard := sessions.ProtocolReplayGuard{
		AppendOnlyCommunicationEntity: sessions.AppendOnlyCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: model.NewID(), TenantID: tenant, WorkspaceID: claim.WorkspaceID,
				Version: 1, CreatedAt: time.Now().UTC(),
			},
		},
		Protocol: claim.Protocol, PeerAuthority: claim.PeerAuthority,
		ReplayKind: claim.Kind, ExpiresAt: claim.ExpiresAt, BindingID: settlement.BindingID,
	}
	k.replayGuards[key] = guard
	return sessions.ProtocolReplayResult{Guard: guard}, nil
}

func (k *recordingA2AInboundKernel) GetProtocolBindingSpec(
	_ context.Context,
	_ model.TenantID,
	id model.ID,
) (sessions.ProtocolBindingSpec, error) {
	if id != k.spec.ID {
		return sessions.ProtocolBindingSpec{}, sessions.ErrProtocolBindingNotFound
	}
	return k.spec, nil
}

func (k *recordingA2AInboundKernel) GetProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	ref sessions.ProtocolBindingRef,
) (sessions.ProtocolBinding, error) {
	if ref.ID != k.binding.ID {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingNotFound
	}
	return k.binding, nil
}

func (k *recordingA2AInboundKernel) ProjectProtocolReply(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolReplyCommand,
) (sessions.ProtocolReplyResult, error) {
	k.replyProjects++
	k.replySettled = len(k.settlements)
	k.replyCommand = command
	k.replyResult = sessions.ProtocolReplyResult{
		BindingID: command.BindingID, Generation: command.Generation,
		WorkItemID: k.binding.WorkItemID, CommandID: model.NewID(),
		MessageID: model.NewID(), DeliveryID: model.NewID(), ThreadID: model.NewID(),
		EventID: model.NewID(), EventSeq: 3, Version: 1, State: sessions.MessagePublished,
	}
	return k.replyResult, nil
}

func (k *recordingA2AInboundKernel) GetProtocolReply(
	_ context.Context,
	_ model.TenantID,
	ref sessions.ProtocolReplyRef,
) (sessions.ProtocolReplyResult, error) {
	k.replyReloads++
	if k.replyProjects != 1 || ref != k.replyCommand.Ref() {
		return sessions.ProtocolReplyResult{}, sessions.ErrCommunicationEvidenceUnknown
	}
	result := k.replyResult
	result.Replayed = true
	return result, nil
}

func (k *recordingA2AInboundKernel) Apply(
	_ context.Context,
	tenant model.TenantID,
	_ sessions.WorkPrincipal,
	cmd sessions.WorkCommand,
) (sessions.CommandResult, error) {
	k.tenant = tenant
	k.apply = append(k.apply, cmd)
	if cmd.Command == "item.create" {
		return sessions.CommandResult{
			ResultID: k.workID, Version: 1, Status: "draft",
			OwnerEpoch: 1, CommandID: model.NewID(), EventID: model.NewID(), EventSeq: 1,
		}, nil
	}
	return sessions.CommandResult{
		ResultID: k.workID, Version: 2, Status: "ready",
		OwnerEpoch: 1, CommandID: model.NewID(), EventID: model.NewID(), EventSeq: 2,
	}, nil
}

func (k *recordingA2AInboundKernel) Get(
	context.Context,
	model.TenantID,
	sessions.WorkPrincipal,
	model.ID,
) (sessions.WorkSnapshot, error) {
	return sessions.WorkSnapshot{Item: k.item}, nil
}

func (k *recordingA2AInboundKernel) ReserveProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	reservation sessions.ProtocolBindingReservation,
) (sessions.ProtocolBinding, error) {
	k.reservations = append(k.reservations, reservation)
	k.item.ID = reservation.WorkItemID
	k.item.WorkspaceID = reservation.WorkspaceID
	k.item.Status = "active"
	if !k.binding.ID.IsZero() {
		return k.binding, nil
	}
	k.binding = sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: k.bindingID, TenantID: k.tenant, WorkspaceID: k.workspace, Version: 1,
			},
		},
		BindingSpecID: reservation.BindingSpecID, BindingSpecGeneration: reservation.BindingSpecGeneration,
		WorkItemID: reservation.WorkItemID, Protocol: sessions.BindingProtocolA2A,
		ProtocolVersion: a2a.ProtocolVersion, Direction: sessions.BindingInbound,
		PeerAuthority: k.peer, Generation: reservation.Generation,
		AttemptID: model.NewID(), SyntheticSID: "osn_018f47a1-23ab-7def-8abc-0123456789ab",
		OwnerKind: reservation.OwnerKind, OwnerRef: reservation.OwnerRef,
		OwnerEpoch: reservation.OwnerEpoch, LeaseFence: 1,
	}
	return k.binding, nil
}

func (k *recordingA2AInboundKernel) SettleProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	settlement sessions.ProtocolBindingSettlement,
) (sessions.ProtocolBinding, error) {
	k.settlements = append(k.settlements, settlement)
	k.binding.Version++
	k.binding.ExternalKind = string(settlement.ResultKind)
	k.binding.ExternalID = settlement.ExternalID
	k.binding.ContextID = settlement.ContextID
	k.binding.LocalState = settlement.LocalState
	k.binding.RemoteState = settlement.RemoteState
	return k.binding, nil
}

func (k *recordingA2AInboundKernel) RequestProtocolBindingCancel(
	_ context.Context,
	_ model.TenantID,
	intent sessions.ProtocolBindingCancelIntent,
) (sessions.ProtocolBinding, error) {
	k.cancels = append(k.cancels, intent)
	k.binding.Version++
	k.binding.CancelRequested = true
	k.binding.LocalState = "cancel_requested"
	k.item.Status = "blocked"
	k.item.BlockedCode = "cancel_pending"
	return k.binding, nil
}

func (k *recordingA2AInboundKernel) ObserveProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	observation sessions.ProtocolBindingObservation,
) (sessions.ProtocolBinding, error) {
	k.observations = append(k.observations, observation)
	k.binding.Version++
	k.binding.LocalState = observation.LocalState
	k.binding.RemoteState = observation.RemoteState
	k.binding.ObservationVerdict = observation.Verdict
	k.binding.ObservationCode = observation.Code
	k.binding.Terminal = observation.Terminal
	k.item.Status = observation.LocalState
	return k.binding, nil
}

func TestA2AInboundRouterServesDurableTaskLifecycle(t *testing.T) {
	tenant := model.NewTenantID()
	workspace := model.NewID()
	specID := model.NewID()
	kernel := &recordingA2AInboundKernel{
		workspace: workspace, workID: model.NewID(), bindingID: model.NewID(),
		peer: "https://peer.example", item: sessions.WorkItem{Status: "ready"},
	}
	kernel.spec = a2aInboundSpecForTest(
		t, tenant, workspace, specID, kernel.peer, 3, sessions.ProtocolBindingSpecActive,
	)
	router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
		PeerAuthority: kernel.peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		BindingSpecID: specID.String(), BindingSpecGeneration: 3,
		ChannelID: model.NewID().String(), SenderUserID: model.NewID().String(),
		RecipientUserID: model.NewID().String(),
		OwnerKind:       "user", OwnerRef: model.NewID().String(),
	}}, []string{kernel.peer})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	expires := time.Now().UTC().Add(time.Hour)
	created, err := router.RouteInboundA2A(context.Background(), a2a.InboundMessage{
		PeerAuthority: kernel.peer, PeerSubject: "peer-agent", Protocol: a2a.ProtocolVersion,
		MessageID: "peer-lifecycle-message", ContextID: "peer-lifecycle-context", Role: "ROLE_AGENT",
		Parts:    []a2a.InboundPart{{Text: "Perform the routed work."}},
		ReplayID: "inbound-life-send", ReplayExpiresAt: expires,
	})
	if err != nil {
		t.Fatalf("route inbound: %v", err)
	}
	request := a2a.InboundTaskRequest{
		PeerAuthority: kernel.peer, PeerSubject: "peer-agent",
		TaskID: created.TaskID, ReplayID: "inbound-life-get", ReplayExpiresAt: expires,
	}
	got, err := router.GetInboundA2ATask(context.Background(), request)
	if err != nil || got.TaskID != created.TaskID || got.State != a2a.TaskStateWorking {
		t.Fatalf("get task = %+v, err=%v", got, err)
	}
	if _, err := router.GetInboundA2ATask(context.Background(), request); !errors.Is(err, a2a.ErrReplay) {
		t.Fatalf("replayed get err = %v, want replay", err)
	}
	request.ReplayID = "inbound-life-cancel"
	canceled, err := router.CancelInboundA2ATask(context.Background(), request)
	if err != nil || canceled.TaskID != created.TaskID || canceled.State != a2a.TaskStateCanceled {
		t.Fatalf("cancel task = %+v, err=%v", canceled, err)
	}
	if len(kernel.cancels) != 1 || len(kernel.observations) != 1 ||
		!kernel.binding.Terminal || kernel.item.Status != "canceled" {
		t.Fatalf("cancel effects = intents:%d observations:%d binding:%+v item:%+v",
			len(kernel.cancels), len(kernel.observations), kernel.binding, kernel.item)
	}
	request.ReplayID = "inbound-life-terminal-get"
	terminal, err := router.GetInboundA2ATask(context.Background(), request)
	if err != nil || terminal.State != a2a.TaskStateCanceled {
		t.Fatalf("terminal get = %+v, err=%v", terminal, err)
	}
}

func TestA2AInboundRouterCreatesActiveWorkAndSettlesBindingBeforeReply(t *testing.T) {
	tenant := model.NewTenantID()
	workspace := model.NewID()
	specID := model.NewID()
	kernel := &recordingA2AInboundKernel{
		workspace: workspace, workID: model.NewID(), bindingID: model.NewID(),
		peer: "https://peer.example", item: sessions.WorkItem{Status: "ready"},
	}
	kernel.spec = a2aInboundSpecForTest(
		t, tenant, workspace, specID, kernel.peer, 3, sessions.ProtocolBindingSpecActive,
	)
	channelID, senderID, recipientID := model.NewID(), model.NewID(), model.NewID()
	router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
		PeerAuthority: kernel.peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		BindingSpecID: specID.String(), BindingSpecGeneration: 3,
		ChannelID: channelID.String(), SenderUserID: senderID.String(),
		RecipientUserID: recipientID.String(),
		OwnerKind:       "user", OwnerRef: model.NewID().String(),
	}}, []string{kernel.peer})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	message := a2a.InboundMessage{
		PeerAuthority: kernel.peer, PeerSubject: "peer-agent", Protocol: a2a.ProtocolVersion,
		MessageID: "peer-message-1", ContextID: "peer-context-1", Role: "ROLE_AGENT",
		Parts: []a2a.InboundPart{
			{Kind: "text", Text: "Perform the routed work.", Digest: strings.Repeat("a", 64)},
			{Kind: "data", Data: json.RawMessage(`{"risk":7}`),
				Reference: "a2a-part:" + strings.Repeat("b", 64), Digest: strings.Repeat("b", 64)},
		},
		Metadata: map[string]json.RawMessage{
			"io.olivares.work_item_id": json.RawMessage(`"untrusted"`),
			"channel_id":               json.RawMessage(`"peer-channel"`),
			"sender_user_id":           json.RawMessage(`"peer-sender"`),
			"recipient_user_id":        json.RawMessage(`"peer-recipient"`),
		},
		ReplayID: "inbound-jti-1", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	result, err := router.RouteInboundA2A(context.Background(), message)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if result.ResultKind != "task" || result.TaskID != kernel.bindingID.String() ||
		result.ContextID != message.ContextID || result.State != a2a.TaskStateWorking {
		t.Fatalf("result = %+v", result)
	}
	if kernel.tenant != tenant || len(kernel.apply) != 2 ||
		kernel.apply[0].Command != "item.create" || kernel.apply[1].Command != "item.ready" {
		t.Fatalf("work calls = %#v", kernel.apply)
	}
	create := kernel.apply[0]
	if create.WorkspaceID != workspace || create.OwnerKind != "user" ||
		create.ProvenanceKind != "a2a" || create.BriefMD != "Perform the routed work.\n\n```json\n{\"risk\":7}\n```" ||
		create.ProvenanceRef != kernel.peer+"#peer-agent" || len(create.Acceptance) != 1 {
		t.Fatalf("create command = %+v", create)
	}
	if len(kernel.reservations) != 1 || len(kernel.settlements) != 1 {
		t.Fatalf("binding calls = reserve %d settle %d", len(kernel.reservations), len(kernel.settlements))
	}
	reservation := kernel.reservations[0]
	if reservation.WorkItemID != kernel.workID || reservation.BindingSpecID != specID ||
		reservation.BindingSpecGeneration != 3 || reservation.Generation != 1 {
		t.Fatalf("reservation = %+v", reservation)
	}
	settlement := kernel.settlements[0]
	if settlement.BindingID != kernel.bindingID || settlement.ExternalID != kernel.bindingID.String() ||
		settlement.LocalState != "active" || settlement.RemoteState != "submitted" ||
		settlement.Verdict != sessions.ProtocolObservationClean {
		t.Fatalf("settlement = %+v", settlement)
	}
	reply := kernel.replyCommand
	if kernel.replyProjects != 1 || kernel.replySettled != 1 ||
		reply.Flow != sessions.ProtocolReplyFlowInbound || reply.BindingID != kernel.bindingID ||
		reply.Generation != 1 || reply.PeerAuthority != kernel.peer ||
		reply.Kind != sessions.ProtocolReplyMessage || reply.TaskID != kernel.bindingID.String() ||
		reply.ContextID != message.ContextID || reply.MessageID != message.MessageID ||
		reply.SourceDigest != create.ProvenanceHash || reply.Route.ChannelID != channelID ||
		reply.Route.SenderUserID != senderID || reply.Route.RecipientUserID != recipientID ||
		len(reply.Parts) != 2 || reply.Parts[0].Kind != sessions.ProtocolReplyPartText ||
		reply.Parts[0].Text != "Perform the routed work." || reply.Parts[0].Digest != strings.Repeat("a", 64) ||
		reply.Parts[1].Kind != sessions.ProtocolReplyPartData || reply.Parts[1].Text != "" ||
		reply.Parts[1].Digest != strings.Repeat("b", 64) ||
		reply.Parts[1].Reference != "a2a-part:"+reply.Parts[1].Digest {
		t.Fatalf("inbound K3 Message command = %+v; projects=%d settled=%d",
			reply, kernel.replyProjects, kernel.replySettled)
	}

	// The same protocol message replays the same WorkItem/binding and does not
	// create another binding settlement once the external Task ID is durable.
	message.ReplayID = "inbound-jti-2"
	if _, err := router.RouteInboundA2A(context.Background(), message); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(kernel.settlements) != 1 || kernel.replyProjects != 1 || kernel.replyReloads != 2 {
		t.Fatalf("effects after Message retry = settlements:%d projects:%d reloads:%d",
			len(kernel.settlements), kernel.replyProjects, kernel.replyReloads)
	}
	if _, err := router.RouteInboundA2A(context.Background(), message); !errors.Is(err, a2a.ErrReplay) {
		t.Fatalf("exact JTI replay err = %v, want replay", err)
	}
	if kernel.replyProjects != 1 || kernel.replyReloads != 3 {
		t.Fatalf("effects after JTI replay = projects:%d reloads:%d",
			kernel.replyProjects, kernel.replyReloads)
	}
}

func TestA2AInboundRouterRequiresAllowlistedExactRoute(t *testing.T) {
	route := a2aInboundRouteConfig{
		PeerAuthority: "https://peer.example", Tenant: model.NewTenantID().String(),
		WorkspaceID: model.NewID().String(), BindingSpecID: model.NewID().String(),
		BindingSpecGeneration: 1, OwnerKind: "user", OwnerRef: model.NewID().String(),
		ChannelID: model.NewID().String(), SenderUserID: model.NewID().String(),
		RecipientUserID: model.NewID().String(),
	}
	if _, err := newA2AInboundRouter(&recordingA2AInboundKernel{}, []a2aInboundRouteConfig{route}, []string{"https://other.example"}); err == nil {
		t.Fatal("unallowlisted route was accepted")
	}
	for name, clear := range map[string]func(*a2aInboundRouteConfig){
		"channel":   func(route *a2aInboundRouteConfig) { route.ChannelID = "" },
		"sender":    func(route *a2aInboundRouteConfig) { route.SenderUserID = "" },
		"recipient": func(route *a2aInboundRouteConfig) { route.RecipientUserID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := route
			clear(&candidate)
			if _, err := newA2AInboundRouter(
				&recordingA2AInboundKernel{}, []a2aInboundRouteConfig{candidate},
				[]string{route.PeerAuthority},
			); err == nil {
				t.Fatal("incomplete operator-owned Message route was accepted")
			}
		})
	}
}

func TestProjectInboundA2AMessagePartsSanitizesTextAndReferencesData(t *testing.T) {
	fileDigest := strings.Repeat("a", 64)
	parts, err := projectInboundA2AMessageParts([]a2a.InboundPart{
		{Text: "line one\r\nline\x01 two"},
		{Data: json.RawMessage(`{ "nested": {"value": 9} }`)},
		{Kind: "file", Reference: "artifact:inbound-result", Digest: fileDigest},
	})
	if err != nil {
		t.Fatalf("project parts: %v", err)
	}
	if len(parts) != 3 || parts[0].Text != "line one\nline two" ||
		parts[0].Kind != sessions.ProtocolReplyPartText || len(parts[0].Digest) != 64 ||
		parts[1].Kind != sessions.ProtocolReplyPartData || parts[1].Text != "" ||
		parts[1].Reference != "a2a-part:"+parts[1].Digest || len(parts[1].Digest) != 64 ||
		parts[2].Kind != sessions.ProtocolReplyPartFile ||
		parts[2].Reference != "artifact:inbound-result" || parts[2].Digest != fileDigest {
		t.Fatalf("sanitized inbound parts = %+v", parts)
	}
}

func TestA2AInboundRouterValidatesConfiguredProtocolSuccessor(t *testing.T) {
	tenant, workspace, specID := model.NewTenantID(), model.NewID(), model.NewID()
	peer := "https://peer.example"
	kernel := &recordingA2AInboundKernel{spec: a2aInboundSpecForTest(
		t, tenant, workspace, specID, peer, 3, sessions.ProtocolBindingSpecActive,
	)}
	router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
		PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		BindingSpecID: specID.String(), BindingSpecGeneration: 3,
		ChannelID: model.NewID().String(), SenderUserID: model.NewID().String(),
		RecipientUserID: model.NewID().String(),
		OwnerKind:       "user", OwnerRef: model.NewID().String(),
	}}, []string{peer})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: kernel.spec.BindingKey, Generation: 4,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingInbound, LocalKind: sessions.BindingLocalWorkItem,
		PeerAuthority: peer, RemoteResourceKind: "agent", RemoteResourceRef: "agent:peer",
		MappingSchema: kernel.spec.MappingSchema, Mapping: kernel.spec.Mapping,
		KnownLosses: kernel.spec.KnownLosses, RuleRefs: kernel.spec.RuleRefs,
		PermissionProfileRef: kernel.spec.PermissionProfileRef,
		CurrencyPolicy:       sessions.BindingCurrencyPinned, SupersedesID: specID,
	}
	validation, err := router.ValidateProtocolBindingSpec(context.Background(), tenant, input)
	if err != nil || validation.Verdict != sessions.ProtocolObservationClean ||
		validation.Code != "a2a_inbound_capability_validated" || validation.ObservedAt.IsZero() {
		t.Fatalf("inbound validation = %#v, %v", validation, err)
	}
	input.Direction = sessions.BindingOutbound
	if _, err := router.ValidateProtocolBindingSpec(context.Background(), tenant, input); !errors.Is(err, sessions.ErrProtocolBindingSpecUnsupported) {
		t.Fatalf("outbound spec through inbound validator = %v", err)
	}
}

func TestA2AInboundRouterValidatesConfiguredFirstDraftGeneration(t *testing.T) {
	tenant, workspace, specID := model.NewTenantID(), model.NewID(), model.NewID()
	peer := "https://bootstrap-peer.example"
	spec := a2aInboundSpecForTest(
		t, tenant, workspace, specID, peer, 1, sessions.ProtocolBindingSpecDraft,
	)
	kernel := &recordingA2AInboundKernel{spec: spec}
	router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
		PeerAuthority: peer, Tenant: tenant.String(), WorkspaceID: workspace.String(),
		BindingSpecID: specID.String(), BindingSpecGeneration: 1,
		ChannelID: model.NewID().String(), SenderUserID: model.NewID().String(),
		RecipientUserID: model.NewID().String(),
		OwnerKind:       "user", OwnerRef: model.NewID().String(),
	}}, []string{peer})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: spec.WorkspaceID, BindingKey: spec.BindingKey, Generation: spec.Generation,
		Protocol: spec.Protocol, ProtocolVersion: spec.ProtocolVersion,
		Direction: spec.Direction, LocalKind: spec.LocalKind, LocalSelector: spec.LocalSelector,
		PeerAuthority: spec.PeerAuthority, RemoteResourceKind: spec.RemoteResourceKind,
		RemoteResourceRef: spec.RemoteResourceRef, MappingSchema: spec.MappingSchema,
		Mapping: spec.Mapping, KnownLosses: spec.KnownLosses, RuleRefs: spec.RuleRefs,
		PermissionProfileRef: spec.PermissionProfileRef, CurrencyPolicy: spec.CurrencyPolicy,
		Validation: spec.Validation,
	}
	validation, err := router.ValidateProtocolBindingSpec(context.Background(), tenant, input)
	if err != nil || validation.Verdict != sessions.ProtocolObservationClean {
		t.Fatalf("first draft validation = %#v, %v", validation, err)
	}
}

func TestProjectWorkStateToA2APreservesInterruptAndTerminalMeaning(t *testing.T) {
	rows := []struct {
		item sessions.WorkItem
		want a2a.TaskState
	}{
		{sessions.WorkItem{Status: "draft"}, a2a.TaskStateSubmitted},
		{sessions.WorkItem{Status: "active"}, a2a.TaskStateWorking},
		{sessions.WorkItem{Status: "blocked", BlockedCode: "input_required"}, a2a.TaskStateInputReq},
		{sessions.WorkItem{Status: "blocked", BlockedCode: "auth_required"}, a2a.TaskStateAuthRequired},
		{sessions.WorkItem{Status: "review"}, a2a.TaskStateWorking},
		{sessions.WorkItem{Status: "completed"}, a2a.TaskStateCompleted},
		{sessions.WorkItem{Status: "failed"}, a2a.TaskStateFailed},
		{sessions.WorkItem{Status: "canceled"}, a2a.TaskStateCanceled},
	}
	for _, row := range rows {
		if got := projectWorkStateToA2A(row.item); got != row.want {
			t.Fatalf("state %s/%s = %s, want %s", row.item.Status, row.item.BlockedCode, got, row.want)
		}
	}
}
