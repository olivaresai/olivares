// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package siemforward

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/eventing"
)

// forwardHarness wires a real store with the eventing + siemforward schemas, the
// eventing engine (with the real SIEM renderer) and the siemforward module, plus a
// business tenant — enough to drive ForwardDue against a live audit ledger.
type forwardHarness struct {
	t      *testing.T
	st     store.Store
	evt    *eventing.Module
	sf     *Module
	tenant model.TenantID
}

func newForwardHarness(t *testing.T) *forwardHarness {
	t.Helper()
	ctx := context.Background()

	evt := eventing.New(
		eventing.WithAuthorizer(auth.NewAuthorizer(nil)),
		eventing.WithSecretSealer(fakeSealer{}),
		eventing.WithSinkRenderer(NewRenderer()),
		eventing.WithAllowLoopback(true),
	)
	sf := New(evt)

	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if e := evt.RegisterSchema(reg); e != nil {
			return e
		}
		return sf.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		org, e := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	evt.UseData(api.NewModuleData(st))
	sf.UseData(api.NewModuleData(st))
	// The two store-dependent seams, wired as the composition root wires them
	// (cmd/olivares/boot.go). Leaving the fence nil is the one configuration in which a
	// governed write costs nothing, so a harness that skips it tests a composition that
	// does not ship. On a fresh database the fence is ARMED by classification, which is
	// exactly what this harness builds.
	evt.UseEgressWriterFence(writerFenceOf(st))
	return &forwardHarness{t: t, st: st, evt: evt, sf: sf, tenant: tenant}
}

// writerFenceOf adapts the store to the fence's durable state, the same three lines the
// composition root uses (cmd/olivares/eventingegress.go). It is here rather than imported
// because that adapter lives in package main.
type writerFenceSource struct{ src store.RolloutStater }

func (r writerFenceSource) EgressWriterFence(ctx context.Context) (store.RolloutState, error) {
	if r.src == nil {
		return store.RolloutState{}, nil
	}
	return r.src.RolloutState(ctx, eventing.EgressWriterFenceControlKey)
}

func writerFenceOf(st store.Store) eventing.EgressWriterFenceSource {
	rs, ok := st.(store.RolloutStater)
	if !ok {
		return nil
	}
	return writerFenceSource{src: rs}
}

// appendAudit seals n audit events on the tenant's ledger and returns nothing —
// they become the records ForwardDue walks.
func (h *forwardHarness) appendAudit(n int) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, e := sc.Audit().Append(ctx, model.AuditDraft{Actor: "system", ActorKind: "system", Action: "test.event"}); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		h.t.Fatal(err)
	}
}

// ForwardDue walks the ledger from the cursor, advances it, and is idempotent once
// caught up. The walk is the at-least-once driver: a second pass over the same
// records forwards nothing new.
func TestForwardDueWalksAndAdvancesCursor(t *testing.T) {
	h := newForwardHarness(t)
	ctx := context.Background()
	h.drainBaseline() // forward the org-provisioning genesis event(s) first
	h.appendAudit(3)

	n, err := h.sf.ForwardDue(ctx, h.tenant)
	if err != nil || n != 3 {
		t.Fatalf("first pass: n=%d err=%v, want 3/nil", n, err)
	}
	// Cursor caught up: nothing new.
	if n, err := h.sf.ForwardDue(ctx, h.tenant); err != nil || n != 0 {
		t.Fatalf("second pass: n=%d err=%v, want 0/nil", n, err)
	}

	// New records after the cursor are picked up; the cursor resumes, never restarts.
	h.appendAudit(2)
	if n, err := h.sf.ForwardDue(ctx, h.tenant); err != nil || n != 2 {
		t.Fatalf("incremental pass: n=%d err=%v, want 2/nil", n, err)
	}
}

// With a ledger sink subscription, ForwardDue enqueues a delivery per walked record
// through the durable engine (and dedups on re-walk).
func TestForwardDueEnqueuesDeliveries(t *testing.T) {
	h := newForwardHarness(t)
	ctx := context.Background()

	// A ledger sink subscription: audit.recorded → a Splunk HEC sink. Created before
	// any forwarding, so EVERY ledger record (the org-genesis event + the appended
	// ones) is enqueued exactly once.
	h.createLedgerSub(t)
	h.appendAudit(2)

	if _, err := h.sf.ForwardDue(ctx, h.tenant); err != nil {
		t.Fatal(err)
	}
	first := h.deliveryCount()
	if first == 0 {
		t.Fatal("expected at least one ledger delivery enqueued")
	}
	// Re-walk after a reset cursor must dedup (idempotent intake): the SAME records
	// produce no new deliveries.
	if err := h.resetCursor(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sf.ForwardDue(ctx, h.tenant); err != nil {
		t.Fatal(err)
	}
	if got := h.deliveryCount(); got != first {
		t.Fatalf("delivery rows after re-walk = %d, want %d (dedup)", got, first)
	}
}

// drainBaseline forwards any pre-existing ledger records (the org-provisioning
// genesis event) so a test starts from a quiescent cursor.
func (h *forwardHarness) drainBaseline() {
	ctx := context.Background()
	for {
		n, err := h.sf.ForwardDue(ctx, h.tenant)
		if err != nil {
			h.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// createLedgerSub inserts an audit.recorded subscription directly into the eventing
// subscription table (the kind is registered by the engine; the API server is not
// wired in this store-level harness).
func (h *forwardHarness) createLedgerSub(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// A subscription is a GOVERNED row: the egress writer fence refuses one that carries
	// no capability attestation, whoever writes it. This fixture writes it directly rather
	// than through the module's handler, so it is a writer in its own right and must prove
	// it carries the gate — the same thing the CLI does, through the same exported API.
	//
	// The generation is read BEFORE the transaction, never inside it. Reading it takes a
	// connection from the pool, and the store pins SQLite to ONE, so an in-transaction read
	// waits for the connection its own transaction is holding: an unbounded hang, not a
	// failure. That was this unit's own P0.
	gen, err := eventing.FenceGeneration(ctx, writerFenceOf(h.st))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("eventing.subscription"))
		if err != nil {
			return err
		}
		rec := model.Record{
			"name": "ledger", "enabled": true, "event_types": "audit.recorded",
			"match_sources": "", "endpoint": "https://splunk:8088",
			"secret_sealed": "sealed", "secret_hint": "hint", "role": auth.RoleAdmin,
			"description": "", "owner_actor": "system", "owner_actor_kind": "system",
		}
		if err := eventing.StampWriterProof(ctx, sc, rec, gen); err != nil {
			return err
		}
		_, err = repo.Create(ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (h *forwardHarness) deliveryCount() int {
	ctx := context.Background()
	var n int
	if err := h.st.View(ctx, h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("eventing.delivery"))
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1000})
		n = len(recs)
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *forwardHarness) resetCursor() error {
	ctx := context.Background()
	return h.st.Mutate(ctx, h.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(cursorKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil || len(recs) == 0 {
			return err
		}
		rec := recs[0]
		rec[colCurLastForwarded] = int64(0)
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// fakeSealer is a no-op sealer for the store-level harness (the renderer opens the
// sink credential, which this test does not exercise on the delivery path).
type fakeSealer struct{}

func (fakeSealer) Seal(_ context.Context, _ model.TenantID, pt []byte) (string, error) {
	return string(pt), nil
}
func (fakeSealer) Open(_ context.Context, _ model.TenantID, s string) ([]byte, error) {
	return []byte(s), nil
}
