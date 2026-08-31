// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// mcpgateway_evidence_test.go — Stage 3 composition-root proofs of the
// enforced tools/call evidence seam (mcpGateAuditor over the REAL durable
// journal): claim/replay/rebind/concurrency against a real store, degrade Seq==0
// loss accounting, tenant-resolution strictness, fence + settlement behavior.
//
// RED note: these tests are seam-dependent — the pre auditor had a void
// Record(ctx, dec) and no journal, so they could not compile against the
// fail-open code; their RED is the syntactic impossibility (the behavioral RED
// for the same exploits was captured connector-level in
// connectors/mcp/evidence_test.go — see sessions-q1-mcp-evidence.md).

func mcpEvidenceAuditor(f *mcpLedgerFixture) mcpGateAuditor {
	return mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), store: f.store, tenant: f.tenant,
	}
}

func mcpEvidenceBinding(op, digest string) sdk.EvidenceBinding {
	return sdk.EvidenceBinding{OperationID: sdk.OperationID(op), EffectDigest: sdk.EffectDigest(digest)}
}

func mcpAllowDecision(f *mcpLedgerFixture, idKind string) mcpc.ToolDecision {
	return mcpc.ToolDecision{
		Tenant: f.tenant.String(), Subject: "agent:mcp-evidence", Tool: "deploy",
		RequiredScope: "tools:deploy", Allowed: true, Reason: "tools/call authorized",
		MCPTag: "MCP07", TokenBinding: "dpop", OperationIDKind: idKind,
	}
}

func mcpJournalRow(t *testing.T, f *mcpLedgerFixture, opID string) (model.EvidenceOperation, bool) {
	t.Helper()
	var op model.EvidenceOperation
	found := true
	err := f.store.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var gerr error
		op, gerr = sc.EvidenceOperations().Get(context.Background(), opID)
		if gerr == store.ErrNotFound {
			found = false
			return nil
		}
		return gerr
	})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Fatalf("read journal row %s: %v", opID, err)
	}
	return op, found && err == nil
}

// TestMCPEvidenceEnforcedClaimJournalLifecycle drives the full enforced life of
// one keyed operation against the REAL store: fresh claim (paired ledger event
// mcp.tool.call.keyed.claim), fence, settlement (…settle event), and the
// replay-settled record a retry receives — on a verified signed chain.
func TestMCPEvidenceEnforcedClaimJournalLifecycle(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()
	binding := mcpEvidenceBinding("op-lifecycle-1", "digest-a")
	before := mcpLedgerHead(t, f.store, f.tenant)

	rec := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if rec.State != mcpc.GateRecordFresh || !rec.MayEmit(binding) {
		t.Fatalf("fresh claim record = %+v, want fresh+emittable", rec)
	}
	if rec.FenceToken != "1" {
		t.Errorf("fence token = %q, want the single-node durable epoch \"1\"", rec.FenceToken)
	}
	events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 1 || events[0].Action != "mcp.tool.call.keyed.claim" {
		t.Fatalf("claim events = %+v, want exactly one mcp.tool.call.keyed.claim", events)
	}
	row, ok := mcpJournalRow(t, f, "op-lifecycle-1")
	if !ok || row.State != model.EvidenceOpClaimed || row.LeaderEpoch != 1 {
		t.Fatalf("journal row after claim = %+v ok=%t, want claimed at epoch 1", row, ok)
	}

	if fence := a.BeforeEffect(ctx, rec); fence.MustRefuse(binding) {
		t.Fatalf("pre-effect fence refused on the leader: %+v", fence)
	}

	settlement := a.Settle(ctx, mcpc.GateOutcome{
		Record: rec, State: mcpc.DispatchCompleted, ResultDigest: "res-digest-1", DispatchRef: "disp-1",
	})
	if settlement.FailureClass != sdk.FailureNone || settlement.EvidenceRef == "" {
		t.Fatalf("settlement = %+v, want recorded with an evidence ref", settlement)
	}
	events = mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1)
	if len(events) != 2 || events[1].Action != "mcp.tool.call.keyed.settle" {
		t.Fatalf("post-settle events = %d (%v), want claim+settle", len(events), events)
	}
	row, _ = mcpJournalRow(t, f, "op-lifecycle-1")
	if row.State != model.EvidenceOpCompleted || row.ResultDigest != "res-digest-1" || row.DispatchRef != "disp-1" {
		t.Fatalf("settled row = %+v", row)
	}

	// Exact replay: recorded state only — no new events, never emittable.
	replay := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if replay.State != mcpc.GateRecordReplaySettled || replay.MayEmit(binding) {
		t.Fatalf("replay record = %+v, want replay_settled and NOT emittable", replay)
	}
	if replay.Recorded == nil || replay.Recorded.State != mcpc.DispatchCompleted ||
		replay.Recorded.ResultDigest != "res-digest-1" {
		t.Fatalf("replay recorded outcome = %+v", replay.Recorded)
	}
	if got := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(got) != 2 {
		t.Fatalf("replay appended events: total %d, want 2", len(got))
	}
	verifyMCPLedger(t, f)
}

// TestMCPEvidenceRebindRefused: the same OperationID with a different digest is
// FailureReplay — no second claim event, the recorded binding stands.
func TestMCPEvidenceRebindRefused(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()
	if rec := a.Record(ctx, mcpAllowDecision(f, "keyed"), mcpEvidenceBinding("op-rebind-1", "digest-a")); rec.State != mcpc.GateRecordFresh {
		t.Fatalf("first claim = %+v", rec)
	}
	before := mcpLedgerHead(t, f.store, f.tenant)
	rebind := a.Record(ctx, mcpAllowDecision(f, "keyed"), mcpEvidenceBinding("op-rebind-1", "digest-B"))
	if rebind.State != mcpc.GateRecordRefused || rebind.FailureClass != sdk.FailureReplay {
		t.Fatalf("rebind record = %+v, want refused/FailureReplay", rebind)
	}
	if rebind.MayEmit(mcpEvidenceBinding("op-rebind-1", "digest-B")) {
		t.Fatal("a rebind must never be emittable")
	}
	if events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
		t.Fatalf("rebind appended %d events, want 0", len(events))
	}
	if row, _ := mcpJournalRow(t, f, "op-rebind-1"); row.EffectDigest != "digest-a" {
		t.Fatalf("rebind mutated the recorded binding: %+v", row)
	}
}

// TestMCPEvidenceConcurrentDuplicateClaims: N racing claims of one binding on the
// REAL store yield EXACTLY one fresh winner (single-use claim under concurrency).
func TestMCPEvidenceConcurrentDuplicateClaims(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	binding := mcpEvidenceBinding("op-race-1", "digest-a")
	const n = 8
	var wg sync.WaitGroup
	states := make([]mcpc.GateRecordState, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			states[i] = a.Record(context.Background(), mcpAllowDecision(f, "keyed"), binding).State
		}(i)
	}
	wg.Wait()
	fresh := 0
	for _, st := range states {
		switch st {
		case mcpc.GateRecordFresh:
			fresh++
		case mcpc.GateRecordReplayPending:
		default:
			t.Fatalf("unexpected race state %q (states=%v)", st, states)
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh winners = %d (states=%v), want EXACTLY 1", fresh, states)
	}
	verifyMCPLedger(t, f)
}

// TestMCPEvidenceNilStoreRefusesEnforcedAllow: exploit 1 at the adapter — an
// enforced allow with no store refuses ledger_unwired (never emittable), while a
// policy DENY record remains a no-op refusal (denial never depends on evidence).
func TestMCPEvidenceNilStoreRefusesEnforcedAllow(t *testing.T) {
	a := mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), tenant: model.NewTenantID(),
	}
	binding := mcpEvidenceBinding("op-unwired-1", "digest-a")
	rec := a.Record(context.Background(), mcpc.ToolDecision{
		Tenant: a.tenant.String(), Subject: "agent:x", Tool: "deploy", Allowed: true,
	}, binding)
	if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultLedgerUnwired {
		t.Fatalf("nil-store enforced allow = %+v, want refused/ledger_unwired", rec)
	}
	if rec.MayEmit(binding) {
		t.Fatal("nil-store record must never be emittable")
	}
}

// TestMCPEvidenceNotLeaderRefuses: a standby write gate (ErrNotLeader from
// Mutate) refuses the claim ledger_unavailable — the effect never runs there.
func TestMCPEvidenceNotLeaderRefuses(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpGateAuditor{
		log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		store:  notLeaderHookStore{f.store},
		tenant: f.tenant,
	}
	binding := mcpEvidenceBinding("op-standby-1", "digest-a")
	rec := a.Record(context.Background(), mcpAllowDecision(f, "keyed"), binding)
	if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultLedgerUnavailable {
		t.Fatalf("standby claim = state %q fault %q, want refused/ledger_unavailable", rec.State, rec.Receipt.Fault)
	}
	if _, ok := mcpJournalRow(t, f, "op-standby-1"); ok {
		t.Fatal("standby refusal must not create a journal row")
	}
}

// TestMCPEvidenceDegradeSeqZeroRefusesAndCountsLoss — exploit 10: under the
// DEGRADE audit-spool policy a claim whose evidence event is dropped (Seq==0)
// REFUSES the effect AND durably advances the pending-drops loss accounting (the
// F9 discipline: commit the gap, THEN refuse); no journal row is created, so the
// refused operation is retryable once the ledger recovers.
func TestMCPEvidenceDegradeSeqZeroRefusesAndCountsLoss(t *testing.T) {
	ctx := context.Background()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("build signer: %v", err)
	}
	dsn := filepath.Join(t.TempDir(), "mcp-evidence-degrade.db")

	// Phase 1: provision with no budget so tenant provisioning is unaffected.
	seed, err := coreengine.Open(ctx, store.Config{
		Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	var tenant model.TenantID
	if err := seed.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, cerr := sys.CreateOrg(ctx, model.Org{Name: "mcp-degrade", Slug: "mcp-degrade", Status: model.StatusActive})
		if cerr == nil {
			tenant = org.TenantID
		}
		return cerr
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	// Phase 2: reopen with a 1-byte spool budget in DEGRADE mode — every governed
	// append drops with durable loss accounting.
	st := openHookSpoolStore(t, dsn, signer, 1, store.AuditSpoolDegrade)
	a := mcpGateAuditor{
		log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), store: st, tenant: tenant,
	}
	base := hookPendingDrops(t, st)
	binding := mcpEvidenceBinding("op-degrade-1", "digest-a")
	rec := a.Record(ctx, mcpc.ToolDecision{
		Tenant: tenant.String(), Subject: "agent:degrade", Tool: "deploy", Allowed: true,
		OperationIDKind: "keyed",
	}, binding)
	if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultSpoolDegraded {
		t.Fatalf("degrade claim = state %q fault %q, want refused/spool_degraded", rec.State, rec.Receipt.Fault)
	}
	if rec.MayEmit(binding) {
		t.Fatal("degrade-dropped claim must never be emittable")
	}
	if got := hookPendingDrops(t, st) - base; got != 1 {
		t.Fatalf("durable pending drops advanced by %d, want exactly 1 (commit the gap, THEN refuse)", got)
	}
	// No journal row: the refusal left nothing claimed (retryable post-recovery).
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		_, gerr := sc.EvidenceOperations().Get(ctx, "op-degrade-1")
		if gerr == store.ErrNotFound {
			return nil
		}
		if gerr == nil {
			t.Fatal("degrade drop must not create a journal row")
		}
		return gerr
	})
	if err != nil {
		t.Fatalf("journal check: %v", err)
	}
}

// TestMCPEvidenceTenantResolution pins the tenant-fallback fix on the
// ENFORCED leg: empty decision tenant falls back to the configured tenant;
// malformed or MISMATCHED non-empty tenants refuse TenantUnresolved (no silent
// re-attribution) and journal nothing.
func TestMCPEvidenceTenantResolution(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()

	t.Run("empty tenant falls back to the configured tenant", func(t *testing.T) {
		d := mcpAllowDecision(f, "keyed")
		d.Tenant = ""
		rec := a.Record(ctx, d, mcpEvidenceBinding("op-tenant-empty", "digest-a"))
		if rec.State != mcpc.GateRecordFresh {
			t.Fatalf("empty-tenant record = %+v, want fresh (configured fallback)", rec)
		}
		if _, ok := mcpJournalRow(t, f, "op-tenant-empty"); !ok {
			t.Fatal("fallback claim missing from the journal")
		}
	})

	t.Run("malformed tenant refuses TenantUnresolved", func(t *testing.T) {
		d := mcpAllowDecision(f, "keyed")
		d.Tenant = "not-a-tenant-id"
		rec := a.Record(ctx, d, mcpEvidenceBinding("op-tenant-bad", "digest-a"))
		if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultTenantUnresolved {
			t.Fatalf("malformed-tenant record = state %q fault %q, want refused/tenant_unresolved", rec.State, rec.Receipt.Fault)
		}
		if _, ok := mcpJournalRow(t, f, "op-tenant-bad"); ok {
			t.Fatal("malformed-tenant refusal must journal nothing")
		}
	})

	t.Run("mismatched valid tenant refuses TenantUnresolved", func(t *testing.T) {
		d := mcpAllowDecision(f, "keyed")
		d.Tenant = model.NewTenantID().String() // parseable, NOT the configured tenant
		rec := a.Record(ctx, d, mcpEvidenceBinding("op-tenant-other", "digest-a"))
		if rec.State != mcpc.GateRecordRefused || rec.Receipt.Fault != sdk.EvidenceFaultTenantUnresolved {
			t.Fatalf("mismatched-tenant record = state %q fault %q, want refused/tenant_unresolved", rec.State, rec.Receipt.Fault)
		}
		if _, ok := mcpJournalRow(t, f, "op-tenant-other"); ok {
			t.Fatal("mismatched-tenant refusal must journal nothing")
		}
	})

	t.Run("legacy best-effort with malformed tenant is a loud gap, never a silent fallback", func(t *testing.T) {
		var logs bytes.Buffer
		loud := mcpGateAuditor{log: slog.New(slog.NewTextHandler(&logs, nil)), store: f.store, tenant: f.tenant}
		before := mcpLedgerHead(t, f.store, f.tenant)
		loud.Record(ctx, mcpc.ToolDecision{
			Tenant: "###broken###", Subject: "agent:x", Tool: "read", Allowed: false,
		}, sdk.EvidenceBinding{})
		if !strings.Contains(logs.String(), "malformed decision tenant") {
			t.Fatalf("missing loud malformed-tenant gap log: %s", logs.String())
		}
		if events := mcpLedgerEventsFrom(t, f.store, f.tenant, before.Seq+1); len(events) != 0 {
			t.Fatalf("malformed-tenant legacy decision anchored %d events under the WRONG tenant, want 0", len(events))
		}
	})
}

// TestMCPEvidenceSettlementFailureLeavesClaimAmbiguous — exploit 8 at the
// adapter: a settlement the store refuses does NOT record; the operation stays
// claimed (status replay only, never a re-dispatch, never a false success).
func TestMCPEvidenceSettlementFailureLeavesClaimAmbiguous(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()
	binding := mcpEvidenceBinding("op-settle-fail", "digest-a")
	rec := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if rec.State != mcpc.GateRecordFresh {
		t.Fatalf("claim = %+v", rec)
	}
	// The settlement leg hits a store whose Mutate refuses (standby shape).
	failing := mcpGateAuditor{
		log:    slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		store:  notLeaderHookStore{f.store},
		tenant: f.tenant,
	}
	settlement := failing.Settle(ctx, mcpc.GateOutcome{Record: rec, State: mcpc.DispatchCompleted})
	if settlement.FailureClass != sdk.FailureEvidenceFault {
		t.Fatalf("failed settlement = %+v, want FailureEvidenceFault (response withheld)", settlement)
	}
	row, _ := mcpJournalRow(t, f, "op-settle-fail")
	if row.State != model.EvidenceOpClaimed {
		t.Fatalf("row after failed settlement = %q, want claimed (ambiguous-but-safe)", row.State)
	}
	// A same-operation retry is a status replay of the claimed state.
	replay := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if replay.State != mcpc.GateRecordReplayPending || replay.MayEmit(binding) {
		t.Fatalf("retry record = %+v, want replay_pending and NOT emittable", replay)
	}
}

// TestMCPEvidenceBeforeEffectFence pins the pre-dispatch fence legs: a good
// token passes on the leader; a malformed token and a missing store refuse.
func TestMCPEvidenceBeforeEffectFence(t *testing.T) {
	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()
	binding := mcpEvidenceBinding("op-fence-1", "digest-a")
	rec := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if fence := a.BeforeEffect(ctx, rec); fence.MustRefuse(binding) {
		t.Fatalf("leader fence refused: %+v", fence)
	}

	bad := rec
	bad.FenceToken = "not-a-number"
	if fence := a.BeforeEffect(ctx, bad); !fence.MustRefuse(binding) {
		t.Fatal("malformed fence token must refuse (fail closed)")
	}

	unwired := mcpGateAuditor{log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if fence := unwired.BeforeEffect(ctx, rec); fence.Fault != sdk.EvidenceFaultLedgerUnwired {
		t.Fatalf("unwired fence fault = %q, want ledger_unwired", fence.Fault)
	}
}

// TestMCPEvidenceWithheldSettlementCrossesTheSeam — P-1 of the stage-7 review.
// The word "withheld" exists on BOTH sides of the license boundary —
// connectors/mcp.DispatchWithheld and core/model.EvidenceOpWithheld — and the
// gateway crosses it with raw string casts in both directions (Settle:
// model.EvidenceOperationState(out.State) at mcpgateway.go; replay:
// mcpc.DispatchState(outcome.Op.State)). No connector-level test can pin the
// pair, because connectors must never import /core, so the equality lives HERE,
// in the composition root that imports both. Measured before this test existed:
// mutating the connector literal left the whole suite green while every
// withheld-release settlement would have refused at runtime — the refusal
// stands (fail-safe) but the release child's evidence row is silently lost.
func TestMCPEvidenceWithheldSettlementCrossesTheSeam(t *testing.T) {
	// The direct pin first, so a drift names itself before the flow-proof runs.
	// The flow below is the real assurance (a matching pair with broken wiring
	// would still fail there); this line only sharpens the failure message.
	if string(mcpc.DispatchWithheld) != string(model.EvidenceOpWithheld) {
		t.Fatalf("the seam literals diverged: connector %q vs core %q — the release child's settlements will refuse",
			mcpc.DispatchWithheld, model.EvidenceOpWithheld)
	}

	f := newMCPLedgerFixture(t)
	a := mcpEvidenceAuditor(f)
	ctx := context.Background()
	binding := mcpEvidenceBinding("op-withheld-1", "digest-w")

	rec := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if rec.State != mcpc.GateRecordFresh {
		t.Fatalf("fresh claim record = %+v", rec)
	}

	// Settle with the CONNECTOR's constant — the exact value the withheld-release
	// child uses. If the literal stops naming a state the core enum admits, this
	// settlement refuses (Terminal() fails, and the CHECK constraint backstops it).
	settlement := a.Settle(ctx, mcpc.GateOutcome{
		Record: rec, State: mcpc.DispatchWithheld, ResultDigest: "res-withheld-1", DispatchRef: "disp-w",
	})
	if settlement.FailureClass != sdk.FailureNone || settlement.EvidenceRef == "" {
		t.Fatalf("withheld settlement = %+v, want recorded — the connector literal no longer crosses the seam",
			settlement)
	}

	// The row, read back through the CORE model, must carry EvidenceOpWithheld:
	// this is the connector→core direction pinned on the real journal.
	row, ok := mcpJournalRow(t, f, "op-withheld-1")
	if !ok || row.State != model.EvidenceOpWithheld {
		t.Fatalf("journal row = %+v ok=%t, want state %q via the core constant", row, ok, model.EvidenceOpWithheld)
	}
	if row.ResultDigest != "res-withheld-1" {
		t.Fatalf("withheld row lost its result digest: %+v (the digest of the bytes that never left)", row)
	}

	// And the core→connector direction: an exact replay must hand the recorded
	// state back across the boundary as DispatchWithheld, never a second effect.
	replay := a.Record(ctx, mcpAllowDecision(f, "keyed"), binding)
	if replay.State != mcpc.GateRecordReplaySettled || replay.MayEmit(binding) {
		t.Fatalf("replay record = %+v, want replay_settled and NOT emittable", replay)
	}
	if replay.Recorded == nil || replay.Recorded.State != mcpc.DispatchWithheld {
		t.Fatalf("replay recorded outcome = %+v, want the withheld state back across the seam", replay.Recorded)
	}
	verifyMCPLedger(t, f)
}
