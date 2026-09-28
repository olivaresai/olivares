// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inventory

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func d08Scope() sdkmodel.InventoryScope {
	return sdkmodel.InventoryScope{Contract: sdkmodel.AzureInventoryContract, Family: "azure.resource", Selectors: []string{"sub-1"}}
}
func d08Run(tenant model.TenantID, id string) event.InventoryRun {
	return event.InventoryRun{ID: id, Tenant: tenant.String(), Registration: event.SourceRegistration{SourceID: "d08-source", SourceRevision: 1, EnvironmentRef: "env-a"}, StartedAt: baseTime}
}
func d08Start(t *testing.T, m *Module, run event.InventoryRun) {
	t.Helper()
	if err := m.BeginRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := m.StartCollection(context.Background(), run, sdkmodel.InventoryCollectionStart{Scope: d08Scope(), ObservedAt: baseTime}); err != nil {
		t.Fatal(err)
	}
}
func d08Finish(count int64) event.InventoryFinish {
	return event.InventoryFinish{Expected: count, FinishedAt: baseTime.Add(time.Minute), Report: sdkmodel.InventoryCollectionReport{State: "complete", Reason: "exhausted", Count: count, RequestedScope: d08Scope().Fingerprint(), FulfilledScope: d08Scope().Fingerprint(), ObservedUntil: baseTime.Add(time.Minute)}}
}
func d08Read(t *testing.T, h *c3HTTP, token string, tenant model.TenantID) map[string]any {
	t.Helper()
	r := h.do("GET", "/v1/m/inventory/collections?source_id=d08-source&source_revision=1&environment_ref=env-a", token, nil, tenant)
	if r.code != 200 {
		t.Fatalf("collection read: %d %s", r.code, r.raw)
	}
	return r.body["items"].([]any)[0].(map[string]any)
}
func d08Consumer(t *testing.T, m *Module, bus eventbus.Bus) *runtime.Runtime {
	t.Helper()
	rt := runtime.New(runtime.Options{Bus: bus})
	if err := rt.AddModule(m, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	return rt
}
func d08Event(run event.InventoryRun, id string, ordinal int64) event.Event {
	edge := mkEdge("azure.subscription", "sub-1", "azure.resource", "/subscriptions/sub-1/providers/test/things/"+id, sdkmodel.ModeUnknown, "azure", "", baseTime)
	e := event.FromObservation(run.Tenant, "d08-source", edge)
	e.ID = id
	e.SourceRegistration = run.Registration.Clone()
	e.InventoryMember = &event.InventoryMember{RunID: run.ID, Ordinal: ordinal, Digest: event.InventoryMemberDigest(e)}
	return e
}

func TestInventoryCoverageDurableRestart(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	path := filepath.Join(t.TempDir(), "coverage.db")
	m, st := c1Open(t, path, false)
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "d08-restart")
	first := d08Run(tenant, "before-restart")
	d08Start(t, m, first)
	if err := m.FinishRun(context.Background(), first, d08Finish(0)); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, reopened := c1Open(t, path, false)
	h2 := newC3HTTP(t, resumed, reopened)
	next := d08Run(tenant, "after-restart")
	next.StartedAt = baseTime.Add(-time.Hour)
	d08Start(t, resumed, next)
	item := d08Read(t, h2, admin, tenant)
	current := item["current"].(map[string]any)
	if current["run_order"] != float64(2) || current["coverage"] != "unknown" || item["last_qualified_success"].(map[string]any)["run_id"] != first.ID {
		t.Fatalf("restart lost durable order/history: %+v", item)
	}
}

func TestInventoryCoveragePostgresConcurrent(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	cfg := c1PostgresConfig(t)
	m, st := c1PostgresOpen(t, cfg, false)
	other := New()
	other.UseData(api.NewModuleData(st))
	h := newC3HTTP(t, m, st)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "d08-concurrent")
	a, b := eventbus.NewInProc(eventbus.Options{}), eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	d08Consumer(t, m, a)
	d08Consumer(t, other, b)
	for i := 0; i < 8; i++ {
		run := d08Run(tenant, fmt.Sprintf("parallel-%d", i))
		d08Start(t, m, run)
		e1, e2 := d08Event(run, fmt.Sprintf("a-%d", i), 1), d08Event(run, fmt.Sprintf("b-%d", i), 2)
		for _, e := range []event.Event{e1, e2} {
			if err := m.AdmitMember(context.Background(), run, e.ID, *e.InventoryMember); err != nil {
				t.Fatal(err)
			}
		}
		gate := make(chan struct{})
		errs := make(chan error, 3)
		var wg sync.WaitGroup
		for _, fn := range []func() error{func() error { return a.Publish(context.Background(), e1) }, func() error { return b.Publish(context.Background(), e2) }, func() error { return other.FinishRun(context.Background(), run, d08Finish(2)) }} {
			wg.Add(1)
			go func(f func() error) { defer wg.Done(); <-gate; errs <- f() }(fn)
		}
		close(gate)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			item := d08Read(t, h, admin, tenant)
			current := item["current"].(map[string]any)
			if current["committed_count"] == float64(2) && current["projection"] == "committed" && item["last_qualified_success"] != nil && item["last_qualified_success"].(map[string]any)["run_id"] == run.ID {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("lost concurrent member or terminal: %+v", item)
			}
			time.Sleep(time.Millisecond * 5)
		}
		// Identical replay through PostgreSQL must
		// still refresh C1 once, without changing collection membership or success.
		if err := a.Publish(context.Background(), e1); err != nil {
			t.Fatal(err)
		}
		for {
			r := c1Receipt(t, st, tenant, e1.ID)
			if r.Int(colDeliveries) == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("identical linkage replay rejected")
			}
			time.Sleep(time.Millisecond * 5)
		}
		item := d08Read(t, h, admin, tenant)
		current := item["current"].(map[string]any)
		if current["committed_count"] != float64(2) || current["rejection_reason"] != nil {
			t.Fatalf("replay changed qualified population: %+v", item)
		}
	}
}

func TestInventoryCoverageMissingLinkDoesNotReplayC1(t *testing.T) {
	m, st, tenant := newInv(t)
	run := d08Run(tenant, "linked")
	d08Start(t, m, run)
	e := d08Event(run, "linked-event", 1)
	if err := m.AdmitMember(context.Background(), run, e.ID, *e.InventoryMember); err != nil {
		t.Fatal(err)
	}
	bus := eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	d08Consumer(t, m, bus)
	// Delivery goes through the real subscribed consumer, not the private reducer.
	if err := bus.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var receipt model.Record
	for {
		rows := c1Rows(t, st, tenant, observationReceiptKind)
		if len(rows) == 1 {
			receipt = rows[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt missing")
		}
		time.Sleep(time.Millisecond)
	}
	beforeHash, beforeFacts, beforeDeliveries := receipt.String(colFactsHash), receipt.String(colFacts), receipt.Int(colDeliveries)
	plain := e
	plain.InventoryMember = nil
	if err := bus.Publish(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	for {
		rows := c1Rows(t, st, tenant, collectionRejectionKind)
		if len(rows) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing linkage was not rejected")
		}
		time.Sleep(time.Millisecond)
	}
	after := c1Receipt(t, st, tenant, e.ID)
	if after.String(colFactsHash) != beforeHash || after.String(colFacts) != beforeFacts || after.Int(colDeliveries) != beforeDeliveries {
		t.Fatal("missing linkage changed C1 facts or delivery count")
	}
	// The host's exact admission also prevents a fabricated event population.
	if err := m.AdmitMember(context.Background(), run, "substitute", *e.InventoryMember); err == nil {
		t.Fatal("same ordinal accepted under a different event ID")
	}
}

func TestInventoryCoverageEmptyKeepsStaleResources(t *testing.T) {
	m, st, tenant := newInv(t, WithClock(pinnedClock{at: baseTime}))
	bus := eventbus.NewInProc(eventbus.Options{})
	t.Cleanup(func() { _ = bus.Close() })
	d08Consumer(t, m, bus)
	first := d08Run(tenant, "observed")
	d08Start(t, m, first)
	e := d08Event(first, "known-resource", 1)
	if err := m.AdmitMember(context.Background(), first, e.ID, *e.InventoryMember); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		rows := c1Rows(t, st, tenant, observationReceiptKind)
		if len(rows) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receipt missing")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.FinishRun(context.Background(), first, d08Finish(1)); err != nil {
		t.Fatal(err)
	}
	before := c1Rows(t, st, tenant, catalogEntryKind)
	if len(before) != 1 {
		t.Fatalf("positive edge materialized %d rows", len(before))
	}
	empty := d08Run(tenant, "empty-after-observation")
	d08Start(t, m, empty)
	if err := m.FinishRun(context.Background(), empty, d08Finish(0)); err != nil {
		t.Fatal(err)
	}
	if marked, err := m.Sweep(context.Background(), baseTime.Add(defaultStaleAfter+time.Hour)); err != nil || marked != len(before) {
		t.Fatalf("freshness sweep marked=%d error=%v", marked, err)
	}
	after := c1Rows(t, st, tenant, catalogEntryKind)
	if len(after) != len(before) {
		t.Fatal("empty enumeration removed positive rows")
	}
	ids := map[string]bool{}
	for _, r := range before {
		ids[r.String(model.ColID)] = true
	}
	for _, r := range after {
		if !ids[r.String(model.ColID)] || r.String(colStatus) != statusStale {
			t.Fatal("stale became absence or identity changed", r)
		}
	}
	if len(c1Rows(t, st, tenant, observationReceiptKind)) != 1 {
		t.Fatal("empty pass rewrote C1 receipts")
	}
}

type coverageDeclarationRegistry struct {
	store.ExtensionRegistry
	t    *testing.T
	seen map[model.Kind]bool
}

func (r coverageDeclarationRegistry) Register(d model.EntityDescriptor) error {
	for _, err := range d.PrincipalDefects() {
		r.t.Error(err)
	}
	switch d.Kind {
	case collectionRunKind, collectionSourceKind, collectionScopeKind, collectionAdmissionKind, collectionRejectionKind:
		r.seen[d.Kind] = true
		for _, idx := range d.Indexes {
			if len(idx.Columns) == 0 || idx.Columns[0] != model.ColTenantID {
				r.t.Errorf("%s index %s lacks tenant prefix", d.Kind, idx.Name)
			}
		}
	}
	return nil
}
func TestInventoryCoverageSchemaDeclarations(t *testing.T) {
	reg := coverageDeclarationRegistry{t: t, seen: map[model.Kind]bool{}}
	if err := New().RegisterSchema(reg); err != nil {
		t.Fatal(err)
	}
	if len(reg.seen) != 5 {
		t.Fatalf("collection relation count=%d", len(reg.seen))
	}
}
