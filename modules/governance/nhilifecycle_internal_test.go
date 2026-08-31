// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// intClock is a deterministic in-package clock for the staleness/event tests.
type intClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *intClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

var intBase = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

// --- staleness state machine (recomputeStaleness) ----------------------------

func TestRecomputeStalenessStateMachine(t *testing.T) {
	clk := &intClock{t: intBase}
	m := New(WithClock(clk))
	now := clk.Now()

	// Fresh HIGH credential → ok, no enforcement.
	rec := newLifecycleRecord("nhi:1", "vault", "")
	rec[colNHICriticality] = string(RiskTierHigh)
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-10 * 24 * time.Hour)).String()
	changed, fs := m.recomputeStaleness(rec, now)
	if rec[colNHIStaleStatus] != staleOK || len(fs) != 0 {
		t.Fatalf("fresh should be ok with no findings, got %v %+v", rec[colNHIStaleStatus], fs)
	}
	_ = changed

	// Stale HIGH (100d, window 90d) → alert + a stale finding; block_after set 30d out.
	rec = newLifecycleRecord("nhi:2", "vault", "")
	rec[colNHICriticality] = string(RiskTierHigh)
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-100 * 24 * time.Hour)).String()
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIEnforce] != enforceAlert || !hasKind(fs, "nhi_credential_stale") {
		t.Fatalf("stale HIGH should alert, got enforce=%v fs=%+v", rec[colNHIEnforce], fs)
	}
	if rec[colNHIBlockAfter] == nil {
		t.Fatalf("expected a block_after deadline")
	}

	// CRITICAL stale → immediate block (no grace).
	rec = newLifecycleRecord("nhi:3", "vault", "")
	rec[colNHICriticality] = string(RiskTierCritical)
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-40 * 24 * time.Hour)).String()
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIEnforce] != enforceBlocked || !hasKind(fs, "nhi_credential_blocked") {
		t.Fatalf("CRITICAL stale should block immediately, got %v", rec[colNHIEnforce])
	}

	// Already-stale HIGH past its 30-day deadline → escalate alert → block.
	rec = newLifecycleRecord("nhi:4", "vault", "")
	rec[colNHICriticality] = string(RiskTierHigh)
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-100 * 24 * time.Hour)).String()
	rec[colNHIStaleStatus] = staleStale
	rec[colNHIEnforce] = enforceAlert
	rec[colNHIBlockAfter] = model.NewTimestamp(intBase.Add(-time.Hour)).String() // deadline passed
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIEnforce] != enforceBlocked || !hasKind(fs, "nhi_credential_blocked") {
		t.Fatalf("stale past 30d should escalate to block, got %v", rec[colNHIEnforce])
	}

	// Unknown rotation recency → unknown + a coverage finding, never a silent fresh.
	rec = newLifecycleRecord("nhi:5", "vault", "")
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIStaleStatus] != staleUnknown || !hasKind(fs, "nhi_rotation_unknown") {
		t.Fatalf("unknown rotated_at should be unknown coverage, got %v", rec[colNHIStaleStatus])
	}

	// A credential reclassified to CRITICAL while ALREADY stale must block on the
	// next sweep, not wait out the non-critical grace window it inherited.
	rec = newLifecycleRecord("nhi:7", "vault", "")
	rec[colNHICriticality] = string(RiskTierCritical) // operator just raised it
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-100 * 24 * time.Hour)).String()
	rec[colNHIStaleStatus] = staleStale
	rec[colNHIEnforce] = enforceAlert                                                     // it went stale while non-critical
	rec[colNHIBlockAfter] = model.NewTimestamp(intBase.Add(20 * 24 * time.Hour)).String() // deadline still 20d away
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIEnforce] != enforceBlocked || !hasKind(fs, "nhi_credential_blocked") {
		t.Fatalf("reclassify-to-CRITICAL while stale must block immediately, got %v", rec[colNHIEnforce])
	}

	// A staleness block clears when the credential is freshly rotated (but never an
	// offboard block).
	rec = newLifecycleRecord("nhi:6", "vault", "")
	rec[colNHICriticality] = string(RiskTierHigh)
	rec[colNHIRotatedAt] = model.NewTimestamp(intBase.Add(-1 * 24 * time.Hour)).String()
	rec[colNHIStaleStatus] = staleStale
	rec[colNHIEnforce] = enforceBlocked
	rec[colNHIEnforceWhy] = "stale credential (escalated to block after 30 days)"
	_, fs = m.recomputeStaleness(rec, now)
	if rec[colNHIEnforce] != enforceMonitor {
		t.Fatalf("fresh rotation should clear a staleness block, got %v", rec[colNHIEnforce])
	}
}

func hasKind(fs []pendingNHIFinding, kind string) bool {
	for _, f := range fs {
		if f.kind == kind {
			return true
		}
	}
	return false
}

// --- event-driven lifecycle trigger (onLifecycleSignal) ----------------------

// capturingHost is a minimal in-package sdk.Host capturing published events.
type capturingHost struct {
	mu     sync.Mutex
	events []event.Event
}

func (h *capturingHost) Publish(_ context.Context, e event.Event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return nil
}
func (h *capturingHost) Subscribe([]event.Type, event.Handler) (func(), error) {
	return func() {}, nil
}
func (h *capturingHost) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
func (h *capturingHost) Config() sdk.Config { return sdk.Config{} }

func TestOnLifecycleSignalBlocksAndOrphans(t *testing.T) {
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
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = model.TenantID(org.ID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	host := &capturingHost{}
	if err := m.Init(ctx, host); err != nil {
		t.Fatal(err)
	}

	// Seed: a managed NHI, and a managed NHI sponsored by a human.
	seed := func(ext, kind, provider, pt string) {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := sc.Identities().Create(ctx, model.Identity{Name: ext, Kind: kind, ExternalID: ext, Provider: provider,
				Metadata: map[string]any{"principal_type": pt}})
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("vault:nhi:a", "vault_entity", "vault", string(identitysource.PrincipalNHI))
	seed("human:sue", "user", "okta", string(identitysource.PrincipalHuman))

	// Register lifecycle rows: a sponsored NHI, plus a stand-alone NHI.
	mutate := func(fn func(sc store.Scope) error) {
		if err := st.Mutate(ctx, tenant, fn); err != nil {
			t.Fatal(err)
		}
	}
	mutate(func(sc store.Scope) error {
		repo, rec, err := foLifecycle(ctx, sc, "vault:nhi:a")
		if err != nil {
			return err
		}
		rec[colNHISponsorRef] = "human:sue"
		_, err = repo.Update(ctx, rec)
		return err
	})
	mutate(func(sc store.Scope) error {
		_, _, err := foLifecycle(ctx, sc, "human:sue")
		return err // sue is a human, but we only seed a row to confirm it is NOT touched as an NHI
	})

	// (a) A credential-revoked CAEP finding for the NHI itself → it blocks.
	revoke := event.FromObservation(tenant.String(), "ssf", sdkmodel.FindingReport{
		Kind: "caep_credential_revoked", SubjectKind: "identity", SubjectRef: "vault:nhi:a",
	})
	m.onLifecycleSignal(ctx, revoke)
	if enf := readEnforce(t, st, tenant, "vault:nhi:a"); enf != enforceBlocked {
		t.Fatalf("external revoke should block the NHI, got %q", enf)
	}

	// (b) A session-revoked CAEP finding for the SPONSOR → the sponsored NHI orphans.
	sponsorRevoke := event.FromObservation(tenant.String(), "ssf", sdkmodel.FindingReport{
		Kind: "caep_session_revoked", SubjectKind: "identity", SubjectRef: "human:sue",
	})
	m.onLifecycleSignal(ctx, sponsorRevoke)
	if !readOrphan(t, st, tenant, "vault:nhi:a") {
		t.Fatalf("sponsor revoke should orphan the sponsored NHI")
	}

	// A non-revocation finding (its own kind) is ignored — no loop, no spurious change.
	noop := event.FromObservation(tenant.String(), Name, sdkmodel.FindingReport{
		Kind: "nhi_credential_stale", SubjectKind: "identity", SubjectRef: "vault:nhi:a",
	})
	m.onLifecycleSignal(ctx, noop) // must not panic or change state
}

func readEnforce(t *testing.T, st store.Store, tenant model.TenantID, ref string) string {
	t.Helper()
	var out string
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(nhiLifecycleKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(context.Background(), repo, eq(colNHIIdentityRef, ref))
		if ok {
			out = rec.String(colNHIEnforce)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func readOrphan(t *testing.T, st store.Store, tenant model.TenantID, ref string) bool {
	t.Helper()
	var out bool
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, e := sc.Ext(nhiLifecycleKind)
		if e != nil {
			return e
		}
		rec, ok, e := findOne(context.Background(), repo, eq(colNHIIdentityRef, ref))
		if ok {
			out = rec.Bool(colNHIOrphaned)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
