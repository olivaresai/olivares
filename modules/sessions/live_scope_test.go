// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// B2 acceptance (provider-session-identity-lot.md §8, B2 rows 1–5): scoped folds,
// the managed row, and every read surface — list, detail, timeline, SSE, runs
// lookup, exports — over rows that share one external id. The fold and managed
// tables run on BOTH engines through profileBackends; the HTTP surfaces run on the
// harness (SQLite) because the uniqueness they lean on is proven on Postgres by
// provider_profile_test.go and the fold tables below.

const dupID = "sess-dup"

// openProfiledRuntime is openProfileModule with runtime options and the settable
// clock the bridge reads.
func openProfiledRuntime(t *testing.T, be profileBackend, opts ...Option) (*Module, store.Store, *testClock) {
	t.Helper()
	clk := &testClock{now: baseTime}
	m := New(append([]Option{WithClock(clk)}, opts...)...)
	m.UseExecutionEnvironmentRef(testEnvRef)
	st, err := engine.Open(context.Background(), store.Config{Engine: be.engine, DSN: be.dsn, Debug: true}, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open %s: %v", be.name, err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	return m, st, clk
}

func regFor(src model.ID, rev int64) *event.SourceRegistration {
	return &event.SourceRegistration{SourceID: src.String(), SourceRevision: rev, EnvironmentRef: testEnvRef}
}

// stamped builds the envelope the engine would build: the host-stamped
// registration on the event, never on the payload.
func stamped(tenant model.TenantID, reg *event.SourceRegistration, o sdkmodel.Observation) event.Event {
	e := event.FromObservation(tenant.String(), "src", o)
	e.SourceRegistration = reg
	return e
}

// admittedStamped exercises the host admission seam before delivering a fresh
// observation. Tests of replay retain the resulting envelope instead.
func admittedStamped(t *testing.T, m *Module, tenant model.TenantID, reg *event.SourceRegistration, o sdkmodel.Observation) event.Event {
	t.Helper()
	e := stamped(tenant, reg, o)
	if reg != nil {
		admitted, err := m.AdmitSourceRegistration(context.Background(), tenant.String(), *reg)
		if err != nil {
			t.Fatal(err)
		}
		e.SourceRegistration = &admitted
	}
	return e
}

func liveRowsFor(t *testing.T, st store.Store, tenant model.TenantID, ref string) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(liveKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colSessionRef, ref)}, Limit: 50})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("liveRowsFor: %v", err)
	}
	return out
}

func rowWithScope(rows []model.Record, scope string) (model.Record, bool) {
	for _, r := range rows {
		if r.String(colObservationScope) == scope {
			return r, true
		}
	}
	return nil, false
}

func timelineRows(t *testing.T, st store.Store, tenant model.TenantID, filters []model.Filter) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(timelineKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: filters, Limit: 100})
		out = recs
		return err
	}); err != nil {
		t.Fatalf("timelineRows: %v", err)
	}
	return out
}

func runRecord(t *testing.T, st store.Store, tenant model.TenantID, runRef string) model.Record {
	t.Helper()
	var rec model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err = findRunRec(context.Background(), repo, runRef)
		return err
	}); err != nil {
		t.Fatalf("runRecord %s: %v", runRef, err)
	}
	return rec
}

// bindDedicated dedicates src@rev to prof through a fake composition port.
func bindDedicated(t *testing.T, m *Module, tenant model.TenantID, resolver *fakeSourceResolver, src model.ID, rev int64, prof ProviderProfile) ProviderSourceBinding {
	t.Helper()
	m.UseProviderSourceResolver(resolver)
	b, err := m.CreateBinding(context.Background(), tenant, auth.Principal{}, CreateBindingInput{SourceID: src, SourceRevision: rev, ProfileRef: prof.Ref})
	if err != nil {
		t.Fatalf("bind %s@%d → %s: %v", src, rev, prof.DisplayName, err)
	}
	return b
}

// B2 rows 1, 3, 4 (fold half): the scope is computed by the server from the
// stamped registration; the same external id through two dedicated sources, an
// unbound source, an old revision and the legacy channel is FIVE rows with five
// timelines, and a payload label never moves a profiled row's driver.
func TestLiveScope_FoldsByStampedRegistration(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m, st, _ := openProfiledRuntime(t, be)
			tenant := ensureTenant(t, st, "fold-"+be.name)
			configA, userA, configB, userB := twoHomes(t)
			profA := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "A"})
			profB := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB, DisplayName: "B"})
			srcA, srcB, srcC := model.NewID(), model.NewID(), model.NewID()
			resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{
				srcA: {ID: srcA, Version: 2, Name: "claude-home-a", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
				srcB: {ID: srcB, Version: 1, Name: "claude-home-b", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
			}}
			bindA := bindDedicated(t, m, tenant, resolver, srcA, 2, profA)
			bindB := bindDedicated(t, m, tenant, resolver, srcB, 1, profB)

			edge := func(at time.Time, labels map[string]string) sdkmodel.EdgeObservation {
				e := sessEdge(dupID, "file", "/a.txt", sdkmodel.ModeRead, "Read", at)
				e.Labels = labels
				return e
			}
			fold := func(reg *event.SourceRegistration, o sdkmodel.Observation) {
				t.Helper()
				if err := m.onEvent(ctx, admittedStamped(t, m, tenant, reg, o)); err != nil {
					t.Fatalf("fold: %v", err)
				}
			}
			// The observed row's driver is the binding's; the payload says codex and loses.
			fold(regFor(srcA, 2), edge(baseTime, map[string]string{labelEngine: "codex"}))
			fold(regFor(srcB, 1), edge(baseTime.Add(time.Second), nil))
			fold(regFor(srcC, 1), edge(baseTime.Add(2*time.Second), nil))                         // known source, no binding
			fold(nil, edge(baseTime.Add(3*time.Second), map[string]string{labelEngine: "codex"})) // legacy channel
			fold(regFor(srcA, 1), edge(baseTime.Add(4*time.Second), nil))                         // an old revision of A: its own binding (none)
			fold(regFor(srcA, 2), sdkmodel.CostSample{SessionRef: dupID, InputTokens: 10, OutputTokens: 5, CostMicroUSD: 7, OccurredAt: baseTime.Add(5 * time.Second)})

			rows := liveRowsFor(t, st, tenant, dupID)
			if len(rows) != 5 {
				t.Fatalf("rows for one external id = %d, want 5", len(rows))
			}
			obsA, ok := rowWithScope(rows, scopeObservedPrefix+profA.Ref)
			if !ok {
				t.Fatal("no observed row for profile A")
			}
			if obsA.String(colEngine) != "claude" || obsA.String(colLiveProfileID) != profA.Ref || obsA.String(colLiveBindingRef) != bindA.Ref ||
				obsA.String(colLiveEnvRef) != testEnvRef || obsA.Int(colEventCount) != 1 || obsA.Int(colInputTokens) != 10 {
				t.Fatalf("observed A = %v", obsA)
			}
			if obsA.String(colLiveCanonicalSID) != "" || obsA.String(colLiveRunRef) != "" {
				t.Fatal("an observed row must never carry canonical_sid or run_ref")
			}
			obsB, ok := rowWithScope(rows, scopeObservedPrefix+profB.Ref)
			if !ok || obsB.String(colLiveBindingRef) != bindB.Ref || obsB.Int(colInputTokens) != 0 {
				t.Fatalf("observed B = %v", obsB)
			}
			srcRowC, ok := rowWithScope(rows, scopeSourcePrefix+srcC.String()+":1:"+testEnvRef)
			if !ok || srcRowC.String(colLiveProfileID) != "" || srcRowC.String(colEngine) != "" {
				t.Fatalf("unbound source row = %v", srcRowC)
			}
			if _, ok := rowWithScope(rows, scopeSourcePrefix+srcA.String()+":1:"+testEnvRef); !ok {
				t.Fatal("an event of revision 1 must resolve against revision 1's (absent) binding, not revision 2's")
			}
			legacy, ok := rowWithScope(rows, "")
			if !ok || legacy.String(colEngine) != "codex" || legacy.String(colLiveProfileID) != "" {
				t.Fatalf("legacy row = %v", legacy)
			}

			// Timelines: each row's events carry ITS id; legacy events carry none.
			if n := len(timelineRows(t, st, tenant, timelineFiltersFor(obsA))); n != 2 {
				t.Fatalf("observed A timeline = %d, want 2 (edge + cost)", n)
			}
			for _, ev := range timelineRows(t, st, tenant, timelineFiltersFor(obsA)) {
				if ev.String(colTLBindingRef) != bindA.Ref {
					t.Fatalf("observed event without its binding: %v", ev)
				}
			}
			if n := len(timelineRows(t, st, tenant, legacyTimelineFilters(dupID))); n != 1 {
				t.Fatalf("legacy timeline = %d, want 1", n)
			}
			if n := len(timelineRows(t, st, tenant, timelineFiltersFor(obsB))); n != 1 {
				t.Fatalf("observed B timeline = %d, want 1", n)
			}

			// DTO attribution: references and labels, no path anywhere.
			dto := m.toLiveDTO(obsA)
			if dto.Attribution != attributionObserved || dto.LiveRef != obsA.String(model.ColID) || dto.ProviderProfileRef != profA.Ref || dto.CanonicalSID != "" || dto.RunRef != "" {
				t.Fatalf("observed DTO = %+v", dto)
			}
			raw, _ := json.Marshal(dto)
			if strings.Contains(string(raw), configA) || strings.Contains(string(raw), userA) {
				t.Fatalf("live DTO leaks a home: %s", raw)
			}
			if m.toLiveDTO(legacy).Attribution != attributionLegacy || m.toLiveDTO(srcRowC).Attribution != attributionSource {
				t.Fatal("attribution classification")
			}

			// Row 4: revoking the binding stops attributing NEW observations without
			// rewriting the old row; the same registration now lands in a source row.
			if _, err := m.RevokeBinding(ctx, tenant, bindA.Ref); err != nil {
				t.Fatal(err)
			}
			fold(regFor(srcA, 2), edge(baseTime.Add(6*time.Second), nil))
			rows = liveRowsFor(t, st, tenant, dupID)
			if len(rows) != 6 {
				t.Fatalf("rows after revoke = %d, want 6", len(rows))
			}
			obsA2, _ := rowWithScope(rows, scopeObservedPrefix+profA.Ref)
			if obsA2.Int(colEventCount) != 1 {
				t.Fatalf("revoked binding still attributes: event_count=%d", obsA2.Int(colEventCount))
			}
			if _, ok := rowWithScope(rows, scopeSourcePrefix+srcA.String()+":2:"+testEnvRef); !ok {
				t.Fatal("post-revocation observation must land in the source row")
			}
			// The legacy entry points remain the legacy channel.
			if err := m.onEdge(ctx, tenant.String(), sessEdge("sess-legacy-only", "file", "/b", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
				t.Fatal(err)
			}
			if lr := liveRowsFor(t, st, tenant, "sess-legacy-only"); len(lr) != 1 || lr[0].String(colObservationScope) != "" {
				t.Fatalf("onEdge rows = %v", lr)
			}
		})
	}
}

// B2 rows 1 and 2 (managed half): the managed row exists only because the bridge
// of the launched process proved the id, in the same transaction as the scoped
// alias; two homes announcing one id are two managed rows on two canonical
// sessions; a cooperative observation of the same profile and id lands beside the
// managed row, never in it, and a legacy lookup sees neither.
func TestManagedLive_ProvenByBridgeOnly(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: dupID}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "managed-"+be.name)
			configA, userA, configB, userB := twoHomes(t)
			profA := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "A"})
			profB := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB, DisplayName: "B"})
			srcA := model.NewID()
			resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{
				srcA: {ID: srcA, Version: 2, Name: "claude-home-a", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
			}}
			bindDedicated(t, m, tenant, resolver, srcA, 2, profA)

			launch := func(prof ProviderProfile) runDTO {
				t.Helper()
				dto, err := m.createRun(ctx, tenant, CreateRunParams{
					Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
					Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: prof.Ref,
				})
				if err != nil {
					t.Fatalf("createRun %s: %v", prof.DisplayName, err)
				}
				waitFor(t, "managed row proven for "+prof.DisplayName, func() bool {
					d, _ := m.getRun(ctx, tenant, dto.RunRef)
					return d.ClaudeSessionID == dupID && d.LiveRef != ""
				})
				d, _ := m.getRun(ctx, tenant, dto.RunRef)
				return d
			}
			runA := launch(profA)
			runB := launch(profB)
			if runA.LiveRef == runB.LiveRef {
				t.Fatal("two homes announcing one id must be two managed rows")
			}
			for _, tc := range []struct {
				run  runDTO
				prof ProviderProfile
			}{{runA, profA}, {runB, profB}} {
				rec := runRecord(t, st, tenant, tc.run.RunRef)
				sid := rec.String(colRunClaimSID)
				var managed model.Record
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					var err error
					managed, err = findLiveByID(ctx, sc, model.ID(tc.run.LiveRef))
					return err
				}); err != nil {
					t.Fatalf("managed row of %s: %v", tc.prof.DisplayName, err)
				}
				if managed.String(colObservationScope) != scopeManagedPrefix+sid || managed.String(colLiveCanonicalSID) != sid ||
					managed.String(colLiveRunRef) != tc.run.RunRef || managed.String(colLiveProfileID) != tc.prof.Ref ||
					managed.String(colSessionRef) != dupID || managed.String(colEngine) != "claude" || managed.String(colLiveEnvRef) != testEnvRef {
					t.Fatalf("managed row of %s = %v (sid %s)", tc.prof.DisplayName, managed, sid)
				}
				alias, found, err := m.LookupScopedAlias(ctx, tenant, tc.prof.Ref, "claude", dupID)
				if err != nil || !found || alias.SID != sid || alias.RunRef != tc.run.RunRef {
					t.Fatalf("scoped alias of %s = %+v %v %v", tc.prof.DisplayName, alias, found, err)
				}
			}

			// A cooperative observation of profile A's id lands in the OBSERVED row.
			if err := m.onEvent(ctx, admittedStamped(t, m, tenant, regFor(srcA, 2), sessEdge(dupID, "file", "/x", sdkmodel.ModeRead, "Read", baseTime.Add(time.Minute)))); err != nil {
				t.Fatal(err)
			}
			rows := liveRowsFor(t, st, tenant, dupID)
			if len(rows) != 3 {
				t.Fatalf("rows = %d, want 3 (two managed + one observed)", len(rows))
			}
			obs, ok := rowWithScope(rows, scopeObservedPrefix+profA.Ref)
			if !ok || obs.String(colLiveCanonicalSID) != "" || obs.String(colLiveRunRef) != "" || obs.Int(colEventCount) != 1 {
				t.Fatalf("observed row = %v", obs)
			}
			for _, r := range rows {
				if attributionOf(r.String(colObservationScope)) == attributionManaged && r.Int(colEventCount) != 0 {
					t.Fatalf("a cooperative observation reached a managed row: %v", r)
				}
			}
			// The legacy selector sees no scoped row at all.
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(liveKind)
				if err != nil {
					return err
				}
				recs, _, err := repo.List(ctx, model.Query{Filters: liveKeyFilters(dupID, ""), Limit: 5})
				if err != nil {
					return err
				}
				if len(recs) != 0 {
					return errors.New("legacy selector returned a scoped row")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, d := range []runDTO{runA, runB} {
				if _, err := m.stopRun(ctx, tenant, d.RunRef, "user:u1", "user"); err != nil {
					t.Fatalf("stop: %v", err)
				}
			}
		})
	}
}

// profiledHTTP is the HTTP harness with profiled launches enabled, two profiles
// and a fake runner announcing dupID on every launch.
func profiledHTTP(t *testing.T, fr *fakeRunner, clk *testClock) (*harness, string, model.TenantID, string, string) {
	t.Helper()
	m := New(WithRunner(fr), WithCredentialSource(staticCred()), WithClock(clk))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	configA, userA, configB, userB := twoHomes(t)
	mk := func(cfg, usr, name string) string {
		r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": cfg, "user_home": usr, "display_name": name}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("profile %s = %d %s", name, r.code, r.raw)
		}
		return r.body["profile_ref"].(string)
	}
	return h, admin, tenant, mk(configA, userA, "A"), mk(configB, userB, "B")
}

func launchProfiledHTTP(t *testing.T, h *harness, admin string, tenant model.TenantID, profile string) (runRef, liveRef string) {
	t.Helper()
	r := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("launch = %d %s", r.code, r.raw)
	}
	runRef = r.body["run_ref"].(string)
	waitFor(t, "live_ref on "+runRef, func() bool {
		g := h.do("GET", "/v1/m/sessions/runs/"+runRef, admin, tenantHdr(tenant))
		v, _ := g.body["live_ref"].(string)
		liveRef = v
		return v != "" && g.body["claude_session_id"] == dupID
	})
	return runRef, liveRef
}

func itemsOf(t *testing.T, r resp) []map[string]any {
	t.Helper()
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		out = append(out, it.(map[string]any))
	}
	return out
}

// B2 rows 1 and 3 (API half): list, detail and timeline by live_ref name exactly
// one row; the bare external-id routes answer for the legacy row only; the runs
// lookup by live_ref returns the proven run and nothing for an unproven row; the
// bare claude_session_id lookup never returns a profiled run; another tenant's
// live_ref is not found.
func TestLiveRefAPI_ReadSurfacesAreUnambiguous(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	h, admin, tenant, profA, profB := profiledHTTP(t, fr, &testClock{now: baseTime})
	other := h.createOrg(admin, "globex")
	runA, liveA := launchProfiledHTTP(t, h, admin, tenant, profA)
	runB, liveB := launchProfiledHTTP(t, h, admin, tenant, profB)
	if liveA == liveB {
		t.Fatal("two profiled runs share a live_ref")
	}

	get := func(path string, hdr map[string]string) resp { return h.do("GET", path, admin, hdr) }
	// Detail by live_ref: managed, with its proven run.
	for _, tc := range []struct{ live, run, prof string }{{liveA, runA, profA}, {liveB, runB, profB}} {
		r := get("/v1/m/sessions/live/by-id/"+tc.live, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["attribution"] != attributionManaged || r.body["run_ref"] != tc.run ||
			r.body["provider_profile_ref"] != tc.prof || r.body["session_ref"] != dupID || r.body["live_ref"] != tc.live || r.body["canonical_sid"] == "" {
			t.Fatalf("by-id %s = %d %s", tc.live, r.code, r.raw)
		}
		if strings.Contains(r.raw, "config_home") || strings.Contains(r.raw, "user_home") {
			t.Fatalf("live detail leaks a home: %s", r.raw)
		}
	}
	// The legacy route answers for a legacy row only: none yet ⇒ 404, never "the first".
	if r := get("/v1/m/sessions/live/"+dupID, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("legacy detail with only scoped rows = %d %s", r.code, r.raw)
	}
	if err := h.m.onEdge(context.Background(), tenant.String(), sessEdge(dupID, "file", "/legacy", sdkmodel.ModeRead, "Read", baseTime)); err != nil {
		t.Fatal(err)
	}
	r := get("/v1/m/sessions/live/"+dupID, tenantHdr(tenant))
	legacyLive, _ := r.body["live_ref"].(string)
	if r.code != http.StatusOK || r.body["attribution"] != attributionLegacy || legacyLive == "" || legacyLive == liveA || legacyLive == liveB {
		t.Fatalf("legacy detail = %d %s", r.code, r.raw)
	}
	// Timelines: the legacy route lists legacy events; a managed row's own timeline is its own.
	if items := itemsOf(t, get("/v1/m/sessions/live/"+dupID+"/timeline", tenantHdr(tenant))); len(items) != 1 {
		t.Fatalf("legacy timeline = %d, want 1", len(items))
	}
	if items := itemsOf(t, get("/v1/m/sessions/live/by-id/"+liveA+"/timeline", tenantHdr(tenant))); len(items) != 0 {
		t.Fatalf("managed timeline = %d, want 0", len(items))
	}
	if items := itemsOf(t, get("/v1/m/sessions/live/by-id/"+legacyLive+"/timeline", tenantHdr(tenant))); len(items) != 1 {
		t.Fatalf("legacy timeline by id = %d, want 1", len(items))
	}
	// List filters are exact store filters.
	if items := itemsOf(t, get("/v1/m/sessions/live?session_ref="+dupID, tenantHdr(tenant))); len(items) != 3 {
		t.Fatalf("list by session_ref = %d, want 3", len(items))
	}
	items := itemsOf(t, get("/v1/m/sessions/live?session_ref="+dupID+"&provider_profile_ref="+profA, tenantHdr(tenant)))
	if len(items) != 1 || items[0]["live_ref"] != liveA {
		t.Fatalf("list by profile = %v", items)
	}
	// Runs by live_ref: the proven run for a managed row; nothing for a legacy row.
	runs := func(query string) []map[string]any {
		return itemsOf(t, get("/v1/m/sessions/runs"+query, tenantHdr(tenant)))
	}
	if got := runs("?live_ref=" + liveA); len(got) != 1 || got[0]["run_ref"] != runA {
		t.Fatalf("runs by live_ref A = %v", got)
	}
	if got := runs("?live_ref=" + liveB); len(got) != 1 || got[0]["run_ref"] != runB {
		t.Fatalf("runs by live_ref B = %v", got)
	}
	if got := runs("?live_ref=" + legacyLive); len(got) != 0 {
		t.Fatalf("runs by a legacy live_ref = %v, want none", got)
	}
	if got := runs("?live_ref=not-an-id"); len(got) != 0 {
		t.Fatalf("runs by garbage live_ref = %v", got)
	}
	// The bare id is a LEGACY question: it never answers with a profiled run.
	if got := runs("?claude_session_id=" + dupID); len(got) != 0 {
		t.Fatalf("runs by bare claude_session_id returned profiled runs: %v", got)
	}
	if got := runs(""); len(got) != 2 {
		t.Fatalf("all runs = %d", len(got))
	}
	// Another tenant, a malformed id.
	if r := get("/v1/m/sessions/live/by-id/"+liveA, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant by-id = %d %s", r.code, r.raw)
	}
	if r := get("/v1/m/sessions/live/by-id/zz", tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("malformed by-id = %d", r.code)
	}
	if r := get("/v1/m/sessions/live/by-id/"+liveA+"/timeline", tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant timeline = %d", r.code)
	}
	for _, ref := range []string{runA, runB} {
		if r := h.doJSON("POST", "/v1/m/sessions/runs/"+ref+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("stop = %d %s", r.code, r.raw)
		}
	}
}

// openSSE opens the live stream and returns the frame reader once ": connected"
// has been read (so a later publish is guaranteed to reach the subscription).
func openSSE(t *testing.T, ts *httptest.Server, token string, tenant model.TenantID, query string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/v1/m/sessions/stream"+query, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Olivares-Tenant", tenant.String())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		cancel()
		t.Fatalf("stream %s status = %d", query, res.StatusCode)
	}
	reader := bufio.NewReader(res.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read prelude: %v", err)
	}
	return reader, func() { cancel(); res.Body.Close() }
}

func nextFrame(t *testing.T, reader *bufio.Reader, timeout time.Duration) liveDTO {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				got <- strings.TrimSpace(data)
				return
			}
		}
	}()
	select {
	case payload := <-got:
		var dto liveDTO
		if err := json.Unmarshal([]byte(payload), &dto); err != nil {
			t.Fatalf("bad stream payload %q: %v", payload, err)
		}
		return dto
	case <-time.After(timeout):
		t.Fatal("did not receive a stream event in time")
		return liveDTO{}
	}
}

// B2 row 1 (SSE half): a live_ref subscription receives exactly that row; a
// legacy ref subscription never receives a scoped row that shares the id; a
// live_ref of another tenant, or a malformed one, does not open.
func TestStream_LiveRefAndLegacyRefStaySeparate(t *testing.T) {
	fr := &fakeRunner{initSID: dupID}
	clk := &testClock{now: baseTime}
	h, admin, tenant, profA, _ := profiledHTTP(t, fr, clk)
	other := h.createOrg(admin, "globex")
	runA, liveA := launchProfiledHTTP(t, h, admin, tenant, profA)
	ts := httptest.NewServer(h.srv.Handler())
	defer ts.Close()

	byID, closeByID := openSSE(t, ts, admin, tenant, "?live_ref="+liveA)
	defer closeByID()
	legacy, closeLegacy := openSSE(t, ts, admin, tenant, "?ref="+dupID)
	defer closeLegacy()

	// Activity on the launched process moves the managed row (past the write
	// throttle) and publishes it: the live_ref subscriber sees it.
	clk.advance(activityWriteInterval + time.Second)
	fr.mu.Lock()
	proc := fr.procs[0]
	fr.mu.Unlock()
	proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(`{"type":"assistant"}`)}
	frame := nextFrame(t, byID, 3*time.Second)
	if frame.LiveRef != liveA || frame.Attribution != attributionManaged || frame.RunRef != runA || frame.SessionRef != dupID {
		t.Fatalf("live_ref frame = %+v", frame)
	}
	// The legacy subscriber did NOT get that managed snapshot: the first frame it
	// sees is the legacy row folded afterwards.
	if err := h.m.onEdge(context.Background(), tenant.String(), sessEdge(dupID, "file", "/legacy", sdkmodel.ModeRead, "Read", clk.get())); err != nil {
		t.Fatal(err)
	}
	lf := nextFrame(t, legacy, 3*time.Second)
	if lf.Attribution != attributionLegacy || lf.SessionRef != dupID || lf.LiveRef == liveA {
		t.Fatalf("legacy frame = %+v", lf)
	}

	// A live_ref is checked against THIS tenant before the stream opens.
	for _, tc := range []struct {
		tenant model.TenantID
		query  string
	}{{other, "?live_ref=" + liveA}, {tenant, "?live_ref=nope"}} {
		req, _ := http.NewRequest("GET", ts.URL+"/v1/m/sessions/stream"+tc.query, nil)
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("X-Olivares-Tenant", tc.tenant.String())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("stream %s for %s = %d, want 404", tc.query, tc.tenant, res.StatusCode)
		}
	}
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+runA+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
}

// B2 row 3 (export half): the credential → run → timeline join of a PROFILED run
// goes through its managed row, never through the bare id; the bare-id replay
// stays the legacy events; a scoped row replays by its live_ref; samples carry
// the row identity.
func TestExport_ScopedTimelinesNeverCross(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			fr := &fakeRunner{initSID: dupID}
			m, st, _ := openProfiledRuntime(t, be, WithRunner(fr), WithCredentialSource(staticCred()))
			tenant := ensureTenant(t, st, "export-"+be.name)
			configA, userA, _, _ := twoHomes(t)
			profA := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "A"})
			srcA := model.NewID()
			resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{
				srcA: {ID: srcA, Version: 2, Name: "claude-home-a", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
			}}
			bindDedicated(t, m, tenant, resolver, srcA, 2, profA)
			dto, err := m.createRun(ctx, tenant, CreateRunParams{
				Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, ProviderProfileRef: profA.Ref,
			})
			if err != nil {
				t.Fatal(err)
			}
			waitFor(t, "managed row", func() bool { d, _ := m.getRun(ctx, tenant, dto.RunRef); return d.LiveRef != "" })
			run, _ := m.getRun(ctx, tenant, dto.RunRef)
			const credProfiled, credLegacy = "cred-profiled", "cred-legacy"
			// The profiled run's credential, and a LEGACY run on the same bare id.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(runKind)
				if err != nil {
					return err
				}
				rec, err := findRunRec(ctx, repo, run.RunRef)
				if err != nil {
					return err
				}
				rec[colCredentialID] = credProfiled
				if _, err := repo.Update(ctx, rec); err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{
					colRunRef: "run-legacy", colTransport: "stream-json", colPermissionMode: "default", colIsolation: "native",
					colState: stateStopped, colLastEventSeq: int64(0), colCredentialID: credLegacy, colClaudeSessionID: dupID,
				})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Three timelines under one external id: observed (via the binding), legacy,
			// and one event written against the managed row itself.
			if err := m.onEvent(ctx, admittedStamped(t, m, tenant, regFor(srcA, 2), sessEdge(dupID, "file", "/observed", sdkmodel.ModeRead, "Read", baseTime))); err != nil {
				t.Fatal(err)
			}
			if err := m.onEdge(ctx, tenant.String(), sessEdge(dupID, "file", "/legacy", sdkmodel.ModeRead, "Read", baseTime.Add(time.Second))); err != nil {
				t.Fatal(err)
			}
			var managed model.Record
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				rec, err := findLiveByID(ctx, sc, model.ID(run.LiveRef))
				if err != nil {
					return err
				}
				managed = rec
				scope := managedScope(rec.String(colLiveProfileID), rec.String(colLiveProvider), rec.String(colLiveEnvRef), rec.String(colLiveCanonicalSID), rec.String(colLiveRunRef))
				return m.appendTimelineScoped(ctx, sc, dupID, rec, scope, baseTime.Add(2*time.Second), tlTool, "Bash", "/managed", "", "bridge", "")
			}); err != nil {
				t.Fatal(err)
			}
			rows := liveRowsFor(t, st, tenant, dupID)
			obs, _ := rowWithScope(rows, scopeObservedPrefix+profA.Ref)

			ref, tl, _, _, err := m.TimelineByCredential(ctx, tenant, credProfiled, 100, "")
			if err != nil || ref != dupID || len(tl) != 1 || tl[0].ResourceRef != "/managed" {
				t.Fatalf("profiled credential timeline = %q %+v %v", ref, tl, err)
			}
			ref, tl, _, _, err = m.TimelineByCredential(ctx, tenant, credLegacy, 100, "")
			if err != nil || ref != dupID || len(tl) != 1 || tl[0].ResourceRef != "/legacy" {
				t.Fatalf("legacy credential timeline = %q %+v %v", ref, tl, err)
			}
			events, truncated, err := m.ReplayTimeline(ctx, tenant, dupID, 0)
			if err != nil || truncated || len(events) != 1 || events[0].ResourceRef != "/legacy" {
				t.Fatalf("bare-id replay = %+v %v %v", events, truncated, err)
			}
			events, _, err = m.ReplayTimelineByLiveRef(ctx, tenant, obs.String(model.ColID), 0)
			if err != nil || len(events) != 1 || events[0].ResourceRef != "/observed" {
				t.Fatalf("observed replay = %+v %v", events, err)
			}
			events, _, err = m.ReplayTimelineByLiveRef(ctx, tenant, managed.String(model.ColID), 0)
			if err != nil || len(events) != 1 || events[0].ResourceRef != "/managed" {
				t.Fatalf("managed replay = %+v %v", events, err)
			}
			if events, _, err := m.ReplayTimelineByLiveRef(ctx, tenant, "zz", 0); err != nil || len(events) != 0 {
				t.Fatalf("garbage live_ref replay = %+v %v", events, err)
			}
			samples, err := m.SampleLive(ctx, tenant, LiveSampleQuery{Limit: 10})
			if err != nil || len(samples) != 3 {
				t.Fatalf("samples = %+v %v", samples, err)
			}
			seen := map[string]int{}
			for _, s := range samples {
				seen[s.Attribution]++
				if s.LiveRef == "" {
					t.Fatalf("sample without live_ref: %+v", s)
				}
			}
			if seen[attributionManaged] != 1 || seen[attributionObserved] != 1 || seen[attributionLegacy] != 1 {
				t.Fatalf("sample attributions = %v", seen)
			}
			one, err := m.SampleLive(ctx, tenant, LiveSampleQuery{LiveRef: obs.String(model.ColID), Limit: 10})
			if err != nil || len(one) != 1 || one[0].ProfileRef != profA.Ref || one[0].Attribution != attributionObserved {
				t.Fatalf("sample by live_ref = %+v %v", one, err)
			}
			if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", "user"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// B2 row 5: the Community composition create → persist → bridge → API, with a REAL
// OS child through the productive endpoint once profiled launches are enabled.
func TestProfiledLaunch_HTTPCompositionWithRealChild(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fixture-claude.sh")
	body := "#!/bin/sh\n" +
		"trap 'exit 0' TERM\n" +
		"printf 'HOME=%s\\nCLAUDE_CONFIG_DIR=%s\\n' \"$HOME\" \"$CLAUDE_CONFIG_DIR\" > \"$CLAUDE_CONFIG_DIR/seen-env\"\n" +
		"printf '{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"" + dupID + "\"}\\n'\n" +
		"while IFS= read -r line; do :; done\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	m := New(WithRunner(NewProcRunner()), WithProgram(script), WithCredentialSource(staticCred()), WithStopWaitDelay(2*time.Second))
	m.UseExecutionEnvironmentRef(testEnvRef)
	m.EnableProfiledLaunches()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	configA, userA, _, _ := twoHomes(t)
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": configA, "user_home": userA, "display_name": "A"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("profile = %d %s", r.code, r.raw)
	}
	prof := r.body["profile_ref"].(string)
	runRef, liveRef := launchProfiledHTTP(t, h, admin, tenant, prof)
	seen, err := os.ReadFile(filepath.Join(configA, "seen-env"))
	if err != nil {
		t.Fatalf("fixture wrote nothing: %v", err)
	}
	if !strings.Contains(string(seen), "HOME="+userA+"\n") || !strings.Contains(string(seen), "CLAUDE_CONFIG_DIR="+configA+"\n") {
		t.Fatalf("fixture saw %q", seen)
	}
	live := h.do("GET", "/v1/m/sessions/live/by-id/"+liveRef, admin, tenantHdr(tenant))
	if live.code != http.StatusOK || live.body["attribution"] != attributionManaged || live.body["run_ref"] != runRef || live.body["provider_profile_ref"] != prof {
		t.Fatalf("managed row = %d %s", live.code, live.raw)
	}
	if got := itemsOf(t, h.do("GET", "/v1/m/sessions/runs?live_ref="+liveRef, admin, tenantHdr(tenant))); len(got) != 1 || got[0]["run_ref"] != runRef {
		t.Fatalf("runs by live_ref = %v", got)
	}
	if r := h.do("GET", "/v1/m/sessions/live/"+dupID, admin, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("legacy detail for a profiled session = %d", r.code)
	}
	if r := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("stop = %d %s", r.code, r.raw)
	}
}
