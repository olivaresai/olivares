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

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type mcpDurableWorkFake struct {
	sessions.WorkKernel
	item     sessions.WorkItem
	lease    sessions.WorkLease
	commands []sessions.WorkCommand
}

type mcpDurableSpecFake struct {
	spec  sessions.ProtocolBindingSpec
	specs map[model.ID]sessions.ProtocolBindingSpec
}

func (f mcpDurableSpecFake) GetProtocolBindingSpec(
	_ context.Context,
	_ model.TenantID,
	id model.ID,
) (sessions.ProtocolBindingSpec, error) {
	if id == f.spec.ID && !f.spec.ID.IsZero() {
		return f.spec, nil
	}
	if spec, ok := f.specs[id]; ok {
		return spec, nil
	}
	return sessions.ProtocolBindingSpec{}, sessions.ErrProtocolBindingNotFound
}

func (f *mcpDurableWorkFake) Apply(
	_ context.Context,
	_ model.TenantID,
	_ sessions.WorkPrincipal,
	command sessions.WorkCommand,
) (sessions.CommandResult, error) {
	f.commands = append(f.commands, command)
	switch command.Command {
	case "item.create":
		f.item = sessions.WorkItem{
			ID: model.NewID(), WorkspaceID: command.WorkspaceID, Version: 1,
			WorkKind: command.WorkKind, Title: command.Title, BriefMD: command.BriefMD,
			Status: "draft", Priority: command.Priority,
			OwnerKind: command.OwnerKind, OwnerRef: command.OwnerRef, OwnerEpoch: 1,
			ProvenanceKind: command.ProvenanceKind, ProvenanceRef: command.ProvenanceRef,
		}
		return f.result(), nil
	case "item.ready":
		if command.WorkItemID != f.item.ID || command.ExpectedVersion != f.item.Version || f.item.Status != "draft" {
			return sessions.CommandResult{}, errors.New("fake WorkItem ready conflict")
		}
		f.item.Version++
		f.item.Status = "ready"
		return f.result(), nil
	case "item.cancel":
		if command.WorkItemID != f.item.ID || command.ExpectedVersion != f.item.Version {
			return sessions.CommandResult{}, errors.New("fake WorkItem cancel conflict")
		}
		f.item.Version++
		f.item.Status = "canceled"
		return f.result(), nil
	default:
		return sessions.CommandResult{}, errors.New("unexpected fake WorkKernel command")
	}
}

func (f *mcpDurableWorkFake) Get(
	_ context.Context,
	_ model.TenantID,
	_ sessions.WorkPrincipal,
	id model.ID,
) (sessions.WorkSnapshot, error) {
	if id.IsZero() || id != f.item.ID {
		return sessions.WorkSnapshot{}, errors.New("fake WorkItem not found")
	}
	return sessions.WorkSnapshot{Item: f.item}, nil
}

func (f *mcpDurableWorkFake) GetLease(
	_ context.Context,
	_ model.TenantID,
	_ sessions.WorkPrincipal,
	id model.ID,
) (sessions.WorkLease, error) {
	if id.IsZero() || id != f.item.ID || f.lease.WorkItemID != id {
		return sessions.WorkLease{}, errors.New("fake WorkItem lease not found")
	}
	return f.lease, nil
}

func (f *mcpDurableWorkFake) result() sessions.CommandResult {
	return sessions.CommandResult{
		ResultID: f.item.ID, Version: f.item.Version, Status: f.item.Status,
		OwnerEpoch: f.item.OwnerEpoch,
	}
}

type mcpDurableBindingFake struct {
	sessions.ProtocolBindingStore
	work  *mcpDurableWorkFake
	specs map[model.ID]sessions.ProtocolBindingSpec

	bindings     []sessions.ProtocolBinding
	reservations []sessions.ProtocolBindingReservation
	settlements  []sessions.ProtocolBindingSettlement
	observations []sessions.ProtocolBindingObservation
	cancels      []sessions.ProtocolBindingCancelIntent
	interrupts   []sessions.ProtocolInterruptCommand
	responses    []sessions.ProtocolInputResponseCommand
	reserveErr   error
	now          time.Time
}

func (f *mcpDurableBindingFake) RecordProtocolInterrupt(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolInterruptCommand,
) (sessions.ProtocolInterruptResult, error) {
	f.interrupts = append(f.interrupts, command)
	result := sessions.ProtocolInterruptResult{
		BindingID: command.BindingID, Generation: command.Generation,
	}
	for _, request := range command.Requests {
		result.Messages = append(result.Messages, sessions.ProtocolInterruptMessage{
			KeyDigest: request.KeyDigest, MessageID: model.NewID(), DeliveryID: model.NewID(),
		})
	}
	return result, nil
}

func (f *mcpDurableBindingFake) PrepareProtocolInputResponses(
	_ context.Context,
	_ model.TenantID,
	command sessions.ProtocolInputResponseCommand,
) (sessions.ProtocolInputResponseResult, error) {
	f.responses = append(f.responses, command)
	result := sessions.ProtocolInputResponseResult{
		BindingID: command.BindingID, Generation: command.Generation,
	}
	for _, response := range command.Responses {
		result.Responses = append(result.Responses, sessions.ProtocolInputResponseEvidence{
			KeyDigest: response.KeyDigest, AckID: model.NewID(), ResponseMessageID: model.NewID(),
		})
	}
	return result, nil
}

func (f *mcpDurableBindingFake) ReserveProtocolBinding(
	_ context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReservation,
) (sessions.ProtocolBinding, error) {
	f.reservations = append(f.reservations, request)
	if f.reserveErr != nil {
		return sessions.ProtocolBinding{}, f.reserveErr
	}
	if f.work.item.ID != request.WorkItemID || f.work.item.Status != "ready" {
		return sessions.ProtocolBinding{}, errors.New("fake binding reserved non-ready work")
	}
	f.work.item.Version++
	f.work.item.Status = "active"
	projectionJSON, projectionHash, err := mcpTaskProjectionEvidence(*request.MCPTask)
	if err != nil {
		return sessions.ProtocolBinding{}, err
	}
	spec, ok := f.specs[request.BindingSpecID]
	if !ok || spec.Generation != request.BindingSpecGeneration {
		return sessions.ProtocolBinding{}, errors.New("fake binding spec not found")
	}
	binding := sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: model.NewID(), TenantID: tenant, WorkspaceID: request.WorkspaceID,
				Version: 1, CreatedAt: f.tick(),
			},
			UpdatedAt: f.now,
		},
		BindingSpecID: request.BindingSpecID, BindingSpecGeneration: request.BindingSpecGeneration,
		PinnedSpecHash:    append([]byte(nil), spec.SpecHash...),
		PinnedMappingHash: append([]byte(nil), spec.MappingHash...),
		PinnedLossesHash:  append([]byte(nil), spec.LossesHash...),
		WorkItemID:        request.WorkItemID, Protocol: sessions.BindingProtocolMCP,
		ProtocolVersion: spec.ProtocolVersion,
		Direction:       sessions.BindingOutbound, PeerAuthority: spec.PeerAuthority,
		RemoteResourceRef: spec.RemoteResourceRef,
		AttemptID:         request.AttemptID, Generation: request.Generation,
		SyntheticSID: "osn_" + model.NewID().String(),
		OwnerKind:    request.OwnerKind, OwnerRef: request.OwnerRef,
		OwnerDigest: append([]byte(nil), request.OwnerDigest...), OwnerEpoch: request.OwnerEpoch,
		LeaseFence: 1, ExternalKind: request.ExpectedExternalKind,
		ExternalID: request.ExpectedExternalID, LocalState: "reserved", RemoteState: "unobserved",
		ObservationVerdict:    sessions.ProtocolObservationUnknown,
		ObservationCode:       "reserved_before_transmit",
		CurrentTTLMs:          cloneMCPInt64(request.MCPTask.TTLMs),
		CurrentPollIntervalMs: cloneMCPInt64(request.MCPTask.PollIntervalMs),
		MCPTask:               request.MCPTask, MCPTaskHash: projectionHash,
		ProtocolMetadataJSON: projectionJSON,
		LastCommandID:        model.NewID(), LastEventID: model.NewID(), LastEventSeq: 1,
	}
	f.bindings = append(f.bindings, binding)
	return binding, nil
}

func (f *mcpDurableBindingFake) SettleProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingSettlement,
) (sessions.ProtocolBinding, error) {
	f.settlements = append(f.settlements, request)
	index := f.index(request.BindingID)
	if index < 0 || f.bindings[index].Version != request.ExpectedVersion {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingConflict
	}
	binding := &f.bindings[index]
	binding.Version++
	binding.UpdatedAt = f.tick()
	binding.ExternalKind = string(request.ResultKind)
	binding.ExternalID = request.ExternalID
	binding.LocalState, binding.RemoteState = request.LocalState, request.RemoteState
	binding.RemoteRevision = request.RemoteRevision
	binding.ObservationVerdict, binding.ObservationCode = request.Verdict, request.Code
	binding.LastObservedAt = mcpDurableTimePtr(f.now)
	binding.DetailHash = append([]byte(nil), request.DetailHash...)
	binding.CurrentTTLMs = cloneMCPInt64(request.TTLMs)
	binding.CurrentPollIntervalMs = cloneMCPInt64(request.PollIntervalMs)
	binding.Terminal = request.Terminal
	f.work.item.Version++
	f.work.item.Status = request.LocalState
	return *binding, nil
}

func (f *mcpDurableBindingFake) ObserveProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingObservation,
) (sessions.ProtocolBinding, error) {
	f.observations = append(f.observations, request)
	index := f.index(request.BindingID)
	if index < 0 || f.bindings[index].Version != request.ExpectedVersion {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingConflict
	}
	binding := &f.bindings[index]
	binding.Version++
	binding.UpdatedAt = f.tick()
	binding.LocalState, binding.RemoteState = request.LocalState, request.RemoteState
	binding.RemoteRevision = request.RemoteRevision
	binding.ObservationVerdict, binding.ObservationCode = request.Verdict, request.Code
	binding.LastObservedAt = mcpDurableTimePtr(f.now)
	binding.DetailHash = append([]byte(nil), request.DetailHash...)
	binding.CurrentTTLMs = cloneMCPInt64(request.TTLMs)
	binding.CurrentPollIntervalMs = cloneMCPInt64(request.PollIntervalMs)
	binding.Terminal = request.Terminal
	f.work.item.Version++
	f.work.item.Status = request.LocalState
	return *binding, nil
}

func (f *mcpDurableBindingFake) RequestProtocolBindingCancel(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingCancelIntent,
) (sessions.ProtocolBinding, error) {
	f.cancels = append(f.cancels, request)
	index := f.index(request.BindingID)
	if index < 0 || f.bindings[index].Version != request.ExpectedVersion {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingConflict
	}
	binding := &f.bindings[index]
	binding.Version++
	binding.UpdatedAt = f.tick()
	binding.CancelRequested = true
	binding.CancelRequestedAt = mcpDurableTimePtr(f.now)
	binding.CancelReasonCode = request.ReasonCode
	binding.LocalState = "cancel_requested"
	binding.ObservationVerdict = sessions.ProtocolObservationUnknown
	binding.ObservationCode = "cancel_requested_unobserved"
	binding.LastObservedAt = mcpDurableTimePtr(f.now)
	return *binding, nil
}

func (f *mcpDurableBindingFake) ListProtocolBindings(
	_ context.Context,
	_ model.TenantID,
	query sessions.ProtocolBindingQuery,
) (sessions.ProtocolBindingPage, error) {
	page := sessions.ProtocolBindingPage{Items: []sessions.ProtocolBinding{}}
	for _, binding := range f.bindings {
		if binding.WorkspaceID != query.WorkspaceID ||
			(!query.BindingSpecID.IsZero() && binding.BindingSpecID != query.BindingSpecID) ||
			(query.Protocol != "" && binding.Protocol != query.Protocol) ||
			(query.PeerAuthority != "" && binding.PeerAuthority != query.PeerAuthority) ||
			(query.ExternalKind != "" && binding.ExternalKind != query.ExternalKind) ||
			(query.ExternalID != "" && binding.ExternalID != query.ExternalID) {
			continue
		}
		binding.Replayed = false
		page.Items = append(page.Items, binding)
	}
	return page, nil
}

func (f *mcpDurableBindingFake) GetProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	ref sessions.ProtocolBindingRef,
) (sessions.ProtocolBinding, error) {
	index := f.index(ref.ID)
	if index < 0 {
		return sessions.ProtocolBinding{}, sessions.ErrProtocolBindingNotFound
	}
	binding := f.bindings[index]
	binding.Replayed = false
	return binding, nil
}

func (f *mcpDurableBindingFake) index(id model.ID) int {
	for i := range f.bindings {
		if f.bindings[i].ID == id {
			return i
		}
	}
	return -1
}

func (f *mcpDurableBindingFake) tick() time.Time {
	if f.now.IsZero() {
		f.now = time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	} else {
		f.now = f.now.Add(time.Second)
	}
	return f.now
}

type mcpDurableAdapterFixture struct {
	tenant   model.TenantID
	config   mcpDurableTaskStoreConfig
	owner    mcpc.TaskOwner
	work     *mcpDurableWorkFake
	bindings *mcpDurableBindingFake
	store    *mcpDurableTaskStore
}

type mcpDurableWorkItemResolverFunc func(
	context.Context,
	model.TenantID,
	mcpDurableWorkItemResolveRequest,
) (model.ID, bool, error)

func (f mcpDurableWorkItemResolverFunc) ResolveMCPDurableWorkItem(
	ctx context.Context,
	tenant model.TenantID,
	request mcpDurableWorkItemResolveRequest,
) (model.ID, bool, error) {
	return f(ctx, tenant, request)
}

func newMCPDurableAdapterFixture(t *testing.T) *mcpDurableAdapterFixture {
	t.Helper()
	fixture := &mcpDurableAdapterFixture{
		tenant: model.NewTenantID(),
		config: mcpDurableTaskStoreConfig{
			WorkspaceID: model.NewID(), BindingSpecID: model.NewID(), Generation: 3,
			OwnerKind: "agent", OwnerRef: "agent-local",
			InterruptRoute: sessions.ProtocolInterruptRoute{
				ChannelID: model.NewID(), SenderUserID: model.NewID(), RecipientUserID: model.NewID(),
			},
		},
		work: &mcpDurableWorkFake{},
	}
	fixture.owner = mcpc.TaskOwner{
		Tenant: fixture.tenant.String(), Issuer: "https://issuer.example",
		Subject: "subject-1", ClientID: "client-1",
	}
	fixture.bindings = &mcpDurableBindingFake{
		work: fixture.work, specs: make(map[model.ID]sessions.ProtocolBindingSpec),
	}
	store, err := newMCPDurableTaskStoreWithPorts(
		fixture.tenant, fixture.work, fixture.bindings, fixture.bindings, fixture.config,
	)
	if err != nil {
		t.Fatalf("construct MCP durable adapter: %v", err)
	}
	fixture.store = store
	if err := fixture.store.bindUpstreamDescriptor("mcp.example"); err != nil {
		t.Fatalf("bind MCP durable adapter upstream descriptor: %v", err)
	}
	specInput := sessions.ProtocolBindingSpecInput{
		WorkspaceID: fixture.config.WorkspaceID, BindingKey: "mcp-tasks", Generation: fixture.config.Generation,
		Protocol: sessions.BindingProtocolMCP, ProtocolVersion: "2026-07-28",
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: "mcp.example", RemoteResourceKind: "tasks", RemoteResourceRef: "resource-server:primary",
		MappingSchema: sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "task.summary", Target: "work.brief",
			Cardinality: sessions.ProtocolMappingOneToOne, Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{}, RuleRefs: []string{"rule:mcp-task"},
		PermissionProfileRef: "permission:mcp-task", CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{Verdict: sessions.ProtocolObservationClean, Code: "validated"},
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(specInput)
	if err != nil {
		t.Fatalf("MCP runtime spec digests: %v", err)
	}
	spec := sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{CommunicationEntity: sessions.CommunicationEntity{
			ID: fixture.config.BindingSpecID, TenantID: fixture.tenant,
			WorkspaceID: fixture.config.WorkspaceID, Version: 2,
		}},
		BindingKey: specInput.BindingKey, Generation: specInput.Generation,
		Protocol: specInput.Protocol, ProtocolVersion: specInput.ProtocolVersion,
		Direction: specInput.Direction, LocalKind: specInput.LocalKind, LocalSelector: specInput.LocalSelector,
		PeerAuthority: specInput.PeerAuthority, RemoteResourceKind: specInput.RemoteResourceKind,
		RemoteResourceRef: specInput.RemoteResourceRef, MappingSchema: specInput.MappingSchema,
		Mapping: specInput.Mapping, MappingHash: digests.MappingHash,
		KnownLosses: specInput.KnownLosses, LossesHash: digests.LossesHash,
		RuleRefs: specInput.RuleRefs, PermissionProfileRef: specInput.PermissionProfileRef,
		CurrencyPolicy: specInput.CurrencyPolicy, Validation: specInput.Validation,
		State: sessions.ProtocolBindingSpecActive, SpecHash: digests.SpecHash,
	}
	store.specs = mcpDurableSpecFake{spec: spec}
	fixture.bindings.specs[spec.ID] = spec
	return fixture
}

func (f *mcpDurableAdapterFixture) intent(taskID, operationID, effectDigest string) mcpc.DurableTaskIntent {
	ttl, poll := int64(60_000), int64(1_000)
	return mcpc.DurableTaskIntent{
		Owner: f.owner, TaskID: taskID, Tool: "tools/search", RequiredScope: "search:read",
		CreatedAt: time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC),
		TTLMs:     &ttl, PollIntervalMs: &poll,
		InitialStatus: "working", InitialStatusReason: "upstream text must not persist",
		UpstreamDescriptor: "mcp.example", ProtocolVersion: "2026-07-28",
		OriginOperationID: operationID, OriginEffectDigest: effectDigest,
	}
}

func (f *mcpDurableAdapterFixture) seedBinding(
	intent mcpc.DurableTaskIntent,
	generation int64,
	terminal bool,
	localState string,
	remoteState string,
) sessions.ProtocolBinding {
	spec, ok := f.bindings.specs[f.config.BindingSpecID]
	if !ok {
		panic("MCP durable fixture has no configured spec")
	}
	projection := mcpTaskProjection(intent)
	metadata, projectionHash, _ := mcpTaskProjectionEvidence(projection)
	ownerDigest, _ := mcpTaskOwnerDigest(intent.Owner)
	observed := f.bindings.tick()
	return sessions.ProtocolBinding{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: model.NewID(), TenantID: f.tenant, WorkspaceID: f.config.WorkspaceID,
				Version: 2, CreatedAt: observed.Add(-time.Minute),
			},
			UpdatedAt: observed,
		},
		BindingSpecID: f.config.BindingSpecID, BindingSpecGeneration: f.config.Generation,
		PinnedSpecHash:    append([]byte(nil), spec.SpecHash...),
		PinnedMappingHash: append([]byte(nil), spec.MappingHash...),
		PinnedLossesHash:  append([]byte(nil), spec.LossesHash...),
		WorkItemID:        model.NewID(), Protocol: sessions.BindingProtocolMCP,
		ProtocolVersion: spec.ProtocolVersion, Direction: sessions.BindingOutbound,
		PeerAuthority: spec.PeerAuthority, RemoteResourceRef: spec.RemoteResourceRef,
		AttemptID: model.NewID(), Generation: generation,
		SyntheticSID: "osn_" + model.NewID().String(),
		OwnerKind:    f.config.OwnerKind, OwnerRef: f.config.OwnerRef,
		OwnerDigest: ownerDigest, OwnerEpoch: 1, LeaseFence: 1,
		ExternalKind: mcpDurableExternalKind, ExternalID: intent.TaskID,
		LocalState: localState, RemoteState: remoteState, RemoteRevision: intent.ProtocolVersion,
		ObservationVerdict: sessions.ProtocolObservationClean, ObservationCode: "mcp_register",
		LastObservedAt: &observed, DetailHash: projectionHash,
		CurrentTTLMs:          cloneMCPInt64(intent.TTLMs),
		CurrentPollIntervalMs: cloneMCPInt64(intent.PollIntervalMs),
		Terminal:              terminal, MCPTask: &projection, MCPTaskHash: projectionHash,
		ProtocolMetadataJSON: metadata,
		LastCommandID:        model.NewID(), LastEventID: model.NewID(), LastEventSeq: 2,
	}
}

func mcpDurableSuccessorSpec(
	t *testing.T,
	prior sessions.ProtocolBindingSpec,
) sessions.ProtocolBindingSpec {
	t.Helper()
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: prior.WorkspaceID, BindingKey: prior.BindingKey, Generation: prior.Generation + 1,
		Protocol: prior.Protocol, ProtocolVersion: prior.ProtocolVersion,
		Direction: prior.Direction, LocalKind: prior.LocalKind, LocalSelector: prior.LocalSelector,
		PeerAuthority: prior.PeerAuthority, RemoteResourceKind: prior.RemoteResourceKind,
		RemoteResourceRef: prior.RemoteResourceRef, MappingSchema: prior.MappingSchema,
		Mapping:     append([]sessions.ProtocolMappingRule(nil), prior.Mapping...),
		KnownLosses: append([]sessions.ProtocolBindingLoss(nil), prior.KnownLosses...),
		RuleRefs:    append([]string(nil), prior.RuleRefs...), PermissionProfileRef: prior.PermissionProfileRef,
		CurrencyPolicy: prior.CurrencyPolicy, Validation: prior.Validation, SupersedesID: prior.ID,
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(input)
	if err != nil {
		t.Fatalf("successor spec digests: %v", err)
	}
	return sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{CommunicationEntity: sessions.CommunicationEntity{
			ID: model.NewID(), TenantID: prior.TenantID, WorkspaceID: prior.WorkspaceID, Version: 2,
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
		State: sessions.ProtocolBindingSpecActive, SupersedesID: prior.ID, SpecHash: digests.SpecHash,
	}
}

func TestMCPDurableTaskValidatesConfiguredProtocolSuccessor(t *testing.T) {
	tenant := model.NewTenantID()
	workspace, specID := model.NewID(), model.NewID()
	current := sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{
			CommunicationEntity: sessions.CommunicationEntity{
				ID: specID, TenantID: tenant, WorkspaceID: workspace, Version: 2,
			},
		},
		BindingKey: "mcp-tasks", Generation: 3,
		Protocol: sessions.BindingProtocolMCP, ProtocolVersion: "2026-06-18",
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		PeerAuthority:      "mcp.example",
		RemoteResourceKind: "tasks", RemoteResourceRef: "resource-server:primary",
		RuleRefs: []string{"rule:mcp-task"}, PermissionProfileRef: "permission:mcp-task",
		State: sessions.ProtocolBindingSpecActive,
	}
	store := &mcpDurableTaskStore{
		tenant: tenant, specs: mcpDurableSpecFake{spec: current},
		config: mcpDurableTaskStoreConfig{
			WorkspaceID: workspace, BindingSpecID: specID, Generation: current.Generation,
			Policy: mcpTaskRuntimePolicy,
		},
	}
	successor := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: current.BindingKey, Generation: 4,
		Protocol: current.Protocol, ProtocolVersion: current.ProtocolVersion,
		Direction: current.Direction, LocalKind: current.LocalKind,
		PeerAuthority:      current.PeerAuthority,
		RemoteResourceKind: current.RemoteResourceKind,
		RemoteResourceRef:  current.RemoteResourceRef, RuleRefs: current.RuleRefs,
		PermissionProfileRef: current.PermissionProfileRef, SupersedesID: current.ID,
	}
	validation, err := store.ValidateProtocolBindingSpec(context.Background(), tenant, successor)
	if err != nil || validation.Verdict != sessions.ProtocolObservationClean ||
		validation.Code != "mcp_tasks_capability_validated" || validation.ObservedAt.IsZero() {
		t.Fatalf("MCP successor validation = %#v, %v", validation, err)
	}
	changed := successor
	changed.RemoteResourceRef = "resource-server:other"
	if _, err := store.ValidateProtocolBindingSpec(context.Background(), tenant, changed); err == nil {
		t.Fatal("unconfigured MCP resource was accepted as a validated successor")
	}
}

func TestMCPDurableTaskValidatesConfiguredFirstDraftGeneration(t *testing.T) {
	tenant, workspace, specID := model.NewTenantID(), model.NewID(), model.NewID()
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: "mcp-bootstrap", Generation: 1,
		Protocol: sessions.BindingProtocolMCP, ProtocolVersion: "2025-11-25",
		Direction: sessions.BindingOutbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: "mcp-bootstrap.example", RemoteResourceKind: "tasks",
		RemoteResourceRef: "resource-server:primary", MappingSchema: sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "task.summary", Target: "work.brief",
			Cardinality: sessions.ProtocolMappingOneToOne, Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{}, RuleRefs: []string{"rule:mcp-task"},
		PermissionProfileRef: "permission:mcp-task", CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{Verdict: sessions.ProtocolObservationClean, Code: "validated"},
	}
	digests, err := sessions.ComputeProtocolBindingSpecDigests(input)
	if err != nil {
		t.Fatalf("digests: %v", err)
	}
	current := sessions.ProtocolBindingSpec{
		MutableCommunicationEntity: sessions.MutableCommunicationEntity{CommunicationEntity: sessions.CommunicationEntity{
			ID: specID, TenantID: tenant, WorkspaceID: workspace, Version: 1,
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
		State: sessions.ProtocolBindingSpecDraft, SpecHash: digests.SpecHash,
	}
	store := &mcpDurableTaskStore{
		tenant: tenant, specs: mcpDurableSpecFake{spec: current},
		config: mcpDurableTaskStoreConfig{
			WorkspaceID: workspace, BindingSpecID: specID, Generation: 1,
			Policy: mcpTaskRuntimePolicy,
		},
	}
	validation, err := store.ValidateProtocolBindingSpec(context.Background(), tenant, input)
	if err != nil || validation.Verdict != sessions.ProtocolObservationClean {
		t.Fatalf("first draft validation = %#v, %v", validation, err)
	}
}

func TestMCPDurableTaskRegisterCreatesWorkAndReplays(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	intent := fixture.intent("task-register-1", "operation-register-1", strings.Repeat("a", 64))

	ref, err := fixture.store.Register(context.Background(), intent)
	if err != nil {
		t.Fatalf("register durable MCP task: %v", err)
	}
	if ref.TaskID != intent.TaskID || ref.Generation != 1 || ref.BindingID == "" ||
		ref.WorkItemID == "" || !strings.HasPrefix(ref.SID, "osn_") {
		t.Fatalf("durable ref = %#v", ref)
	}
	if len(fixture.work.commands) != 2 || fixture.work.commands[0].Command != "item.create" ||
		fixture.work.commands[1].Command != "item.ready" {
		t.Fatalf("WorkKernel commands = %#v", fixture.work.commands)
	}
	created := fixture.work.commands[0]
	if created.WorkKind != "operations" || created.Priority != "p2" ||
		!strings.Contains(created.BriefMD, intent.Tool) ||
		!strings.Contains(created.BriefMD, intent.OriginOperationID) ||
		!strings.Contains(created.BriefMD, intent.OriginEffectDigest) ||
		strings.Contains(created.BriefMD, intent.TaskID) ||
		strings.Contains(created.BriefMD, intent.InitialStatusReason) {
		t.Fatalf("WorkItem brief crossed its projection boundary: %q", created.BriefMD)
	}
	if len(fixture.bindings.reservations) != 1 || len(fixture.bindings.settlements) != 1 {
		t.Fatalf("binding writes: reserve=%d settle=%d",
			len(fixture.bindings.reservations), len(fixture.bindings.settlements))
	}
	reservation := fixture.bindings.reservations[0]
	if reservation.ExpectedExternalID != intent.TaskID || reservation.WorkItemID.String() != ref.WorkItemID ||
		reservation.MCPTask.InitialStatusReason != "" || len(reservation.OwnerDigest) != 32 {
		t.Fatalf("binding reservation = %#v", reservation)
	}
	if got := fixture.bindings.settlements[0].LocalState; got != "active" {
		t.Fatalf("Register LocalState = %q, want active", got)
	}

	replayed, err := fixture.store.Register(context.Background(), intent)
	if err != nil || replayed != ref {
		t.Fatalf("exact Register replay = %#v, %v; want %#v", replayed, err, ref)
	}
	if len(fixture.work.commands) != 2 || len(fixture.bindings.reservations) != 1 {
		t.Fatalf("exact replay repeated effects: work=%d reserve=%d",
			len(fixture.work.commands), len(fixture.bindings.reservations))
	}
	view, err := fixture.store.Get(context.Background(), fixture.owner, intent.TaskID, 0)
	if err != nil || view.Intent.InitialStatusReason != "" {
		t.Fatalf("rehydrated immutable projection = %#v, %v", view.Intent, err)
	}
}

func TestMCPDurableTaskRegisterRequiresRuntimePeerAndProtocolPins(t *testing.T) {
	for _, row := range []struct {
		name   string
		mutate func(*mcpc.DurableTaskIntent)
	}{
		{name: "protocol version", mutate: func(intent *mcpc.DurableTaskIntent) {
			intent.ProtocolVersion = "2026-06-18"
		}},
		{name: "upstream authority", mutate: func(intent *mcpc.DurableTaskIntent) {
			intent.UpstreamDescriptor = "other-mcp.example"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			fixture := newMCPDurableAdapterFixture(t)
			intent := fixture.intent("task-runtime-pin", "operation-runtime-pin", strings.Repeat("a", 64))
			row.mutate(&intent)
			if _, err := fixture.store.Register(context.Background(), intent); err == nil {
				t.Fatal("registration with changed runtime pin succeeded")
			}
			if len(fixture.work.commands) != 0 || len(fixture.bindings.reservations) != 0 {
				t.Fatalf("changed pin performed local effects: work=%d reservations=%d",
					len(fixture.work.commands), len(fixture.bindings.reservations))
			}
		})
	}
}

func TestMCPDurableTaskRegisterConsumesConfiguredPolicyTuple(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	fixture.store.config.Policy = protocolRuntimePolicy{
		ruleRefs: []string{"rule:mcp-task-v2"}, permissionProfileRef: "permission:mcp-task",
	}
	intent := fixture.intent("task-policy-pin", "operation-policy-pin", strings.Repeat("a", 64))
	if _, err := fixture.store.Register(context.Background(), intent); err == nil {
		t.Fatal("registration with a policy tuple different from the active spec succeeded")
	}
	if len(fixture.work.commands) != 0 || len(fixture.bindings.reservations) != 0 {
		t.Fatalf("changed policy performed local effects: work=%d reservations=%d",
			len(fixture.work.commands), len(fixture.bindings.reservations))
	}
}

func TestMCPDurableTaskRegisterConflicts(t *testing.T) {
	t.Run("live task ID", func(t *testing.T) {
		fixture := newMCPDurableAdapterFixture(t)
		incoming := fixture.intent("task-collision", "operation-old", strings.Repeat("b", 64))
		live := fixture.intent(incoming.TaskID, "operation-current", strings.Repeat("c", 64))
		fixture.bindings.bindings = append(fixture.bindings.bindings,
			fixture.seedBinding(incoming, 1, true, "review", "completed"),
			fixture.seedBinding(live, 2, false, "active", "working"))

		_, err := fixture.store.Register(context.Background(), incoming)
		if !errors.Is(err, mcpc.ErrDurableTaskConflict) {
			t.Fatalf("Register collision = %v, want ErrDurableTaskConflict", err)
		}
		if len(fixture.work.commands) != 0 {
			t.Fatalf("preflight collision created WorkItem: %#v", fixture.work.commands)
		}
	})

	t.Run("atomic reservation race compensates local work", func(t *testing.T) {
		fixture := newMCPDurableAdapterFixture(t)
		fixture.bindings.reserveErr = sessions.ErrProtocolBindingConflict
		intent := fixture.intent("task-race", "operation-race", strings.Repeat("d", 64))

		_, err := fixture.store.Register(context.Background(), intent)
		if !errors.Is(err, mcpc.ErrDurableTaskConflict) {
			t.Fatalf("Reserve race = %v, want ErrDurableTaskConflict", err)
		}
		if len(fixture.work.commands) != 3 || fixture.work.commands[2].Command != "item.cancel" ||
			fixture.work.item.Status != "canceled" {
			t.Fatalf("race compensation = commands %#v, status %q",
				fixture.work.commands, fixture.work.item.Status)
		}
	})
}

func TestMCPDurableTaskGetAndListRehydrateStrictCurrentProjection(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	oldIntent := fixture.intent("task-a", "operation-a-old", strings.Repeat("e", 64))
	newIntent := fixture.intent("task-a", "operation-a-new", strings.Repeat("f", 64))
	otherIntent := fixture.intent("task-b", "operation-b", strings.Repeat("1", 64))
	old := fixture.seedBinding(oldIntent, 1, true, "review", "completed")
	current := fixture.seedBinding(newIntent, 2, false, "active", "working")
	other := fixture.seedBinding(otherIntent, 1, false, "active", "working")
	fixture.bindings.bindings = []sessions.ProtocolBinding{old, current, other}

	view, err := fixture.store.Get(context.Background(), fixture.owner, newIntent.TaskID, 0)
	if err != nil || view.Ref.Generation != 2 || view.Intent.OriginOperationID != newIntent.OriginOperationID {
		t.Fatalf("Get current projection = %#v, %v", view, err)
	}
	wrongOwner := fixture.owner
	wrongOwner.Issuer = "https://another-issuer.example"
	if _, err := fixture.store.Get(context.Background(), wrongOwner, newIntent.TaskID, 0); !errors.Is(err, mcpc.ErrDurableTaskNotFound) {
		t.Fatalf("Get wrong owner = %v, want not found", err)
	}

	selector := mcpc.TaskOwner{Tenant: fixture.tenant.String()}
	first, err := fixture.store.List(context.Background(), selector, "", 1)
	if err != nil || len(first.Tasks) != 1 || first.Tasks[0].Ref.TaskID != "task-a" || first.NextCursor == "" {
		t.Fatalf("first inventory page = %#v, %v", first, err)
	}
	second, err := fixture.store.List(context.Background(), selector, first.NextCursor, 1)
	if err != nil || len(second.Tasks) != 1 || second.Tasks[0].Ref.TaskID != "task-b" || second.NextCursor != "" {
		t.Fatalf("second inventory page = %#v, %v", second, err)
	}

	fixture.bindings.bindings[1].ProtocolMetadataJSON = append(
		fixture.bindings.bindings[1].ProtocolMetadataJSON, byte(' '),
	)
	if _, err := fixture.store.List(context.Background(), selector, "", 10); err == nil {
		t.Fatal("List accepted a non-canonical durable projection")
	}
}

func TestMCPDurableTaskSuccessorKeepsAncestorTasksLive(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	oldSpec := fixture.bindings.specs[fixture.config.BindingSpecID]
	oldIntent := fixture.intent("task-old-generation", "operation-old-generation", strings.Repeat("a", 64))
	oldBinding := fixture.seedBinding(oldIntent, 1, false, "active", "working")
	oldSpec.State = sessions.ProtocolBindingSpecSuperseded
	successor := mcpDurableSuccessorSpec(t, oldSpec)
	fixture.bindings.specs[oldSpec.ID] = oldSpec
	fixture.bindings.specs[successor.ID] = successor
	fixture.store.specs = mcpDurableSpecFake{specs: fixture.bindings.specs}
	fixture.store.config.BindingSpecID = successor.ID
	fixture.store.config.Generation = successor.Generation
	fixture.bindings.bindings = []sessions.ProtocolBinding{oldBinding}
	fixture.work.item = sessions.WorkItem{
		ID: oldBinding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 4,
		Status: "active", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
		OwnerEpoch: 1,
	}

	view, err := fixture.store.Get(context.Background(), fixture.owner, oldIntent.TaskID, 1)
	if err != nil || view.Ref.BindingID != oldBinding.ID.String() {
		t.Fatalf("Get ancestor generation = %#v, %v", view, err)
	}
	page, err := fixture.store.List(
		context.Background(), mcpc.TaskOwner{Tenant: fixture.tenant.String()}, "", 10,
	)
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].Ref.TaskID != oldIntent.TaskID {
		t.Fatalf("List after successor = %#v, %v", page, err)
	}
	if err := fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
		TaskID: oldIntent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationUpdate,
		Status: "working", Verdict: mcpc.DurableTaskVerdictClean,
		ObservedAt:   time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC),
		ResultDigest: strings.Repeat("b", 64), OperationID: "operation-old-update",
		Dispatched: true, Acknowledged: true,
	}); err != nil {
		t.Fatalf("Update ancestor generation: %v", err)
	}
	if err := fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
		TaskID: oldIntent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationCancel,
		Status: "working", Verdict: mcpc.DurableTaskVerdictClean,
		ObservedAt:   time.Date(2026, 8, 19, 10, 1, 0, 0, time.UTC),
		ResultDigest: strings.Repeat("c", 64), OperationID: "operation-old-cancel",
		Dispatched: true, Acknowledged: true, CancelRequested: true,
	}); err != nil {
		t.Fatalf("Cancel ancestor generation: %v", err)
	}
	if len(fixture.bindings.cancels) != 1 || fixture.bindings.cancels[0].BindingID != oldBinding.ID {
		t.Fatalf("ancestor cancel writes = %#v", fixture.bindings.cancels)
	}

	newIntent := fixture.intent("task-new-generation", "operation-new-generation", strings.Repeat("d", 64))
	ref, err := fixture.store.Register(context.Background(), newIntent)
	if err != nil {
		t.Fatalf("Register successor generation: %v", err)
	}
	if ref.TaskID != newIntent.TaskID || len(fixture.bindings.reservations) != 1 ||
		fixture.bindings.reservations[0].BindingSpecID != successor.ID ||
		fixture.bindings.reservations[0].BindingSpecGeneration != successor.Generation {
		t.Fatalf("successor registration = ref %#v reservations %#v", ref, fixture.bindings.reservations)
	}
}

func TestMCPDurableTaskExistingWorkItemUsesOnlyLocalAuthority(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	existing := model.NewID()
	fixture.work.item = sessions.WorkItem{
		ID: existing, WorkspaceID: fixture.config.WorkspaceID, Version: 7,
		Status: "ready", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
		OwnerEpoch: 4,
	}
	fixture.work.lease = sessions.WorkLease{
		ID: model.NewID(), WorkspaceID: fixture.config.WorkspaceID, WorkItemID: existing,
		Version: 3, Fence: 9, State: "available",
	}
	var resolved mcpDurableWorkItemResolveRequest
	fixture.store.config.WorkItemResolver = mcpDurableWorkItemResolverFunc(func(
		_ context.Context,
		tenant model.TenantID,
		request mcpDurableWorkItemResolveRequest,
	) (model.ID, bool, error) {
		if tenant != fixture.tenant {
			t.Fatalf("resolver tenant = %s, want %s", tenant, fixture.tenant)
		}
		resolved = request
		return existing, true, nil
	})
	intent := fixture.intent("task-existing-work", "operation-existing-work", strings.Repeat("e", 64))
	intent.InitialStatusReason = `{"work_item_id":"` + model.NewID().String() + `","lease_fence":999}`

	ref, err := fixture.store.Register(context.Background(), intent)
	if err != nil {
		t.Fatalf("Register against existing WorkItem: %v", err)
	}
	if ref.WorkItemID != existing.String() || len(fixture.work.commands) != 0 ||
		resolved.WorkspaceID != fixture.config.WorkspaceID || resolved.TaskID != intent.TaskID ||
		len(fixture.bindings.reservations) != 1 ||
		fixture.bindings.reservations[0].WorkItemID != existing ||
		fixture.bindings.reservations[0].OwnerEpoch != 4 ||
		fixture.bindings.reservations[0].LeaseFence != 9 {
		t.Fatalf("existing WorkItem registration = ref %#v request %#v commands %#v reservation %#v",
			ref, resolved, fixture.work.commands, fixture.bindings.reservations)
	}
}

func TestMCPDurableTaskExistingWorkItemIsNeverConflictCompensation(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	existing := model.NewID()
	fixture.work.item = sessions.WorkItem{
		ID: existing, WorkspaceID: fixture.config.WorkspaceID, Version: 7,
		Status: "ready", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
		OwnerEpoch: 4,
	}
	fixture.work.lease = sessions.WorkLease{
		ID: model.NewID(), WorkspaceID: fixture.config.WorkspaceID, WorkItemID: existing,
		Version: 3, Fence: 9, State: "available",
	}
	fixture.store.config.WorkItemResolver = mcpDurableWorkItemResolverFunc(func(
		context.Context, model.TenantID, mcpDurableWorkItemResolveRequest,
	) (model.ID, bool, error) {
		return existing, true, nil
	})
	fixture.bindings.reserveErr = sessions.ErrProtocolBindingConflict
	intent := fixture.intent("task-existing-conflict", "operation-existing-conflict", strings.Repeat("f", 64))
	if _, err := fixture.store.Register(context.Background(), intent); !errors.Is(err, mcpc.ErrDurableTaskConflict) {
		t.Fatalf("existing WorkItem conflict = %v, want durable conflict", err)
	}
	if len(fixture.work.commands) != 0 || fixture.work.item.Status != "ready" {
		t.Fatalf("existing WorkItem was compensated: commands %#v item %#v", fixture.work.commands, fixture.work.item)
	}
}

func TestMCPDurableTaskUpdateObservationDrivesProtocolLifecycle(t *testing.T) {
	t.Run("unexpected remote cancel is broken and blocked", func(t *testing.T) {
		fixture := newMCPDurableAdapterFixture(t)
		intent := fixture.intent("task-unexpected-cancel", "operation-unexpected-cancel", strings.Repeat("7", 64))
		binding := fixture.seedBinding(intent, 1, false, "active", "working")
		fixture.bindings.bindings = []sessions.ProtocolBinding{binding}
		fixture.work.item = sessions.WorkItem{
			ID: binding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 4,
			Status: "active", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
			OwnerEpoch: 1,
		}

		err := fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "cancelled", Verdict: mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("8", 64), OperationID: "operation-unexpected-cancel-get",
			Dispatched: true, Terminal: true,
		})
		if err != nil {
			t.Fatalf("persist unexpected remote cancellation: %v", err)
		}
		if len(fixture.bindings.cancels) != 0 || len(fixture.bindings.observations) != 1 {
			t.Fatalf("unexpected cancellation writes: cancel=%#v observe=%#v",
				fixture.bindings.cancels, fixture.bindings.observations)
		}
		observation := fixture.bindings.observations[0]
		if observation.LocalState != "blocked" || fixture.work.item.Status != "blocked" ||
			observation.Verdict != sessions.ProtocolObservationBroken ||
			observation.Code != "unexpected_remote_cancel" || !observation.Terminal {
			t.Fatalf("unexpected cancellation lifecycle: observation=%#v work=%#v",
				observation, fixture.work.item)
		}
	})

	t.Run("cancel intent stays active until confirmed", func(t *testing.T) {
		fixture := newMCPDurableAdapterFixture(t)
		intent := fixture.intent("task-cancel", "operation-cancel", strings.Repeat("2", 64))
		binding := fixture.seedBinding(intent, 1, false, "active", "working")
		fixture.bindings.bindings = []sessions.ProtocolBinding{binding}
		fixture.work.item = sessions.WorkItem{
			ID: binding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 4,
			Status: "active", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
			OwnerEpoch: 1,
		}

		err := fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationCancel,
			Status: "working", StatusReason: "raw cancellation acknowledgement",
			Verdict:      mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("3", 64), OperationID: "operation-cancel-request",
			Dispatched: true, Acknowledged: true, CancelRequested: true,
		})
		if err != nil {
			t.Fatalf("persist cancel acknowledgement: %v", err)
		}
		if len(fixture.bindings.cancels) != 1 || len(fixture.bindings.observations) != 1 ||
			fixture.bindings.observations[0].LocalState != "blocked" || fixture.work.item.Status != "blocked" {
			t.Fatalf("unconfirmed cancel lifecycle: cancels=%#v observations=%#v work=%#v",
				fixture.bindings.cancels, fixture.bindings.observations, fixture.work.item)
		}
		err = fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "working", Verdict: mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 14, 30, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("9", 64), OperationID: "operation-cancel-still-working",
			Dispatched: true,
		})
		if err != nil || fixture.work.item.Status != "blocked" {
			t.Fatalf("pending cancellation was reactivated by working report: work=%#v, err=%v",
				fixture.work.item, err)
		}

		err = fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "cancelled", Verdict: mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 15, 0, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("4", 64), OperationID: "operation-cancel-confirm",
			Dispatched: true, Terminal: true,
		})
		if err != nil {
			t.Fatalf("persist confirmed cancellation: %v", err)
		}
		last := fixture.bindings.observations[len(fixture.bindings.observations)-1]
		if last.LocalState != "canceled" || fixture.work.item.Status != "canceled" ||
			len(fixture.bindings.cancels) != 1 {
			t.Fatalf("confirmed cancellation lifecycle: observation=%#v work=%#v",
				last, fixture.work.item)
		}
	})

	t.Run("input resumes active then completed enters review", func(t *testing.T) {
		fixture := newMCPDurableAdapterFixture(t)
		intent := fixture.intent("task-completed", "operation-completed", strings.Repeat("5", 64))
		binding := fixture.seedBinding(intent, 1, false, "active", "working")
		fixture.bindings.bindings = []sessions.ProtocolBinding{binding}
		fixture.work.item = sessions.WorkItem{
			ID: binding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 4,
			Status: "active", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
			OwnerEpoch: 1,
		}
		err := fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "input_required", Verdict: mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("a", 64), OperationID: "operation-get-input-required",
			Dispatched: true,
			InputRequests: []mcpc.DurableTaskInputRef{
				{KeyDigest: strings.Repeat("f", 64), ContentDigest: strings.Repeat("1", 64)},
				{KeyDigest: strings.Repeat("e", 64), ContentDigest: strings.Repeat("2", 64)},
			},
		})
		if err != nil || fixture.work.item.Status != "blocked" ||
			fixture.bindings.observations[len(fixture.bindings.observations)-1].LocalState != "blocked" {
			t.Fatalf("input-required lifecycle: work=%#v observations=%#v, err=%v",
				fixture.work.item, fixture.bindings.observations, err)
		}
		if len(fixture.bindings.interrupts) != 1 ||
			fixture.bindings.interrupts[0].BindingID != binding.ID ||
			fixture.bindings.interrupts[0].Generation != binding.Generation ||
			fixture.bindings.interrupts[0].Route != fixture.config.InterruptRoute ||
			len(fixture.bindings.interrupts[0].Requests) != 2 ||
			fixture.bindings.interrupts[0].Requests[0].KeyDigest != strings.Repeat("e", 64) {
			t.Fatalf("input-required communication = %#v", fixture.bindings.interrupts)
		}
		err = fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "working", Verdict: mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 13, 30, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("b", 64), OperationID: "operation-get-resumed",
			Dispatched: true,
		})
		if err != nil || fixture.work.item.Status != "active" ||
			fixture.bindings.observations[len(fixture.bindings.observations)-1].LocalState != "active" {
			t.Fatalf("resumed lifecycle: work=%#v observations=%#v, err=%v",
				fixture.work.item, fixture.bindings.observations, err)
		}
		ttl, poll := int64(120_000), int64(2_000)
		err = fixture.store.UpdateObservation(context.Background(), fixture.owner, mcpc.DurableTaskObservation{
			TaskID: intent.TaskID, Generation: 1, Kind: mcpc.DurableTaskObservationGet,
			Status: "completed", StatusReason: "raw result summary is hash-only",
			TTLMs: &ttl, PollIntervalMs: &poll,
			Verdict:      mcpc.DurableTaskVerdictClean,
			ObservedAt:   time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC),
			ResultDigest: strings.Repeat("6", 64), OperationID: "operation-get-completed",
			Dispatched: true, Terminal: true,
		})
		if err != nil {
			t.Fatalf("persist completed observation: %v", err)
		}
		observation := fixture.bindings.observations[len(fixture.bindings.observations)-1]
		if observation.LocalState != "review" || fixture.work.item.Status != "review" ||
			observation.TTLMs == nil || *observation.TTLMs != ttl ||
			observation.PollIntervalMs == nil || *observation.PollIntervalMs != poll ||
			len(observation.DetailHash) != 32 {
			t.Fatalf("completed lifecycle observation = %#v, work=%#v", observation, fixture.work.item)
		}
	})
}

func TestMCPDurableTaskPrepareInputResponsesUsesExactBindingAndRoute(t *testing.T) {
	fixture := newMCPDurableAdapterFixture(t)
	intent := fixture.intent("task-input-response", "operation-input-response-origin", strings.Repeat("3", 64))
	binding := fixture.seedBinding(intent, 2, false, "blocked", "input_required")
	fixture.bindings.bindings = []sessions.ProtocolBinding{binding}
	fixture.work.item = sessions.WorkItem{
		ID: binding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 4,
		Status: "blocked", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
		OwnerEpoch: 1,
	}
	batch := mcpc.DurableTaskInputResponseBatch{
		TaskID: intent.TaskID, Generation: binding.Generation,
		OperationID: "operation-input-response", EffectDigest: strings.Repeat("4", 64),
		Responses: []mcpc.DurableTaskInputRef{
			{KeyDigest: strings.Repeat("b", 64), ContentDigest: strings.Repeat("5", 64)},
			{KeyDigest: strings.Repeat("a", 64), ContentDigest: strings.Repeat("6", 64)},
		},
	}
	if err := fixture.store.PrepareInputResponses(context.Background(), fixture.owner, batch); err != nil {
		t.Fatalf("prepare durable input responses: %v", err)
	}
	if len(fixture.bindings.responses) != 1 {
		t.Fatalf("prepared response calls = %#v", fixture.bindings.responses)
	}
	command := fixture.bindings.responses[0]
	if command.BindingID != binding.ID || command.Generation != binding.Generation ||
		command.Route != fixture.config.InterruptRoute || command.OperationID != batch.OperationID ||
		command.EffectDigest != batch.EffectDigest || len(command.Responses) != 2 ||
		command.Responses[0].KeyDigest != strings.Repeat("a", 64) ||
		command.Responses[0].ResponseDigest != strings.Repeat("6", 64) {
		t.Fatalf("prepared response command = %#v", command)
	}

	wrongOwner := fixture.owner
	wrongOwner.Subject = "another-subject"
	if err := fixture.store.PrepareInputResponses(context.Background(), wrongOwner, batch); !errors.Is(err, mcpc.ErrDurableTaskNotFound) {
		t.Fatalf("wrong-owner input response = %v, want not found", err)
	}
	invalid := batch
	invalid.Responses = append([]mcpc.DurableTaskInputRef(nil), batch.Responses[1], batch.Responses[1])
	if err := fixture.store.PrepareInputResponses(context.Background(), fixture.owner, invalid); err == nil {
		t.Fatal("duplicate input response key was accepted")
	}
	if len(fixture.bindings.responses) != 1 {
		t.Fatalf("invalid input response reached communication port: %#v", fixture.bindings.responses)
	}
}

func mcpDurableTimePtr(value time.Time) *time.Time {
	copy := value
	return &copy
}
