// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package suspension_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

// Withdrawing SERVICE is a commercial decision. Keeping a customer's evidence
// anchored and provable is a CUSTODY obligation, and it does not depend on their
// paying — during a grace period it is precisely when it matters, because that is
// the window in which the data may be needed (a dispute, an exit, a regulator).
//
// The tests in this file pin the boundary between those two, in both directions:
// custody REACHES a withdrawn tenant (below), and custody is not a way to reach
// anything else (TestCustodyScopeCannotReachProductData / cannot cross a region).

// TestSuspensionDoesNotCutVerifiableCustody is the P0 the external contrast
// (2026-08-08) named as the worst of the four: suspending a tenant stopped
// anchoring its chain, and then DR could report success over it.
//
// The claim the whole design rested on — "a withdrawn tenant has a FROZEN
// chain, so it has nothing new to anchor" — is false in the very lines that
// implement it: SetOrgStatus changes orgs.status and THEN appends
// org.suspend_service to that tenant's own chain, in the same transaction
// (core/internal/store/sqlstore/system.go:337, :361). The chain advances at the
// exact instant the checkpoint filter starts skipping it, so the suspension event
// itself — and every event since the previous checkpoint — is left unanchored.
//
// This test asserts the property directly, against the REAL audit.CheckpointAll:
// after a checkpoint sweep, the withdrawn tenant's tail is attested up to its
// head. It does not re-implement the sweep; if CheckpointAll stops covering
// withdrawn tenants, this fails.
func TestSuspensionDoesNotCutVerifiableCustody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	signer, pub := testSigner(t)
	inner := openSignedStore(t, signer)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")

	// Real work on the chain before the suspension, so there is a tail that the
	// previous checkpoint did not cover.
	appendEvent(t, inner, tenant, "agent.created")
	appendEvent(t, inner, tenant, "agent.updated")

	setStatus(t, inner, tenant, model.StatusSuspended)

	// The head now includes org.suspend_service, appended by the suspension itself.
	head := headSeq(t, inner, tenant)
	if head == 0 {
		t.Fatal("no chain to anchor; the test cannot measure what it claims")
	}

	if err := signer.CheckpointAll(ctx, guarded); err != nil {
		t.Fatalf("CheckpointAll over an estate with a withdrawn tenant: %v", err)
	}

	rep := checkpointReport(t, inner, tenant, pub)
	if rep.LatestAttestedSeq < head {
		t.Fatalf("the withdrawn tenant's custody was cut: its chain reaches seq %d but the "+
			"latest checkpoint attests only seq %d — the suspension event and everything "+
			"since the previous checkpoint are left with no anchor (checkpoints found: %d)",
			head, rep.LatestAttestedSeq, rep.Checkpoints)
	}
	if !rep.OK {
		t.Fatalf("the withdrawn tenant's checkpoints do not verify: %s", rep.Reason)
	}
}

// TestCustodyReachesAWithdrawnTenantAndNothingElse pins the NARROWNESS that makes
// the custody door defensible. It is not a context flag smuggled past the guard
// and it is not a second unguarded store handle: it is a distinct method yielding
// a scope whose TYPE carries the restriction.
//
// The package doc's own objection to carving reads apart was that "the store sees
// a View, not the HTTP route". A separate scope type answers it: the store does
// not have to know the route, because a CustodyScope has no product data on it to
// give away. This test is the compile-time-plus-runtime proof of that claim —
// custody works on a suspended tenant, and View on the same tenant still does not.
func TestCustodyReachesAWithdrawnTenantAndNothingElse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")
	setStatus(t, inner, tenant, model.StatusSuspended)

	// Custody reaches it, for READS and for WRITES. The write half is what the
	// key-transition marker needs: `olivares audit key-transition` fences the
	// signing-key epoch by APPENDING to each chain, and through Mutate a withdrawn
	// tenant came back "FAILED: tenant suspended" — a failure reported for something
	// that was not one, on a chain that then went unfenced. (A plain action stands in
	// for the marker here: audit.key.rotation is a RESERVED action that only
	// audit.RecordKeyRotation may write, and it additionally requires an off-box
	// checkpoint signer. What custody has to provide is a committing append, and that
	// is what this asserts.)
	var seen model.TenantID
	var appended int64
	if err := guarded.Custody(ctx, tenant, func(cs store.CustodyScope) error {
		seen = cs.Tenant()
		if _, _, err := cs.Audit().Head(ctx); err != nil {
			return err
		}
		ev, err := cs.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: "agent.updated", TargetKind: "core.agent",
		})
		appended = ev.Seq
		return err
	}); err != nil {
		t.Fatalf("custody must reach a withdrawn tenant's evidence: %v", err)
	}
	if seen != tenant {
		t.Fatalf("custody scope bound to %s, want %s", seen, tenant)
	}
	if appended == 0 {
		t.Fatal("custody append produced no event; the key-transition marker could not be recorded")
	}
	// It really committed — a custody write that rolled back would look identical
	// from inside the closure.
	if got := headSeq(t, inner, tenant); got != appended {
		t.Fatalf("custody append did not commit: head is seq %d, the append returned seq %d", got, appended)
	}

	// The service door is still shut, on the same tenant, in the same test — so a
	// mutant that simply disarms the guard fails HERE rather than passing both.
	if err := guarded.View(ctx, tenant, func(store.Scope) error { return nil }); err == nil {
		t.Fatal("View on a withdrawn tenant must still be denied; custody must not have opened the service door")
	}
	if err := guarded.Mutate(ctx, tenant, func(store.Scope) error { return nil }); err == nil {
		t.Fatal("Mutate on a withdrawn tenant must still be denied")
	}
}

// --- helpers -----------------------------------------------------------------

func testSigner(t *testing.T) (*audit.Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s, pub
}

// openSignedStore is openStore with per-event signing armed, which is how the
// composition root wires it. A manifest tip is only VerifiedAtBackup when the
// per-event signatures check out, so a test that asserts on that verdict has to
// seed a ledger that is actually signed — otherwise it would be asserting against
// event-sig-missing and measuring the fixture instead of the code.
func openSignedStore(t *testing.T, signer *audit.Signer) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open signed store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(context.Background())
		return e
	}); err != nil {
		t.Fatalf("ensure system tenant: %v", err)
	}
	return st
}

// appendEvent writes through the UNGUARDED store: these tests are about what the
// custodial sweeps do afterwards, not about the guard on the write path.
func appendEvent(t *testing.T, st store.Store, tenant model.TenantID, action string) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, e := sc.Audit().Append(context.Background(), model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: action, TargetKind: "core.agent",
		})
		return e
	}); err != nil {
		t.Fatalf("append %q: %v", action, err)
	}
}

func headSeq(t *testing.T, st store.Store, tenant model.TenantID) int64 {
	t.Helper()
	var seq int64
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		h, ok, e := sc.Audit().Head(context.Background())
		if ok {
			seq = h.Seq
		}
		return e
	}); err != nil {
		t.Fatalf("head: %v", err)
	}
	return seq
}

func checkpointReport(t *testing.T, st store.Store, tenant model.TenantID, pub ed25519.PublicKey) audit.CheckpointReport {
	t.Helper()
	var rep audit.CheckpointReport
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		r, e := audit.VerifyCheckpoints(context.Background(), sc.Audit(), pub)
		rep = r
		return e
	}); err != nil {
		t.Fatalf("verify checkpoints: %v", err)
	}
	return rep
}

// TestDRVerifyCannotPassWithoutLookingAtAWithdrawnTenant closes the second half of
// the same P0. Anchoring the chain is worth nothing if the restore gate never
// checks it.
//
// RestoreVerify only ever verifies the tenants listed in the manifest's Tenants
// array. While a withdrawn tenant got a free-text Note instead of a TenantTip, it
// was absent from that array, so `dr verify` walked every OTHER tenant, found them
// healthy and reported PASSED — over a bundle whose withdrawn tenant's chain,
// signatures, checkpoints and tip nobody had looked at. OK is what authorizes
// resuming writes after a restore. A backup that reports success it did not earn
// is worse than one that reports failure.
func TestDRVerifyCannotPassWithoutLookingAtAWithdrawnTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	signer, pub := testSigner(t)
	inner := openSignedStore(t, signer)
	guarded := suspension.Guard(inner, nil)
	withdrawn := createOrg(t, inner, "acme")
	appendEvent(t, inner, withdrawn, "agent.created")
	setStatus(t, inner, withdrawn, model.StatusSuspended)

	cpv, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatalf("checkpoint verifier: %v", err)
	}
	m, err := dr.BuildManifest(ctx, guarded, pub, cpv, dr.BuildOptions{
		EngineKind: "sqlite", TipMatch: dr.TipExact, Now: time.Unix(0, 0).UTC(),
		Keys: []dr.KeyRef{{
			File: "keys/audit-signing.key.enc", Name: "audit-signing.key",
			Role: dr.RoleAudit, PubSHA256: dr.PubFingerprint(pub),
		}},
	})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}

	rep, err := dr.RestoreVerify(ctx, guarded, m, pub, cpv)
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a healthy estate with one withdrawn tenant must verify: %v", rep.Problems)
	}
	// The assertion that matters: it was actually LOOKED AT, not just passed over.
	var saw bool
	for _, tv := range rep.Tenants {
		if tv.Tenant == withdrawn.String() {
			saw = true
			if !tv.ChainOK || !tv.EventsOK || !tv.TipOK {
				t.Fatalf("the withdrawn tenant was verified but did not pass: chain=%v events=%v tip=%v",
					tv.ChainOK, tv.EventsOK, tv.TipOK)
			}
		}
	}
	if !saw {
		t.Fatal("dr verify reported OK without producing any verdict for the withdrawn tenant: " +
			"its continuity was never checked, and OK is what authorizes resuming writes")
	}
}

// TestAnUnknownStateIsNotReportedAsASuspension pins the second P2 of the contrast.
//
// The guard classified EVERY status other than "active" as a withdrawal of
// service and answered store.ErrTenantSuspended, which the API renders as 423
// tenant_suspended. SetOrgStatus accepts only the two service states, so this is
// not reachable through the public API — but the internal CreateOrg persists
// whatever status it is handed without validating it, so an import, a migration or
// an internal caller can leave a row on "inactive", "error" or a value this binary
// has never heard of.
//
// Such a tenant then gets a COMMERCIAL diagnosis for a DATA problem: a console
// shows the customer their service was withdrawn, and the operator goes looking
// for a billing decision nobody ever made instead of at the inconsistent row. The
// work must still not run — an unrecognized state is never "in service" — so this
// asserts the denial stays and only the DIAGNOSIS changes.
func TestAnUnknownStateIsNotReportedAsASuspension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)

	// CreateOrg does not validate the status it is handed — which is exactly how a
	// row like this comes to exist.
	var broken model.TenantID
	if err := inner.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "legacy", Slug: "legacy", Status: model.LifecycleStatus("error")})
		broken = o.TenantID
		return e
	}); err != nil {
		t.Fatalf("create org with an unknown status: %v", err)
	}

	var ran bool
	err := guarded.View(ctx, broken, func(store.Scope) error { ran = true; return nil })
	if ran {
		t.Fatal("work ran for a tenant whose state is not recognized; the guard must stay deny-closed")
	}
	if !errors.Is(err, store.ErrTenantNotInService) {
		t.Fatalf("got %v, want ErrTenantNotInService", err)
	}
	// The load-bearing half: it must NOT be reported as a commercial suspension.
	if errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("a tenant on status %q was diagnosed as a withdrawal of service; "+
			"no suspension was ever recorded for it: %v", "error", err)
	}

	// And the real thing still reports as the real thing — otherwise this test
	// would pass just as well against a guard that had stopped saying "suspended"
	// at all.
	suspended := createOrg(t, inner, "acme")
	setStatus(t, inner, suspended, model.StatusSuspended)
	serr := guarded.View(ctx, suspended, func(store.Scope) error { return nil })
	if !errors.Is(serr, store.ErrTenantSuspended) {
		t.Fatalf("a genuinely suspended tenant must still report ErrTenantSuspended, got %v", serr)
	}
	if errors.Is(serr, store.ErrTenantNotInService) {
		t.Fatalf("a genuinely suspended tenant must not be reported as an unknown state: %v", serr)
	}
}

// TestCustodyScopeCannotBeWidenedBackIntoAScope defends the claim the whole design
// rests on: that the narrowing is structural, not a convention.
//
// If a store.CustodyScope could be type-asserted back to a store.Scope, the custody
// door would be a full tenant-scoped store handle wearing a smaller name — and
// since suspension does not gate it, that would be strictly worse than the context
// flag the package doc rejected, because it would look narrow while being wide.
//
// This is a runtime assertion of something the compiler already enforces, and that
// is the point: it fails LOUDLY if someone later "helpfully" embeds store.Scope in
// the custody scope, which is the natural refactor that would quietly reopen it.
func TestCustodyScopeCannotBeWidenedBackIntoAScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")
	setStatus(t, inner, tenant, model.StatusSuspended)

	if err := guarded.Custody(ctx, tenant, func(cs store.CustodyScope) error {
		if sc, ok := cs.(store.Scope); ok {
			return fmt.Errorf("a CustodyScope widened back into a full store.Scope (%T): the custody door, "+
				"which service state does NOT gate, would hand out every repository on the tenant", sc)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestWithdrawnTenantCanStillTakeItsOwnDataOut pins the line as it was decided:
// deny mutations and interactive service, keep an EXPLICIT and AUDITED export.
//
// Neither of the two options originally posed was right. Allowing every read
// leaves a non-paying tenant OPERATING — a routing policy resolves and a model
// executes under View — and that is service. Denying every read withdraws
// /v1/audit/export and GET /v1/m/knowledge/memory/export, which is the customer's
// own subject-access and anti-lock-in copy, and custody does not lapse for
// non-payment.
//
// All three properties are asserted together ON PURPOSE: a mutant that opens the
// service door, and a mutant that shuts the export door, must each fail here, and
// asserting only one of them would let the other through.
func TestWithdrawnTenantCanStillTakeItsOwnDataOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")
	appendEvent(t, inner, tenant, "agent.created")
	setStatus(t, inner, tenant, model.StatusSuspended)

	before := headSeq(t, inner, tenant)

	// 1. The EXPORT door is open, and reaches the ledger.
	var walked int
	if err := guarded.Export(ctx, tenant, func(es store.ExportScope) error {
		_, _, e := es.Audit().Head(ctx)
		walked++
		return e
	}); err != nil {
		t.Fatalf("a withdrawn tenant must still be able to take its own data out: %v", err)
	}
	if walked != 1 {
		t.Fatal("the export closure did not run")
	}

	// 2. It was AUDITED — on the tenant's own chain, in the same transaction.
	after := headSeq(t, inner, tenant)
	if after <= before {
		t.Fatalf("the export was not recorded: chain head stayed at %d. A copy of a customer's "+
			"data leaving during a grace period must never be takeable silently", before)
	}
	if got := lastAction(t, inner, tenant); got != suspension.ActionExportDuringWithdrawal {
		t.Fatalf("last chain event is %q, want %q", got, suspension.ActionExportDuringWithdrawal)
	}

	// 3. And the SERVICE door is still shut — export is a carve-out, not an opening.
	if err := guarded.View(ctx, tenant, func(store.Scope) error { return nil }); !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("View on a withdrawn tenant: got %v, want ErrTenantSuspended", err)
	}
	if err := guarded.Mutate(ctx, tenant, func(store.Scope) error { return nil }); !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("Mutate on a withdrawn tenant: got %v, want ErrTenantSuspended", err)
	}
}

// TestExportOfAnActiveTenantIsNotAudited is the negative that keeps the assertion
// above honest: the ledger event marks an export taken DURING a withdrawal, so an
// ordinary export must not write one. Without this, a guard that recorded every
// export would pass the test above while burying the signal that matters in noise.
func TestExportOfAnActiveTenantIsNotAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")
	appendEvent(t, inner, tenant, "agent.created")

	before := headSeq(t, inner, tenant)
	if err := guarded.Export(ctx, tenant, func(es store.ExportScope) error {
		_, _, e := es.Audit().Head(ctx)
		return e
	}); err != nil {
		t.Fatalf("export for an in-service tenant: %v", err)
	}
	if after := headSeq(t, inner, tenant); after != before {
		t.Fatalf("an ordinary export wrote %d chain event(s); the marker must mean "+
			"\"taken while service was withdrawn\", or it means nothing", after-before)
	}
}

func lastAction(t *testing.T, st store.Store, tenant model.TenantID) string {
	t.Helper()
	var action string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(ev model.AuditEvent) error {
			action = ev.Action // Walk is ordered, so the last one wins
			return nil
		})
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	return action
}
