// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func applyProtocolSpecForTest(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	input ProtocolBindingSpecInput,
) ProtocolBindingSpec {
	t.Helper()
	draft := ProtocolBindingSpecCommand{
		Operation: ProtocolBindingSpecCreateDraft, WorkspaceID: input.WorkspaceID,
		Input: &input, IdempotencyKey: model.NewID().String(),
	}
	plan, err := m.PlanProtocolBindingSpec(context.Background(), tenant, draft)
	if err != nil {
		t.Fatalf("plan protocol draft: %v", err)
	}
	draft.ExpectedPlanHash = plan.PlanHash
	created, err := m.ApplyProtocolBindingSpec(context.Background(), tenant, draft)
	if err != nil {
		t.Fatalf("apply protocol draft: %v", err)
	}
	activate := ProtocolBindingSpecCommand{
		Operation: ProtocolBindingSpecActivate, WorkspaceID: input.WorkspaceID,
		SpecID: created.Spec.ID, ExpectedVersion: created.Spec.Version,
	}
	activationPlan, err := m.PlanProtocolBindingSpec(context.Background(), tenant, activate)
	if err != nil {
		t.Fatalf("plan protocol activation: %v", err)
	}
	activate.ExpectedPlanHash = activationPlan.PlanHash
	active, err := m.ApplyProtocolBindingSpec(context.Background(), tenant, activate)
	if err != nil {
		t.Fatalf("activate protocol spec: %v", err)
	}
	return active.Spec
}

func protocolSpecInputForTest(
	workspace model.ID,
	protocol BindingProtocol,
	bindingKey string,
	generation int64,
	supersedes model.ID,
) ProtocolBindingSpecInput {
	mapping := []ProtocolMappingRule{{
		Source: "work.title", Target: "message.text", Cardinality: ProtocolMappingOneToOne,
		Transform: ProtocolTransformText,
	}}
	if protocol == BindingProtocolMCP {
		mapping = []ProtocolMappingRule{{
			Source: "task.summary", Target: "work.brief", Cardinality: ProtocolMappingOneToOne,
			Transform: ProtocolTransformText,
		}}
	}
	return ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: bindingKey, Generation: generation,
		Protocol: protocol, ProtocolVersion: "2026-08-01", Direction: BindingOutbound,
		LocalKind: BindingLocalWorkItem, LocalSelector: json.RawMessage(`{"work_kind":"implementation"}`),
		PeerAuthority: "https://peer.example", RemoteResourceKind: "agent",
		RemoteResourceRef: "agent:remote", MappingSchema: ProtocolBindingMappingSchemaV1,
		Mapping:     mapping,
		KnownLosses: []ProtocolBindingLoss{}, RuleRefs: []string{"rule:remote-work"},
		PermissionProfileRef: "permission:remote-work", CurrencyPolicy: BindingCurrencyPinned,
		Validation: ProtocolBindingValidation{
			Verdict: ProtocolObservationClean, Code: "capability_validated",
			ObservedAt: time.Now().UTC(),
		},
		SupersedesID: supersedes,
	}
}

func requireProtocolBindingWorkReceiptForTest(
	t *testing.T,
	f workFixture,
	binding ProtocolBinding,
	eventType string,
) model.Record {
	t.Helper()
	var eventRows, outboxRows []model.Record
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		events, err := sc.Ext(workEventKind)
		if err != nil {
			return err
		}
		eventRows, _, err = events.List(context.Background(), model.Query{
			Filters: []model.Filter{
				eq(colEventID, binding.LastEventID.String()),
				eq(colEventAggregateID, binding.WorkItemID.String()),
			},
			Limit: 2,
		})
		if err != nil {
			return err
		}
		outbox, err := sc.Ext(workOutboxKind)
		if err != nil {
			return err
		}
		outboxRows, _, err = outbox.List(context.Background(), model.Query{
			Filters: []model.Filter{eq(colOutboxEventID, binding.LastEventID.String())}, Limit: 2,
		})
		return err
	}); err != nil {
		t.Fatalf("read %s receipt: %v", eventType, err)
	}
	if len(eventRows) != 1 || len(outboxRows) != 1 {
		t.Fatalf("%s receipt rows = events:%d outbox:%d", eventType, len(eventRows), len(outboxRows))
	}
	event := eventRows[0]
	if event.String(colEventType) != eventType ||
		event.Int(colEventSeq) != binding.LastEventSeq ||
		event.String(colEventCommandID) != binding.LastCommandID.String() ||
		event.Int(colEventAuditSeq) <= 0 || len(event.Bytes(colEventAuditHash)) == 0 ||
		outboxRows[0].String(colOutboxState) != "pending" {
		t.Fatalf("%s receipt = event:%#v outbox:%#v", eventType, event, outboxRows[0])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.String(colEventPayload)), &payload); err != nil {
		t.Fatalf("decode %s payload: %v", eventType, err)
	}
	if payload["binding_id"] != binding.ID.String() ||
		payload["work_item_id"] != binding.WorkItemID.String() ||
		payload["protocol"] != string(binding.Protocol) ||
		payload["verdict"] != string(binding.ObservationVerdict) {
		t.Fatalf("%s payload = %#v", eventType, payload)
	}
	return event
}

func TestProtocolBindingReserveSettleReplayAndRestart(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "protocol-binding.db")
	f := newWorkFixture(t, dbPath, nil)
	work := addWorkLeaseDomainItem(t, f, "protocol binding restart")
	active := applyProtocolSpecForTest(t, f.m, f.tenant,
		protocolSpecInputForTest(f.workspace, BindingProtocolA2A, "remote-agent", 1, model.ID("")))

	reservation := ProtocolBindingReservation{
		WorkspaceID: f.workspace, BindingSpecID: active.ID,
		BindingSpecGeneration: active.Generation, ExpectedDirection: BindingOutbound,
		WorkItemID:  work.ready.ResultID,
		DispatchKey: "dispatch:restart", ExpectedExternalKind: string(ProtocolBindingResultTaskOrMessage),
		Generation: 1, OwnerKind: "agent", OwnerRef: work.agentRef,
		OwnerEpoch: 1, LeaseFence: 0,
	}
	reserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil {
		t.Fatalf("reserve protocol binding: %v", err)
	}
	if reserved.Replayed || reserved.BindingSpecID != active.ID || reserved.BindingSpecGeneration != 1 ||
		reserved.RemoteResourceRef != "agent:remote" || reserved.LeaseFence != 1 ||
		reserved.ObservationVerdict != ProtocolObservationUnknown || reserved.LastEventSeq <= 0 ||
		reserved.LastCommandID.IsZero() || reserved.LastEventID.IsZero() || !validCanonicalSID(reserved.SyntheticSID) {
		t.Fatalf("reserved protocol binding = %#v", reserved)
	}
	lease := getWorkLease(t, work)
	if lease.HolderSID != reserved.SyntheticSID || lease.Fence != reserved.LeaseFence || lease.State != workLeaseActive {
		t.Fatalf("protocol lease = %#v, binding %#v", lease, reserved)
	}
	if snapshot := getWorkSnapshot(t, work); snapshot.Item.Status != "active" ||
		snapshot.Item.LastEventSeq != reserved.LastEventSeq {
		t.Fatalf("reserved WorkItem = %#v", snapshot.Item)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, reserved, "work.binding.reserved")
	replayed, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil || !replayed.Replayed || replayed.ID != reserved.ID ||
		replayed.LastCommandID != reserved.LastCommandID || replayed.LastEventID != reserved.LastEventID {
		t.Fatalf("reservation replay = %#v, %v", replayed, err)
	}
	if got := getWorkLease(t, work); got.Fence != lease.Fence {
		t.Fatalf("reservation replay advanced lease: %#v", got)
	}

	settled, err := f.m.SettleProtocolBinding(context.Background(), f.tenant, ProtocolBindingSettlement{
		BindingID: reserved.ID, Generation: reserved.Generation, ExpectedVersion: reserved.Version,
		DispatchKey: reservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
		ExternalID: "task:remote-1", ContextID: "context:remote-1",
		LocalState: "delegated", RemoteState: "working", RemoteRevision: "rev:1",
		Verdict: ProtocolObservationClean, Code: "task_accepted", Observed: true,
	})
	if err != nil {
		t.Fatalf("settle protocol binding: %v", err)
	}
	if settled.ExternalKind != "task" || settled.ExternalID != "task:remote-1" ||
		settled.LastEventSeq != reserved.LastEventSeq+1 || settled.LastCommandID == reserved.LastCommandID ||
		settled.ObservationVerdict != ProtocolObservationClean {
		t.Fatalf("settled protocol binding = %#v", settled)
	}
	if snapshot := getWorkSnapshot(t, work); snapshot.Item.Status != "active" ||
		snapshot.Item.LastEventSeq != settled.LastEventSeq {
		t.Fatalf("settled WorkItem = %#v", snapshot.Item)
	}
	if renewed := getWorkLease(t, work); renewed.State != workLeaseActive ||
		renewed.Fence != settled.LeaseFence || renewed.RenewalCount != lease.RenewalCount+1 {
		t.Fatalf("settled protocol lease = %#v", renewed)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, settled, "work.binding.observed")
	current, err := f.m.GetProtocolBinding(context.Background(), f.tenant, ProtocolBindingRef{
		WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: active.PeerAuthority, ExternalKind: "task", ExternalID: settled.ExternalID,
	})
	if err != nil || current.ID != settled.ID {
		t.Fatalf("current external binding = %#v, %v", current, err)
	}
	interrupted, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: settled.ID, Generation: settled.Generation, ExpectedVersion: settled.Version,
		SemanticKey: "observe:remote-input-required", PeerAuthority: active.PeerAuthority,
		ExternalID: settled.ExternalID, ContextID: settled.ContextID,
		LocalState: "blocked", RemoteState: "input_required", RemoteRevision: "rev:2",
		Verdict: ProtocolObservationClean, Code: "input_required", Observed: true,
	})
	if err != nil {
		t.Fatalf("observe interrupted protocol binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, work); snapshot.Item.Status != "blocked" ||
		snapshot.Item.BlockedCode != "input_required" || snapshot.Item.LastEventSeq != interrupted.LastEventSeq {
		t.Fatalf("interrupted WorkItem = %#v", snapshot.Item)
	}
	if retained := getWorkLease(t, work); retained.State != workLeaseActive ||
		retained.Fence != settled.LeaseFence {
		t.Fatalf("interrupted protocol lease = %#v", retained)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, interrupted, "work.binding.observed")
	reviewed, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: interrupted.ID, Generation: interrupted.Generation, ExpectedVersion: interrupted.Version,
		SemanticKey: "observe:remote-completed", PeerAuthority: active.PeerAuthority,
		ExternalID: interrupted.ExternalID, ContextID: interrupted.ContextID,
		LocalState: "review", RemoteState: "completed", RemoteRevision: "rev:3",
		Verdict: ProtocolObservationClean, Code: "task_completed", Observed: true, Terminal: true,
	})
	if err != nil {
		t.Fatalf("observe completed protocol binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, work); snapshot.Item.Status != "review" ||
		snapshot.Item.LastEventSeq != reviewed.LastEventSeq {
		t.Fatalf("reviewed WorkItem = %#v", snapshot.Item)
	}
	if released := getWorkLease(t, work); released.State != workLeaseReleased ||
		released.Fence != settled.LeaseFence+1 {
		t.Fatalf("reviewed protocol lease = %#v", released)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, reviewed, "work.binding.observed")

	if err := f.st.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	restarted := New(WithWorkIdentityResolver(allowWorkIdentity{}), WithWorkContentGuard(allowWorkContent{}))
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dbPath, Debug: true}, restarted.RegisterSchema)
	if err != nil {
		t.Fatalf("restart store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	restarted.UseData(api.NewModuleData(st))
	afterRestart, err := restarted.GetProtocolBinding(context.Background(), f.tenant, ProtocolBindingRef{ID: reviewed.ID})
	if err != nil || afterRestart.ID != reviewed.ID || afterRestart.ExternalID != reviewed.ExternalID ||
		afterRestart.LastCommandID != reviewed.LastCommandID || afterRestart.LastEventID != reviewed.LastEventID {
		t.Fatalf("binding after restart = %#v, %v", afterRestart, err)
	}
	replayed, err = restarted.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil || !replayed.Replayed || replayed.ID != reviewed.ID {
		t.Fatalf("restart reservation replay = %#v, %v", replayed, err)
	}
	page, err := restarted.ListProtocolBindings(context.Background(), f.tenant, ProtocolBindingQuery{
		WorkspaceID: f.workspace, WorkItemID: work.ready.ResultID, Limit: 10,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != reviewed.ID {
		t.Fatalf("restart binding list = %#v, %v", page, err)
	}
}

func TestProtocolBindingPinsGenerationAndDetectsDrift(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "protocol-generation.db"), nil)
	defer f.st.Close()
	work := addWorkLeaseDomainItem(t, f, "protocol generation pin")
	first := applyProtocolSpecForTest(t, f.m, f.tenant,
		protocolSpecInputForTest(f.workspace, BindingProtocolA2A, "generation-pin", 1, model.ID("")))
	second := applyProtocolSpecForTest(t, f.m, f.tenant,
		protocolSpecInputForTest(f.workspace, BindingProtocolA2A, "generation-pin", 2, first.ID))
	reservation := ProtocolBindingReservation{
		WorkspaceID: f.workspace, BindingSpecID: second.ID, BindingSpecGeneration: 2,
		ExpectedDirection: BindingOutbound,
		WorkItemID:        work.ready.ResultID, DispatchKey: "dispatch:generation",
		ExpectedExternalKind: "task", Generation: 1, OwnerKind: "agent",
		OwnerRef: work.agentRef, OwnerEpoch: 1, LeaseFence: 0,
	}
	reserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil || reserved.BindingSpecGeneration != 2 ||
		string(reserved.PinnedSpecHash) != string(second.SpecHash) {
		t.Fatalf("generation-pinned reserve = %#v, %v", reserved, err)
	}
	wrongDirection := reservation
	wrongDirection.DispatchKey = "dispatch:wrong-direction"
	wrongDirection.ExpectedDirection = BindingInbound
	if _, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, wrongDirection); !errors.Is(err, ErrProtocolBindingConflict) {
		t.Fatalf("reserve through wrong spec direction = %v, want conflict", err)
	}
	reservation.DispatchKey = "dispatch:inactive-generation"
	reservation.BindingSpecID, reservation.BindingSpecGeneration = first.ID, 1
	if _, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation); !errors.Is(err, ErrProtocolBindingConflict) {
		t.Fatalf("reserve superseded generation = %v, want conflict", err)
	}
	ambiguous, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: reserved.ID, Generation: reserved.Generation, ExpectedVersion: reserved.Version,
		SemanticKey: "observe:generation-unknown", PeerAuthority: second.PeerAuthority,
		LocalState: "active", RemoteState: "unknown", Verdict: ProtocolObservationUnknown,
		Code: "post_transmit_unknown", Observed: true,
	})
	if err != nil || ambiguous.LastEventSeq != reserved.LastEventSeq+1 {
		t.Fatalf("ambiguous observation = %#v, %v", ambiguous, err)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, ambiguous, "work.binding.ambiguous")
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		record, err := repo.Get(context.Background(), reserved.ID)
		if err != nil {
			return err
		}
		record[colBindingPinnedSpecHash] = []byte("drift")
		_, err = repo.Update(context.Background(), record)
		return err
	}); err == nil {
		t.Fatal("direct protocol binding drift unexpectedly bypassed the storage guard")
	}
	intact, err := f.m.GetProtocolBinding(context.Background(), f.tenant, ProtocolBindingRef{ID: reserved.ID})
	if err != nil || intact.ID != reserved.ID ||
		string(intact.PinnedSpecHash) != string(reserved.PinnedSpecHash) {
		t.Fatalf("binding after rejected direct drift = %#v, %v", intact, err)
	}
}

func TestProtocolBindingBidirectionalSpecServesEitherDirection(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "protocol-bidirectional.db"), nil)
	defer f.st.Close()
	input := protocolSpecInputForTest(
		f.workspace, BindingProtocolA2A, "bidirectional", 1, model.ID(""),
	)
	input.Direction = BindingBidirectional
	input.Mapping = append(input.Mapping, ProtocolMappingRule{
		Source: "message.text", Target: "work.brief", Cardinality: ProtocolMappingOneToOne,
		Transform: ProtocolTransformText,
	})
	active := applyProtocolSpecForTest(t, f.m, f.tenant, input)

	for _, direction := range []BindingDirection{BindingInbound, BindingOutbound} {
		work := addWorkLeaseDomainItem(t, f, "bidirectional "+string(direction))
		reserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant,
			ProtocolBindingReservation{
				WorkspaceID: f.workspace, BindingSpecID: active.ID,
				BindingSpecGeneration: active.Generation, ExpectedDirection: direction,
				WorkItemID:           work.ready.ResultID,
				DispatchKey:          "dispatch:bidirectional:" + string(direction),
				ExpectedExternalKind: string(ProtocolBindingResultTask), Generation: 1,
				OwnerKind: "agent", OwnerRef: work.agentRef, OwnerEpoch: 1,
			})
		if err != nil || reserved.Direction != BindingBidirectional {
			t.Fatalf("reserve bidirectional spec as %s = %#v, %v", direction, reserved, err)
		}
	}
}

func TestProtocolBindingMCPMetadataCurrentCollisionAndCancelReplay(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "protocol-mcp.db"), nil)
	defer f.st.Close()
	firstWork := addWorkLeaseDomainItem(t, f, "MCP durable task")
	secondWork := addWorkLeaseDomainItem(t, f, "MCP collision contender")
	active := applyProtocolSpecForTest(t, f.m, f.tenant,
		protocolSpecInputForTest(f.workspace, BindingProtocolMCP, "mcp-task", 1, model.ID("")))
	ttl, poll := int64(60_000), int64(1_000)
	projection := &ProtocolMCPTaskProjection{
		Owner: ProtocolMCPTaskOwner{
			Subject: "user:mcp", Issuer: "https://issuer.example", ClientID: "client:mcp",
		},
		Tool: "tools.execute", RequiredScope: "", CreatedAt: time.Now().UTC(),
		TTLMs: &ttl, PollIntervalMs: &poll, InitialStatus: "working",
		InitialStatusReason: "accepted", UpstreamDescriptor: "upstream:mcp",
		ProtocolRevision: "2026-08-01", OriginOperationID: "operation:mcp-1",
		OriginEffectDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	reservation := ProtocolBindingReservation{
		WorkspaceID: f.workspace, BindingSpecID: active.ID, BindingSpecGeneration: 1,
		ExpectedDirection: BindingOutbound,
		WorkItemID:        firstWork.ready.ResultID, DispatchKey: "dispatch:mcp-task-1",
		ExpectedExternalKind: "task", ExpectedExternalID: "task:mcp-1", Generation: 1,
		OwnerKind: "agent", OwnerRef: firstWork.agentRef, OwnerEpoch: 1, LeaseFence: 0,
		MCPTask: projection,
	}
	reserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, reservation)
	if err != nil {
		t.Fatalf("reserve MCP binding: %v", err)
	}
	if reserved.MCPTask == nil || len(reserved.ProtocolMetadataJSON) == 0 ||
		reserved.MCPTask.PollIntervalMs == nil || *reserved.MCPTask.PollIntervalMs != poll ||
		reserved.CurrentTTLMs == nil || *reserved.CurrentTTLMs != ttl ||
		reserved.CurrentPollIntervalMs == nil || *reserved.CurrentPollIntervalMs != poll {
		t.Fatalf("reserved MCP metadata = %#v", reserved)
	}
	current, err := f.m.GetProtocolBinding(context.Background(), f.tenant, ProtocolBindingRef{
		WorkspaceID: f.workspace, Protocol: BindingProtocolMCP,
		PeerAuthority: active.PeerAuthority, ExternalKind: "task", ExternalID: reservation.ExpectedExternalID,
	})
	if err != nil || current.ID != reserved.ID {
		t.Fatalf("current MCP binding = %#v, %v", current, err)
	}

	collision := reservation
	collision.WorkItemID = secondWork.ready.ResultID
	collision.OwnerRef = secondWork.agentRef
	collision.DispatchKey = "dispatch:mcp-task-collision"
	if _, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, collision); !errors.Is(err, ErrProtocolBindingConflict) {
		t.Fatalf("current MCP task collision = %v, want conflict", err)
	}
	if lease := getWorkLease(t, secondWork); lease.Fence != 0 || lease.State != workLeaseVacant {
		t.Fatalf("losing MCP collision changed contender lease: %#v", lease)
	}

	settled, err := f.m.SettleProtocolBinding(context.Background(), f.tenant, ProtocolBindingSettlement{
		BindingID: reserved.ID, Generation: 1, ExpectedVersion: reserved.Version,
		DispatchKey: reservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
		ExternalID: reservation.ExpectedExternalID, LocalState: "registered", RemoteState: "working",
		Verdict: ProtocolObservationClean, Code: "task_registered", Observed: true,
		TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("settle MCP binding: %v", err)
	}
	newTTL, newPoll := int64(30_000), int64(2_000)
	observed, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: settled.ID, Generation: 1, ExpectedVersion: settled.Version,
		SemanticKey: "observe:mcp-task-1", PeerAuthority: active.PeerAuthority,
		ExternalID: reservation.ExpectedExternalID, LocalState: "registered", RemoteState: "working",
		RemoteRevision: "revision:2", Verdict: ProtocolObservationClean,
		Code: "task_observed", Observed: true, TTLMs: &newTTL, PollIntervalMs: &newPoll,
	})
	if err != nil || observed.CurrentTTLMs == nil || *observed.CurrentTTLMs != newTTL ||
		observed.CurrentPollIntervalMs == nil || *observed.CurrentPollIntervalMs != newPoll {
		t.Fatalf("observe MCP binding = %#v, %v", observed, err)
	}
	cancel := ProtocolBindingCancelIntent{
		BindingID: observed.ID, Generation: 1, ExpectedVersion: observed.Version,
		SemanticKey: "cancel:mcp-task-1", ReasonCode: "caller_requested",
	}
	canceled, err := f.m.RequestProtocolBindingCancel(context.Background(), f.tenant, cancel)
	if err != nil || canceled.Replayed || !canceled.CancelRequested ||
		canceled.ObservationVerdict != ProtocolObservationUnknown {
		t.Fatalf("MCP cancellation intent = %#v, %v", canceled, err)
	}
	if snapshot := getWorkSnapshot(t, firstWork); snapshot.Item.Status != "blocked" ||
		snapshot.Item.BlockedCode != "cancel_pending" ||
		snapshot.Item.LastEventSeq != canceled.LastEventSeq {
		t.Fatalf("cancel-requested MCP WorkItem = %#v", snapshot.Item)
	}
	if retained := getWorkLease(t, firstWork); retained.State != workLeaseActive ||
		retained.Fence != canceled.LeaseFence {
		t.Fatalf("cancel-requested MCP lease = %#v", retained)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, canceled, "work.binding.cancel_requested")
	replayed, err := f.m.RequestProtocolBindingCancel(context.Background(), f.tenant, cancel)
	if err != nil || !replayed.Replayed || replayed.ID != canceled.ID ||
		replayed.LastCommandID != canceled.LastCommandID || replayed.LastEventID != canceled.LastEventID {
		t.Fatalf("MCP cancellation replay = %#v, %v", replayed, err)
	}
	pending, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: canceled.ID, Generation: 1, ExpectedVersion: canceled.Version,
		SemanticKey: "observe:mcp-task-cancel-pending", PeerAuthority: active.PeerAuthority,
		ExternalID: reservation.ExpectedExternalID, LocalState: "blocked", RemoteState: "working",
		RemoteRevision: "revision:3", Verdict: ProtocolObservationClean,
		Code: "mcp_get", Observed: true, TTLMs: &newTTL, PollIntervalMs: &newPoll,
	})
	if err != nil {
		t.Fatalf("observe pending MCP cancellation: %v", err)
	}
	if snapshot := getWorkSnapshot(t, firstWork); snapshot.Item.Status != "blocked" ||
		snapshot.Item.BlockedCode != "cancel_pending" {
		t.Fatalf("pending-cancel MCP WorkItem = %#v", snapshot.Item)
	}
	if retained := getWorkLease(t, firstWork); retained.State != workLeaseActive ||
		retained.Fence != pending.LeaseFence {
		t.Fatalf("pending-cancel MCP lease = %#v", retained)
	}
	confirmed, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: pending.ID, Generation: 1, ExpectedVersion: pending.Version,
		SemanticKey: "observe:mcp-task-canceled", PeerAuthority: active.PeerAuthority,
		ExternalID: reservation.ExpectedExternalID, LocalState: "canceled", RemoteState: "canceled",
		RemoteRevision: "revision:4", Verdict: ProtocolObservationClean,
		Code: "cancel_confirmed", Observed: true, Terminal: true,
	})
	if err != nil {
		t.Fatalf("observe canceled MCP binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, firstWork); snapshot.Item.Status != "canceled" ||
		snapshot.Item.TerminalCode != "protocol_canceled" || snapshot.Item.LastEventSeq != confirmed.LastEventSeq {
		t.Fatalf("canceled MCP WorkItem = %#v", snapshot.Item)
	}
	if revoked := getWorkLease(t, firstWork); revoked.State != workLeaseRevoked ||
		revoked.Fence != confirmed.LeaseFence+1 {
		t.Fatalf("confirmed-cancel MCP lease = %#v", revoked)
	}
	requireProtocolBindingWorkReceiptForTest(t, f, confirmed, "work.binding.observed")
	terminalCancel := cancel
	terminalCancel.ExpectedVersion = confirmed.Version
	terminalCancel.SemanticKey = "cancel:mcp-task-1-after-terminal"
	if _, err := f.m.RequestProtocolBindingCancel(context.Background(), f.tenant, terminalCancel); !errors.Is(err, ErrProtocolBindingConflict) {
		t.Fatalf("terminal MCP cancel = %v, want conflict", err)
	}

	secondProjection := *projection
	secondProjection.OriginOperationID = "operation:mcp-2"
	secondReservation := reservation
	secondReservation.WorkItemID = secondWork.ready.ResultID
	secondReservation.OwnerRef = secondWork.agentRef
	secondReservation.DispatchKey = "dispatch:mcp-task-2"
	secondReservation.ExpectedExternalID = "task:mcp-2"
	secondReservation.MCPTask = &secondProjection
	secondReserved, err := f.m.ReserveProtocolBinding(context.Background(), f.tenant, secondReservation)
	if err != nil {
		t.Fatalf("reserve second MCP binding: %v", err)
	}
	secondSettled, err := f.m.SettleProtocolBinding(context.Background(), f.tenant, ProtocolBindingSettlement{
		BindingID: secondReserved.ID, Generation: 1, ExpectedVersion: secondReserved.Version,
		DispatchKey: secondReservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
		ExternalID: secondReservation.ExpectedExternalID, LocalState: "active", RemoteState: "working",
		Verdict: ProtocolObservationClean, Code: "mcp_register", Observed: true,
		TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("settle second MCP binding: %v", err)
	}
	authRequired, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: secondSettled.ID, Generation: 1, ExpectedVersion: secondSettled.Version,
		SemanticKey: "observe:mcp-task-2-auth", PeerAuthority: active.PeerAuthority,
		ExternalID: secondReservation.ExpectedExternalID, LocalState: "blocked", RemoteState: "auth_required",
		RemoteRevision: "revision:auth", Verdict: ProtocolObservationClean,
		Code: "auth_required", Observed: true, TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("observe auth-required MCP binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, secondWork); snapshot.Item.Status != "blocked" ||
		snapshot.Item.BlockedCode != "auth_required" {
		t.Fatalf("auth-required MCP WorkItem = %#v", snapshot.Item)
	}
	if retained := getWorkLease(t, secondWork); retained.State != workLeaseActive ||
		retained.Fence != authRequired.LeaseFence {
		t.Fatalf("auth-required MCP lease = %#v", retained)
	}
	resumed, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: authRequired.ID, Generation: 1, ExpectedVersion: authRequired.Version,
		SemanticKey: "observe:mcp-task-2-working", PeerAuthority: active.PeerAuthority,
		ExternalID: secondReservation.ExpectedExternalID, LocalState: "active", RemoteState: "working",
		RemoteRevision: "revision:working", Verdict: ProtocolObservationClean,
		Code: "mcp_get", Observed: true, TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("resume auth-required MCP binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, secondWork); snapshot.Item.Status != "active" ||
		snapshot.Item.BlockedCode != "" {
		t.Fatalf("resumed MCP WorkItem = %#v", snapshot.Item)
	}
	authRequired, err = f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: resumed.ID, Generation: 1, ExpectedVersion: resumed.Version,
		SemanticKey: "observe:mcp-task-2-auth-again", PeerAuthority: active.PeerAuthority,
		ExternalID: secondReservation.ExpectedExternalID, LocalState: "blocked", RemoteState: "authorization_required",
		RemoteRevision: "revision:auth-2", Verdict: ProtocolObservationClean,
		Code: "mcp_get", Observed: true, TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("observe second auth-required MCP binding: %v", err)
	}
	failed, err := f.m.ObserveProtocolBinding(context.Background(), f.tenant, ProtocolBindingObservation{
		BindingID: authRequired.ID, Generation: 1, ExpectedVersion: authRequired.Version,
		SemanticKey: "observe:mcp-task-2-failed", PeerAuthority: active.PeerAuthority,
		ExternalID: secondReservation.ExpectedExternalID, LocalState: "blocked", RemoteState: "failed",
		RemoteRevision: "revision:failed", Verdict: ProtocolObservationBroken,
		Code: "remote_failed", Observed: true, Terminal: true, TTLMs: &ttl, PollIntervalMs: &poll,
	})
	if err != nil {
		t.Fatalf("observe failed MCP binding: %v", err)
	}
	if snapshot := getWorkSnapshot(t, secondWork); snapshot.Item.Status != "blocked" ||
		snapshot.Item.BlockedCode != "protocol_broken" || snapshot.Item.LastEventSeq != failed.LastEventSeq {
		t.Fatalf("failed MCP WorkItem = %#v", snapshot.Item)
	}
	if revoked := getWorkLease(t, secondWork); revoked.State != workLeaseRevoked ||
		revoked.Fence != failed.LeaseFence+1 {
		t.Fatalf("failed MCP lease = %#v", revoked)
	}
}
