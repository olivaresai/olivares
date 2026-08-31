// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// evidenceops_test.go (stage 2) — semantic pins for the durable evidence
// operation journal that enforces the frozen S5 evidence contract for
// external-effect PEPs (the MCP gateway in stage 3): single-use OperationID
// claim + anchor BEFORE effect, durable replay state, durable settlement.
// Written RED-first: every pin below fails against a naive implementation
// (append-on-replay, sentinel-inside-tx on degrade, silent settle of a missing
// row, double-dispatch after a crash).

// testClaim returns a fully-populated claim spec for one operation.
func testClaim(op, digest string) store.EvidenceClaim {
	return store.EvidenceClaim{
		OperationID:  op,
		EffectDigest: digest,
		Surface:      "mcp.gateway",
		Action:       "mcp.tool.call",
		Actor:        "user:test",
		ActorKind:    model.ActorUser,
	}
}

// countAuditAction walks the tenant chain and counts events with the action.
func countAuditAction(t *testing.T, st store.Store, tenant model.TenantID, action string) int {
	t.Helper()
	var n int
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			if ev.Action == action {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit chain: %v", err)
	}
	return n
}

// getEvidenceOp reads one journal row through the Scope surface.
func getEvidenceOp(t *testing.T, st store.Store, tenant model.TenantID, opID string) (model.EvidenceOperation, error) {
	t.Helper()
	var op model.EvidenceOperation
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var e error
		op, e = sc.EvidenceOperations().Get(context.Background(), opID)
		return e
	})
	return op, err
}

// mustVerifyChain asserts the tenant chain is structurally intact.
func mustVerifyChain(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(context.Background(), 1)
		if err != nil {
			return err
		}
		if !rep.OK {
			t.Fatalf("audit chain broken: %+v", rep)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify chain: %v", err)
	}
}

func TestEvidenceClaimFreshAnchorsAndCreatesRow(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-fresh")

	spec := testClaim("op-fresh-1", "digest-a")
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
	if !out.Fresh {
		t.Fatalf("fresh claim reported Fresh=false: %+v", out)
	}
	binding := sdk.EvidenceBinding{OperationID: "op-fresh-1", EffectDigest: "digest-a"}
	if out.Receipt.MustRefuse(binding) {
		t.Fatalf("fresh claim receipt not anchored: %+v", out.Receipt)
	}
	if out.Op.State != model.EvidenceOpClaimed {
		t.Fatalf("fresh claim state = %q, want claimed", out.Op.State)
	}
	if out.Op.ClaimEvidenceRef == "" || out.Op.ClaimEvidenceRef != out.Receipt.EvidenceRef {
		t.Fatalf("claim evidence ref mismatch: row=%q receipt=%q", out.Op.ClaimEvidenceRef, out.Receipt.EvidenceRef)
	}
	// Exactly one claim evidence event, appended in the SAME transaction.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 1 {
		t.Fatalf("claim events = %d, want 1", n)
	}
	// The row is durable and carries the full binding — refs and digests only.
	row, err := getEvidenceOp(t, st, tenant, "op-fresh-1")
	if err != nil {
		t.Fatalf("get journal row: %v", err)
	}
	if row.EffectDigest != "digest-a" || row.Surface != "mcp.gateway" || row.Action != "mcp.tool.call" {
		t.Fatalf("journal row binding = %+v", row)
	}
	if row.OutcomeEvidenceRef != "" || row.ResultDigest != "" || row.DispatchRef != "" {
		t.Fatalf("unsettled row carries outcome fields: %+v", row)
	}
	mustVerifyChain(t, st, tenant)
}

func TestEvidenceClaimExactReplayIsSilent(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-replay")

	spec := testClaim("op-replay-1", "digest-a")
	first, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
	events := countAuditAction(t, st, tenant, "mcp.tool.call.claim")

	second, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if second.Fresh {
		t.Fatalf("replay reported Fresh=true")
	}
	if second.Op.ID != first.Op.ID || second.Op.State != model.EvidenceOpClaimed {
		t.Fatalf("replay row = %+v, want the winner's row", second.Op)
	}
	if second.Receipt.EvidenceRef != first.Receipt.EvidenceRef {
		t.Fatalf("replay receipt ref %q != recorded %q", second.Receipt.EvidenceRef, first.Receipt.EvidenceRef)
	}
	if second.Receipt.MustRefuse(first.Binding) {
		t.Fatalf("replay receipt refused: %+v", second.Receipt)
	}
	// NO new evidence event, NO second row.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != events {
		t.Fatalf("replay appended evidence: %d -> %d claim events", events, n)
	}
}

func TestEvidenceClaimRebindRefusesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-rebind")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-rebind-1", "digest-a")); err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
	events := countAuditAction(t, st, tenant, "mcp.tool.call.claim")

	_, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-rebind-1", "digest-B"))
	if !errors.Is(err, store.ErrEvidenceRebind) {
		t.Fatalf("rebind err = %v, want ErrEvidenceRebind", err)
	}
	// No row mutation, no evidence appended.
	row, gerr := getEvidenceOp(t, st, tenant, "op-rebind-1")
	if gerr != nil || row.EffectDigest != "digest-a" || row.Version != 1 {
		t.Fatalf("rebind mutated the row: %+v err=%v", row, gerr)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != events {
		t.Fatalf("rebind appended evidence: %d -> %d", events, n)
	}
}

func TestEvidenceClaimDegradeDropCommitsLossAccounting(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "ev-degrade.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "ev-degrade")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})

	gapBefore, _ := readPendingAuditGap(t, st, tenant)
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-degrade-1", "digest-a"))
	if err != nil {
		t.Fatalf("degrade claim: %v", err)
	}
	binding := sdk.EvidenceBinding{OperationID: "op-degrade-1", EffectDigest: "digest-a"}
	if !out.Receipt.MustRefuse(binding) {
		t.Fatalf("degrade-drop receipt did not refuse: %+v", out.Receipt)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultSpoolDegraded {
		t.Fatalf("degrade fault = %q, want spool_degraded", out.Receipt.Fault)
	}
	// NO operation row was staged…
	if _, gerr := getEvidenceOp(t, st, tenant, "op-degrade-1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("degrade drop staged a row: err=%v", gerr)
	}
	// …but the loss accounting COMMITTED durably (pending drops advanced).
	gapAfter, ok := readPendingAuditGap(t, st, tenant)
	if !ok || gapAfter.dropped != gapBefore.dropped+1 {
		t.Fatalf("pending drops did not advance: before=%+v after=%+v ok=%v", gapBefore, gapAfter, ok)
	}
}

func TestEvidenceClaimBlockModeSpoolFull(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "ev-block.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "ev-block")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: 1})

	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-block-1", "digest-a"))
	if err != nil {
		t.Fatalf("block claim: %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultSpoolFull {
		t.Fatalf("block fault = %q, want spool_full", out.Receipt.Fault)
	}
	// Nothing durable: no row, no pending drop (block mode refuses, never drops).
	if _, gerr := getEvidenceOp(t, st, tenant, "op-block-1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("block-mode refusal staged a row: err=%v", gerr)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatalf("block-mode refusal recorded a degrade drop")
	}
}

func TestEvidenceClaimNotLeader(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-standby")
	st.(*sqlStore).elector = &epochElector{toggleElector: toggleElector{activeVal: false}, epoch: 3}

	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-standby-1", "digest-a"))
	if err != nil {
		t.Fatalf("standby claim: %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnavailable {
		t.Fatalf("standby fault = %q, want ledger_unavailable", out.Receipt.Fault)
	}
	// Nothing was journaled: the fence refused before any store I/O.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("standby claim appended %d evidence events, want 0", n)
	}
}

func TestEvidenceClaimNoFenceCapabilityFailsClosed(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-nofence")
	// An elector WITHOUT the EpochFencer capability cannot durably fence: the
	// claim must refuse (ledger_unwired — fencing infrastructure not wired),
	// never fall back to the in-memory Active()/Epoch() pair.
	st.(*sqlStore).elector = &toggleElector{activeVal: true}

	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-nofence-1", "digest-a"))
	if err != nil {
		t.Fatalf("no-fence claim: %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnwired {
		t.Fatalf("no-fence fault = %q, want ledger_unwired", out.Receipt.Fault)
	}
	// EvidenceEpochFence fails closed on the same absent capability.
	if ferr := store.EvidenceEpochFence(ctx, st.Leader(), 1); ferr == nil {
		t.Fatalf("EvidenceEpochFence passed without the durable fence capability")
	}
}

func TestEvidenceClaimNilStoreUnwired(t *testing.T) {
	out, err := store.ClaimEvidenceOperation(context.Background(), nil, model.TenantID("t"), testClaim("op-nil-1", "digest-a"))
	if err != nil {
		t.Fatalf("nil-store claim: %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnwired {
		t.Fatalf("nil-store fault = %q, want ledger_unwired", out.Receipt.Fault)
	}
}

func TestEvidenceClaimStoresLeaderEpoch(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-epoch")
	el := &epochElector{toggleElector: toggleElector{activeVal: true}, epoch: 7}
	st.(*sqlStore).elector = el

	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-epoch-1", "digest-a"))
	if err != nil {
		t.Fatalf("epoch claim: %v", err)
	}
	// The stored epoch comes from the DURABLE fence, not the in-memory cache.
	if out.Op.LeaderEpoch != 7 {
		t.Fatalf("stored leader epoch = %d, want 7", out.Op.LeaderEpoch)
	}
	// The BeforeEffect fence: current epoch passes, a bumped epoch refuses.
	if ferr := store.EvidenceEpochFence(ctx, st.Leader(), out.Op.LeaderEpoch); ferr != nil {
		t.Fatalf("current epoch failed the fence: %v", ferr)
	}
	el.epoch = 8
	if ferr := store.EvidenceEpochFence(ctx, st.Leader(), out.Op.LeaderEpoch); ferr == nil {
		t.Fatalf("stale epoch passed the fence after a failover bump")
	}
	el.activeVal = false
	el.epoch = 7
	if ferr := store.EvidenceEpochFence(ctx, st.Leader(), out.Op.LeaderEpoch); ferr == nil {
		t.Fatalf("inactive elector passed the fence")
	}
}

// epochElector is toggleElector with a settable fencing epoch and the
// EpochFencer capability (the fence follows activeVal).
type epochElector struct {
	toggleElector
	epoch uint64
}

func (e *epochElector) Epoch() uint64 { return e.epoch }

func (e *epochElector) FencedEpoch(context.Context) (uint64, error) {
	if !e.activeVal {
		return 0, errors.New("standby (durable fence refused)")
	}
	return e.epoch, nil
}

func TestEvidenceSettleTerminalStates(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-settle")

	// All five terminal words, `withheld` (stage-7 B-bis) included: a FRESH
	// database's CHECK carries the full vocabulary, so a withheld settlement must
	// round-trip through the real store like any other terminal state.
	states := []model.EvidenceOperationState{
		model.EvidenceOpCompleted, model.EvidenceOpNotSent,
		model.EvidenceOpUnknown, model.EvidenceOpBlocked, model.EvidenceOpWithheld,
	}
	for i, state := range states {
		opID := fmt.Sprintf("op-settle-%d", i)
		if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim(opID, "digest-a")); err != nil {
			t.Fatalf("claim %s: %v", opID, err)
		}
		out, err := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
			OperationID: opID, EffectDigest: "digest-a", State: state,
			ResultDigest: "res-1", DispatchRef: "dispatch-1",
			Actor: "user:test", ActorKind: model.ActorUser,
		})
		if err != nil {
			t.Fatalf("settle %s -> %s: %v", opID, state, err)
		}
		if !out.Fresh {
			t.Fatalf("settle %s reported Fresh=false", opID)
		}
		if out.Receipt.EvidenceRef == "" || out.Receipt.Fault != sdk.EvidenceFaultNone {
			t.Fatalf("settle %s receipt = %+v", opID, out.Receipt)
		}
		row, gerr := getEvidenceOp(t, st, tenant, opID)
		if gerr != nil {
			t.Fatalf("get settled %s: %v", opID, gerr)
		}
		if row.State != state || row.OutcomeEvidenceRef != out.Receipt.EvidenceRef ||
			row.ResultDigest != "res-1" || row.DispatchRef != "dispatch-1" {
			t.Fatalf("settled row %s = %+v", opID, row)
		}
	}
	// One outcome evidence event per settled operation, atomically with the update.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != len(states) {
		t.Fatalf("settle events = %d, want %d", n, len(states))
	}
	mustVerifyChain(t, st, tenant)
}

func TestEvidenceSettleMissingRowIsIntegrityError(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-settle-missing")

	_, err := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: "op-never-claimed", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, Actor: "user:test", ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("settle missing row err = %v, want ErrEvidenceIntegrity", err)
	}
	// Nothing was appended for the phantom settle.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != 0 {
		t.Fatalf("phantom settle appended %d events", n)
	}
}

func TestEvidenceSettleIdempotentAndConflicting(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-resettle")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-resettle-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	settle := store.EvidenceSettlement{
		OperationID: "op-resettle-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1", DispatchRef: "dispatch-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	first, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil {
		t.Fatalf("first settle: %v", err)
	}
	events := countAuditAction(t, st, tenant, "mcp.tool.call.settle")

	// Same outcome digest ⇒ idempotent: recorded state, NO second event.
	again, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil {
		t.Fatalf("idempotent re-settle: %v", err)
	}
	if again.Fresh {
		t.Fatalf("idempotent re-settle reported Fresh=true")
	}
	if again.Receipt.EvidenceRef != first.Receipt.EvidenceRef {
		t.Fatalf("idempotent re-settle ref %q != recorded %q", again.Receipt.EvidenceRef, first.Receipt.EvidenceRef)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != events {
		t.Fatalf("idempotent re-settle appended evidence: %d -> %d", events, n)
	}

	// A different outcome (state or result digest) is an integrity error.
	diffState := settle
	diffState.State = model.EvidenceOpUnknown
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, diffState); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("different-state re-settle err = %v, want ErrEvidenceIntegrity", err)
	}
	diffResult := settle
	diffResult.ResultDigest = "res-2"
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, diffResult); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("different-result re-settle err = %v, want ErrEvidenceIntegrity", err)
	}
	// The recorded outcome never moved.
	row, gerr := getEvidenceOp(t, st, tenant, "op-resettle-1")
	if gerr != nil || row.State != model.EvidenceOpCompleted || row.ResultDigest != "res-1" {
		t.Fatalf("conflicting re-settle mutated the row: %+v err=%v", row, gerr)
	}
}

func TestEvidenceSettleWrongDigestIsRebind(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-settle-rebind")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-sr-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, err := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: "op-sr-1", EffectDigest: "digest-OTHER",
		State: model.EvidenceOpCompleted, Actor: "user:test", ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrEvidenceRebind) {
		t.Fatalf("wrong-digest settle err = %v, want ErrEvidenceRebind", err)
	}
}

func TestEvidenceSettleDegradeDropLeavesClaimed(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "ev-settle-degrade.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "ev-settle-degrade")
	if _, err := store.ClaimEvidenceOperation(ctx, initial, tenant, testClaim("op-sd-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})

	gapBefore, _ := readPendingAuditGap(t, st, tenant)
	out, err := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: "op-sd-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("degrade settle: %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultSpoolDegraded {
		t.Fatalf("degrade settle fault = %q, want spool_degraded", out.Receipt.Fault)
	}
	// The settlement did NOT land: the row stays 'claimed' (ambiguous but safe)…
	row, gerr := getEvidenceOp(t, st, tenant, "op-sd-1")
	if gerr != nil || row.State != model.EvidenceOpClaimed {
		t.Fatalf("degrade settle mutated the row: %+v err=%v", row, gerr)
	}
	// …while the loss accounting committed.
	gapAfter, ok := readPendingAuditGap(t, st, tenant)
	if !ok || gapAfter.dropped != gapBefore.dropped+1 {
		t.Fatalf("pending drops did not advance: before=%+v after=%+v", gapBefore, gapAfter)
	}
}

func TestEvidenceCrashShapeUnsettledClaimNeverRedispatches(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-crash")

	spec := testClaim("op-crash-1", "digest-a")
	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Crash between claim-commit and settle: a later exact replay returns the
	// recorded 'claimed' state with Fresh=false — the caller must treat it as
	// non-replayable and MUST NOT re-dispatch (only a Fresh claim dispatches).
	replay, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil {
		t.Fatalf("post-crash replay: %v", err)
	}
	if replay.Fresh {
		t.Fatalf("post-crash replay reported Fresh=true (double-dispatch signal)")
	}
	if replay.Op.State != model.EvidenceOpClaimed {
		t.Fatalf("post-crash replay state = %q, want claimed", replay.Op.State)
	}
}

func TestEvidenceConcurrentDuplicateClaims(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-race")

	const workers = 8
	spec := testClaim("op-race-1", "digest-a")
	outs := make([]store.EvidenceClaimOutcome, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = store.ClaimEvidenceOperation(ctx, st, tenant, spec)
		}(i)
	}
	wg.Wait()

	var fresh int
	var winnerRef string
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent claim %d: %v", i, errs[i])
		}
		if outs[i].Receipt.MustRefuse(outs[i].Binding) {
			t.Fatalf("concurrent claim %d refused: %+v", i, outs[i].Receipt)
		}
		if outs[i].Fresh {
			fresh++
			winnerRef = outs[i].Op.ClaimEvidenceRef
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh winners = %d, want exactly 1", fresh)
	}
	// Every loser observed the winner's committed state and evidence ref.
	for i := 0; i < workers; i++ {
		if outs[i].Op.ClaimEvidenceRef != winnerRef || outs[i].Op.State != model.EvidenceOpClaimed {
			t.Fatalf("claim %d did not observe the winner: %+v", i, outs[i].Op)
		}
	}
	// Exactly ONE claim evidence event across all racers.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 1 {
		t.Fatalf("claim events after race = %d, want 1", n)
	}
	mustVerifyChain(t, st, tenant)
}

// evidenceProbeAction is the uniquely identifiable action the fault-injection
// fakes append INSIDE the transaction before failing, so a test can prove the
// append rolled back with the transaction (review P1-4: an assertion on
// a transaction that never appended is vacuous).
const evidenceProbeAction = "evtest.probe.claim"

// appendEvidenceProbe appends the probe event through the REAL scope's audit
// log — the same in-transaction append a real losing claim performs before its
// insert hits the unique conflict.
func appendEvidenceProbe(ctx context.Context, t *testing.T, sc store.Scope, c store.EvidenceClaim) {
	t.Helper()
	ev, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: c.Actor, ActorKind: c.ActorKind, Action: evidenceProbeAction,
		TargetKind: model.Kind("core.evidence_operation"), TargetID: model.ID(c.OperationID),
	})
	if err != nil || ev.Seq == 0 {
		t.Fatalf("probe append: seq=%d err=%v", ev.Seq, err)
	}
}

// racedOnceStore reproduces the LOSER shape of a concurrent unique-conflict
// claim faithfully: its first in-transaction Claim APPENDS a probe evidence
// event (as the real claim appends before its insert) and THEN fails with
// ErrEvidenceRaced — the mapping of the UNIQUE(tenant_id, operation_id) insert
// conflict. Mutate must roll the whole losing transaction back, probe included;
// the driver then re-runs and finds the committed winner (replay, Fresh=false).
type racedOnceStore struct {
	store.Store
	t     *testing.T
	raced bool
}

func (s *racedOnceStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&racedOnceScope{Scope: sc, st: s})
	})
}

type racedOnceScope struct {
	store.Scope
	st *racedOnceStore
}

func (sc *racedOnceScope) EvidenceOperations() store.EvidenceOperationRepo {
	return &racedOnceRepo{EvidenceOperationRepo: sc.Scope.EvidenceOperations(), sc: sc.Scope, st: sc.st}
}

type racedOnceRepo struct {
	store.EvidenceOperationRepo
	sc store.Scope
	st *racedOnceStore
}

func (r *racedOnceRepo) Claim(ctx context.Context, c store.EvidenceClaim) (store.EvidenceClaimResult, error) {
	if !r.st.raced {
		r.st.raced = true
		appendEvidenceProbe(ctx, r.st.t, r.sc, c)
		return store.EvidenceClaimResult{}, store.ErrEvidenceRaced
	}
	return r.EvidenceOperationRepo.Claim(ctx, c)
}

func TestEvidenceClaimRacedLoserRollsBackAppendAndRereadsWinner(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-raced-retry")

	// CONTROL: the probe is observable when its transaction commits — so the
	// zero-count assertion below is not vacuous.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		appendEvidenceProbe(ctx, t, sc, testClaim("op-probe-control", "digest-a"))
		return nil
	}); err != nil {
		t.Fatalf("control probe: %v", err)
	}
	if n := countAuditAction(t, st, tenant, evidenceProbeAction); n != 1 {
		t.Fatalf("control probe events = %d, want 1", n)
	}

	// The winner commits first.
	spec := testClaim("op-raced-1", "digest-a")
	winner, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil || !winner.Fresh {
		t.Fatalf("winner claim: %+v err=%v", winner, err)
	}
	// EXPERIMENT: the loser appends its probe, hits the conflict, rolls back,
	// re-reads the winner on the driver's retry.
	raced := &racedOnceStore{Store: st, t: t}
	loser, err := store.ClaimEvidenceOperation(ctx, raced, tenant, spec)
	if err != nil {
		t.Fatalf("raced loser claim: %v", err)
	}
	if !raced.raced {
		t.Fatalf("the raced path was never traversed")
	}
	if loser.Fresh {
		t.Fatalf("raced loser reported Fresh=true")
	}
	if loser.Op.ClaimEvidenceRef != winner.Op.ClaimEvidenceRef {
		t.Fatalf("raced loser did not observe the winner: %+v", loser.Op)
	}
	// The loser's in-transaction append is GONE (still exactly the control's 1),
	// and the winner's claim event is still the only claim event.
	if n := countAuditAction(t, st, tenant, evidenceProbeAction); n != 1 {
		t.Fatalf("probe events = %d, want 1 (the loser's append must roll back)", n)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 1 {
		t.Fatalf("claim events = %d, want 1", n)
	}
	mustVerifyChain(t, st, tenant)
}

// blockEvidenceDML installs a RAISE(ABORT) trigger on evidence_operations so
// the REAL repo's row insert/update fails inside the real driver transaction
// (round-3 item 4: no fabricated errors — the DML failure is the
// database's own). Returns a remover.
func blockEvidenceDML(t *testing.T, st store.Store, when string) func() {
	t.Helper()
	db := st.(*sqlStore).db
	name := "evtest_block_" + when
	stmt := fmt.Sprintf(
		"CREATE TRIGGER %s BEFORE %s ON evidence_operations BEGIN SELECT RAISE(ABORT,'evtest %s blocked'); END",
		name, when, when)
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("install %s trigger: %v", name, err)
	}
	return func() {
		if _, err := db.Exec("DROP TRIGGER " + name); err != nil {
			t.Fatalf("drop %s trigger: %v", name, err)
		}
	}
}

func TestEvidenceClaimRealInsertFailureRollsBackAppend(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-claimfail")

	// REAL DML failure: the repo's own row INSERT aborts after its evidence
	// append succeeded in the same transaction.
	remove := blockEvidenceDML(t, st, "INSERT")
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-cf-1", "digest-a"))
	if err != nil {
		t.Fatalf("blocked claim err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultWriteError {
		t.Fatalf("blocked claim fault = %q, want write_error", out.Receipt.Fault)
	}
	// The REAL claim evidence append rolled back with the failed insert.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("claim events = %d, want 0 (append must roll back with the failed insert)", n)
	}
	if _, gerr := getEvidenceOp(t, st, tenant, "op-cf-1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("blocked claim left a row: err=%v", gerr)
	}
	// CONTROL: with the trigger removed the same claim succeeds — the blocked
	// run failed because of the real DML abort, nothing else.
	remove()
	ok, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-cf-1", "digest-a"))
	if err != nil || !ok.Fresh || ok.Receipt.MustRefuse(ok.Binding) {
		t.Fatalf("control claim after trigger removal: %+v err=%v", ok, err)
	}
	mustVerifyChain(t, st, tenant)
}

func TestEvidenceSettleRealUpdateFailureRollsBackAppend(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-settlefail")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-sf-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	settle := store.EvidenceSettlement{
		OperationID: "op-sf-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	// REAL DML failure: the repo's own row UPDATE aborts after the outcome
	// evidence append succeeded in the same transaction.
	remove := blockEvidenceDML(t, st, "UPDATE")
	out, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil {
		t.Fatalf("blocked settle err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultWriteError {
		t.Fatalf("blocked settle fault = %q, want write_error", out.Receipt.Fault)
	}
	// The REAL outcome append rolled back with the failed update; the row is
	// untouched (still claimed, version 1).
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != 0 {
		t.Fatalf("settle events = %d, want 0 (append must roll back with the failed update)", n)
	}
	row, gerr := getEvidenceOp(t, st, tenant, "op-sf-1")
	if gerr != nil || row.State != model.EvidenceOpClaimed || row.Version != 1 {
		t.Fatalf("blocked settle mutated the row: %+v err=%v", row, gerr)
	}
	// CONTROL: with the trigger removed the same settle succeeds.
	remove()
	okOut, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil || !okOut.Fresh {
		t.Fatalf("control settle after trigger removal: %+v err=%v", okOut, err)
	}
	mustVerifyChain(t, st, tenant)
}

// flipFencer passes the stamping fence and then reports a MOVED cluster epoch
// on the pre-commit recheck — the deterministic shape of losing leadership
// between the claim's epoch stamp and its commit (round-3 item 1).
type flipFencer struct {
	toggleElector
	calls         int
	first, second uint64
}

func (f *flipFencer) Epoch() uint64 { return f.first }

func (f *flipFencer) FencedEpoch(context.Context) (uint64, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return f.second, nil
}

func TestEvidenceClaimFenceFailsInsideTransaction(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-fence-intx")
	flip := &flipFencer{toggleElector: toggleElector{activeVal: true}, first: 7, second: 8}
	st.(*sqlStore).elector = flip

	// The stamp fence sees epoch 7; the pre-commit recheck (inside the Mutate
	// callback, after the writes) sees 8 ⇒ the WHOLE transaction — evidence
	// append and row insert — must roll back and classify ledger_unavailable.
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-fx-1", "digest-a"))
	if err != nil {
		t.Fatalf("mid-transaction fence claim err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnavailable {
		t.Fatalf("mid-transaction fence fault = %q, want ledger_unavailable", out.Receipt.Fault)
	}
	if flip.calls < 2 {
		t.Fatalf("fence consulted %d time(s); the pre-commit recheck never ran", flip.calls)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("claim events = %d, want 0 (staged append must roll back on fence failure)", n)
	}
	if _, gerr := getEvidenceOp(t, st, tenant, "op-fx-1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("fence-failed claim left a row: err=%v", gerr)
	}
	mustVerifyChain(t, st, tenant)
}

func TestEvidenceSettleFenceRefusesNewerEpoch(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-settle-fence")
	el := &epochElector{toggleElector: toggleElector{activeVal: true}, epoch: 7}
	st.(*sqlStore).elector = el

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-sfx-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Legitimate failover happened: the durable epoch is now 8 while the row's
	// claim epoch is 7. The DOCUMENTED deny-closed decision: settlement under a
	// NEWER epoch refuses (ledger_unavailable) — a post-failover node must not
	// silently adopt a pre-failover claim; the row stays 'claimed'.
	el.epoch = 8
	settle := store.EvidenceSettlement{
		OperationID: "op-sfx-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	out, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil {
		t.Fatalf("newer-epoch settle err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnavailable {
		t.Fatalf("newer-epoch settle fault = %q, want ledger_unavailable", out.Receipt.Fault)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != 0 {
		t.Fatalf("settle events = %d, want 0 (outcome append must roll back on fence failure)", n)
	}
	row, gerr := getEvidenceOp(t, st, tenant, "op-sfx-1")
	if gerr != nil || row.State != model.EvidenceOpClaimed || row.LeaderEpoch != 7 {
		t.Fatalf("newer-epoch settle mutated the row: %+v err=%v", row, gerr)
	}
	// Back on the claim's epoch the settle lands (the refusal above was the
	// fence, not some other defect).
	el.epoch = 7
	okOut, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil || !okOut.Fresh {
		t.Fatalf("same-epoch settle: %+v err=%v", okOut, err)
	}
}

// postFreshClaimHookStore runs hook() INSIDE the claim transaction, right after
// the real repo staged the fresh claim's append+insert — the seam where a
// leadership loss "during the transaction" is injected deterministically.
type postFreshClaimHookStore struct {
	store.Store
	hook func()
}

func (s *postFreshClaimHookStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(&postFreshClaimHookScope{Scope: sc, st: s})
	})
}

type postFreshClaimHookScope struct {
	store.Scope
	st *postFreshClaimHookStore
}

func (sc *postFreshClaimHookScope) EvidenceOperations() store.EvidenceOperationRepo {
	return &postFreshClaimHookRepo{EvidenceOperationRepo: sc.Scope.EvidenceOperations(), st: sc.st}
}

type postFreshClaimHookRepo struct {
	store.EvidenceOperationRepo
	st *postFreshClaimHookStore
}

func (r *postFreshClaimHookRepo) Claim(ctx context.Context, c store.EvidenceClaim) (store.EvidenceClaimResult, error) {
	res, err := r.EvidenceOperationRepo.Claim(ctx, c)
	if err == nil && res.Fresh {
		r.st.hook()
	}
	return res, err
}

func TestEvidenceClaimLostLockDuringTransactionPGShape(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-lostlock")

	// A REAL pgElector over the fake lock backend: leader at epoch 1.
	shared := &sharedLock{}
	be := &fakeBackend{shared: shared}
	e := newTestElector(be)
	if err := e.Run(ctx); err != nil {
		t.Fatalf("elector Run: %v", err)
	}
	defer e.Resign(ctx) //nolint:errcheck
	st.(*sqlStore).elector = e

	// Between the stamp fence and the commit — with the append+insert already
	// staged — another node steals the lock and bumps the persisted epoch. The
	// local cache still says leader (no maintenance tick runs), which is EXACTLY
	// the reviewed hole the pre-commit durable fence closes.
	steal := func() {
		other := &fakeBackend{shared: shared}
		shared.mu.Lock()
		shared.heldBy = other
		shared.epoch++
		shared.mu.Unlock()
	}
	out, err := store.ClaimEvidenceOperation(ctx, &postFreshClaimHookStore{Store: st, hook: steal},
		tenant, testClaim("op-ll-1", "digest-a"))
	if err != nil {
		t.Fatalf("lost-lock claim err = %v", err)
	}
	if !e.IsLeader() {
		t.Fatal("precondition: the local cache should still believe it leads")
	}
	if out.Receipt.Fault != sdk.EvidenceFaultLedgerUnavailable {
		t.Fatalf("lost-lock claim fault = %q, want ledger_unavailable", out.Receipt.Fault)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("claim events = %d, want 0 (staged append must roll back)", n)
	}
	if _, gerr := getEvidenceOp(t, st, tenant, "op-ll-1"); !errors.Is(gerr, store.ErrNotFound) {
		t.Fatalf("lost-lock claim left a row: err=%v", gerr)
	}
	mustVerifyChain(t, st, tenant)
}

func TestMutateWrapsAvailabilityErrors(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-mutate-wrap")

	// A raw class-08 error surfacing through the Mutate callback must come back
	// wrapped in ErrStoreUnavailable WITH the original cause preserved.
	class08 := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	err := st.Mutate(ctx, tenant, func(store.Scope) error { return class08 })
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("Mutate callback class-08 err = %v, want ErrStoreUnavailable wrap", err)
	}
	if !errors.Is(err, class08) {
		t.Fatalf("Mutate wrap lost the original cause: %v", err)
	}
	// Non-availability callback errors pass through untouched.
	boom := errors.New("boom")
	if err := st.Mutate(ctx, tenant, func(store.Scope) error { return boom }); !errors.Is(err, boom) || errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("Mutate non-availability err = %v, want untouched pass-through", err)
	}
}

func TestEvidenceClaimValidation(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-validate")

	// Incomplete bindings fail closed: the driver refuses with a write_error
	// fault rather than journaling a claim it cannot bind.
	bad := testClaim("", "digest-a")
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, bad)
	if err != nil {
		t.Fatalf("empty-op claim err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultWriteError {
		t.Fatalf("empty-op claim fault = %q, want write_error", out.Receipt.Fault)
	}
	// A settle to a non-terminal state is refused outright.
	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-val-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, serr := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: "op-val-1", EffectDigest: "digest-a",
		State: model.EvidenceOpClaimed, Actor: "user:test", ActorKind: model.ActorUser,
	})
	if serr == nil {
		t.Fatalf("settle to 'claimed' accepted")
	}
	// Writes through a View scope are rejected.
	verr := st.View(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.EvidenceOperations().Claim(ctx, testClaim("op-view-1", "digest-a"))
		return e
	})
	if !errors.Is(verr, store.ErrReadOnly) {
		t.Fatalf("View-scope claim err = %v, want ErrReadOnly", verr)
	}
}

// corruptEvidenceOpsRow mutates the journal through raw SQL, emulating an
// out-of-band corruption (or a legacy row) the typed write path can never
// produce. It clears the SQLite scope pin first so the tripwire triggers treat
// the write as the System path.
func corruptEvidenceOpsRow(t *testing.T, st store.Store, query string, args ...any) error {
	t.Helper()
	db := st.(*sqlStore).db
	if _, err := db.Exec("DELETE FROM " + dialect.ScopeTenantTable); err != nil {
		t.Fatalf("clear scope pin: %v", err)
	}
	_, err := db.Exec(query, args...)
	return err
}

func TestEvidenceSettleDispatchRefDivergenceIsIntegrityError(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-dispatchref")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-dr-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	settle := store.EvidenceSettlement{
		OperationID: "op-dr-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1", DispatchRef: "dispatch-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, settle); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	events := countAuditAction(t, st, tenant, "mcp.tool.call.settle")

	// Same state + result digest but a DIFFERENT upstream dispatch id is NOT the
	// same settlement: it is the signature of a double dispatch and must refuse.
	divergent := settle
	divergent.DispatchRef = "dispatch-2"
	_, err := store.SettleEvidenceOperation(ctx, st, tenant, divergent)
	if !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("divergent dispatch-ref re-settle err = %v, want ErrEvidenceIntegrity", err)
	}
	// No new evidence, no row mutation.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.settle"); n != events {
		t.Fatalf("divergent re-settle appended evidence: %d -> %d", events, n)
	}
	row, gerr := getEvidenceOp(t, st, tenant, "op-dr-1")
	if gerr != nil || row.DispatchRef != "dispatch-1" {
		t.Fatalf("divergent re-settle mutated the row: %+v err=%v", row, gerr)
	}
	// The exact all-field replay stays idempotent.
	again, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil || again.Fresh {
		t.Fatalf("exact replay after divergence attempt: %+v err=%v", again, err)
	}
}

func TestEvidenceSettleCorruptOutcomeRefRefuses(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-corrupt-ref")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-cr-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	settle := store.EvidenceSettlement{
		OperationID: "op-cr-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1", DispatchRef: "dispatch-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, settle); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// Wipe the recorded outcome anchor out of band: the row now claims a terminal
	// settlement with no evidence behind it.
	if err := corruptEvidenceOpsRow(t, st,
		"UPDATE evidence_operations SET outcome_evidence_ref = '' WHERE operation_id = ?", "op-cr-1"); err != nil {
		t.Fatalf("corrupt outcome ref: %v", err)
	}
	// Neither the read nor an idempotent-looking re-settle may treat the corrupt
	// row as a settled operation.
	if _, err := getEvidenceOp(t, st, tenant, "op-cr-1"); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("get corrupt row err = %v, want ErrEvidenceIntegrity", err)
	}
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, settle); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("re-settle over corrupt row err = %v, want ErrEvidenceIntegrity", err)
	}
}

func TestEvidenceLifecycleDecodeInvariants(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-lifecycle")

	// A 'claimed' row must carry NO outcome ref.
	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-lc-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := corruptEvidenceOpsRow(t, st,
		"UPDATE evidence_operations SET outcome_evidence_ref = 'deadbeef' WHERE operation_id = ?", "op-lc-1"); err != nil {
		t.Fatalf("corrupt claimed row: %v", err)
	}
	if _, err := getEvidenceOp(t, st, tenant, "op-lc-1"); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("claimed-with-outcome-ref err = %v, want ErrEvidenceIntegrity", err)
	}
}

func TestEvidenceStateCheckConstraint(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-check")

	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-ck-1", "digest-a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// The five-value CHECK constraint is the database-level backstop: even a raw
	// out-of-band write cannot plant an unknown lifecycle state.
	err := corruptEvidenceOpsRow(t, st,
		"UPDATE evidence_operations SET state = 'bogus' WHERE operation_id = ?", "op-ck-1")
	if err == nil {
		t.Fatalf("raw UPDATE to an unknown state was accepted (no CHECK constraint)")
	}
}

func TestEvidenceDecodeRejectsInvalidState(t *testing.T) {
	// Defense in depth below the CHECK constraint (a pre-CHECK legacy table
	// would decode through this same codec): an unknown state never decodes.
	rec := model.Record{
		model.ColID: model.NewID().String(), model.ColTenantID: "t",
		model.ColCreatedAt: model.NewTimestamp(time.Now()).String(),
		model.ColUpdatedAt: model.NewTimestamp(time.Now()).String(),
		model.ColVersion:   int64(1),
		"operation_id":     "op-x", "effect_digest": "d", "surface": "s", "action": "a",
		"state": "bogus", "claim_evidence_ref": "ref", "leader_epoch": int64(1),
	}
	if _, err := decodeEvidenceOp(rec); !errors.Is(err, store.ErrEvidenceIntegrity) {
		t.Fatalf("decode of invalid state err = %v, want ErrEvidenceIntegrity", err)
	}
}

func TestEvidenceWhitespaceBindingNeverTouchesTheStore(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-whitespace")

	// Whitespace-only binding fields are invalid under the sdk TrimSpace rule:
	// the claim must refuse write_error WITHOUT any store I/O — no evidence
	// event, no journal row (a naive == "" check let " " append + insert +
	// commit before classifying).
	out, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("   ", "digest-a"))
	if err != nil {
		t.Fatalf("whitespace-op claim err = %v", err)
	}
	if out.Receipt.Fault != sdk.EvidenceFaultWriteError {
		t.Fatalf("whitespace-op claim fault = %q, want write_error", out.Receipt.Fault)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("whitespace-op claim appended %d evidence events, want 0", n)
	}
	var rows int64
	if err := st.(*sqlStore).db.QueryRow(
		"SELECT COUNT(*) FROM evidence_operations WHERE tenant_id = ?", tenant.String()).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("whitespace-op claim created %d rows, want 0", rows)
	}

	// Whitespace-only metadata (surface/action/actor) is a caller bug: a loud
	// ErrEvidenceInvalid, again without store I/O.
	badSurface := testClaim("op-ws-1", "digest-a")
	badSurface.Surface = "   "
	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, badSurface); !errors.Is(err, store.ErrEvidenceInvalid) {
		t.Fatalf("whitespace-surface claim err = %v, want ErrEvidenceInvalid", err)
	}
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 0 {
		t.Fatalf("whitespace-surface claim appended evidence")
	}
	// Whitespace-only settlement fields are equally invalid.
	_, serr := store.SettleEvidenceOperation(ctx, st, tenant, store.EvidenceSettlement{
		OperationID: " ", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, Actor: "user:test", ActorKind: model.ActorUser,
	})
	if !errors.Is(serr, store.ErrEvidenceInvalid) {
		t.Fatalf("whitespace settlement err = %v, want ErrEvidenceInvalid", serr)
	}
}

func TestEvidenceSettleValidationPrecedesAvailability(t *testing.T) {
	ctx := context.Background()
	invalid := store.EvidenceSettlement{
		OperationID: "op-va-1", EffectDigest: "digest-a",
		State: model.EvidenceOpClaimed, // non-terminal: always a caller bug
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	// A nil store must NOT mask the caller bug as ledger_unwired.
	if _, err := store.SettleEvidenceOperation(ctx, nil, model.TenantID("t"), invalid); !errors.Is(err, store.ErrEvidenceInvalid) {
		t.Fatalf("nil-store invalid settlement err = %v, want ErrEvidenceInvalid", err)
	}
	// A standby must NOT mask it as ledger_unavailable either.
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "ev-va")
	st.(*sqlStore).elector = &toggleElector{activeVal: false}
	if _, err := store.SettleEvidenceOperation(ctx, st, tenant, invalid); !errors.Is(err, store.ErrEvidenceInvalid) {
		t.Fatalf("standby invalid settlement err = %v, want ErrEvidenceInvalid", err)
	}
}

func TestEvidenceFaultForStoreError(t *testing.T) {
	cases := []struct {
		err  error
		want sdk.EvidenceFault
	}{
		{nil, sdk.EvidenceFaultNone},
		{store.ErrAuditSpoolFull, sdk.EvidenceFaultSpoolFull},
		{fmt.Errorf("wrap: %w", store.ErrAuditSpoolFull), sdk.EvidenceFaultSpoolFull},
		{store.ErrNotLeader, sdk.EvidenceFaultLedgerUnavailable},
		{fmt.Errorf("wrap: %w", store.ErrNotLeader), sdk.EvidenceFaultLedgerUnavailable},
		// An unreachable backend is ledger_unavailable per the frozen SDK
		// taxonomy (sdk/evidence.go:120), never write_error.
		{store.ErrStoreUnavailable, sdk.EvidenceFaultLedgerUnavailable},
		{fmt.Errorf("wrap: %w", store.ErrStoreUnavailable), sdk.EvidenceFaultLedgerUnavailable},
		{errors.New("boom"), sdk.EvidenceFaultWriteError},
	}
	for _, c := range cases {
		if got := store.EvidenceFaultForStoreError(c.err); got != c.want {
			t.Errorf("EvidenceFaultForStoreError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestBackendUnavailableClassification(t *testing.T) {
	// Class-08 SQLSTATE (Connection Exception) and driver-level bad connections
	// wrap ErrStoreUnavailable at the SQL boundary; constraint/ordinary failures
	// do not.
	class08 := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	unavailable := []error{
		class08,
		fmt.Errorf("exec: %w", class08),
		driver.ErrBadConn,
		fmt.Errorf("exec: %w", driver.ErrBadConn),
		&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
	}
	for _, e := range unavailable {
		if !errors.Is(mapWriteErr(e), store.ErrStoreUnavailable) {
			t.Errorf("mapWriteErr(%v) did not wrap ErrStoreUnavailable", e)
		}
		if !errors.Is(wrapUnavailableErr(e), store.ErrStoreUnavailable) {
			t.Errorf("wrapUnavailableErr(%v) did not wrap ErrStoreUnavailable", e)
		}
		// CAUSE-PRESERVING wrap (round-3 item 3): the sentinel wrap must
		// keep the ORIGINAL error in the chain — existing consumers match the
		// underlying sentinels with errors.Is.
		if !errors.Is(mapWriteErr(e), e) {
			t.Errorf("mapWriteErr(%v) destroyed the original error chain", e)
		}
		if !errors.Is(wrapUnavailableErr(e), e) {
			t.Errorf("wrapUnavailableErr(%v) destroyed the original error chain", e)
		}
	}
	// The canonical chains consumers depend on stay matchable through the wrap.
	if got := mapWriteErr(fmt.Errorf("exec: %w", driver.ErrBadConn)); !errors.Is(got, driver.ErrBadConn) {
		t.Errorf("driver.ErrBadConn no longer matchable through the wrap: %v", got)
	}
	if got := wrapUnavailableErr(fmt.Errorf("query: %w", context.DeadlineExceeded)); !errors.Is(got, context.DeadlineExceeded) || !errors.Is(got, store.ErrStoreUnavailable) {
		t.Errorf("context.DeadlineExceeded chain broken through the wrap: %v", got)
	}
	notUnavailable := []error{
		&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"},
		&pgconn.PgError{Code: "40001", Message: "serialization failure"},
		errors.New("UNIQUE constraint failed: evidence_operations.operation_id"),
		errors.New("boom"),
	}
	for _, e := range notUnavailable {
		if errors.Is(mapWriteErr(e), store.ErrStoreUnavailable) {
			t.Errorf("mapWriteErr(%v) wrongly classified as unavailable", e)
		}
	}
	// The unique-violation mapping is untouched.
	if !errors.Is(mapWriteErr(errors.New("UNIQUE constraint failed: x")), store.ErrConflict) {
		t.Errorf("unique violation no longer maps to ErrConflict")
	}
	// End-to-end through the /core mapping: a wrapped class-08 becomes
	// ledger_unavailable.
	if got := store.EvidenceFaultForStoreError(mapWriteErr(class08)); got != sdk.EvidenceFaultLedgerUnavailable {
		t.Errorf("class-08 end-to-end fault = %q, want ledger_unavailable", got)
	}
}

// TestPostgresEvidenceOperations exercises the journal against a real Postgres
// (both-engines coverage, including the REAL unique-conflict raced path that
// SQLite's single-writer serialization cannot produce). It runs on its OWN
// database — it arms the elector, so the single olivares.leader.v1
// advisory lock and the leader_epoch arithmetic it asserts must be its alone.
func TestPostgresEvidenceOperations(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 4}, nil)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer st.Close()
	tenant := provisionTenant(t, st, "pg-ev-"+uniqueSuffix())

	// ARM the elector (round-3 item 5): without Run the elector is unarmed
	// and FencedEpoch returns the local zero epoch — the DURABLE fence path
	// (held lock session + persisted leader_epoch row) would never execute
	// against real Postgres.
	if err := st.Leader().Run(ctx); err != nil {
		t.Fatalf("elector Run: %v", err)
	}
	if !st.Leader().IsLeader() {
		t.Fatalf("test node did not acquire leadership (is another process holding %s?)", leaderLockName)
	}
	fencer := st.Leader().(store.EpochFencer)
	baseEpoch, err := fencer.FencedEpoch(ctx)
	if err != nil || baseEpoch == 0 {
		t.Fatalf("durable fence after Run = (%d, %v), want a positive persisted epoch", baseEpoch, err)
	}

	// Lifecycle: fresh claim → replay → settle → idempotent re-settle.
	spec := testClaim("op-pg-1", "digest-a")
	fresh, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil || !fresh.Fresh || fresh.Receipt.MustRefuse(fresh.Binding) {
		t.Fatalf("pg fresh claim: %+v err=%v", fresh, err)
	}
	// The claim was stamped from the DURABLE fence, not a local cache.
	if fresh.Op.LeaderEpoch != baseEpoch {
		t.Fatalf("pg claim leader epoch = %d, want the durable %d", fresh.Op.LeaderEpoch, baseEpoch)
	}
	if ferr := store.EvidenceEpochFence(ctx, st.Leader(), fresh.Op.LeaderEpoch); ferr != nil {
		t.Fatalf("pre-handoff EvidenceEpochFence: %v", ferr)
	}
	replay, err := store.ClaimEvidenceOperation(ctx, st, tenant, spec)
	if err != nil || replay.Fresh || replay.Receipt.EvidenceRef != fresh.Receipt.EvidenceRef {
		t.Fatalf("pg replay claim: %+v err=%v", replay, err)
	}
	if _, err := store.ClaimEvidenceOperation(ctx, st, tenant, testClaim("op-pg-1", "digest-B")); !errors.Is(err, store.ErrEvidenceRebind) {
		t.Fatalf("pg rebind err = %v", err)
	}
	settle := store.EvidenceSettlement{
		OperationID: "op-pg-1", EffectDigest: "digest-a",
		State: model.EvidenceOpCompleted, ResultDigest: "res-1", DispatchRef: "dispatch-1",
		Actor: "user:test", ActorKind: model.ActorUser,
	}
	settled, err := store.SettleEvidenceOperation(ctx, st, tenant, settle)
	if err != nil || !settled.Fresh {
		t.Fatalf("pg settle: %+v err=%v", settled, err)
	}
	if again, err := store.SettleEvidenceOperation(ctx, st, tenant, settle); err != nil || again.Fresh {
		t.Fatalf("pg idempotent re-settle: %+v err=%v", again, err)
	}

	// Real concurrency on a multi-connection pool, INSTRUMENTED (review
	// P1-4): the racedCountingStore counts every losing Mutate that surfaced
	// ErrEvidenceRaced, so this test PROVES the real unique-conflict retry was
	// traversed at least once, instead of merely tolerating either path. On
	// Postgres a loser whose read-miss precedes the winner's commit is
	// guaranteed onto this path: its Append serializes on the per-tenant
	// advisory xact lock until the winner commits, and its insert then hits the
	// committed unique index. Rounds with fresh operation ids bound the
	// residual scheduling luck.
	counting := &racedCountingStore{Store: st}
	const workers = 6
	const maxRounds = 20
	rounds := 0
	for r := 0; r < maxRounds && counting.raced.Load() == 0; r++ {
		rounds++
		race := testClaim(fmt.Sprintf("op-pg-race-%d", r), "digest-a")
		outs := make([]store.EvidenceClaimOutcome, workers)
		errs := make([]error, workers)
		var start sync.WaitGroup
		start.Add(1)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				start.Wait() // barrier: maximize read-miss overlap
				outs[i], errs[i] = store.ClaimEvidenceOperation(ctx, counting, tenant, race)
			}(i)
		}
		start.Done()
		wg.Wait()
		var freshCount int
		for i := 0; i < workers; i++ {
			if errs[i] != nil {
				t.Fatalf("pg concurrent claim %d (round %d): %v", i, r, errs[i])
			}
			if outs[i].Receipt.MustRefuse(outs[i].Binding) {
				t.Fatalf("pg concurrent claim %d (round %d) refused: %+v", i, r, outs[i].Receipt)
			}
			if outs[i].Fresh {
				freshCount++
			}
		}
		if freshCount != 1 {
			t.Fatalf("pg round %d fresh winners = %d, want 1", r, freshCount)
		}
	}
	if counting.raced.Load() == 0 {
		t.Fatalf("no loser traversed the real unique-conflict raced path in %d rounds", rounds)
	}
	// One claim event per operation: op-pg-1 + one per race round — the losing
	// appends (raced or replayed) all rolled back or never happened.
	if n := countAuditAction(t, st, tenant, "mcp.tool.call.claim"); n != 1+rounds {
		t.Fatalf("pg claim events = %d, want %d", n, 1+rounds)
	}
	mustVerifyChain(t, st, tenant)

	// HANDOFF (round-3 item 5): a second elector takes over after this
	// node resigns — the persisted epoch bumps and the DURABLE fence must flip
	// from passing to refusing on both sides. Runs LAST: after Resign this
	// store's write gate is closed.
	second, err := newPGElector(store.Config{Engine: store.EnginePostgres, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("second elector: %v", err)
	}
	defer second.Resign(ctx) //nolint:errcheck
	if err := second.Run(ctx); err != nil {
		t.Fatalf("second elector Run: %v", err)
	}
	if second.IsLeader() {
		t.Fatalf("second elector acquired leadership while the first still holds the lock")
	}
	if err := st.Leader().Resign(ctx); err != nil {
		t.Fatalf("first elector Resign: %v", err)
	}
	second.tick(ctx) // deterministic takeover instead of waiting for the poll
	if !second.IsLeader() {
		t.Fatalf("second elector did not take over after resign")
	}
	handoffEpoch, err := second.FencedEpoch(ctx)
	if err != nil {
		t.Fatalf("second elector durable fence: %v", err)
	}
	if handoffEpoch != baseEpoch+1 {
		t.Fatalf("post-handoff persisted epoch = %d, want %d", handoffEpoch, baseEpoch+1)
	}
	// The resigned first node refuses its own fence, and a claim stamped under
	// the old epoch no longer passes the BeforeEffect fence on EITHER node.
	if _, err := fencer.FencedEpoch(ctx); err == nil {
		t.Fatalf("resigned first elector still passes its durable fence")
	}
	if ferr := store.EvidenceEpochFence(ctx, second, fresh.Op.LeaderEpoch); ferr == nil {
		t.Fatalf("old-epoch claim passed the fence on the new leader")
	}
}

// racedCountingStore counts Mutate calls that lost the unique-conflict race
// (ErrEvidenceRaced) before the driver's retry, WITHOUT altering behavior.
type racedCountingStore struct {
	store.Store
	raced atomic.Int64
}

func (s *racedCountingStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := s.Store.Mutate(ctx, tenant, fn)
	if errors.Is(err, store.ErrEvidenceRaced) {
		s.raced.Add(1)
	}
	return err
}
