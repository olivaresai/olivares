// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-00 DoD. Each test states the invariant the canonical identity plane owes
// its consumers (SG-01..SG-08), and each was RED before the fix that carries it:
// the first cut of registerIdentitySchema declared the alias table WITHOUT the
// unique index, exactly reproducing the find-then-create defect this plane
// exists to abolish (modules/inventory/entities.go:38-44 admits the same hole in
// the core entity tables: "there is no DB-level backstop").

// countRows returns how many rows of a kind the tenant holds.
func countRows(t *testing.T, m *Module, tenant model.TenantID, kind model.Kind, filters ...model.Filter) int {
	t.Helper()
	n := 0
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: filters, Limit: 1000})
		n = len(recs)
		return err
	}); err != nil {
		t.Fatalf("count %s: %v", kind, err)
	}
	return n
}

// (a) Concurrent re-delivery of the SAME observation yields ONE identity and ONE
// alias. N goroutines resolve the same binding; every one must return the same
// sid and the tables must hold a single row each.
func TestIdentity_ConcurrentRedelivery_OneRowOneAlias(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	b := SessionBinding{Provider: "claude", ExternalID: "sess-abc", At: baseTime}

	const n = 16
	sids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			sids[i], errs[i] = m.ResolveSession(ctx, tenant, b)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: ResolveSession: %v", i, err)
		}
	}
	for i, sid := range sids {
		if sid != sids[0] {
			t.Errorf("goroutine %d resolved %q, want the single canonical %q", i, sid, sids[0])
		}
	}
	if got := countRows(t, m, tenant, identityKind); got != 1 {
		t.Errorf("identity rows = %d, want 1 (a re-delivered observation must not mint a second session)", got)
	}
	if got := countRows(t, m, tenant, aliasKind); got != 1 {
		t.Errorf("alias rows = %d, want 1", got)
	}
}

// (a2) The LOSING side of a race, exercised deterministically.
//
// This test was REWRITTEN after an adversarial contrast showed the first version
// proved nothing: it pre-created the winner, so the final public call returned
// through ResolveSession's opening lookup and the whole recovery branch could
// have been deleted with the test still green.
//
// What this test DOES cover: the loser's mint fails on the alias unique index,
// and its transaction rolls back whole — no orphan identity survives.
//
// What it CANNOT cover, stated rather than implied: ResolveSession's recovery
// branch. That branch is only reachable when the opening lookup MISSES and the
// mint then loses, and once the winner has committed, a sequential caller's
// lookup never misses again — the branch is unreachable by construction here. A
// mutation check confirmed it: disabling the recovery block leaves this test
// GREEN. The branch is covered, with a verified mutation kill on BOTH engines,
// by TestIdentity_CrossBackend_ConcurrentRedeliveryDistinctTimestamps, where the
// lookups genuinely miss because the read and the write are separate
// transactions. Anyone tempted to delete that test should run the mutant first.
func TestIdentity_LoserAdoptsWinner_NoOrphanIdentity(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	b := SessionBinding{Provider: "claude", ExternalID: "sess-race", At: baseTime}

	// The loser's own first attempt: its lookup missed (nothing exists yet), so
	// it goes to mint. Meanwhile the winner commits the same triple first.
	winner, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("winner resolve: %v", err)
	}
	// mintIdentity is EXACTLY what the loser calls after its read missed. It must
	// fail on the alias unique index and roll back its identity with it.
	sid, err := m.mintIdentity(ctx, tenant, b)
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("loser mintIdentity err = %v (sid %q), want store.ErrConflict from the alias unique index", err, sid)
	}
	if got := countRows(t, m, tenant, identityKind); got != 1 {
		t.Fatalf("identity rows = %d, want 1: the losing transaction must roll back its identity too", got)
	}

	// A later resolve returns the committed winner (through the opening lookup,
	// NOT through recovery — see the note above).
	again, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("loser ResolveSession: %v", err)
	}
	if again != winner {
		t.Errorf("loser resolved %q, want the committed winner %q", again, winner)
	}
	if got := countRows(t, m, tenant, aliasKind); got != 1 {
		t.Errorf("alias rows = %d, want 1", got)
	}
}

// (b) Resume keeps its Olivares id. The same provider session id seen again —
// after any gap — resolves to the identity already minted, never to a new one.
func TestIdentity_Resume_KeepsSameSID(t *testing.T) {
	t.Parallel()

	m, _, tenant, clk := newSess(t)
	ctx := context.Background()
	b := SessionBinding{Provider: "claude", ExternalID: "sess-resume", At: baseTime}

	first, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	clk.set(baseTime.Add(72 * 3600e9)) // three days later
	b.At = clk.get()
	second, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second != first {
		t.Errorf("resume minted %q, want the original %q", second, first)
	}
	if got := countRows(t, m, tenant, identityKind); got != 1 {
		t.Errorf("identity rows = %d, want 1", got)
	}
}

// (c) Observed BEFORE declared: telemetry arrives first, the declaration adopts
// the identity that already exists instead of minting a rival one.
func TestIdentity_ObservedBeforeDeclared_DeclareAdopts(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	b := SessionBinding{Provider: "claude", ExternalID: "sess-late-declare", At: baseTime}

	observed, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	declared, err := m.DeclareSession(ctx, tenant, b)
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if declared != observed {
		t.Errorf("declare minted %q, want adoption of the observed %q", declared, observed)
	}
	if got := countRows(t, m, tenant, identityKind); got != 1 {
		t.Errorf("identity rows = %d, want 1", got)
	}
	// The adopted identity keeps its observed provenance and records that the
	// declaration arrived: origin is provenance, not authority.
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, ok, err := findIdentity(ctx, sc, observed)
		if err != nil || !ok {
			t.Fatalf("identity missing: %v", err)
		}
		if got := rec.String(colOrigin); got != OriginObserved {
			t.Errorf("origin = %q, want %q (the declaration adopts, it does not rewrite provenance)", got, OriginObserved)
		}
		if rec.String(colDeclaredAt) == "" {
			t.Error("declared_at empty: the adoption must record that a declaration arrived")
		}
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
}

// (d) Two providers handing out the SAME external id are two sessions.
func TestIdentity_SameExternalIDAcrossProviders_NoCollision(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()

	claude, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "abc", At: baseTime})
	if err != nil {
		t.Fatalf("claude: %v", err)
	}
	codex, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "codex", ExternalID: "abc", At: baseTime})
	if err != nil {
		t.Fatalf("codex: %v", err)
	}
	if claude == codex {
		t.Fatalf("claude:abc and codex:abc collapsed onto one identity %q", claude)
	}
	if got := countRows(t, m, tenant, identityKind); got != 2 {
		t.Errorf("identity rows = %d, want 2", got)
	}
	// Case folding of the provider is normalization, NOT a second provider.
	upper, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "CLAUDE", ExternalID: "abc", At: baseTime})
	if err != nil {
		t.Fatalf("CLAUDE: %v", err)
	}
	if upper != claude {
		t.Errorf("provider %q resolved %q, want the same session as %q", "CLAUDE", upper, claude)
	}
}

// (e) The NEGATIVE, and the one that actually proves the guarantee: inserting
// the same triple twice must fail in the ENGINE. A test that only ever goes
// through the writer's own find-then-create proves the writer is careful, not
// that the database enforces anything — and the writer is exactly what a second
// process bypasses.
func TestIdentity_DuplicateTriple_RejectedByTheEngine(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	b := SessionBinding{Provider: "claude", ExternalID: "sess-dup", At: baseTime}

	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		return bindAlias(ctx, sc, "osn_first", b)
	})
	if err != nil {
		t.Fatalf("first raw bind: %v", err)
	}
	// Second insert of the SAME (tenant, provider, external_id), deliberately
	// bypassing every read the writer would do, pointed at a DIFFERENT session.
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		return bindAlias(ctx, sc, "osn_second", b)
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second raw bind err = %v, want store.ErrConflict: the UNIQUE (tenant_id, provider, external_id) index is the guarantee, not the writer's care", err)
	}
	if got := countRows(t, m, tenant, aliasKind); got != 1 {
		t.Errorf("alias rows = %d, want 1", got)
	}
}

// An alias binds ONCE. Re-pointing a bound triple at another session would
// silently re-attribute work already written to the ledger, so it is refused.
func TestIdentity_BindAlias_NeverRepoints(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()

	a, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "one", At: baseTime})
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	b2, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "two", At: baseTime})
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	// Re-binding "one" onto session b2 must be refused, not applied.
	err = m.BindAlias(ctx, tenant, b2, SessionBinding{Provider: "claude", ExternalID: "one", At: baseTime})
	if !errors.Is(err, ErrAliasBound) {
		t.Fatalf("repoint err = %v, want ErrAliasBound", err)
	}
	got, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "one", At: baseTime})
	if err != nil {
		t.Fatalf("re-resolve: %v", err)
	}
	if got != a {
		t.Errorf("after a refused repoint, one resolves to %q, want %q", got, a)
	}
	// Binding a NEW provider id onto an existing session is the supported case.
	if err := m.BindAlias(ctx, tenant, a, SessionBinding{Provider: "codex", ExternalID: "one-codex", At: baseTime}); err != nil {
		t.Fatalf("additional alias: %v", err)
	}
	got, err = m.ResolveSession(ctx, tenant, SessionBinding{Provider: "codex", ExternalID: "one-codex", At: baseTime})
	if err != nil {
		t.Fatalf("resolve additional: %v", err)
	}
	if got != a {
		t.Errorf("additional alias resolved %q, want %q", got, a)
	}
}

// A binding with no provider is refused: without it the key degenerates to the
// bare external id, which is the collision the plane exists to prevent.
func TestIdentity_BindingValidation(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newSess(t)
	ctx := context.Background()
	if _, err := m.ResolveSession(ctx, tenant, SessionBinding{ExternalID: "x"}); !errors.Is(err, ErrNoProvider) {
		t.Errorf("empty provider err = %v, want ErrNoProvider", err)
	}
	if _, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "  "}); !errors.Is(err, ErrNoExternalID) {
		t.Errorf("blank external id err = %v, want ErrNoExternalID", err)
	}
}
