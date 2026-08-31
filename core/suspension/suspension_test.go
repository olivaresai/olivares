// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package suspension_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
)

// The three properties below are deliberately asserted by THREE SEPARATE tests,
// each of which fails for its own reason:
//
//	TestSuspendedTenantIsNotServed  — a suspended tenant's work is DENIED.
//	TestSuspensionDestroysNothing   — the data survives the suspension ITSELF,
//	                                  proven while still suspended, via the
//	                                  UNGUARDED store, so it cannot be confused
//	                                  with "restore worked".
//	TestRestoringServiceIsLossless  — restoring brings the tenant back, with the
//	                                  estate it had.
//
// A mutant that turns suspension into destruction must fail only the second; one
// that makes it irreversible must fail only the third; one that serves a
// suspended tenant must fail only the first. If one test catches two of them it
// does not discriminate, and the matrix in docs/ records which killed which.

func TestSuspendedTenantIsNotServed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")

	// Baseline: served.
	if err := guarded.View(ctx, tenant, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("active tenant must be served: %v", err)
	}
	if err := guarded.Mutate(ctx, tenant, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("active tenant must be writable: %v", err)
	}

	setStatus(t, inner, tenant, model.StatusSuspended)

	// READS are denied as hard as writes. This is the load-bearing assertion of
	// the whole design: in this product reading IS the service (a routing policy
	// resolves and a model executes under View — modules/models/execute.go), so a
	// read-permitting suspension would leave a non-paying tenant spending against
	// our provider bill. A guard that only gated Mutate would pass every other
	// test in this file.
	var ranView bool
	err := guarded.View(ctx, tenant, func(sc store.Scope) error { ranView = true; return nil })
	if !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("View on a suspended tenant: got %v, want ErrTenantSuspended", err)
	}
	if ranView {
		t.Fatal("View ran the caller's work for a suspended tenant")
	}
	var ranMutate bool
	err = guarded.Mutate(ctx, tenant, func(sc store.Scope) error { ranMutate = true; return nil })
	if !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("Mutate on a suspended tenant: got %v, want ErrTenantSuspended", err)
	}
	if ranMutate {
		t.Fatal("Mutate ran the caller's work for a suspended tenant")
	}
}

// TestSuspensionDestroysNothing proves the difference between this operation and
// DropTenant — the entire reason exists. It asserts through the UNGUARDED
// store while the tenant is STILL SUSPENDED, so it can never be satisfied by a
// working restore path: an implementation that deleted the rows and re-created
// them on restore would pass TestRestoringServiceIsLossless and fail here.
func TestSuspensionDestroysNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")

	want := []string{"alpha", "beta", "gamma"}
	for _, name := range want {
		if err := guarded.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := sc.Agents().Create(ctx, model.Agent{Name: name, Kind: "assistant", Status: model.StatusActive})
			return e
		}); err != nil {
			t.Fatalf("seed agent %q: %v", name, err)
		}
	}

	setStatus(t, inner, tenant, model.StatusSuspended)

	// NOTE: this test deliberately does NOT assert that the guard refuses. That is
	// TestSuspendedTenantIsNotServed's job, and asserting it here would make this
	// test fail for a mutant that breaks DENIAL as well as for one that breaks
	// DATA — which is precisely the "one test catches two" that stops a matrix
	// discriminating. The precondition is read off the org row instead.
	if org := getOrg(t, inner, tenant); org.Status != model.StatusSuspended {
		t.Fatalf("precondition: org status = %q, want suspended", org.Status)
	}
	got := agentNames(t, inner, tenant)
	if len(got) != len(want) {
		t.Fatalf("suspension destroyed data: %d agents survive, want %d (%v)", len(got), len(want), got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("suspension altered data: agent %d = %q, want %q (all: %v)", i, got[i], name, got)
		}
	}
	// The org row itself survives too — only its status moved.
	org := getOrg(t, inner, tenant)
	if org.Status != model.StatusSuspended {
		t.Fatalf("org status = %q, want %q", org.Status, model.StatusSuspended)
	}
	if org.Slug != "acme" {
		t.Fatalf("suspension altered the org row: slug = %q, want %q", org.Slug, "acme")
	}
}

// TestRestoringServiceIsLossless asserts reversibility as a first-class property:
// a suspension you cannot come back from is a DELETE with another name, and the
// normal case is that the customer pays and returns.
func TestRestoringServiceIsLossless(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")

	if err := guarded.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Agents().Create(ctx, model.Agent{Name: "alpha", Kind: "assistant", Status: model.StatusActive})
		return e
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	setStatus(t, inner, tenant, model.StatusSuspended)
	setStatus(t, inner, tenant, model.StatusActive)

	// Service is back through the SAME guarded store — no new wiring, no restart.
	if err := guarded.View(ctx, tenant, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("restored tenant must be served again: %v", err)
	}
	if err := guarded.Mutate(ctx, tenant, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("restored tenant must be writable again: %v", err)
	}
	// ...and it came back with the estate it had, read through the guard this time.
	var names []string
	if err := guarded.View(ctx, tenant, func(sc store.Scope) error {
		as, _, e := sc.Agents().List(ctx, model.Query{})
		for _, a := range as {
			names = append(names, a.Name)
		}
		return e
	}); err != nil {
		t.Fatalf("list after restore: %v", err)
	}
	if len(names) != 1 || names[0] != "alpha" {
		t.Fatalf("restore was lossy: agents = %v, want [alpha]", names)
	}
}

// TestSuspensionIsPerTenant guards against the blunt mutant that denies everyone:
// suspending one tenant must not touch its neighbors.
func TestSuspensionIsPerTenant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	suspended := createOrg(t, inner, "acme")
	neighbor := createOrg(t, inner, "globex")

	setStatus(t, inner, suspended, model.StatusSuspended)

	if err := guarded.View(ctx, suspended, func(sc store.Scope) error { return nil }); !errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("suspended tenant: got %v, want ErrTenantSuspended", err)
	}
	if err := guarded.View(ctx, neighbor, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("neighbor tenant must still be served: %v", err)
	}
}

// TestOperatorPathSurvivesSuspension is the counterpart of the denial test: the
// System and Auth paths MUST pass through, or suspension would be irreversible
// by construction — the operator could no longer read, restore or delete the
// tenant it just suspended, and its users could not authenticate to be told why.
func TestOperatorPathSurvivesSuspension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")
	setStatus(t, inner, tenant, model.StatusSuspended)

	// System: the operator can still see it...
	var seen bool
	if err := guarded.System(ctx, func(sys store.SystemScope) error {
		orgs, e := sys.ListOrgs(ctx)
		for _, o := range orgs {
			if o.TenantID == tenant {
				seen = true
			}
		}
		return e
	}); err != nil {
		t.Fatalf("System path must pass through a suspension: %v", err)
	}
	if !seen {
		t.Fatal("a suspended tenant vanished from the operator's org list")
	}
	// ...and restore it THROUGH THE GUARDED STORE, which is the only handle the
	// running server has. If System were gated, this call is the one that could
	// never happen.
	if err := guarded.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, tenant, model.StatusActive)
		return e
	}); err != nil {
		t.Fatalf("restore through the guarded store: %v", err)
	}
	if err := guarded.View(ctx, tenant, func(sc store.Scope) error { return nil }); err != nil {
		t.Fatalf("tenant must be served after restore: %v", err)
	}
	// Auth: unchanged by suspension, so a suspended tenant's user still
	// authenticates and can be shown WHY it is refused.
	if err := guarded.AuthView(ctx, func(as store.AuthScope) error { return nil }); err != nil {
		t.Fatalf("Auth path must pass through a suspension: %v", err)
	}
}

// TestUnknownTenantIsDenied closes the hole the reproduction measured: after
// DELETE /v1/system/orgs/{id} the rows are gone but nothing checked that the org
// existed, so a request naming a deleted tenant was answered 200 with an empty
// list — "served nothing" where the honest answer is "not served". An absent org
// can never mean "in service".
func TestUnknownTenantIsDenied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	guarded := suspension.Guard(inner, nil)
	tenant := createOrg(t, inner, "acme")

	if err := guarded.System(ctx, func(sys store.SystemScope) error {
		return sys.DropTenant(ctx, tenant)
	}); err != nil {
		t.Fatalf("drop tenant: %v", err)
	}
	var ran bool
	err := guarded.View(ctx, tenant, func(sc store.Scope) error { ran = true; return nil })
	if ran {
		t.Fatal("View ran the caller's work for a tenant with no org row")
	}
	// Deny-closed, but NOT reported as a suspension: a tenant that never existed —
	// a typo'd id, or one hard-deleted after the grace period — must not be told
	// "your service is suspended", which a console renders as a billing problem for
	// an account that is not there.
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted tenant: got %v, want ErrNotFound (deny-closed, honestly named)", err)
	}
	if errors.Is(err, store.ErrTenantSuspended) {
		t.Fatalf("a tenant that does not exist must not be reported as suspended: %v", err)
	}
}

// TestSystemTenantIsNeverSuspendable is the lockout guard, the counterpart of
// core/auth's ErrLastSuperadmin: suspending the reserved system partition would
// take out the auth and provisioning path that is the only way back.
func TestSystemTenantIsNeverSuspendable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	err := inner.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, model.SystemTenantID, model.StatusSuspended)
		return e
	})
	if err == nil {
		t.Fatal("suspending the reserved system tenant must fail")
	}
	// And it stays served.
	guarded := suspension.Guard(inner, nil)
	if err := guarded.System(ctx, func(sys store.SystemScope) error { return nil }); err != nil {
		t.Fatalf("system path after the refused suspension: %v", err)
	}
}

// TestSetOrgStatusRejectsNonServiceStates: the store must not persist a status
// the guard has no rule for. "inactive" and "error" are lifecycle states for
// other entities; an org that held one would be neither served nor suspended.
func TestSetOrgStatusRejectsNonServiceStates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	tenant := createOrg(t, inner, "acme")

	for _, bad := range []model.LifecycleStatus{model.StatusInactive, model.StatusError, "", "deleted"} {
		err := inner.System(ctx, func(sys store.SystemScope) error {
			_, e := sys.SetOrgStatus(ctx, tenant, bad)
			return e
		})
		if err == nil {
			t.Fatalf("SetOrgStatus(%q) must be rejected", bad)
		}
	}
	// The org is untouched by the refusals.
	if org := getOrg(t, inner, tenant); org.Status != model.StatusActive {
		t.Fatalf("a rejected status still changed the row: %q", org.Status)
	}
}

// TestSetOrgStatusIsIdempotentAndAudited: re-asserting the same state must not
// fail (the control plane may replay a webhook), and both directions must land
// on the tenant's own audit chain — "when did we stop serving them" and "when did
// we resume" are evidence questions.
func TestSetOrgStatusIsIdempotentAndAudited(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner := openStore(t)
	tenant := createOrg(t, inner, "acme")

	for i := range 2 {
		if err := inner.System(ctx, func(sys store.SystemScope) error {
			_, e := sys.SetOrgStatus(ctx, tenant, model.StatusSuspended)
			return e
		}); err != nil {
			t.Fatalf("suspend call %d: %v", i+1, err)
		}
	}
	org := getOrg(t, inner, tenant)
	if org.Status != model.StatusSuspended {
		t.Fatalf("status after two suspends = %q", org.Status)
	}
	// Idempotent means NO state change on the re-assertion: the second suspend must
	// not bump the version, or a control plane replaying a webhook would invalidate
	// a concurrent holder of the current version. CreateOrg leaves version 1, the
	// first suspend makes it 2, and the second must leave it there.
	if org.Version != 2 {
		t.Fatalf("version after two suspends = %d, want 2 (the no-op must not bump it)", org.Version)
	}
	if err := inner.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(ctx, tenant, model.StatusActive)
		return e
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// The chain carries both actions.
	var actions []string
	if err := inner.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			actions = append(actions, ev.Action)
			return nil
		})
	}); err != nil {
		t.Fatalf("read audit chain: %v", err)
	}
	var suspends, restores int
	for _, a := range actions {
		switch a {
		case "org.suspend_service":
			suspends++
		case "org.restore_service":
			restores++
		}
	}
	if suspends != 2 || restores != 1 {
		t.Fatalf("audit actions = %v; want 2 suspend + 1 restore", actions)
	}
}

// --- helpers -----------------------------------------------------------------

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
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

func createOrg(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = o.TenantID
		return nil
	}); err != nil {
		t.Fatalf("create org %q: %v", slug, err)
	}
	return tenant
}

// setStatus flips service state through the UNGUARDED store, so the tests that
// use it are exercising the guard, not the API in front of it.
func setStatus(t *testing.T, st store.Store, tenant model.TenantID, status model.LifecycleStatus) {
	t.Helper()
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.SetOrgStatus(context.Background(), tenant, status)
		return e
	}); err != nil {
		t.Fatalf("set status %q: %v", status, err)
	}
}

func getOrg(t *testing.T, st store.Store, tenant model.TenantID) model.Org {
	t.Helper()
	var org model.Org
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		o, e := sys.GetOrg(context.Background(), tenant)
		org = o
		return e
	}); err != nil {
		t.Fatalf("get org: %v", err)
	}
	return org
}

// agentNames reads through the UNGUARDED store on purpose: it is the only way to
// prove data SURVIVES a suspension without first lifting it.
func agentNames(t *testing.T, st store.Store, tenant model.TenantID) []string {
	t.Helper()
	var names []string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		as, _, e := sc.Agents().List(context.Background(), model.Query{})
		for _, a := range as {
			names = append(names, a.Name)
		}
		return e
	}); err != nil {
		t.Fatalf("list agents: %v", err)
	}
	return names
}

// TestSuspensionDoesNotBreakTheRestOfTheEstate pins the worst defect this change
// introduced before an adversarial panel found it: the estate-wide custodial
// loops enumerated EVERY org and aborted on the first error, so one suspended
// tenant stopped audit checkpointing and made a DR backup manifest impossible for
// the WHOLE install — every other customer silently lost tamper-evidence
// anchoring and certified backups for as long as one account was suspended.
//
// That is collateral damage on tenants who are paying, caused by a decision about
// one who is not, and it is far worse than the gap the feature closes.
//
// ⚠ THIS TEST USED TO PROVE NOTHING, and the external contrast of 2026-08-08 is
// what caught it. It enumerated under System and applied its OWN private copy of
// the `o.Status == active` filter, then called guarded.Mutate directly. It never
// imported core/audit or core/dr, so deleting the real filter in either of them
// left it green: it re-implemented the algorithm it claimed to be testing. A test
// that cannot fail for the reason it names reports green over nothing.
//
// It now drives the REAL estate-wide loops — audit.CheckpointAll and
// dr.BuildManifest — against a store with a withdrawn tenant in it. Both must
// cover the whole estate, and both must cover the withdrawn tenant too, because
// anchoring evidence is custodial and does not stop when service does.
func TestSuspensionDoesNotBreakTheRestOfTheEstate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	signer, pub := testSigner(t)
	inner := openSignedStore(t, signer)
	guarded := suspension.Guard(inner, nil)
	suspended := createOrg(t, inner, "acme")
	paying := createOrg(t, inner, "globex")
	appendEvent(t, inner, paying, "agent.created")
	setStatus(t, inner, suspended, model.StatusSuspended)

	// 1. The REAL checkpoint sweep, over the REAL guarded store.
	if err := signer.CheckpointAll(ctx, guarded); err != nil {
		t.Fatalf("one withdrawn tenant broke audit.CheckpointAll for the whole estate: %v", err)
	}
	// The paying tenant is anchored...
	if rep := checkpointReport(t, inner, paying, pub); !rep.OK {
		t.Fatalf("the paying tenant lost its anchor while another tenant was withdrawn: %s", rep.Reason)
	}
	// ...and so is the withdrawn one: its chain moved when it was suspended.
	if rep := checkpointReport(t, inner, suspended, pub); !rep.OK {
		t.Fatalf("the withdrawn tenant's chain was left unanchored: %s", rep.Reason)
	}

	// 2. The REAL DR manifest build.
	cpv, err := signer.CheckpointVerifier(ctx)
	if err != nil {
		t.Fatalf("checkpoint verifier: %v", err)
	}
	m, err := dr.BuildManifest(ctx, guarded, pub, cpv, dr.BuildOptions{
		EngineKind: "sqlite", TipMatch: dr.TipAdvisory, Now: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("one withdrawn tenant made a certified backup impossible for the whole estate: %v", err)
	}
	inManifest := map[string]dr.TenantTip{}
	for _, tip := range m.Tenants {
		inManifest[tip.Tenant] = tip
	}
	for name, tid := range map[string]model.TenantID{"paying": paying, "withdrawn": suspended} {
		tip, ok := inManifest[tid.String()]
		if !ok {
			t.Fatalf("the %s tenant is absent from the DR manifest: a backup that omits a tenant cannot certify it", name)
		}
		// A note is not a control record. The withdrawn tenant needs a REAL tip —
		// sequence, hash and verdict — or `dr verify` can report PASSED without ever
		// having looked at its chain.
		if !tip.VerifiedAtBackup {
			t.Fatalf("the %s tenant is in the manifest but was not verified at backup: %s", name, tip.VerifyReason)
		}
	}
	if inManifest[suspended.String()].HeadSeq == 0 {
		t.Fatal("the withdrawn tenant's manifest entry carries no chain tip; its continuity cannot be proven from this bundle")
	}
}
