// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestHandoffAcceptFenceRuleIsConditionalOnTheOfferedGeneration pins the model
// rule in both directions. It is deliberately exhaustive about the pairs the
// proposal names, including the two that the naive relaxation would have let
// through: (offered 0, resulting >= 1) mints a fence out of a generation that
// never existed, and it stays forbidden.
func TestHandoffAcceptFenceRuleIsConditionalOnTheOfferedGeneration(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		offered, resulting int64
		want               bool
	}{
		{name: "vacant generation ends nothing", offered: 0, resulting: 0, want: true},
		{name: "vacant generation cannot mint a fence", offered: 0, resulting: 1},
		{name: "vacant generation cannot mint a high fence", offered: 0, resulting: 8},
		{name: "active generation must advance", offered: 5, resulting: 6, want: true},
		{name: "active generation cannot end without a fence", offered: 5, resulting: 0},
		{name: "active generation cannot stand still", offered: 5, resulting: 5},
		{name: "active generation cannot move backwards", offered: 5, resulting: 4},
		{name: "a negative offered fence is never accepted", offered: -1, resulting: 0},
		{name: "a negative resulting fence is never accepted", offered: 0, resulting: -1},
		{name: "a negative resulting fence never ends a generation", offered: 5, resulting: -1},
	} {
		if got := handoffAcceptFenceOK(tc.offered, tc.resulting); got != tc.want {
			t.Errorf("handoffAcceptFenceOK(%d, %d) = %v, want %v",
				tc.offered, tc.resulting, got, tc.want)
		}
	}
}

// TestHandoffAcceptedModelRuleRefusesInventedAndMissingFences drives the same
// pairs through the two exported model seams, which is where a caller meets the
// rule: PlanHandoffTransition on the way in and ValidateHandoff on the way back
// out of the durable row.
func TestHandoffAcceptedModelRuleRefusesInventedAndMissingFences(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		offered, resulting int64
		accepted           bool
	}{
		{name: "vacant/absent", offered: 0, resulting: 0, accepted: true},
		{name: "vacant/invented", offered: 0, resulting: 1},
		{name: "active/advanced", offered: 5, resulting: 6, accepted: true},
		{name: "active/absent", offered: 5, resulting: 0},
		{name: "active/unchanged", offered: 5, resulting: 5},
	} {
		before := vacantTransferTestOfferedHandoff(t, tc.offered)
		ackID := model.NewID()
		plan, err := PlanHandoffTransition(
			before, HandoffAccept, ackID, tc.resulting, "", nil,
			before.CreatedAt.Add(time.Minute),
		)
		if tc.accepted {
			if err != nil {
				t.Errorf("%s: PlanHandoffTransition = %v, want an accepted plan", tc.name, err)
				continue
			}
			if plan.After.ResultingLeaseFence != tc.resulting || !plan.CreatesAck {
				t.Errorf("%s: accepted plan = %+v", tc.name, plan)
			}
			// ChangesLease is a statement about the transaction, so a transfer that
			// writes no lease row must not claim one.
			if plan.ChangesLease != (tc.resulting > 0) {
				t.Errorf("%s: plan.ChangesLease = %v with resulting fence %d",
					tc.name, plan.ChangesLease, tc.resulting)
			}
			if err := ValidateHandoff(plan.After); err != nil {
				t.Errorf("%s: ValidateHandoff refused its own accepted plan: %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Errorf("%s: PlanHandoffTransition = %v, want ErrInvalidCommunicationModel", tc.name, err)
		}
		// And the same shape is refused on the way back out, so a row that reached
		// the table by some other route cannot be replayed as if it were valid.
		forged := before
		forged.Version++
		forged.UpdatedAt = before.CreatedAt.Add(time.Minute)
		forged.State = HandoffAccepted
		forged.AckID = ackID
		acceptedAt := forged.UpdatedAt
		forged.AcceptedAt = &acceptedAt
		forged.ResultingLeaseFence = tc.resulting
		if err := ValidateHandoff(forged); !errors.Is(err, ErrInvalidCommunicationModel) {
			t.Errorf("%s: ValidateHandoff accepted a forged row: %v", tc.name, err)
		}
	}
}

// vacantTransferTestOfferedHandoff builds a valid OFFERED Handoff whose sealed
// context hash is computed from its own fields, so the pairs above are refused
// by the fence rule rather than by a hash mismatch.
func vacantTransferTestOfferedHandoff(t *testing.T, offeredFence int64) Handoff {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	handoff := Handoff{
		MutableCommunicationEntity: MutableCommunicationEntity{
			CommunicationEntity: CommunicationEntity{
				ID: model.NewID(), TenantID: model.TenantID(model.NewID()),
				WorkspaceID: model.NewID(), Version: 1, CreatedAt: now,
			},
			UpdatedAt: now,
		},
		WorkItemID: model.NewID(), MessageID: model.NewID(), DeliveryID: model.NewID(),
		From:              RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		To:                RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		FromOwnerEpoch:    1,
		OfferedLeaseFence: offeredFence,
		ContextEventSeq:   1,
		State:             HandoffOffered,
		AckDeadline:       now.Add(10 * time.Minute),
		Payload:           communicationTestPayloadForSlot(t, PayloadSlotHandoff),
	}
	hash, err := CanonicalHandoffContextHash(handoff)
	if err != nil {
		t.Fatalf("seal the fixture Handoff context: %v", err)
	}
	handoff.ContextHash = hash
	if err := ValidateHandoff(handoff); err != nil {
		t.Fatalf("the fixture offered Handoff is not valid: %v", err)
	}
	return handoff
}

// TestHandoffVacantWorkLeaseWitnessMirrorsTheTrigger mutates the witness one
// column at a time. The list is a verbatim mirror of the trigger's own "vacant
// sessions work lease carries authority" clause, and this is the control that
// catches an edit to one that is not mirrored in the other.
func TestHandoffVacantWorkLeaseWitnessMirrorsTheTrigger(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{
		TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID(),
	}
	itemID := model.NewID()
	before := Handoff{WorkItemID: itemID, OfferedLeaseFence: 0}
	base := func() model.Record {
		return model.Record{
			colWorkWorkspaceID: scope.WorkspaceID.String(), colWorkItemID: itemID.String(),
			colLeaseHolderSID: nil, colLeaseHolderRunRef: nil, colLeaseHolderAgentRef: nil,
			colLeaseFence: int64(0), colLeaseState: workLeaseVacant,
			colLeaseAcquiredAt: nil, colLeaseRenewedAt: nil, colLeaseExpiresAt: nil,
			colLeaseEndedAt: nil, colLeaseEndReason: nil, colLeaseRenewalCount: int64(0),
		}
	}
	locked := func(lease model.Record) handoffLockedWork {
		state, err := workLeaseFenceState(lease)
		if err != nil {
			t.Fatalf("decode the fixture lease: %v", err)
		}
		return handoffLockedWork{lease: lease, leaseState: state}
	}
	if !handoffVacantWorkLeaseWitness(scope, before, locked(base())) {
		t.Fatal("the exact row createVacantWorkLease writes is not recognised as the witness")
	}
	for _, tc := range []struct {
		name   string
		mutate func(model.Record)
		offer  int64
	}{
		{name: "holder sid", mutate: func(r model.Record) {
			r[colLeaseHolderSID] = "osn_" + model.NewID().String()
		}},
		{name: "holder run ref", mutate: func(r model.Record) { r[colLeaseHolderRunRef] = "run:x" }},
		{name: "holder agent ref", mutate: func(r model.Record) { r[colLeaseHolderAgentRef] = "agent:x" }},
		{name: "acquired at", mutate: func(r model.Record) {
			r[colLeaseAcquiredAt] = model.NewTimestamp(time.Now().UTC()).String()
		}},
		{name: "renewed at", mutate: func(r model.Record) {
			r[colLeaseRenewedAt] = model.NewTimestamp(time.Now().UTC()).String()
		}},
		{name: "expires at", mutate: func(r model.Record) {
			r[colLeaseExpiresAt] = model.NewTimestamp(time.Now().UTC()).String()
		}},
		{name: "ended at", mutate: func(r model.Record) {
			r[colLeaseEndedAt] = model.NewTimestamp(time.Now().UTC()).String()
		}},
		{name: "end reason", mutate: func(r model.Record) { r[colLeaseEndReason] = "lease_revoked" }},
		{name: "renewal count", mutate: func(r model.Record) { r[colLeaseRenewalCount] = int64(1) }},
		{name: "fence", mutate: func(r model.Record) { r[colLeaseFence] = int64(1) }},
		{name: "state", mutate: func(r model.Record) { r[colLeaseState] = workLeaseReleased }},
		{name: "workspace", mutate: func(r model.Record) {
			r[colWorkWorkspaceID] = model.NewID().String()
		}},
		{name: "work item", mutate: func(r model.Record) { r[colWorkItemID] = model.NewID().String() }},
		{name: "offered fence disagrees with the lease", mutate: func(model.Record) {}, offer: 3},
	} {
		lease := base()
		tc.mutate(lease)
		offered := before
		offered.OfferedLeaseFence = tc.offer
		if handoffVacantWorkLeaseWitness(scope, offered, locked(lease)) {
			t.Errorf("%s: a lease that carries authority was accepted as the vacant witness", tc.name)
		}
	}
}

// vacantTransferEngine is one engine this lot's durable controls must hold on.
// The PostgreSQL leg is the SPLIT-OWNER topology — the owner role runs DDL and
// the application role holds DML only — because that is the posture the release
// targets and the one whose triggers this lot moves to a new function identity.
type vacantTransferEngine struct {
	name       string
	newBackend func(t *testing.T, label string) communicationSchemaBackend
}

func vacantTransferEngines(t *testing.T) []vacantTransferEngine {
	t.Helper()
	engines := []vacantTransferEngine{{
		name: "sqlite",
		newBackend: func(t *testing.T, label string) communicationSchemaBackend {
			t.Helper()
			return communicationSchemaBackend{
				name: "sqlite-" + label, engineName: store.EngineSQLite,
				dsn: filepath.Join(t.TempDir(), label+".db"),
			}
		},
	}}
	if !enginetest.PostgresAvailable(t) {
		required, err := strconv.ParseBool(
			strings.TrimSpace(os.Getenv("OLIVARES_TEST_POSTGRES_REQUIRED")))
		if err == nil && required {
			t.Fatal("the vacant-generation durable controls require PostgreSQL for this run")
		}
		t.Logf("%s unset: the PostgreSQL vacant-generation controls are NOT exercised",
			enginetest.EnvSuperuserDSN)
		return engines
	}
	engines = append(engines, vacantTransferEngine{
		name: "postgres-split-owner",
		newBackend: func(t *testing.T, label string) communicationSchemaBackend {
			t.Helper()
			pg := enginetest.IsolatedPostgresSplitOwner(t)
			return communicationSchemaBackend{
				name: "postgres-" + label, engineName: store.EnginePostgres,
				dsn: pg.App, ownerDSN: pg.Owner,
			}
		},
	})
	return engines
}

func vacantTransferOffer(
	t *testing.T,
	fixture handoffServiceFixture,
	summary string,
) HandoffOfferResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer, err := fixture.m.offerHandoffWithAuthority(ctx, fixture.scope, fixture.ref,
		HandoffOfferCommand{
			ChannelID: fixture.channel.ID, WorkItemID: fixture.workID,
			MessageID: fixture.message.ID, DeliveryID: fixture.delivery.ID,
			Content: HandoffContent{Summary: summary, NextAction: "Continue " + summary},
			IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("offer Handoff: %v", err)
	}
	return offer
}

// TestHandoffVacantGenerationTransferWritesNoLeaseOrClockRow is the durable half
// of OT-V on both engines: ownership moves, and the WorkLease row and the
// workspace lease-clock guard are exactly what they were — including the guard
// that does not exist, which this transaction must not create.
func TestHandoffVacantGenerationTransferWritesNoLeaseOrClockRow(t *testing.T) {
	t.Parallel()

	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			backend := engine.newBackend(t, "vacant-transfer")
			fixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
				backend: &backend, vacantLease: true, noClockGuard: true,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			t.Cleanup(cancel)
			offer := vacantTransferOffer(t, fixture, "Transfer a vacant generation")

			leaseBefore := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
			if leaseBefore.String(colLeaseState) != workLeaseVacant ||
				leaseBefore.Int(colLeaseFence) != 0 {
				t.Fatalf("fixture lease is not the vacant generation: %v", leaseBefore)
			}
			if guards := communicationRowsForTest(t, fixture.directNoticeFixture, workGuardKind); len(guards) != 0 {
				t.Fatalf("the fixture workspace already observed a lease clock: %v", guards)
			}

			key := model.NewID().String()
			response, err := fixture.m.respondHandoffWithAuthority(
				ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
				HandoffResponseCommand{
					Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: key,
				},
			)
			if err != nil {
				t.Fatalf("accept a vacant generation: %v", err)
			}
			if response.State != HandoffAccepted || response.OwnerEpoch != 2 ||
				response.ResultingLeaseFence != 0 || response.AckID == "" || response.Replayed {
				t.Fatalf("vacant-generation accept = %+v", response)
			}

			stored, err := handoffFromRecord(
				handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID))
			if err != nil {
				t.Fatalf("decode the accepted Handoff: %v", err)
			}
			if stored.State != HandoffAccepted || stored.ResultingLeaseFence != 0 ||
				stored.AckID != response.AckID {
				t.Fatalf("stored accepted Handoff = %+v", stored)
			}
			raw := handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID)
			if !raw.IsNull(colCommResultingLeaseFence) {
				t.Fatalf("the durable row carries a resulting fence: %v",
					raw[colCommResultingLeaseFence])
			}

			work := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
			if work.String(colWorkOwnerRef) != fixture.delivery.Recipient.Ref ||
				work.Int(colWorkOwnerEpoch) != 2 {
				t.Fatalf("ownership did not move: %v", work)
			}
			leaseAfter := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
			for column, want := range map[string]any{
				colLeaseState: workLeaseVacant, colLeaseFence: int64(0),
				colLeaseRenewalCount: int64(0),
			} {
				if got, ok := leaseAfter[column]; !ok || got != want {
					t.Fatalf("lease column %s = %v, want %v", column, got, want)
				}
			}
			if leaseAfter.Int(model.ColVersion) != leaseBefore.Int(model.ColVersion) {
				t.Fatalf("the vacant arm wrote the lease row: v%d -> v%d",
					leaseBefore.Int(model.ColVersion), leaseAfter.Int(model.ColVersion))
			}
			for _, column := range []string{
				colLeaseHolderSID, colLeaseHolderRunRef, colLeaseHolderAgentRef,
				colLeaseAcquiredAt, colLeaseRenewedAt, colLeaseExpiresAt,
				colLeaseEndedAt, colLeaseEndReason,
			} {
				if !leaseAfter.IsNull(column) {
					t.Fatalf("the transfer wrote lease authority into %s: %v",
						column, leaseAfter[column])
				}
			}
			// THE CLOCK. The accept keeps taking the workspace clock lock, so its
			// reading of "this workspace has never observed a clock" is stable for
			// the whole transaction — and it still makes no claim about time.
			if guards := communicationRowsForTest(t, fixture.directNoticeFixture, workGuardKind); len(guards) != 0 {
				t.Fatalf("the vacant arm created a lease clock guard: %v", guards)
			}

			// Replay reconstructs the result from the DURABLE receipt and Handoff,
			// never from a live lease read, so the absent fence survives it.
			replay, err := fixture.m.respondHandoffWithAuthority(
				ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
				HandoffResponseCommand{
					Transition: HandoffAccept, IfMatch: offer.ETag, IdempotencyKey: key,
				},
			)
			if err != nil || !replay.Replayed || replay.CommandID != response.CommandID ||
				replay.ResultingLeaseFence != 0 || replay.OwnerEpoch != response.OwnerEpoch {
				t.Fatalf("vacant-generation accept replay = %+v, err %v", replay, err)
			}
		})
	}
}

// TestHandoffAcceptStillRefusesAnActiveGenerationWithoutItsClockGuard is the
// other side of the evidence correction. Zero guard rows is a KNOWN fact, but it
// is only a harmless one on the arm that makes no liveness decision: an accept
// that has an execution generation to fence still needs the clock and still says
// so, and a missing guard is never again a blanket refusal.
func TestHandoffAcceptStillRefusesAnActiveGenerationWithoutItsClockGuard(t *testing.T) {
	t.Parallel()

	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			backend := engine.newBackend(t, "active-without-clock")
			fixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
				backend: &backend, noClockGuard: true,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			t.Cleanup(cancel)
			offer := vacantTransferOffer(t, fixture, "Active generation with no clock")

			before := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
			_, err := fixture.m.respondHandoffWithAuthority(
				ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
				HandoffResponseCommand{
					Transition: HandoffAccept, IfMatch: offer.ETag,
					IdempotencyKey: model.NewID().String(),
				},
			)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("accept of an active generation with no clock guard = %v, "+
					"want ErrCommunicationEvidenceUnknown", err)
			}
			after := handoffStoredRecord(t, fixture, workItemKind, fixture.workID)
			if after.Int(colWorkOwnerEpoch) != before.Int(colWorkOwnerEpoch) ||
				after.Int(model.ColVersion) != before.Int(model.ColVersion) {
				t.Fatalf("a refused accept moved ownership: before=%v after=%v", before, after)
			}
			if guards := communicationRowsForTest(t, fixture.directNoticeFixture, workGuardKind); len(guards) != 0 {
				t.Fatalf("a refused accept created a lease clock guard: %v", guards)
			}
			lease := handoffStoredRecord(t, fixture, workLeaseKind, fixture.leaseID)
			if lease.String(colLeaseState) != workLeaseActive || lease.Int(colLeaseFence) != 7 {
				t.Fatalf("a refused accept wrote the lease: %v", lease)
			}
		})
	}
}

// TestHandoffAcceptedFenceTriggerMirrorsTheGoRuleOnBothEngines is the control
// that proves the SQL twin did not drift from handoffAcceptFenceOK. Every pair
// is a DURABLE write through the module's own repositories — no raw SQL, no
// model validation in the way — so the verdict comes from the database.
//
// Each attempt acknowledges the Delivery, appends the Ack and rewrites the
// Handoff in ONE transaction, exactly as the accept does. A refused pair
// therefore rolls the whole attempt back and the next one starts from the same
// offered row.
func TestHandoffAcceptedFenceTriggerMirrorsTheGoRuleOnBothEngines(t *testing.T) {
	t.Parallel()

	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			t.Run("vacant generation", func(t *testing.T) {
				backend := engine.newBackend(t, "trigger-vacant")
				fixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
					backend: &backend, vacantLease: true, noClockGuard: true,
				})
				offer := vacantTransferOffer(t, fixture, "Trigger control, vacant")
				// Refused: an accept sealed against a vacant generation may not
				// invent one. This is the half a naive relaxation would have opened.
				assertDurableAcceptedFenceRefused(t, fixture, offer.HandoffID, 1)
				assertDurableAcceptedFenceRefused(t, fixture, offer.HandoffID, 8)
				// Accepted, and forbidden before this lot: the absent fence.
				assertDurableAcceptedFenceAccepted(t, fixture, offer.HandoffID, 0)
			})
			t.Run("active generation", func(t *testing.T) {
				backend := engine.newBackend(t, "trigger-active")
				fixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
					backend: &backend,
				})
				offer := vacantTransferOffer(t, fixture, "Trigger control, active")
				// Unchanged in every direction: the offered generation is fence 7.
				assertDurableAcceptedFenceRefused(t, fixture, offer.HandoffID, 0)
				assertDurableAcceptedFenceRefused(t, fixture, offer.HandoffID, 7)
				assertDurableAcceptedFenceRefused(t, fixture, offer.HandoffID, 6)
				assertDurableAcceptedFenceAccepted(t, fixture, offer.HandoffID, 8)
			})
		})
	}
}

func assertDurableAcceptedFenceRefused(
	t *testing.T,
	fixture handoffServiceFixture,
	handoffID model.ID,
	resulting int64,
) {
	t.Helper()
	err := writeDurableAcceptedHandoff(t, fixture, handoffID, resulting)
	if err == nil {
		t.Fatalf("the database accepted resulting fence %d", resulting)
	}
	// The two engines word the same guard differently — SQLite's UPDATE trigger
	// says "Handoff terminal state evidence is inconsistent" and PostgreSQL says
	// "Handoff state evidence is inconsistent" — so the assertion names the part
	// they share. It is still the exact clause: a refusal from any other guard
	// (lineage, Ack, timestamps) carries a different message and fails here.
	if !strings.Contains(err.Error(), "state evidence is inconsistent") {
		t.Fatalf("resulting fence %d was refused for the wrong reason: %v", resulting, err)
	}
	stored := handoffStoredRecord(t, fixture, handoffKind, handoffID)
	if stored.String(colCommState) != string(HandoffOffered) {
		t.Fatalf("a refused durable write left the Handoff in state %q",
			stored.String(colCommState))
	}
}

func assertDurableAcceptedFenceAccepted(
	t *testing.T,
	fixture handoffServiceFixture,
	handoffID model.ID,
	resulting int64,
) {
	t.Helper()
	if err := writeDurableAcceptedHandoff(t, fixture, handoffID, resulting); err != nil {
		t.Fatalf("the database refused resulting fence %d: %v", resulting, err)
	}
	stored := handoffStoredRecord(t, fixture, handoffKind, handoffID)
	if stored.String(colCommState) != string(HandoffAccepted) {
		t.Fatalf("an accepted durable write left the Handoff in state %q",
			stored.String(colCommState))
	}
	if resulting == 0 && !stored.IsNull(colCommResultingLeaseFence) {
		t.Fatalf("an absent fence was stored as %v", stored[colCommResultingLeaseFence])
	}
	if resulting != 0 && stored.Int(colCommResultingLeaseFence) != resulting {
		t.Fatalf("resulting fence stored as %v, want %d",
			stored[colCommResultingLeaseFence], resulting)
	}
}

// writeDurableAcceptedHandoff performs the three writes an accept performs on the
// carrier — acknowledge the Delivery, append the Ack, move the Handoff — in one
// transaction, with the resulting fence chosen by the caller.
func writeDurableAcceptedHandoff(
	t *testing.T,
	fixture handoffServiceFixture,
	handoffID model.ID,
	resulting int64,
) error {
	t.Helper()
	ctx := context.Background()
	// The engine stamps updated_at from the store's clock, and this fixture pins
	// that clock while the offer path stamped its row from the DURABLE
	// transaction time. Move the fixture clock past the row it is about to
	// rewrite, so the guard judges the fence rule rather than a timestamp that
	// went backwards. It stays well inside the Ack deadline.
	clock, ok := fixture.m.clock.(*testClock)
	if !ok {
		t.Fatalf("the fixture store clock is %T, not a controllable test clock", fixture.m.clock)
	}
	stored := handoffStoredRecord(t, fixture, handoffKind, handoffID)
	last, err := model.ParseTimestamp(stored.String(model.ColUpdatedAt))
	if err != nil {
		t.Fatalf("read the stored Handoff time: %v", err)
	}
	at := last.Time().Add(time.Second)
	if !at.After(clock.get()) {
		at = clock.get().Add(time.Second)
	}
	clock.set(at)
	ackID := model.NewID()
	return fixture.m.data.Mutate(ctx, fixture.tenant, func(sc store.Scope) error {
		deliveries, err := sc.Ext(messageDeliveryKind)
		if err != nil {
			return err
		}
		deliveryAfter := fixture.delivery
		deliveryAfter.Version++
		deliveryAfter.UpdatedAt = at
		deliveryAfter.State = DeliveryAcknowledged
		deliveryAfter.AckID = ackID
		deliveryAfter.AcknowledgedAt = &at
		deliveryRecord, err := messageDeliveryToRecord(deliveryAfter)
		if err != nil {
			return err
		}
		deliveryRecord[model.ColVersion] = fixture.delivery.Version
		if _, err := deliveries.Update(ctx, deliveryRecord); err != nil {
			return err
		}
		acks, err := sc.Ext(messageAckKind)
		if err != nil {
			return err
		}
		ackRecord, err := messageAckToRecord(MessageAck{
			AppendOnlyCommunicationEntity: AppendOnlyCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: ackID, TenantID: fixture.tenant, WorkspaceID: fixture.workspace,
					Version: 1, CreatedAt: at,
				},
			},
			DeliveryID: fixture.delivery.ID, Kind: MessageAckReceived,
			Actor: CommunicationActorRef{
				Kind: ActorUser, Ref: fixture.delivery.Recipient.Ref,
			},
			AcknowledgedAt: at, Late: false,
		})
		if err != nil {
			return err
		}
		if _, err := acks.CreateWithID(ctx, ackID, ackRecord); err != nil {
			return err
		}
		handoffs, err := sc.Ext(handoffKind)
		if err != nil {
			return err
		}
		record, err := handoffs.Get(ctx, handoffID)
		if err != nil {
			return err
		}
		record[colCommState] = string(HandoffAccepted)
		record[colCommAckID] = ackID.String()
		record[colCommAcceptedAt] = model.NewTimestamp(at).String()
		record[model.ColUpdatedAt] = model.NewTimestamp(at).String()
		if resulting == 0 {
			record[colCommResultingLeaseFence] = nil
		} else {
			record[colCommResultingLeaseFence] = resulting
		}
		_, err = handoffs.Update(ctx, record)
		return err
	})
}

// The three OT-V migrations claim, and now PROVE, that no already-accepted row
// is in the shape the new rule forbids. The claim used to be an ARGUMENT on
// PostgreSQL — "the pre-transition public accept refuses a vacant lease" — and
// an argument about the HTTP path is not a statement about the durable
// relation's accepted-state domain. The pre-transition guard
// (0012_communication_validate_function.sql) required only a non-null resulting
// fence greater than COALESCE(offered,0), so an otherwise valid accepted
// envelope with offered absent and resulting 1 satisfied it. That envelope is
// exactly what this control writes, through the module's own repositories, on an
// estate opened at the LAST PRE-OT-V migration tip.
//
// Then it meets the upgrade, and the upgrade must fail closed: no row repaired,
// no trigger replaced, no function identity reserved, no tracking row recorded.
const (
	vacantTransferPreviousSQLiteTip   = 95
	vacantTransferPreviousPostgresTip = 23
)

// vacantTransferPreviousRegistration is the live sessions registration truncated
// to the last tip before OT-V, with each moved trigger rewound to the digest its
// transition names as the prestate.
func vacantTransferPreviousRegistration(t *testing.T) func(store.ExtensionRegistry) error {
	t.Helper()
	live := communicationCaptureSchema(t)
	if len(live.migrations) != 1 || len(live.invariants) != 1 {
		t.Fatalf("live sessions registration = %d migrations/%d invariants, want 1/1",
			len(live.migrations), len(live.invariants))
	}
	migrations := communicationMigrationThrough(t, live.migrations[0].fs, map[string]int{
		"postgres": vacantTransferPreviousPostgresTip,
		"sqlite":   vacantTransferPreviousSQLiteTip,
	})
	invariants := communicationInvariantsThrough(live.invariants[0].byEngine,
		map[store.Engine]int{
			store.EnginePostgres: vacantTransferPreviousPostgresTip,
			store.EngineSQLite:   vacantTransferPreviousSQLiteTip,
		})
	return communicationCapturedRegistration(live, migrations, invariants)
}

func TestHandoffVacantTransferMigrationsRefuseAnIncompatibleAcceptedRow(t *testing.T) {
	t.Parallel()

	for _, backing := range vacantTransferEngines(t) {
		t.Run(backing.name, func(t *testing.T) {
			previousTip := vacantTransferPreviousSQLiteTip
			currentTip := 97
			if backing.name != "sqlite" {
				previousTip, currentTip = vacantTransferPreviousPostgresTip, 24
			}

			// The estate the previous schema left behind, with the envelope the
			// previous schema allowed and this one forbids.
			backend := backing.newBackend(t, "premigration-incompatible")
			backend.registerSchema = vacantTransferPreviousRegistration(t)
			fixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
				backend: &backend, vacantLease: true, noClockGuard: true,
			})
			communicationAssertMigrationTip(
				t, backend.engineName, vacantTransferTrackingDSN(backend), previousTip)
			offer := vacantTransferOffer(t, fixture, "Pre-OT-V accepted row")
			if err := writeDurableAcceptedHandoff(t, fixture, offer.HandoffID, 1); err != nil {
				t.Fatalf("the pre-OT-V schema refused the envelope this control needs, so the "+
					"upgrade exposure it measures could not exist: %v", err)
			}
			stored := handoffStoredRecord(t, fixture, handoffKind, offer.HandoffID)
			if stored.String(colCommState) != string(HandoffAccepted) ||
				stored.Int(colCommResultingLeaseFence) != 1 ||
				!stored.IsNull(colCommOfferedLeaseFence) {
				t.Fatalf("the planted previous-schema row is not the 0 -> positive envelope: %+v",
					stored)
			}
			prestate := vacantTransferGuardPrestate(t, backend)
			if err := fixture.st.Close(); err != nil {
				t.Fatalf("close the previous-schema estate: %v", err)
			}

			// The upgrade meets it. Nothing is repaired: it refuses.
			opened, err := engine.Open(context.Background(), store.Config{
				Engine: backend.engineName, DSN: backend.dsn, OwnerDSN: backend.ownerDSN,
				Debug: true,
			}, New().RegisterSchema)
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatal("the upgrade accepted an estate holding an incompatible accepted row")
			}
			if !strings.Contains(err.Error(), vacantTransferRefusalMarker(backing.name)) {
				t.Fatalf("the upgrade failed for the wrong reason: %v", err)
			}
			t.Logf("OTV_UPGRADE_REFUSED engine=%s error=%v", backing.name, err)

			// Rollback, by identity: the tip, both guard definitions, the reserved
			// function identity and the row itself are what they were.
			communicationAssertMigrationTip(
				t, backend.engineName, vacantTransferTrackingDSN(backend), previousTip)
			if after := vacantTransferGuardPrestate(t, backend); after != prestate {
				t.Fatalf("the refused upgrade changed the guard prestate:\nbefore %+v\nafter  %+v",
					prestate, after)
			}
			if backend.engineName == store.EnginePostgres {
				if communicationPostgresFunctionExists(t, backend.dsn,
					"olivares_sessions_work_handoff_validate_v24") {
					t.Fatal("the refused upgrade left the reserved v24 function behind")
				}
			}
			vacantTransferAssertPlantedRowIntact(t, backend, fixture.tenant, offer.HandoffID)

			// The positive control, on the SAME previous schema and the same
			// upgrade: without that row the upgrade completes. Without this the
			// refusal above could be an upgrade that never works.
			clean := backing.newBackend(t, "premigration-compatible")
			clean.registerSchema = vacantTransferPreviousRegistration(t)
			cleanFixture := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{
				backend: &clean, vacantLease: true, noClockGuard: true,
			})
			cleanOffer := vacantTransferOffer(t, cleanFixture, "Pre-OT-V compatible row")
			// And the other direction of the same change, on the same previous
			// schema: the absent fence OT-V newly permits was REFUSED before it.
			// The compatible estate therefore keeps this Handoff offered.
			if err := writeDurableAcceptedHandoff(
				t, cleanFixture, cleanOffer.HandoffID, 0); err == nil {
				t.Fatal("the previous schema accepted an absent resulting fence")
			} else if !strings.Contains(err.Error(), "state evidence is inconsistent") {
				t.Fatalf("the previous schema refused the absent fence for the wrong reason: %v", err)
			}
			communicationAssertMigrationTip(
				t, clean.engineName, vacantTransferTrackingDSN(clean), previousTip)
			if err := cleanFixture.st.Close(); err != nil {
				t.Fatalf("close the compatible previous-schema estate: %v", err)
			}
			upgraded, err := engine.Open(context.Background(), store.Config{
				Engine: clean.engineName, DSN: clean.dsn, OwnerDSN: clean.ownerDSN,
				Debug: true,
			}, New().RegisterSchema)
			if err != nil {
				t.Fatalf("the upgrade refused an estate with no incompatible row: %v", err)
			}
			if err := upgraded.Close(); err != nil {
				t.Fatalf("close the upgraded estate: %v", err)
			}
			communicationAssertMigrationTip(
				t, clean.engineName, vacantTransferTrackingDSN(clean), currentTip)
			t.Logf("OTV_UPGRADE_CLEAN engine=%s tip=%d", backing.name, currentTip)
		})
	}
}

// vacantTransferGuardPrestate is the identity of the guards OT-V moves, read
// from the live catalog rather than from any declaration.
type vacantTransferGuardPrestateWitness struct {
	insert string
	update string
}

func vacantTransferGuardPrestate(
	t *testing.T,
	backend communicationSchemaBackend,
) vacantTransferGuardPrestateWitness {
	t.Helper()
	if backend.engineName == store.EngineSQLite {
		return vacantTransferGuardPrestateWitness{
			insert: communicationSQLiteTriggerDigest(t, backend.dsn,
				"sessions_work_handoff_guard_ins"),
			update: communicationSQLiteTriggerDigest(t, backend.dsn,
				"sessions_work_handoff_guard_upd"),
		}
	}
	return vacantTransferGuardPrestateWitness{
		insert: communicationPostgresTriggerDigest(t, backend.dsn,
			"sessions_work_handoff", "sessions_work_handoff_guard"),
		update: strconv.Itoa(communicationPostgresFunctionCallerCount(t, backend.dsn,
			"olivares_sessions_communication_validate")),
	}
}

// vacantTransferRefusalMarker is the engine's own words for the pre-check. The
// SQLite half aborts through the documented integer-overflow abort its migration
// uses; PostgreSQL raises the message its DO block names.
func vacantTransferRefusalMarker(engineName string) string {
	if engineName == "sqlite" {
		return "integer overflow"
	}
	return "an accepted Handoff already records a lease effect on a vacant offered generation"
}

// vacantTransferAssertPlantedRowIntact proves the refusal repaired nothing: the
// row is still accepted, still carries the fence the previous schema allowed,
// and still has no offered fence.
//
// On PostgreSQL the read BINDS the tenant, because sessions_work_handoff carries
// FORCE row-level security whose policy reads app.tenant_id without missing_ok:
// unbound it raises, and bound to the empty string it answers zero rows for
// every tenant. That is precisely why the migration's own pre-check cannot be a
// SELECT, and this helper is the control that shows the row was there all along.
func vacantTransferAssertPlantedRowIntact(
	t *testing.T,
	backend communicationSchemaBackend,
	tenant model.TenantID,
	handoffID model.ID,
) {
	t.Helper()
	driver, placeholder := "sqlite", "?"
	if backend.engineName == store.EnginePostgres {
		driver, placeholder = "pgx", "$1"
	}
	raw, err := sql.Open(driver, backend.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close() //nolint:errcheck
	ctx := context.Background()
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck
	if backend.engineName == store.EnginePostgres {
		if _, err := conn.ExecContext(ctx,
			"SELECT pg_catalog.set_config('app.tenant_id',$1,false)", tenant.String()); err != nil {
			t.Fatalf("bind the tenant for the planted-row read: %v", err)
		}
	}
	var state string
	var resulting, offered sql.NullInt64
	query := "SELECT state, resulting_lease_fence, offered_lease_fence " +
		"FROM sessions_work_handoff WHERE id = " + placeholder
	if err := conn.QueryRowContext(ctx, query, handoffID.String()).Scan(
		&state, &resulting, &offered); err != nil {
		t.Fatalf("read the planted row after the refused upgrade: %v", err)
	}
	if state != string(HandoffAccepted) || !resulting.Valid || resulting.Int64 != 1 ||
		offered.Valid {
		t.Fatalf("the refused upgrade changed the planted row: state=%s resulting=%v offered=%v",
			state, resulting, offered)
	}
}

// vacantTransferTrackingDSN reads the module's migration tracking table as the
// role that owns it. The tracking table is plain DDL with no tenant policy, but
// in the split-owner topology the application role is not the one that created
// it, so the DDL owner is the honest reader.
func vacantTransferTrackingDSN(backend communicationSchemaBackend) string {
	if backend.ownerDSN != "" {
		return backend.ownerDSN
	}
	return backend.dsn
}
