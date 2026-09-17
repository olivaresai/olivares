// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/modules/sessions"
)

// runlineagerepair_test.go (P2 / W3) — the bounded run-lineage maintenance loop and
// the managed Stop binding, through the real composition root and a real store.
//
// Rows are created exactly as a pre-W2 database holds them: a sessions.run row with
// an admission SID and no authorization lineage. The column and kind names are the
// sessions module's own persisted names; this package cannot import its unexported
// constants, so they are spelled here once.

const (
	lineageRunKind      model.Kind = "sessions.run"
	lineageIdentityKind model.Kind = "sessions.identity"
)

// switchableElector is the store's real elector with Active and IsLeader under test
// control, so leadership can move between two loop ticks or two pages.
type switchableElector struct {
	store.LeaderElector
	active atomic.Bool
}

func (e *switchableElector) Active() bool   { return e.active.Load() }
func (e *switchableElector) IsLeader() bool { return e.active.Load() }

// electedStore is a store whose Leader() is the switchable elector. Every unit of
// work still runs through the wrapped store.
type electedStore struct {
	store.Store
	le store.LeaderElector
}

func (s electedStore) Leader() store.LeaderElector { return s.le }

// lineageFixture is the sessions module over a real store composed like boot: the
// suspension guard wraps the opened store, and the module's data handle and the
// loop both use the composed store.
type lineageFixture struct {
	t       *testing.T
	raw     store.Store
	st      store.Store
	sm      *sessions.Module
	le      *switchableElector
	tenantA model.TenantID
	tenantB model.TenantID
}

func newLineageFixture(t *testing.T, cfg store.Config) *lineageFixture {
	t.Helper()
	ctx := context.Background()
	sm := sessions.New()
	raw, err := coreengine.Open(ctx, cfg, sm.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	f := &lineageFixture{t: t, raw: raw, sm: sm}
	if err := raw.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		a, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme", Status: model.StatusActive})
		if err != nil {
			return err
		}
		b, err := sys.CreateOrg(ctx, model.Org{Name: "Globex", Slug: "globex", Status: model.StatusActive})
		f.tenantA, f.tenantB = a.TenantID, b.TenantID
		return err
	}); err != nil {
		t.Fatalf("tenants: %v", err)
	}
	f.le = &switchableElector{LeaderElector: raw.Leader()}
	f.le.active.Store(true)
	f.st = electedStore{Store: suspension.Guard(raw, discardLog()), le: f.le}
	sm.UseData(api.NewModuleData(f.st))
	return f
}

func lineageBackends(t *testing.T) []struct {
	name   string
	config func(*testing.T) store.Config
} {
	t.Helper()
	backends := []struct {
		name   string
		config func(*testing.T) store.Config
	}{{"sqlite", func(*testing.T) store.Config {
		return store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}
	}}}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, struct {
			name   string
			config func(*testing.T) store.Config
		}{"postgres", func(t *testing.T) store.Config {
			pg := enginetest.IsolatedPostgres(t)
			return store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, OwnerDSN: pg.Owner}
		}})
	} else {
		t.Log("PostgreSQL NOT exercised: no configured disposable server")
	}
	return backends
}

func (f *lineageFixture) defaultWorkspace(tenant model.TenantID) model.ID {
	f.t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := f.raw.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		id = ws.ID
		return err
	}); err != nil {
		f.t.Fatalf("default workspace: %v", err)
	}
	return id
}

func (f *lineageFixture) createWorkspace(tenant model.TenantID, slug string) model.ID {
	f.t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := f.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
		id = ws.ID
		return err
	}); err != nil {
		f.t.Fatalf("create workspace %s: %v", slug, err)
	}
	return id
}

// legacyIdentity writes one canonical session identity. A zero workspace is the
// identity owner's NULL, which it reads as the tenant default.
func legacyIdentity(t *testing.T, st store.Store, tenant model.TenantID, workspace model.ID, mergedInto string) string {
	t.Helper()
	ctx := context.Background()
	sid := "osn_" + model.NewID().String()
	now := model.NewTimestamp(time.Now()).String()
	rec := model.Record{"sid": sid, "origin": "w3-test", "first_seen_at": now, "last_seen_at": now}
	if !workspace.IsZero() {
		rec["workspace_id"] = workspace.String()
	}
	if mergedInto != "" {
		rec["merged_into"] = mergedInto
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lineageIdentityKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}
	return sid
}

// legacyRuns writes n terminal runs claimed under sid (none when empty) and with no
// authorization lineage, in one transaction and in ascending id order.
func legacyRuns(t *testing.T, st store.Store, tenant model.TenantID, sid string, n int) []string {
	t.Helper()
	ctx := context.Background()
	refs := make([]string, 0, n)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lineageRunKind)
		if err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			ref := "legacy-" + model.NewID().String()
			rec := model.Record{
				"run_ref": ref, "transport": string(sessions.TransportStreamJSON), "permission_mode": "",
				"isolation": string(sessions.IsolationNative), "state": "stopped", "last_event_seq": int64(0),
			}
			if sid != "" {
				rec["claim_sid"] = sid
			}
			if _, err := repo.Create(ctx, rec); err != nil {
				return err
			}
			refs = append(refs, ref)
		}
		return nil
	}); err != nil {
		t.Fatalf("create %d legacy runs: %v", n, err)
	}
	return refs
}

func runLineageOf(t *testing.T, st store.Store, tenant model.TenantID, runRef string) (string, bool) {
	t.Helper()
	ctx := context.Background()
	var value string
	var null bool
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(lineageRunKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{{Column: "run_ref", Op: model.OpEq, Value: runRef}}, Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return store.ErrNotFound
		}
		value, null = rows[0].String("authz_workspace_id"), rows[0].IsNull("authz_workspace_id")
		return nil
	}); err != nil {
		t.Fatalf("read lineage of %s: %v", runRef, err)
	}
	return value, null
}

func (f *lineageFixture) requireLineage(tenant model.TenantID, runRef string, want model.ID) {
	f.t.Helper()
	got, null := runLineageOf(f.t, f.raw, tenant, runRef)
	if want.IsZero() {
		if !null {
			f.t.Fatalf("%s acquired lineage %q; it must stay NULL", runRef, got)
		}
		return
	}
	if null || got != want.String() {
		f.t.Fatalf("%s lineage = %q null=%t, want %s", runRef, got, null, want)
	}
}

// observedRepairer is the real module operation with every call recorded. before
// and after run around the real call; neither changes its arguments or result.
type observedRepairer struct {
	inner  runLineageRepairer
	mu     sync.Mutex
	calls  []observedPage
	before func(call int, tenant model.TenantID)
	after  func(call int, tenant model.TenantID, res sessions.RunLineageRepairResult)
}

type observedPage struct {
	tenant model.TenantID
	cursor sessions.RunLineageRepairCursor
	result sessions.RunLineageRepairResult
	err    error
}

func (o *observedRepairer) RepairRunLineage(ctx context.Context, tenant model.TenantID, cursor sessions.RunLineageRepairCursor) (sessions.RunLineageRepairResult, error) {
	o.mu.Lock()
	n := len(o.calls) + 1
	o.mu.Unlock()
	if o.before != nil {
		o.before(n, tenant)
	}
	res, err := o.inner.RepairRunLineage(ctx, tenant, cursor)
	o.mu.Lock()
	o.calls = append(o.calls, observedPage{tenant: tenant, cursor: cursor, result: res, err: err})
	o.mu.Unlock()
	if o.after != nil {
		o.after(n, tenant, res)
	}
	return res, err
}

func (o *observedRepairer) pagesFor(tenant model.TenantID) []observedPage {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []observedPage
	for _, c := range o.calls {
		if c.tenant == tenant {
			out = append(out, c)
		}
	}
	return out
}

// loopLog captures the loop's own records.
type loopLog struct {
	mu   sync.Mutex
	recs []loopRecord
}

type loopRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (l *loopLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *loopLog) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *loopLog) WithGroup(string) slog.Handler            { return l }
func (l *loopLog) Handle(_ context.Context, r slog.Record) error {
	rec := loopRecord{level: r.Level, msg: r.Message, attrs: map[string]any{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Any()
		return true
	})
	l.mu.Lock()
	l.recs = append(l.recs, rec)
	l.mu.Unlock()
	return nil
}

func (l *loopLog) find(msgPart string, tenant model.TenantID) (loopRecord, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.recs {
		if strings.Contains(r.msg, msgPart) && (tenant.IsZero() || r.attrs["tenant"] == tenant.String()) {
			return r, true
		}
	}
	return loopRecord{}, false
}

func (f *lineageFixture) loop(obs *observedRepairer, log *loopLog) *runLineageRepairLoop {
	if obs.inner == nil {
		obs.inner = f.sm
	}
	return &runLineageRepairLoop{st: f.st, repair: obs, interval: defaultRunLineageRepairInterval, log: slog.New(log)}
}

// TestRunLineageRepairLoopRegistersOnTheSchedulerBeforeStart pins the job identity,
// the job-owned cadence and the scheduler's before-Start requirement.
func TestRunLineageRepairLoopRegistersOnTheSchedulerBeforeStart(t *testing.T) {
	f := newLineageFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	if newRunLineageRepairLoop(f.st, nil, discardLog()) != nil {
		t.Fatal("a composition without the sessions module must not build the loop")
	}
	l := newRunLineageRepairLoop(f.st, f.sm, discardLog())
	if l == nil || l.interval != time.Minute || runLineageRepairJobName != "sessions.run_lineage_repair" {
		t.Fatalf("loop = %+v, job %q; want the job-owned one-minute sessions.run_lineage_repair", l, runLineageRepairJobName)
	}
	rt := runtime.New(runtime.Options{Logger: discardLog()})
	if err := l.register(rt); err != nil {
		t.Fatalf("register before Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	defer func() { _ = rt.Stop(context.Background()) }()
	if err := l.register(rt); !errors.Is(err, runtime.ErrAlreadyStarted) {
		t.Fatalf("register after Start = %v, want runtime.ErrAlreadyStarted", err)
	}
}

// TestBootBindsManagedStopAndSchedulesRunLineageRepair boots the REAL composition
// root. The scheduler it started repairs a legacy run it registered no request for,
// and the sessions module it composed answers a managed Stop with the real
// authorizer's verdict instead of the unwired refusal.
func TestBootBindsManagedStopAndSchedulesRunLineageRepair(t *testing.T) {
	saved := runLineageRepairInterval
	runLineageRepairInterval = 150 * time.Millisecond
	t.Cleanup(func() { runLineageRepairInterval = saved })

	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Acme", Slug: "acme-w3", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("create org: %v", err)
	}
	var defaultWS model.ID
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		ws, err := sc.DefaultWorkspace(ctx)
		defaultWS = ws.ID
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}
	sid := legacyIdentity(t, eng.store, tenant, "", "")
	run := legacyRuns(t, eng.store, tenant, sid, 1)[0]

	deadline := time.Now().Add(30 * time.Second)
	for {
		got, null := runLineageOf(t, eng.store, tenant, run)
		if !null && got == defaultWS.String() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the booted scheduler never repaired %s (lineage %q null=%t)", run, got, null)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The real authenticator, a real member at AAL1, the real composed authorizer.
	if _, _, err := eng.authr.BootstrapSuperadminOwning(ctx, "root@w3.test", "rootpassword1", tenant); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	adminToken, _, err := eng.authr.Login(ctx, "root@w3.test", "rootpassword1", "127.0.0.1")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	admin, err := eng.authr.Authenticate(ctx, adminToken)
	if err != nil {
		t.Fatalf("admin authenticate: %v", err)
	}
	user, err := eng.authr.CreateUser(ctx, admin, auth.NewUser{Email: "editor@w3.test", Password: "editorpassword1"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := eng.authr.GrantMembership(ctx, admin, user.ID, tenant, auth.RoleEditor, ""); err != nil {
		t.Fatalf("grant editor: %v", err)
	}
	token, _, err := eng.authr.Login(ctx, "editor@w3.test", "editorpassword1", "127.0.0.1")
	if err != nil {
		t.Fatalf("editor login: %v", err)
	}
	editor, err := eng.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatalf("editor authenticate: %v", err)
	}

	permission := auth.Permission("sessions:live:read")
	question := sessions.ManagedStopQuestion{
		Permission: permission,
		Resource:   auth.ResourceAttrs{Kind: permission.Resource(), ID: run, WorkspaceID: defaultWS},
		Route:      auth.RouteMetadata{CedarAction: "run:stop", RBACMinimumRole: auth.RoleEditor, MinimumAAL: auth.AAL3},
	}
	req := sessions.ManagedStopRequest{
		Principal: editor, RunRef: run, ExpectedLaunch: model.NewID(),
		OperationID: "w3-boot-binding", Reason: "boot binding",
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	unbound, err := sessions.New().StopManagedRun(callCtx, tenant, question, req)
	if !errors.Is(err, sessions.ErrManagedStopUnwired) || unbound.Outcome != sessions.ManagedStopUnwired {
		t.Fatalf("an unbound module = %+v, %v; want the unwired refusal", unbound, err)
	}
	res, err := eng.sessionsMod.StopManagedRun(callCtx, tenant, question, req)
	if err != nil {
		t.Fatalf("booted StopManagedRun: %v (%+v)", err, res)
	}
	if res.Outcome != sessions.ManagedStopStepUpRequired || res.Attempted {
		t.Fatalf("booted managed Stop = %+v, want the real authorizer's step_up_required with nothing attempted", res)
	}
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.EvidenceOperations().Get(ctx, store.ManagedStopOperationPrefix+"w3-boot-binding")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("the refused managed Stop left journal state: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("read the journal: %v", err)
	}
	t.Logf("booted managed Stop answered %q (%s); the scheduled repair set lineage %s", res.Outcome, res.Detail, defaultWS)
}

// TestRunLineageRepairLoopRunsEachPassToExhaustion: a pass follows the module's
// cursor across pages with the captured bound, a page that repaired nothing does
// not end it, unresolved identities stay NULL, and repeating the pass is safe.
func TestRunLineageRepairLoopRunsEachPassToExhaustion(t *testing.T) {
	for _, be := range lineageBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f := newLineageFixture(t, be.config(t))
			ctx := context.Background()
			defA := f.defaultWorkspace(f.tenantA)
			wsW := f.createWorkspace(f.tenantA, "w3-scoped")
			// 300 sessionless rows first: the entire first page repairs nothing.
			legacyRuns(t, f.raw, f.tenantA, "", 300)
			missing := legacyRuns(t, f.raw, f.tenantA, "osn_"+model.NewID().String(), 1)[0]
			target := legacyIdentity(t, f.raw, f.tenantA, "", "")
			merged := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", target), 1)[0]
			fromDefault := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]
			fromW := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, wsW, ""), 1)[0]

			obs := &observedRepairer{}
			log := &loopLog{}
			l := f.loop(obs, log)
			if err := l.runOnce(ctx); err != nil {
				t.Fatalf("runOnce: %v", err)
			}
			pages := obs.pagesFor(f.tenantA)
			if len(pages) != 2 {
				t.Fatalf("tenant A pass took %d pages, want 2", len(pages))
			}
			first, second := pages[0], pages[1]
			if first.cursor != (sessions.RunLineageRepairCursor{}) {
				t.Fatalf("the pass did not start from the zero cursor: %+v", first.cursor)
			}
			if first.result.Repaired != 0 || first.result.Exhausted || first.result.Scanned != 256 {
				t.Fatalf("first page = %+v, want a full page that repaired nothing and is not exhaustion", first.result)
			}
			if second.cursor != first.result.Next || second.cursor.UpperBound.IsZero() {
				t.Fatalf("second page cursor %+v, want the first page's Next %+v", second.cursor, first.result.Next)
			}
			if !second.result.Exhausted || second.result.Repaired != 2 {
				t.Fatalf("second page = %+v, want exhaustion with the two resolvable runs repaired", second.result)
			}
			f.requireLineage(f.tenantA, fromDefault, defA)
			f.requireLineage(f.tenantA, fromW, wsW)
			f.requireLineage(f.tenantA, missing, "")
			f.requireLineage(f.tenantA, merged, "")

			rec, ok := log.find("tenant pass exhausted", f.tenantA)
			if !ok {
				t.Fatal("the exhausted pass was not reported")
			}
			want := map[string]any{"pages": int64(2), "scanned": int64(304), "repaired": int64(2),
				"conflicts": int64(0), "unresolved": int64(302), "exhausted": true}
			for k, v := range want {
				if rec.attrs[k] != v {
					t.Fatalf("reported %s = %v, want %v (record %+v)", k, rec.attrs[k], v, rec.attrs)
				}
			}
			if pagesB := obs.pagesFor(f.tenantB); len(pagesB) != 1 || !pagesB[0].result.Exhausted || pagesB[0].result.Scanned != 0 {
				t.Fatalf("empty tenant B pass = %+v, want one exhausted empty page", pagesB)
			}
			if _, reported := log.find("tenant pass exhausted", f.tenantB); reported {
				t.Fatal("an empty tenant pass was reported as work")
			}

			// Repeating the pass is safe: nothing resolvable is left, the unresolved
			// rows are counted again, and the pass still ends only at exhaustion.
			again := &observedRepairer{}
			pass, err := f.loop(again, &loopLog{}).repairTenant(ctx, f.tenantA)
			if err != nil {
				t.Fatalf("repeat pass: %v", err)
			}
			if pass.Pages != 2 || pass.Repaired != 0 || pass.Unresolved != 302 || pass.Conflicts != 0 || !pass.Exhausted {
				t.Fatalf("repeat pass = %+v, want 2 pages, 0 repaired, 302 unresolved, exhausted", pass)
			}
		})
	}
}

// TestRunLineageRepairLoopStopsOnLifecycleCancellationAndRestartCompletes: the
// engine lifecycle ending mid-pass stops further pages; a restarted loop repeats
// the pass from the start and completes it.
func TestRunLineageRepairLoopStopsOnLifecycleCancellationAndRestartCompletes(t *testing.T) {
	f := newLineageFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	defA := f.defaultWorkspace(f.tenantA)
	legacyRuns(t, f.raw, f.tenantA, "", 300)
	resolvable := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]

	preCanceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	idle := &observedRepairer{}
	if err := f.loop(idle, &loopLog{}).runOnce(preCanceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("runOnce under an ended lifecycle = %v, want context.Canceled", err)
	}
	if len(idle.calls) != 0 {
		t.Fatalf("an ended lifecycle still repaired %d pages", len(idle.calls))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := &observedRepairer{after: func(call int, _ model.TenantID, _ sessions.RunLineageRepairResult) {
		if call == 1 {
			cancel()
		}
	}}
	log := &loopLog{}
	if err := f.loop(obs, log).runOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("runOnce canceled mid-pass = %v, want context.Canceled", err)
	}
	if len(obs.calls) != 1 {
		t.Fatalf("the canceled pass ran %d pages, want exactly the page in flight", len(obs.calls))
	}
	if _, ok := log.find("interrupted by the engine lifecycle", f.tenantA); !ok {
		t.Fatal("the interrupted pass was not reported")
	}
	f.requireLineage(f.tenantA, resolvable, "")

	restarted := &observedRepairer{}
	if err := f.loop(restarted, &loopLog{}).runOnce(context.Background()); err != nil {
		t.Fatalf("restarted runOnce: %v", err)
	}
	pages := restarted.pagesFor(f.tenantA)
	if len(pages) != 2 || pages[0].cursor != (sessions.RunLineageRepairCursor{}) {
		t.Fatalf("restarted pass pages = %+v, want a fresh two-page pass", pages)
	}
	f.requireLineage(f.tenantA, resolvable, defA)
}

// TestRunLineageRepairLoopFollowsLeadership: a standby repairs nothing, a demotion
// between pages ends the tick, and a promoted node completes a new pass.
func TestRunLineageRepairLoopFollowsLeadership(t *testing.T) {
	f := newLineageFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	defA, defB := f.defaultWorkspace(f.tenantA), f.defaultWorkspace(f.tenantB)
	legacyRuns(t, f.raw, f.tenantA, "", 300)
	runA := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]
	runB := legacyRuns(t, f.raw, f.tenantB, legacyIdentity(t, f.raw, f.tenantB, "", ""), 1)[0]

	f.le.active.Store(false)
	standby := &observedRepairer{}
	if err := f.loop(standby, &loopLog{}).runOnce(context.Background()); err != nil {
		t.Fatalf("standby runOnce: %v", err)
	}
	if len(standby.calls) != 0 {
		t.Fatalf("a standby repaired %d pages", len(standby.calls))
	}

	f.le.active.Store(true)
	demoted := &observedRepairer{after: func(call int, _ model.TenantID, _ sessions.RunLineageRepairResult) {
		if call == 1 {
			f.le.active.Store(false)
		}
	}}
	log := &loopLog{}
	if err := f.loop(demoted, log).runOnce(context.Background()); err != nil {
		t.Fatalf("demoted runOnce: %v", err)
	}
	if len(demoted.calls) != 1 {
		t.Fatalf("a node demoted after its first page ran %d pages", len(demoted.calls))
	}
	rec, ok := log.find("stopped before exhaustion", model.TenantID(""))
	if !ok || rec.attrs["stopped"] != "leadership" || rec.attrs["exhausted"] != false {
		t.Fatalf("the demotion was not reported as a leadership stop: %+v", rec)
	}
	f.requireLineage(f.tenantA, runA, "")
	f.requireLineage(f.tenantB, runB, "")

	f.le.active.Store(true)
	promoted := &observedRepairer{}
	if err := f.loop(promoted, &loopLog{}).runOnce(context.Background()); err != nil {
		t.Fatalf("promoted runOnce: %v", err)
	}
	f.requireLineage(f.tenantA, runA, defA)
	f.requireLineage(f.tenantB, runB, defB)
}

// TestRunLineageRepairLoopHonorsTenantServiceAndGuards: the system tenant is never
// repaired, a withdrawn tenant is not attempted, and a tenant withdrawn after
// enumeration is refused by the composed guard while the others still complete.
func TestRunLineageRepairLoopHonorsTenantServiceAndGuards(t *testing.T) {
	f := newLineageFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	ctx := context.Background()
	defA, defB := f.defaultWorkspace(f.tenantA), f.defaultWorkspace(f.tenantB)
	runA := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]
	runB := legacyRuns(t, f.raw, f.tenantB, legacyIdentity(t, f.raw, f.tenantB, "", ""), 1)[0]
	setStatus := func(tenant model.TenantID, status model.LifecycleStatus) {
		t.Helper()
		if err := f.raw.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.SetOrgStatus(ctx, tenant, status)
			return err
		}); err != nil {
			t.Fatalf("set org status: %v", err)
		}
	}

	setStatus(f.tenantB, model.StatusSuspended)
	obs := &observedRepairer{}
	if err := f.loop(obs, &loopLog{}).runOnce(ctx); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	for _, c := range obs.calls {
		if c.tenant.IsSystem() || c.tenant == f.tenantB {
			t.Fatalf("the loop attempted tenant %s (system or withdrawn)", c.tenant)
		}
	}
	f.requireLineage(f.tenantA, runA, defA)
	f.requireLineage(f.tenantB, runB, "")

	// Withdraw A after enumeration: the guard refuses its page, B still completes.
	setStatus(f.tenantB, model.StatusActive)
	runA2 := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]
	var once sync.Once
	guarded := &observedRepairer{before: func(_ int, tenant model.TenantID) {
		if tenant == f.tenantA {
			once.Do(func() { setStatus(f.tenantA, model.StatusSuspended) })
		}
	}}
	log := &loopLog{}
	if err := f.loop(guarded, log).runOnce(ctx); err != nil {
		t.Fatalf("runOnce with a mid-tick withdrawal: %v", err)
	}
	pagesA := guarded.pagesFor(f.tenantA)
	if len(pagesA) != 1 || pagesA[0].err == nil {
		t.Fatalf("tenant A pages = %+v, want one page refused by the guard", pagesA)
	}
	if rec, ok := log.find("tenant pass failed", f.tenantA); !ok || rec.level != slog.LevelWarn {
		t.Fatalf("the refused tenant pass was not reported: %+v", rec)
	}
	f.requireLineage(f.tenantA, runA2, "")
	f.requireLineage(f.tenantB, runB, defB)
	t.Logf("the guard refused tenant A's page with: %v", pagesA[0].err)
}

// stalledRepairer answers every page with the same unexhausted cursor.
type stalledRepairer struct{ calls int }

func (s *stalledRepairer) RepairRunLineage(context.Context, model.TenantID, sessions.RunLineageRepairCursor) (sessions.RunLineageRepairResult, error) {
	s.calls++
	return sessions.RunLineageRepairResult{Scanned: 256, Next: sessions.RunLineageRepairCursor{After: "a", UpperBound: "z"}}, nil
}

// TestRunLineageRepairLoopRefusesACursorThatDoesNotAdvance: a continuation that
// does not advance is refused instead of being followed forever.
func TestRunLineageRepairLoopRefusesACursorThatDoesNotAdvance(t *testing.T) {
	f := newLineageFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	stalled := &stalledRepairer{}
	l := &runLineageRepairLoop{st: f.st, repair: stalled, interval: time.Minute, log: discardLog()}
	pass, err := l.repairTenant(context.Background(), f.tenantA)
	if !errors.Is(err, errRunLineageCursorStalled) {
		t.Fatalf("repairTenant over a stalled cursor = %+v, %v; want the stalled refusal", pass, err)
	}
	if stalled.calls != 2 || pass.Exhausted {
		t.Fatalf("stalled cursor was followed for %d pages (pass %+v), want refusal at the second", stalled.calls, pass)
	}
}

// TestManagedStopAdmissionTimeoutIsValidatedAtBoot: T parses from the operator
// environment, and a zero, negative or unparsable value fails the real boot.
func TestManagedStopAdmissionTimeoutIsValidatedAtBoot(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultManagedStopAdmissionTimeout, true},
		{"3s", 3 * time.Second, true},
		{" 2m ", 2 * time.Minute, true},
		{"0", 0, false},
		{"0s", 0, false},
		{"-1s", 0, false},
		{"10", 0, false},
		{"soon", 0, false},
	}
	for _, c := range cases {
		got, err := managedStopAdmissionTimeout(func(string) string { return c.raw })
		if (err == nil) != c.ok || (c.ok && got != c.want) {
			t.Fatalf("managedStopAdmissionTimeout(%q) = %v, %v; want %v ok=%t", c.raw, got, err, c.want, c.ok)
		}
	}
	for _, raw := range []string{"0", "-5s", "soon"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(envManagedStopAdmissionTimeout, raw)
			eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
			if err == nil {
				_ = eng.Close()
				t.Fatalf("boot accepted %s=%q", envManagedStopAdmissionTimeout, raw)
			}
			if !strings.Contains(err.Error(), envManagedStopAdmissionTimeout) {
				t.Fatalf("boot refused with %v, which does not name %s", err, envManagedStopAdmissionTimeout)
			}
		})
	}
}

// TestRunLineageRepairCountsAConcurrentWriterAsAConflictOnPostgres is the
// backend-sensitive case: on PostgreSQL a live-run writer in ANOTHER transaction
// holds the row while the repair page writes it. The page must count a conflict,
// leave the row NULL and still exhaust; the next pass repairs it.
func TestRunLineageRepairCountsAConcurrentWriterAsAConflictOnPostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skip("PostgreSQL NOT exercised: no configured disposable server")
	}
	pg := enginetest.IsolatedPostgres(t)
	f := newLineageFixture(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, OwnerDSN: pg.Owner})
	ctx := context.Background()
	defA := f.defaultWorkspace(f.tenantA)
	run := legacyRuns(t, f.raw, f.tenantA, legacyIdentity(t, f.raw, f.tenantA, "", ""), 1)[0]

	db, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open the writer connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	holder, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the concurrent writer: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	res, err := holder.ExecContext(ctx, `UPDATE sessions_run SET version = version + 1 WHERE run_ref = $1`, run)
	if err != nil {
		t.Fatalf("concurrent writer update: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("concurrent writer touched %d rows", n)
	}

	type outcome struct {
		pass runLineagePass
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		pass, err := f.loop(&observedRepairer{}, &loopLog{}).repairTenant(ctx, f.tenantA)
		done <- outcome{pass, err}
	}()

	var waiting int
	deadline := time.Now().Add(30 * time.Second)
	for waiting == 0 {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatalf("observe lock waiters: %v", err)
		}
		if waiting > 0 {
			break
		}
		select {
		case o := <-done:
			t.Fatalf("the repair page finished without waiting on the concurrent writer: %+v, %v", o.pass, o.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the repair page never waited on the concurrent writer's row lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := holder.Commit(); err != nil {
		t.Fatalf("commit the concurrent writer: %v", err)
	}
	var got outcome
	select {
	case got = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the repair page did not finish after the writer committed")
	}
	if got.err != nil {
		t.Fatalf("repair pass under a concurrent writer: %v (%+v)", got.err, got.pass)
	}
	if got.pass.Conflicts != 1 || got.pass.Repaired != 0 || !got.pass.Exhausted {
		t.Fatalf("pass under a concurrent writer = %+v, want one conflict, nothing repaired, exhausted", got.pass)
	}
	f.requireLineage(f.tenantA, run, "")

	next, err := f.loop(&observedRepairer{}, &loopLog{}).repairTenant(ctx, f.tenantA)
	if err != nil || next.Repaired != 1 || next.Conflicts != 0 || !next.Exhausted {
		t.Fatalf("next pass = %+v, %v; want the conflicted row repaired", next, err)
	}
	f.requireLineage(f.tenantA, run, defA)
}
