// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// D-02 — the built-in high-tier floor ("TWO high+ findings for an agent within
// the window → auto-stop") must count on the CANONICAL (tenant, agent-uuid) key
// and DURABLY, so it cannot be evaded by (a) referencing the agent under two
// different identifiers, (b) a same external id across tenants, or (c) a process
// restart between the two findings.

// advance moves the deterministic in-package clock forward (window tests).
func (c *intClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type tierFloorFixture struct {
	t      *testing.T
	st     store.Store
	host   *capturingHost
	clk    *intClock
	m      *Module
	tenant model.TenantID
}

func newTierFloorFixture(t *testing.T) *tierFloorFixture {
	t.Helper()
	ctx := context.Background()
	clk := &intClock{t: intBase}
	m := New(WithClock(clk))
	st, err := engine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	f := &tierFloorFixture{t: t, st: st, clk: clk, host: &capturingHost{}}
	m.UseData(api.NewModuleData(st))
	if err := m.Init(ctx, f.host); err != nil {
		t.Fatal(err)
	}
	f.m = m
	f.tenant = f.newTenant("acme")
	return f
}

func (f *tierFloorFixture) newTenant(slug string) model.TenantID {
	f.t.Helper()
	var tenant model.TenantID
	if err := f.st.System(context.Background(), func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = model.TenantID(org.ID)
		return e
	}); err != nil {
		f.t.Fatal(err)
	}
	return tenant
}

// reboot swaps in a NEW module instance sharing the SAME store — simulating a
// process restart. Any in-memory counter state is discarded; only what the store
// persisted survives.
func (f *tierFloorFixture) reboot() {
	f.t.Helper()
	m := New(WithClock(f.clk))
	m.UseData(api.NewModuleData(f.st))
	f.host = &capturingHost{}
	if err := m.Init(context.Background(), f.host); err != nil {
		f.t.Fatal(err)
	}
	f.m = m
}

func (f *tierFloorFixture) createAgent(tenant model.TenantID, name, ext, tier string) model.ID {
	f.t.Helper()
	var id model.ID
	if err := f.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Create(context.Background(), model.Agent{
			Name: name, Kind: "claude-code", ExternalID: ext, Status: model.StatusActive, RiskTier: tier,
		})
		if err != nil {
			return err
		}
		id = a.ID
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// fire delivers one finding about subjectRef to the tenant through the real bus
// entry point (onGuardianFinding → checkTierFloor). No guardian rules exist, so
// only the built-in tier floor can act.
func (f *tierFloorFixture) fire(tenant model.TenantID, sev sdkmodel.Severity, subjectRef, detailHash string) {
	f.t.Helper()
	f.m.onGuardianFinding(context.Background(), event.FromObservation(tenant.String(), "olivares.security", sdkmodel.FindingReport{
		Kind: "guardrail_violation", Severity: sev, SubjectKind: "agent", SubjectRef: subjectRef,
		Title: "test finding", DetailHash: detailHash, OccurredAt: f.clk.Now().Time(),
	}))
}

func (f *tierFloorFixture) stops(tenant model.TenantID) []model.Record {
	f.t.Helper()
	var out []model.Record
	if err := f.st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		out, err = listAll(context.Background(), repo, eq(colKSStatus, ksStatusActive))
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	return out
}

// (1) A finding by UUID and one by external-ID of the SAME agent SUM: two HIGH
// findings reach the high-tier stop threshold (2 within the window). The old
// tracker keyed the count on the raw ref, so the UUID bucket and the external-id
// bucket never summed and the mandatory stop was evaded.
func TestTierFloorCanonicalKeyUUIDAndExternalSum(t *testing.T) {
	f := newTierFloorFixture(t)
	uuid := f.createAgent(f.tenant, "prod-bot", "prod-agent", string(RiskTierHigh))

	f.fire(f.tenant, sdkmodel.SeverityHigh, uuid.String(), "d-uuid")
	if got := f.stops(f.tenant); len(got) != 0 {
		t.Fatalf("one HIGH finding must not stop a high-tier agent: %d stops", len(got))
	}
	f.fire(f.tenant, sdkmodel.SeverityHigh, "prod-agent", "d-ext")
	got := f.stops(f.tenant)
	if len(got) != 1 {
		t.Fatalf("UUID + external-ID HIGH findings must SUM to the stop threshold: %d stops", len(got))
	}
	if got[0].String(colKSSource) != ksSourceTierFloor || got[0].String(colKSScopeKind) != ksScopeAgent {
		t.Fatalf("stop is not a tier-floor agent stop: %+v", got[0])
	}
	// The single stop is canonically the same agent under either identifier.
	st, err := f.m.KillSwitchState(context.Background(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, s := st.Stopped("prod-agent"); !s {
		t.Fatalf("stop must bite by external id")
	}
	if _, s := st.Stopped(uuid.String()); !s {
		t.Fatalf("stop must bite by UUID")
	}
}

// (2) Two tenants with the SAME external id do NOT sum: a single HIGH finding in
// each tenant must not stop the wrong tenant's agent. The old tracker was a
// process-global map keyed on the raw external id, so tenant B's finding pushed
// the shared bucket to 2 and stopped B's agent after a single finding.
func TestTierFloorNoCrossTenantCollision(t *testing.T) {
	f := newTierFloorFixture(t)
	tenantB := f.newTenant("beta")
	f.createAgent(f.tenant, "a-bot", "prod-agent", string(RiskTierHigh))
	f.createAgent(tenantB, "b-bot", "prod-agent", string(RiskTierHigh))

	f.fire(f.tenant, sdkmodel.SeverityHigh, "prod-agent", "d-a")
	f.fire(tenantB, sdkmodel.SeverityHigh, "prod-agent", "d-b")

	if got := f.stops(f.tenant); len(got) != 0 {
		t.Fatalf("tenant A must not be stopped by a single finding: %d stops", len(got))
	}
	if got := f.stops(tenantB); len(got) != 0 {
		t.Fatalf("tenant B must not be stopped by a single finding (cross-tenant collision): %d stops", len(got))
	}
}

// (3) Durability: a HIGH finding, then a process restart (a fresh module instance
// preserving the store), then a second HIGH finding — the mandatory high-tier
// stop STILL fires. The old in-memory tracker lost the first signal on restart,
// so the 2-in-window stop was evaded.
func TestTierFloorSurvivesRestart(t *testing.T) {
	f := newTierFloorFixture(t)
	f.createAgent(f.tenant, "svc-bot", "restart-agent", string(RiskTierHigh))

	f.fire(f.tenant, sdkmodel.SeverityHigh, "restart-agent", "d-1")
	if got := f.stops(f.tenant); len(got) != 0 {
		t.Fatalf("first HIGH finding must not stop yet: %d stops", len(got))
	}

	f.reboot() // process restart: in-memory state gone, store preserved.

	f.fire(f.tenant, sdkmodel.SeverityHigh, "restart-agent", "d-2")
	if got := f.stops(f.tenant); len(got) != 1 {
		t.Fatalf("durable count must survive restart and fire the stop: %d stops", len(got))
	}
}

// (4) The window still bounds the count durably (the durable analog of the old
// in-memory tracker's eviction): two HIGH findings more than the window apart do
// NOT sum.
func TestTierFloorWindowEvictionDurable(t *testing.T) {
	f := newTierFloorFixture(t)
	f.createAgent(f.tenant, "win-bot", "win-agent", string(RiskTierHigh))

	f.fire(f.tenant, sdkmodel.SeverityHigh, "win-agent", "d-1")
	f.clk.advance(tierFloorHighWindow + time.Minute) // first signal ages out of the window
	f.fire(f.tenant, sdkmodel.SeverityHigh, "win-agent", "d-2")
	if got := f.stops(f.tenant); len(got) != 0 {
		t.Fatalf("findings more than the window apart must not sum: %d stops", len(got))
	}
	// A third within-window of the second → two in the window → stop.
	f.fire(f.tenant, sdkmodel.SeverityHigh, "win-agent", "d-3")
	if got := f.stops(f.tenant); len(got) != 1 {
		t.Fatalf("two findings within the window must stop: %d stops", len(got))
	}
}

// A critical-tier agent stops on a SINGLE high+ finding (unchanged floor
// doctrine; a regression guard over the restructured check).
func TestTierFloorCriticalStopsOnFirst(t *testing.T) {
	f := newTierFloorFixture(t)
	f.createAgent(f.tenant, "crit-bot", "crit-agent", string(RiskTierCritical))
	f.fire(f.tenant, sdkmodel.SeverityHigh, "crit-agent", "d-1")
	if got := f.stops(f.tenant); len(got) != 1 {
		t.Fatalf("critical-tier agent must stop on the first high finding: %d stops", len(got))
	}
}

// A re-delivered finding (same fingerprint) does NOT double-count: two deliveries
// of the SAME finding leave a high-tier agent below the stop threshold.
func TestTierFloorReDeliveredFindingIdempotent(t *testing.T) {
	f := newTierFloorFixture(t)
	f.createAgent(f.tenant, "idem-bot", "idem-agent", string(RiskTierHigh))
	f.fire(f.tenant, sdkmodel.SeverityHigh, "idem-agent", "d-same")
	f.fire(f.tenant, sdkmodel.SeverityHigh, "idem-agent", "d-same") // same fingerprint
	if got := f.stops(f.tenant); len(got) != 0 {
		t.Fatalf("a re-delivered finding must not double-count into a stop: %d stops", len(got))
	}
	// A genuinely distinct finding tips it over.
	f.fire(f.tenant, sdkmodel.SeverityHigh, "idem-agent", "d-other")
	if got := f.stops(f.tenant); len(got) != 1 {
		t.Fatalf("two distinct findings within the window must stop: %d stops", len(got))
	}
}
