// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func protocolReplayReservationForTest(
	t *testing.T,
	fixture workFixture,
	semantic string,
) ProtocolBindingReservation {
	t.Helper()
	work := addWorkLeaseDomainItem(t, fixture, semantic)
	spec := applyProtocolSpecForTest(t, fixture.m, fixture.tenant,
		protocolSpecInputForTest(fixture.workspace, BindingProtocolA2A, semantic, 1, model.ID("")))
	return ProtocolBindingReservation{
		WorkspaceID: fixture.workspace, BindingSpecID: spec.ID, BindingSpecGeneration: spec.Generation,
		ExpectedDirection: BindingOutbound, WorkItemID: work.ready.ResultID,
		DispatchKey: "replay:" + semantic, ExpectedExternalKind: "task", Generation: 1,
		OwnerKind: "agent", OwnerRef: work.agentRef, OwnerEpoch: work.ready.OwnerEpoch,
	}
}

func TestProtocolReplayGuardSchemaIsAppendOnlyHashAuthority(t *testing.T) {
	t.Parallel()

	registry := communicationCaptureSchema(t)
	var descriptor model.EntityDescriptor
	found := false
	for _, candidate := range registry.descriptors {
		if candidate.Kind == protocolReplayGuardKind {
			descriptor, found = candidate, true
			break
		}
	}
	if !found || descriptor.Table != protocolReplayGuardTable || !descriptor.AppendOnly ||
		descriptor.WorkspaceLineage != hiddenWorkspaceLineage {
		t.Fatalf("protocol replay descriptor = %#v, found=%v", descriptor, found)
	}
	fields := communicationDescriptorFields(descriptor)
	for _, name := range []string{
		colWorkWorkspaceID, colReplayProtocol, colReplayPeerAuthority, colReplayKind,
		colReplayHash, colReplayFirstSeenAt, colReplayExpiresAt, colReplayBindingID,
	} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("protocol replay descriptor is missing %q", name)
		}
	}
	for _, rawColumn := range []string{"replay_id", "jti", "message_id", "request_id", "token"} {
		if _, leaked := fields[rawColumn]; leaked {
			t.Fatalf("protocol replay descriptor persists raw column %q", rawColumn)
		}
	}
	wantUnique := []string{
		model.ColTenantID, colReplayProtocol, colReplayPeerAuthority, colReplayKind, colReplayHash,
	}
	uniqueFound, expiryFound := false, false
	for _, index := range descriptor.Indexes {
		if index.Name == "sessions_communication_replay_guard_claim_uniq" {
			uniqueFound = index.Unique && slices.Equal(index.Columns, wantUnique)
		}
		if index.Name == "sessions_communication_replay_guard_expiry" {
			expiryFound = slices.Contains(index.Columns, colReplayExpiresAt)
		}
	}
	if !uniqueFound || !expiryFound {
		t.Fatalf("protocol replay indexes = %#v", descriptor.Indexes)
	}
}

func settleProtocolReplayBindingForTest(
	ctx context.Context,
	m *Module,
	tenant model.TenantID,
	reservation ProtocolBindingReservation,
) (ProtocolReplaySettlement, error) {
	binding, err := m.ReserveProtocolBinding(ctx, tenant, reservation)
	if err != nil {
		return ProtocolReplaySettlement{}, err
	}
	if binding.ExternalID == "" {
		binding, err = m.SettleProtocolBinding(ctx, tenant, ProtocolBindingSettlement{
			BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
			DispatchKey: reservation.DispatchKey, ResultKind: ProtocolBindingResultTask,
			ExternalID: "remote:" + binding.ID.String(), LocalState: "active", RemoteState: "submitted",
			Verdict: ProtocolObservationClean, Code: "accepted", Observed: true,
		})
		if err != nil {
			return ProtocolReplaySettlement{}, err
		}
	}
	return ProtocolReplaySettlement{BindingID: binding.ID}, nil
}

func TestProtocolReplayGuardSurvivesRestartAndRollsBackAtomically(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "protocol-replay.db")
	fixture := newWorkFixture(t, dbPath, nil)
	reservation := protocolReplayReservationForTest(t, fixture, "durable-replay")
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://peer.example", Kind: ProtocolReplayMessageID,
		ReplayID: "remote-message-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	mutations := 0
	first, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			mutations++
			return settleProtocolReplayBindingForTest(ctx, fixture.m, fixture.tenant, reservation)
		},
	)
	if err != nil || first.Replayed || first.Guard.BindingID.IsZero() || mutations != 1 {
		t.Fatalf("first replay claim = %#v, mutations=%d, err=%v", first, mutations, err)
	}
	assertProtocolReplayRowsForTest(t, fixture.st, fixture.tenant, 1, 1)

	if err := fixture.st.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	restarted := New()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true,
	}, restarted.RegisterSchema)
	if err != nil {
		t.Fatalf("restart replay store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	restarted.UseData(api.NewModuleData(st))
	replayed, err := restarted.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			mutations++
			return ProtocolReplaySettlement{}, errors.New("replay mutation must not run")
		},
	)
	if err != nil || !replayed.Replayed || replayed.Guard.ID != first.Guard.ID ||
		replayed.Guard.BindingID != first.Guard.BindingID || mutations != 1 {
		t.Fatalf("restart replay = %#v, mutations=%d, err=%v", replayed, mutations, err)
	}
	if _, err := restarted.GetProtocolBinding(context.Background(), fixture.tenant, ProtocolBindingRef{
		ID: replayed.Guard.BindingID,
	}); err != nil {
		t.Fatalf("replayed binding is unavailable after restart: %v", err)
	}
}

func TestProtocolReplayGuardRollbackRemovesClaimAndJoinedBinding(t *testing.T) {
	t.Parallel()

	fixture := newWorkFixture(t, filepath.Join(t.TempDir(), "protocol-replay-rollback.db"), nil)
	defer fixture.st.Close()
	reservation := protocolReplayReservationForTest(t, fixture, "rollback-replay")
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://peer.example", Kind: ProtocolReplayJTI,
		ReplayID: "signed-token-jti-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	injected := errors.New("injected post-binding failure")
	_, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			if _, err := fixture.m.ReserveProtocolBinding(ctx, fixture.tenant, reservation); err != nil {
				return ProtocolReplaySettlement{}, err
			}
			return ProtocolReplaySettlement{}, injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("rollback error = %v, want injected", err)
	}
	assertProtocolReplayRowsForTest(t, fixture.st, fixture.tenant, 0, 0)

	result, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			return settleProtocolReplayBindingForTest(ctx, fixture.m, fixture.tenant, reservation)
		},
	)
	if err != nil || result.Replayed || result.Guard.BindingID.IsZero() {
		t.Fatalf("retry after rollback = %#v, err=%v", result, err)
	}
	assertProtocolReplayRowsForTest(t, fixture.st, fixture.tenant, 1, 1)
}

func TestProtocolReplayGuardMessageSettlementRollsBackWithGuard(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	command := ProtocolInterruptCommand{
		BindingID: binding.ID, Generation: binding.Generation,
		Route: ProtocolInterruptRoute{
			ChannelID: fixture.channel.ID, SenderUserID: fixture.sender,
			RecipientUserID: model.ID(fixture.target.Ref),
		},
		RemoteState: "input_required",
		Requests: []ProtocolInterruptRequestRef{{
			KeyDigest: digest("request-key"), ContentDigest: digest("request-content"),
		}},
	}
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayJTI,
		ReplayID: "interrupt-jti-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
		ExpectedBindingID: binding.ID,
	}
	injected := errors.New("injected after message settlement")
	_, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			if _, err := fixture.m.RecordProtocolInterrupt(ctx, fixture.tenant, command); err != nil {
				return ProtocolReplaySettlement{}, err
			}
			return ProtocolReplaySettlement{BindingID: binding.ID}, injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("message rollback error = %v, want injected", err)
	}
	for _, kind := range []model.Kind{messageKind, protocolInterruptKind, protocolReplayGuardKind} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 0 {
			t.Fatalf("%s rows after rollback = %d, want 0", kind, len(rows))
		}
	}
	result, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			if _, err := fixture.m.RecordProtocolInterrupt(ctx, fixture.tenant, command); err != nil {
				return ProtocolReplaySettlement{}, err
			}
			return ProtocolReplaySettlement{BindingID: binding.ID}, nil
		},
	)
	if err != nil || result.Replayed || result.Guard.BindingID != binding.ID {
		t.Fatalf("message retry = %#v, err=%v", result, err)
	}
	for _, kind := range []model.Kind{messageKind, protocolInterruptKind, protocolReplayGuardKind} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != 1 {
			t.Fatalf("%s rows after retry = %d, want 1", kind, len(rows))
		}
	}
}

func TestProtocolReplayGuardExactReplayAndIdentityConflict(t *testing.T) {
	t.Parallel()

	fixture := newWorkFixture(t, filepath.Join(t.TempDir(), "protocol-replay-conflict.db"), nil)
	defer fixture.st.Close()
	reservation := protocolReplayReservationForTest(t, fixture, "identity-replay")
	claim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: "https://PEER.example/", Kind: ProtocolReplayRequestID,
		ReplayID: "request-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	first, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, claim,
		func(ctx context.Context) (ProtocolReplaySettlement, error) {
			return settleProtocolReplayBindingForTest(ctx, fixture.m, fixture.tenant, reservation)
		},
	)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	exact := claim
	exact.PeerAuthority = "https://peer.example"
	exact.ExpectedBindingID = first.Guard.BindingID
	replayed, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, exact,
		func(context.Context) (ProtocolReplaySettlement, error) {
			t.Fatal("exact replay ran mutation")
			return ProtocolReplaySettlement{}, nil
		},
	)
	if err != nil || !replayed.Replayed || replayed.Guard.ID != first.Guard.ID {
		t.Fatalf("exact replay = %#v, err=%v", replayed, err)
	}
	conflict := exact
	conflict.WorkspaceID = model.NewID()
	if _, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, conflict,
		func(context.Context) (ProtocolReplaySettlement, error) { return ProtocolReplaySettlement{}, nil },
	); !errors.Is(err, ErrProtocolReplayConflict) {
		t.Fatalf("cross-workspace replay = %v, want conflict", err)
	}
	wrongBinding := exact
	wrongBinding.ExpectedBindingID = model.NewID()
	if _, err := fixture.m.ApplyProtocolReplay(
		context.Background(), fixture.tenant, wrongBinding,
		func(context.Context) (ProtocolReplaySettlement, error) { return ProtocolReplaySettlement{}, nil },
	); !errors.Is(err, ErrProtocolReplayConflict) {
		t.Fatalf("changed binding replay = %v, want conflict", err)
	}
	assertProtocolReplayRowsForTest(t, fixture.st, fixture.tenant, 1, 1)
}

func assertProtocolReplayRowsForTest(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	wantGuards, wantBindings int,
) {
	t.Helper()
	var guards, bindings []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		guardRepo, err := sc.Ext(protocolReplayGuardKind)
		if err != nil {
			return err
		}
		guards, _, err = guardRepo.List(context.Background(), model.Query{Limit: 10})
		if err != nil {
			return err
		}
		bindingRepo, err := sc.Ext(protocolBindingKind)
		if err != nil {
			return err
		}
		bindings, _, err = bindingRepo.List(context.Background(), model.Query{Limit: 10})
		return err
	}); err != nil {
		t.Fatalf("read replay rows: %v", err)
	}
	if len(guards) != wantGuards || len(bindings) != wantBindings {
		t.Fatalf("replay rows = guards:%d bindings:%d, want %d/%d",
			len(guards), len(bindings), wantGuards, wantBindings)
	}
}
