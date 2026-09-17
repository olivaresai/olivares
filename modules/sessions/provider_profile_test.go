// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 acceptance rows 1, 4 (alias half) and 7 (schema half), on BOTH engines.
// SQLite alone does not prove the uniqueness/rollback behaviour this plane leans
// on (identity_crossbackend_test.go says why), so every table here has a Postgres
// leg that is logged as NOT exercised when no server is configured, never
// silently dropped.

const testEnvRef = "env-test-node-a"

// profileBackend is one engine + a DSN a test can REOPEN (a restart is a close
// and a second Open against the same database).
type profileBackend struct {
	name   string
	engine store.Engine
	dsn    string
	// rawOpen returns a raw database/sql handle for catalog inspection.
	rawOpen func(t *testing.T) *sql.DB
}

func profileBackends(t *testing.T) []profileBackend {
	t.Helper()
	sqlitePath := filepath.Join(t.TempDir(), "profiles.db")
	out := []profileBackend{{
		name: "sqlite", engine: store.EngineSQLite, dsn: sqlitePath,
		rawOpen: func(t *testing.T) *sql.DB {
			db, err := sql.Open("sqlite", sqlitePath)
			if err != nil {
				t.Fatal(err)
			}
			return db
		},
	}}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgres(t)
		out = append(out, profileBackend{
			name: "postgres", engine: store.EnginePostgres, dsn: pg.App,
			rawOpen: func(t *testing.T) *sql.DB {
				db, err := sql.Open("pgx", pg.App)
				if err != nil {
					t.Fatal(err)
				}
				return db
			},
		})
	} else {
		t.Logf("%s unset: Postgres NOT exercised (SQLite-only this run)", enginetest.EnvSuperuserDSN)
	}
	return out
}

// openProfileModule opens (or REOPENS) a module against be and returns it with a
// business tenant. register defaults to the module's own RegisterSchema.
func openProfileModule(t *testing.T, be profileBackend, register func(store.ExtensionRegistry) error) (*Module, store.Store) {
	t.Helper()
	m := New()
	m.clock = &testClock{now: baseTime}
	m.UseExecutionEnvironmentRef(testEnvRef)
	if register == nil {
		register = m.RegisterSchema
	}
	st, err := engine.Open(context.Background(), store.Config{Engine: be.engine, DSN: be.dsn, Debug: true}, register)
	if err != nil {
		t.Fatalf("open %s: %v", be.name, err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	return m, st
}

func ensureTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	return tenant
}

// twoHomes makes two real, distinct home directories for one driver.
func twoHomes(t *testing.T) (configA, userA, configB, userB string) {
	t.Helper()
	root := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return real
	}
	return mk("a/config"), mk("a/home"), mk("b/config"), mk("b/home")
}

func mustCreateProfile(t *testing.T, m *Module, tenant model.TenantID, in CreateProfileInput) ProviderProfile {
	t.Helper()
	p, err := m.CreateProfile(context.Background(), tenant, in)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return p
}

// Row 1: the same database after a restart holds the profiles; rename keeps id
// and home; retiring frees the home for a NEW id and never revives the old one.
func TestProviderProfile_LifecycleSurvivesRestart(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			configA, userA, configB, userB := twoHomes(t)
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "acme-"+be.name)

			a := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "Claude", ConfigHome: configA, UserHome: userA, DisplayName: "home A"})
			if a.Driver != "claude" || a.State != ProfileActive || a.EnvironmentRef != testEnvRef || a.ConfigHome != configA {
				t.Fatalf("created = %+v", a)
			}
			b := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB, DisplayName: "home B"})
			if a.Ref == b.Ref {
				t.Fatal("two homes minted one ref")
			}

			// Rename: id and home unchanged, only the label moves.
			name := "home A (renamed)"
			renamed, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{DisplayName: &name})
			if err != nil {
				t.Fatalf("rename: %v", err)
			}
			if renamed.Ref != a.Ref || renamed.ConfigHome != a.ConfigHome || renamed.DisplayName != name {
				t.Fatalf("rename changed identity: %+v", renamed)
			}
			// Disable refuses a launch snapshot; enable restores it, same id, same home.
			disabled := ProfileDisabled
			if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &disabled}); err != nil {
				t.Fatalf("disable: %v", err)
			}
			if _, err := m.resolveLaunchProfile(ctx, tenant, a.Ref); !errors.Is(err, ErrProfileDisabled) {
				t.Fatalf("disabled profile resolved for launch: %v", err)
			}
			active := ProfileActive
			enabled, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &active})
			if err != nil || enabled.Ref != a.Ref || enabled.ConfigHome != configA {
				t.Fatalf("enable: %+v %v", enabled, err)
			}

			// A second active label for the SAME home is refused by the database.
			if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userB}); !errors.Is(err, ErrProfileHomeTaken) {
				t.Fatalf("second profile on one home = %v, want ErrProfileHomeTaken", err)
			}

			// Retire A: the home is free, the OLD id stays retired and immutable.
			retired := ProfileRetired
			gone, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &retired})
			if err != nil || gone.State != ProfileRetired || gone.RetiredAt == "" {
				t.Fatalf("retire: %+v %v", gone, err)
			}
			if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{DisplayName: &name}); !errors.Is(err, ErrProfileRetired) {
				t.Fatalf("rename of a retired profile = %v, want ErrProfileRetired", err)
			}
			if _, err := m.PatchProfile(ctx, tenant, a.Ref, ProfilePatch{State: &active}); !errors.Is(err, ErrProfileRetired) {
				t.Fatalf("re-activating a retired profile = %v, want ErrProfileRetired", err)
			}
			if _, err := m.resolveLaunchProfile(ctx, tenant, a.Ref); !errors.Is(err, ErrProfileRetired) {
				t.Fatalf("retired profile resolved for launch: %v", err)
			}
			a2 := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "home A (renamed)"})
			if a2.Ref == a.Ref {
				t.Fatal("a new incarnation of the home reused the retired id")
			}

			// RESTART: close and reopen the same database.
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m2, _ := openProfileModule(t, be, nil)
			for _, want := range []ProviderProfile{gone, b, a2} {
				got, err := m2.GetProfile(ctx, tenant, want.Ref)
				if err != nil {
					t.Fatalf("after restart %s: %v", want.Ref, err)
				}
				if got.State != want.State || got.ConfigHome != want.ConfigHome || got.UserHome != want.UserHome || got.Driver != want.Driver || got.EnvironmentRef != want.EnvironmentRef {
					t.Fatalf("after restart %s = %+v, want %+v", want.Ref, got, want)
				}
			}
			list, _, err := m2.ListProfiles(ctx, tenant, ProfileActive, model.Query{Limit: 50})
			if err != nil || len(list) != 2 {
				t.Fatalf("active after restart = %d (%v), want 2", len(list), err)
			}
		})
	}
}

// Row 1 (last sentence): concurrent creates of ONE active home converge on ONE
// identity, on SQLite and on Postgres — decided by the unique index, not by the
// writers agreeing.
func TestProviderProfile_ConcurrentCreateSameHome(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			configA, userA, _, _ := twoHomes(t)
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "race-"+be.name)
			const n = 12
			var wg sync.WaitGroup
			var mu sync.Mutex
			var okRefs []string
			var conflicts, other int
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA})
					mu.Lock()
					defer mu.Unlock()
					switch {
					case err == nil:
						okRefs = append(okRefs, p.Ref)
					case errors.Is(err, ErrProfileHomeTaken):
						conflicts++
					default:
						other++
						t.Errorf("unexpected create error: %v", err)
					}
				}()
			}
			wg.Wait()
			if len(okRefs) != 1 || conflicts != n-1 || other != 0 {
				t.Fatalf("[%s] ok=%d conflicts=%d other=%d, want 1/%d/0", be.name, len(okRefs), conflicts, other, n-1)
			}
			if got := countRows(t, m, tenant, providerProfileKind); got != 1 {
				t.Fatalf("[%s] profile rows = %d, want 1", be.name, got)
			}
		})
	}
}

// Paths are validated on the node that owns them, never trusted, and a missing
// home is never created as a fallback.
func TestProviderProfile_HomeValidation(t *testing.T) {
	ctx := context.Background()
	m, st := openProfileModule(t, profileBackends(t)[0], nil)
	tenant := ensureTenant(t, st, "paths")
	configA, userA, _, _ := twoHomes(t)

	cases := []struct {
		name string
		in   CreateProfileInput
		want int
	}{
		{"relative config home", CreateProfileInput{Driver: "claude", ConfigHome: "relative/dir", UserHome: userA}, http.StatusBadRequest},
		{"missing config home", CreateProfileInput{Driver: "claude", ConfigHome: filepath.Join(t.TempDir(), "nope"), UserHome: userA}, http.StatusUnprocessableEntity},
		{"file as user home", CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: mustTempFile(t)}, http.StatusUnprocessableEntity},
		{"empty driver", CreateProfileInput{Driver: "", ConfigHome: configA, UserHome: userA}, http.StatusBadRequest},
		{"driver with space", CreateProfileInput{Driver: "claude code", ConfigHome: configA, UserHome: userA}, http.StatusBadRequest},
		{"control char in name", CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, DisplayName: "bad\x01name"}, http.StatusBadRequest},
		{"foreign environment", CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA, EnvironmentRef: "env-other-node"}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		_, err := m.CreateProfile(ctx, tenant, tc.in)
		if statusOf(err) != tc.want {
			t.Errorf("%s: err=%v status=%d, want %d", tc.name, err, statusOf(err), tc.want)
		}
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused missing home was created as a fallback")
	}
	if got := countRows(t, m, tenant, providerProfileKind); got != 0 {
		t.Fatalf("refused creates left %d rows", got)
	}

	// A symlinked home is stored as its canonical target, and the child gets
	// that target — never a re-resolution of the operator's alias later on.
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(configA, link); err != nil {
		t.Fatal(err)
	}
	p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: link, UserHome: userA})
	if p.ConfigHome != configA {
		t.Fatalf("symlinked home stored as %q, want canonical %q", p.ConfigHome, configA)
	}
	// The same canonical home through the alias is the SAME home slot.
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA}); !errors.Is(err, ErrProfileHomeTaken) {
		t.Fatalf("alias and target counted as two homes: %v", err)
	}

	// Without a local environment identity the profiled plane is deny-closed.
	m.rt.environmentRef = ""
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA}); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("create without environment = %v, want ErrEnvironmentUnavailable", err)
	}
	if _, err := m.resolveLaunchProfile(ctx, tenant, p.Ref); !errors.Is(err, ErrEnvironmentUnavailable) {
		t.Fatalf("resolve without environment = %v, want ErrEnvironmentUnavailable", err)
	}
	// A profile of another environment is shown but never launched here.
	m.rt.environmentRef = "env-elsewhere"
	if _, err := m.resolveLaunchProfile(ctx, tenant, p.Ref); !errors.Is(err, ErrProfileForeignEnvironment) {
		t.Fatalf("foreign resolve = %v, want ErrProfileForeignEnvironment", err)
	}
	// A driver without an operated runner is observable, not launchable.
	m.rt.environmentRef = testEnvRef
	codex := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "codex", ConfigHome: userA, UserHome: userA})
	if _, err := m.resolveLaunchProfile(ctx, tenant, codex.Ref); !errors.Is(err, ErrProfileDriverNotOperable) {
		t.Fatalf("codex resolve = %v, want ErrProfileDriverNotOperable", err)
	}
	// A home that disappeared after registration refuses the launch before any
	// runner or credential is touched.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.MkdirAll(moved, 0o700); err != nil {
		t.Fatal(err)
	}
	movedReal, _ := filepath.EvalSymlinks(moved)
	q := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: movedReal, UserHome: userA})
	if err := os.RemoveAll(movedReal); err != nil {
		t.Fatal(err)
	}
	if _, err := m.resolveLaunchProfile(ctx, tenant, q.Ref); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("resolve on a vanished home = %v, want 422", err)
	}
}

func mustTempFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The HTTP surface: exact tiers, paths only on the authorized configuration
// read, retirement only through its admin route.
func TestProviderProfile_API(t *testing.T) {
	m := New()
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.viewerToken(admin, tenant, "v@acme.com")
	configA, userA, _, _ := twoHomes(t)

	// A viewer reads; a viewer does not create.
	if r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", viewer, map[string]any{"driver": "claude", "config_home": configA, "user_home": userA}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer create = %d %s", r.code, r.raw)
	}
	r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": configA, "user_home": userA, "display_name": "Home A"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	ref, _ := r.body["profile_ref"].(string)
	if _, leaked := r.body["config_home"]; leaked || ref == "" {
		t.Fatalf("create response leaks a path or has no ref: %s", r.raw)
	}
	if r.body["operable"] != true || r.body["local_environment"] != true {
		t.Fatalf("create response flags: %s", r.raw)
	}
	// An unknown field (a home slot, a state, an environment override to smuggle)
	// is rejected, not dropped.
	if r := h.doJSON("POST", "/v1/m/sessions/provider-profiles", admin, map[string]any{"driver": "claude", "config_home": configA, "user_home": userA, "home_slot": "x"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %s", r.code, r.raw)
	}

	// List and get: labels and references, never paths.
	r = h.do("GET", "/v1/m/sessions/provider-profiles", viewer, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list items = %d: %s", len(items), r.raw)
	}
	if row, _ := items[0].(map[string]any); row["config_home"] != nil || row["user_home"] != nil {
		t.Fatalf("list leaks paths: %s", r.raw)
	}
	if r := h.do("GET", "/v1/m/sessions/provider-profiles/"+ref, viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["config_home"] != nil {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}

	// The configuration read is the ONLY place the paths appear, and it is admin.
	if r := h.do("GET", "/v1/m/sessions/provider-profiles/"+ref+"/configuration", viewer, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer configuration = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/sessions/provider-profiles/"+ref+"/configuration", admin, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["config_home"] != configA || r.body["user_home"] != userA {
		t.Fatalf("admin configuration = %d %s", r.code, r.raw)
	}

	// PATCH: rename and disable; identity fields are not even accepted keys.
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, admin, map[string]any{"display_name": "Renamed", "state": "disabled"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["state"] != "disabled" || r.body["display_name"] != "Renamed" || r.body["operable"] != false {
		t.Fatalf("patch = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, admin, map[string]any{"config_home": userA}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("patch of an identity field = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, admin, map[string]any{"state": "retired"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("patch to retired = %d %s, want 400 (own admin route)", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/provider-profiles/"+ref+"/retire", viewer, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer retire = %d %s", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/sessions/provider-profiles/"+ref+"/retire", admin, tenantHdr(tenant)); r.code != http.StatusOK || r.body["state"] != "retired" {
		t.Fatalf("retire = %d %s", r.code, r.raw)
	}
	if r := h.doJSON("PATCH", "/v1/m/sessions/provider-profiles/"+ref, admin, map[string]any{"state": "active"}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("patch on a retired profile = %d %s", r.code, r.raw)
	}
	// Tenant isolation: the ref does not exist for another tenant.
	other := h.createOrg(admin, "globex")
	if r := h.do("GET", "/v1/m/sessions/provider-profiles/"+ref, admin, tenantHdr(other)); r.code != http.StatusNotFound {
		t.Fatalf("cross-tenant get = %d %s", r.code, r.raw)
	}
}

// fakeSourceResolver is the composition port under test control.
type fakeSourceResolver struct {
	sources   map[model.ID]SourceRevision
	forbidden map[model.ID]bool
	calls     int
}

func (f *fakeSourceResolver) ResolveAppliedSource(_ context.Context, _ auth.Principal, _ model.TenantID, id model.ID) (SourceRevision, error) {
	f.calls++
	if f.forbidden[id] {
		return SourceRevision{}, ErrSourceForbidden
	}
	s, ok := f.sources[id]
	if !ok {
		return SourceRevision{}, ErrSourceNotFound
	}
	return s, nil
}

// Rows 1 and 6 (binding half): bindings survive a restart, are keyed by
// (source id, APPLIED revision, environment), never by name, and are refused for
// a stale revision, another tenant, a forbidden actor, a disabled profile.
func TestProviderBinding_Service(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			configA, userA, configB, userB := twoHomes(t)
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "bind-"+be.name)
			p := auth.Principal{}

			profA := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA})
			profB := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB})

			// No port wired: deny-closed.
			srcA := model.NewID()
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcA, SourceRevision: 1, ProfileRef: profA.Ref}); !errors.Is(err, ErrNoSourceResolver) {
				t.Fatalf("binding without port = %v", err)
			}
			srcOther := model.NewID()
			srcForbidden := model.NewID()
			resolver := &fakeSourceResolver{sources: map[model.ID]SourceRevision{
				srcA:         {ID: srcA, Version: 2, Name: "claude-home-a", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
				srcOther:     {ID: srcOther, Version: 1, Name: "elsewhere", Tenant: "other-tenant", Kind: "claude", EnvironmentRef: testEnvRef},
				srcForbidden: {ID: srcForbidden, Version: 1, Name: "global", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef},
			}, forbidden: map[model.ID]bool{srcForbidden: true}}
			m.UseProviderSourceResolver(resolver)

			// The requested revision must be the APPLIED one (2), not the desired
			// one somebody typed (1 or 3).
			for _, rev := range []int64{1, 3} {
				if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcA, SourceRevision: rev, ProfileRef: profA.Ref}); !errors.Is(err, ErrSourceRevisionStale) {
					t.Fatalf("revision %d = %v, want ErrSourceRevisionStale", rev, err)
				}
			}
			b, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcA, SourceRevision: 2, ProfileRef: profA.Ref})
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			if b.SourceRevision != 2 || b.ProfileRef != profA.Ref || b.Driver != "claude" || b.State != BindingActive || b.EnvironmentRef != testEnvRef || b.SelectorKey != SelectorDedicated {
				t.Fatalf("binding = %+v", b)
			}
			// The same (source, revision, environment) cannot be dedicated twice — not
			// even to another profile: the database refuses it.
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcA, SourceRevision: 2, ProfileRef: profB.Ref}); !errors.Is(err, ErrBindingExists) {
				t.Fatalf("second binding of one revision = %v, want ErrBindingExists", err)
			}
			// Another tenant's source, a forbidden actor, an unknown id.
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcOther, SourceRevision: 1, ProfileRef: profA.Ref}); !errors.Is(err, ErrSourceTenantMismatch) {
				t.Fatalf("other tenant's source = %v", err)
			}
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcForbidden, SourceRevision: 1, ProfileRef: profA.Ref}); !errors.Is(err, ErrSourceForbidden) {
				t.Fatalf("forbidden source = %v", err)
			}
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: model.NewID(), SourceRevision: 1, ProfileRef: profA.Ref}); !errors.Is(err, ErrSourceNotFound) {
				t.Fatalf("unknown source = %v", err)
			}
			// A disabled profile takes no new binding; the existing one stays.
			disabled := ProfileDisabled
			if _, err := m.PatchProfile(ctx, tenant, profB.Ref, ProfilePatch{State: &disabled}); err != nil {
				t.Fatal(err)
			}
			resolver.sources[srcOther] = SourceRevision{ID: srcOther, Version: 1, Name: "b", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef}
			if _, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcOther, SourceRevision: 1, ProfileRef: profB.Ref}); !errors.Is(err, ErrProfileDisabled) {
				t.Fatalf("binding to a disabled profile = %v", err)
			}

			// A rotated source is ANOTHER revision: the old binding keeps explaining
			// old events and a new one is needed for the new revision.
			resolver.sources[srcA] = SourceRevision{ID: srcA, Version: 3, Name: "claude-home-a", Tenant: tenant.String(), Kind: "claude", EnvironmentRef: testEnvRef}
			if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				if _, ok, err := findActiveBinding(ctx, sc, srcA.String(), 3, testEnvRef); err != nil || ok {
					return errors.New("revision 3 resolved to a binding nobody created")
				}
				old, ok, err := findActiveBinding(ctx, sc, srcA.String(), 2, testEnvRef)
				if err != nil || !ok || old.Ref != b.Ref {
					return errors.New("revision 2 lost its binding")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			b3, err := m.CreateBinding(ctx, tenant, p, CreateBindingInput{SourceID: srcA, SourceRevision: 3, ProfileRef: profA.Ref})
			if err != nil || b3.Ref == b.Ref {
				t.Fatalf("bind revision 3: %+v %v", b3, err)
			}

			// Revoke: idempotent, reassigns nothing, stops attribution.
			revoked, err := m.RevokeBinding(ctx, tenant, b.Ref)
			if err != nil || revoked.State != BindingRevoked || revoked.RevokedAt == "" {
				t.Fatalf("revoke: %+v %v", revoked, err)
			}
			if again, err := m.RevokeBinding(ctx, tenant, b.Ref); err != nil || again.RevokedAt != revoked.RevokedAt {
				t.Fatalf("second revoke: %+v %v", again, err)
			}
			if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				if _, ok, err := findActiveBinding(ctx, sc, srcA.String(), 2, testEnvRef); err != nil || ok {
					return errors.New("a revoked binding still attributes")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			// RESTART: bindings and their states are durable.
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m2, _ := openProfileModule(t, be, nil)
			got, err := m2.GetBinding(ctx, tenant, b3.Ref)
			if err != nil || got.State != BindingActive || got.SourceRevision != 3 || got.ProfileRef != profA.Ref {
				t.Fatalf("after restart: %+v %v", got, err)
			}
			list, _, err := m2.ListBindings(ctx, tenant, profA.Ref, model.Query{Limit: 10})
			if err != nil || len(list) != 2 {
				t.Fatalf("bindings after restart = %d %v", len(list), err)
			}
		})
	}
}

// Row 4 (alias half): the scoped alias is written only for the CURRENT
// incarnation of a run launched under the profile and claim it names; the same
// external id under two profiles is two rows to two sids; a conflict never
// rewrites a sid.
func TestProviderAlias_ScopedBindWithin(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			configA, userA, configB, userB := twoHomes(t)
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "alias-"+be.name)
			profA := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configA, UserHome: userA})
			profB := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: configB, UserHome: userB})

			type run struct {
				ref, sid string
				launch   model.ID
				fence    int64
				profile  string
			}
			seed := func(profile string, fence int64) run {
				t.Helper()
				ref := model.NewID().String()
				sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: ProviderOperated, ExternalID: ref, Origin: OriginOperated})
				if err != nil {
					t.Fatal(err)
				}
				launch := model.NewID()
				if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(runKind)
					if err != nil {
						return err
					}
					_, err = repo.Create(ctx, model.Record{
						colRunRef: ref, colTransport: "stream-json", colPermissionMode: "default",
						colIsolation: "native", colState: stateRunning, colLastEventSeq: int64(0),
						colRunClaimSID: sid, colClaimFence: fence, colClaimHolder: "user:x",
						colRuntimeLaunchID: launch.String(),
						colRunProfileID:    profile, colRunProfileDriver: "claude", colRunProfileEnvRef: testEnvRef,
						colRunProfileConfigHome: configA, colRunProfileUserHome: userA,
					})
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return run{ref: ref, sid: sid, launch: launch, fence: fence, profile: profile}
			}
			ra := seed(profA.Ref, 1)
			rb := seed(profB.Ref, 1)
			bind := func(r run, in managedAliasInput) error {
				return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					return m.bindManagedProviderAliasWithin(ctx, sc, in)
				})
			}
			input := func(r run) managedAliasInput {
				return managedAliasInput{profileID: r.profile, provider: "claude", externalID: "shared-ext-id", sid: r.sid, runRef: r.ref, launchID: r.launch, claimFence: r.fence}
			}
			// Same external id, two homes → two aliases to two DIFFERENT sids.
			if err := bind(ra, input(ra)); err != nil {
				t.Fatalf("bind A: %v", err)
			}
			if err := bind(rb, input(rb)); err != nil {
				t.Fatalf("bind B: %v", err)
			}
			a, okA, _ := m.LookupScopedAlias(ctx, tenant, profA.Ref, "claude", "shared-ext-id")
			b, okB, _ := m.LookupScopedAlias(ctx, tenant, profB.Ref, "claude", "shared-ext-id")
			if !okA || !okB || a.SID == b.SID || a.SID != ra.sid || b.SID != rb.sid || a.RunRef != ra.ref {
				t.Fatalf("aliases: %+v %+v", a, b)
			}
			// The run row captured the provider id in the SAME transaction.
			rec, err := m.loadRun(ctx, tenant, ra.ref)
			if err != nil || rec.String(colClaudeSessionID) != "shared-ext-id" {
				t.Fatalf("run A claude_session_id = %q %v", rec.String(colClaudeSessionID), err)
			}
			// Idempotent re-bind of the same pair.
			if err := bind(ra, input(ra)); err != nil {
				t.Fatalf("rebind A: %v", err)
			}
			if got := countRows(t, m, tenant, providerAliasKind); got != 2 {
				t.Fatalf("alias rows = %d, want 2", got)
			}
			// A conflicting sid under the same key is reported, never forced.
			rc := seed(profA.Ref, 1)
			in := input(rc)
			if err := bind(rc, in); !errors.Is(err, ErrScopedAliasBound) {
				t.Fatalf("conflicting sid = %v, want ErrScopedAliasBound", err)
			}
			if again, _, _ := m.LookupScopedAlias(ctx, tenant, profA.Ref, "claude", "shared-ext-id"); again.SID != ra.sid {
				t.Fatalf("conflict rewrote the alias to %s", again.SID)
			}
			// A frame from an OLD incarnation (stale launch id) binds nothing.
			stale := input(rb)
			stale.externalID, stale.launchID = "other-id", model.NewID()
			if err := bind(rb, stale); err == nil || !isRunConflict(err) {
				t.Fatalf("stale launch id = %v, want a run conflict", err)
			}
			// A wrong claim fence or a wrong profile binds nothing either.
			wrongFence := input(rb)
			wrongFence.externalID, wrongFence.claimFence = "other-id", 99
			if err := bind(rb, wrongFence); err == nil || !isRunConflict(err) {
				t.Fatalf("wrong fence = %v, want a run conflict", err)
			}
			wrongProfile := input(rb)
			wrongProfile.externalID, wrongProfile.profileID = "other-id", profA.Ref
			if err := bind(rb, wrongProfile); err == nil || !isRunConflict(err) {
				t.Fatalf("wrong profile = %v, want a run conflict", err)
			}
			if got := countRows(t, m, tenant, providerAliasKind); got != 2 {
				t.Fatalf("refused binds wrote rows: %d", got)
			}
			// Nothing here touched the LEGACY tenant-wide alias table for "claude":
			// the scoped plane is separate from SG-00's global namespace.
			if got := countRows(t, m, tenant, aliasKind, eq(colProvider, "claude")); got != 0 {
				t.Fatalf("legacy claude aliases = %d, want 0", got)
			}
		})
	}
}

// legacy99aRegistry decorates the store's ExtensionRegistry so that a boot of the
// CURRENT module declares exactly what the 99a binary declared: the same tables,
// guards and migrations — which is what keeps the store's guard-lineage identity
// stable across the two boots — except sessions.live/timeline in their 99a shape
// (the old UNIQUE (tenant_id, session_ref) index, none of the B1 columns) and
// none of the B1 migration files. Opening a database through it produces the
// schema an existing deployment upgrades FROM.
type legacy99aRegistry struct{ store.ExtensionRegistry }

func (r legacy99aRegistry) Register(d model.EntityDescriptor) error {
	switch d.Kind {
	case liveKind:
		d.Fields = legacyLiveFields()
		d.Indexes = []model.IndexSpec{{Name: "sessions_live_ref_uniq", Columns: []string{model.ColTenantID, colSessionRef}, Unique: true}}
	case timelineKind:
		d.Fields = legacyTimelineFields()
	}
	return r.ExtensionRegistry.Register(d)
}

// Migrations hands the store the 99a migration set: every file the module ships
// except the B1 ones (sqlite ≥ 0093, postgres ≥ 0021).
func (r legacy99aRegistry) Migrations(namespace string, fsys fs.FS) error {
	filtered := fstest.MapFS{}
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		version, verr := strconv.Atoi(strings.SplitN(path.Base(p), "_", 2)[0])
		if verr != nil {
			return verr
		}
		if (strings.HasPrefix(p, "sqlite/") && version >= 93) || (strings.HasPrefix(p, "postgres/") && version >= 21) {
			return nil
		}
		body, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return rerr
		}
		filtered[p] = &fstest.MapFile{Data: body}
		return nil
	}); err != nil {
		return err
	}
	return r.ExtensionRegistry.Migrations(namespace, filtered)
}

// SchemaInvariants rewinds the declared invariant set to the 99a state as well.
// The set is LIVE, so a trigger transition introduced after 99a would name a
// migration this registry just filtered out, and the store rejects that plan
// before it can open anything — with a message about the transition rather than
// about the upgrade under test.
func (r legacy99aRegistry) SchemaInvariants(
	namespace string,
	byEngine map[store.Engine][]store.SchemaTrigger,
) error {
	return r.ExtensionRegistry.SchemaInvariants(namespace, communicationInvariantsThrough(
		byEngine, map[store.Engine]int{store.EnginePostgres: 20, store.EngineSQLite: 92},
	))
}

func legacyLiveFields() []model.FieldSpec {
	return []model.FieldSpec{
		{Name: colSessionRef, Kind: model.KindText},
		{Name: colAgentRef, Kind: model.KindText, Nullable: true},
		{Name: colCurrentTool, Kind: model.KindText, Nullable: true},
		{Name: colCurrentRes, Kind: model.KindText, Nullable: true},
		{Name: colCurrentMode, Kind: model.KindText, Nullable: true},
		{Name: colModelRef, Kind: model.KindText, Nullable: true},
		{Name: colInputTokens, Kind: model.KindInt},
		{Name: colOutputTokens, Kind: model.KindInt},
		{Name: colCostMicroUSD, Kind: model.KindInt},
		{Name: colEventCount, Kind: model.KindInt},
		{Name: colToolCalls, Kind: model.KindInt},
		{Name: colFirstEventAt, Kind: model.KindTimestamp},
		{Name: colLastEventAt, Kind: model.KindTimestamp, Indexed: true},
		{Name: colEvasionAt, Kind: model.KindTimestamp, Nullable: true},
		{Name: colGoal, Kind: model.KindText, Nullable: true},
		{Name: colSummary, Kind: model.KindText, Nullable: true},
		{Name: colUnclaimedAt, Kind: model.KindTimestamp, Nullable: true},
		{Name: colEngine, Kind: model.KindText, Nullable: true},
		{Name: colPosture, Kind: model.KindText, Nullable: true},
	}
}

func legacyTimelineFields() []model.FieldSpec {
	return []model.FieldSpec{
		{Name: colTLSessionRef, Kind: model.KindText, Indexed: true},
		{Name: colTLAt, Kind: model.KindTimestamp},
		{Name: colTLKind, Kind: model.KindText},
		{Name: colTLToolRef, Kind: model.KindText, Nullable: true},
		{Name: colTLResource, Kind: model.KindText, Nullable: true},
		{Name: colTLMode, Kind: model.KindText, Nullable: true},
		{Name: colTLSource, Kind: model.KindText, Nullable: true},
		{Name: colTLTitle, Kind: model.KindText, Nullable: true},
	}
}

// legacy99aSchema is the 99a binary's registration of this module.
func legacy99aSchema(m *Module) func(store.ExtensionRegistry) error {
	return func(reg store.ExtensionRegistry) error { return m.RegisterSchema(legacy99aRegistry{reg}) }
}

func liveIndexNames(t *testing.T, be profileBackend) map[string]bool {
	t.Helper()
	db := be.rawOpen(t)
	defer db.Close() //nolint:errcheck
	q := "SELECT name FROM sqlite_schema WHERE type='index' AND tbl_name='sessions_live'"
	if be.engine == store.EnginePostgres {
		q = "SELECT indexname FROM pg_indexes WHERE tablename='sessions_live'"
	}
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

// Row 7: a 99a-shaped database with legacy live/timeline rows, upgraded by a
// boot of THIS module, converges with a fresh boot: new nullable columns, the
// old index retired, the COALESCE and partial indexes present, legacy rows intact
// and STILL unique at scope NULL, a scoped row admitted beside them, and a second
// boot changing nothing. Postgres included — SQLite alone would not prove the
// expression index or the drop.
func TestProviderProfile_UpgradeFrom99aSchema(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			// 1. The OLD schema, with legacy rows in two tenants.
			oldModule := New()
			old, err := engine.Open(ctx, store.Config{Engine: be.engine, DSN: be.dsn, Debug: true}, legacy99aSchema(oldModule))
			if err != nil {
				t.Fatalf("open legacy: %v", err)
			}
			tenantA := ensureTenant(t, old, "up-a-"+be.name)
			tenantB := ensureTenant(t, old, "up-b-"+be.name)
			oldData := api.NewModuleData(old)
			seedLegacy := func(tenant model.TenantID, ref string) {
				if err := oldData.Mutate(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(liveKind)
					if err != nil {
						return err
					}
					at := model.NewTimestamp(baseTime).String()
					_, err = repo.Create(ctx, model.Record{
						colSessionRef: ref, colInputTokens: int64(1), colOutputTokens: int64(2), colCostMicroUSD: int64(3),
						colEventCount: int64(4), colToolCalls: int64(1), colFirstEventAt: at, colLastEventAt: at,
					})
					if err != nil {
						return err
					}
					tl, err := sc.Ext(timelineKind)
					if err != nil {
						return err
					}
					_, err = tl.Create(ctx, model.Record{colTLSessionRef: ref, colTLAt: at, colTLKind: tlTool, colTLToolRef: "Read"})
					return err
				}); err != nil {
					t.Fatalf("seed legacy %s: %v", ref, err)
				}
			}
			seedLegacy(tenantA, "legacy-1")
			seedLegacy(tenantA, "legacy-2")
			seedLegacy(tenantB, "legacy-1") // same external id, other tenant: legal before and after
			if idx := liveIndexNames(t, be); !idx["sessions_live_ref_uniq"] || idx["sessions_live_scope_ref_uniq"] {
				t.Fatalf("legacy indexes = %v", idx)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}

			// 2. Boot THIS module on it.
			m, st := openProfileModule(t, be, nil)
			idx := liveIndexNames(t, be)
			if idx["sessions_live_ref_uniq"] || !idx["sessions_live_scope_ref_uniq"] || !idx["sessions_live_canonical_sid_uniq"] {
				t.Fatalf("upgraded indexes = %v", idx)
			}
			// Legacy rows are intact, read as legacy (scope NULL), and none received an
			// identity nobody recorded.
			if err := m.data.View(ctx, tenantA, func(sc store.Scope) error {
				repo, err := sc.Ext(liveKind)
				if err != nil {
					return err
				}
				recs, _, err := repo.List(ctx, model.Query{Limit: 10})
				if err != nil {
					return err
				}
				if len(recs) != 2 {
					return errors.New("tenant A lost legacy rows")
				}
				for _, rec := range recs {
					if !rec.IsNull(colObservationScope) || !rec.IsNull(colLiveCanonicalSID) || !rec.IsNull(colLiveProfileID) || rec.Int(colInputTokens) != 1 {
						return errors.New("a legacy row was assigned identity or lost counters")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Uniqueness at scope NULL still holds (the COALESCE, not a NULL-tolerant index).
			dup := m.data.Mutate(ctx, tenantA, func(sc store.Scope) error {
				repo, err := sc.Ext(liveKind)
				if err != nil {
					return err
				}
				at := model.NewTimestamp(baseTime).String()
				_, err = repo.Create(ctx, model.Record{
					colSessionRef: "legacy-1", colInputTokens: int64(0), colOutputTokens: int64(0), colCostMicroUSD: int64(0),
					colEventCount: int64(0), colToolCalls: int64(0), colFirstEventAt: at, colLastEventAt: at,
				})
				return err
			})
			if !errors.Is(dup, store.ErrConflict) {
				t.Fatalf("duplicate legacy row = %v, want store.ErrConflict", dup)
			}
			// A SCOPED row with the same external id is admitted beside the legacy one,
			// and a second scoped row in the same scope is refused; canonical_sid is
			// unique among non-NULL values.
			scoped := func(scope, sid string) error {
				return m.data.Mutate(ctx, tenantA, func(sc store.Scope) error {
					repo, err := sc.Ext(liveKind)
					if err != nil {
						return err
					}
					at := model.NewTimestamp(baseTime).String()
					rec := model.Record{
						colSessionRef: "legacy-1", colInputTokens: int64(0), colOutputTokens: int64(0), colCostMicroUSD: int64(0),
						colEventCount: int64(0), colToolCalls: int64(0), colFirstEventAt: at, colLastEventAt: at,
						colObservationScope: scope,
					}
					if sid != "" {
						rec[colLiveCanonicalSID] = sid
					}
					_, err = repo.Create(ctx, rec)
					return err
				})
			}
			if err := scoped("observed:ppf_x", ""); err != nil {
				t.Fatalf("scoped row beside legacy: %v", err)
			}
			if err := scoped("observed:ppf_x", ""); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("duplicate scoped row = %v, want conflict", err)
			}
			if err := scoped("managed:osn_1", "osn_1"); err != nil {
				t.Fatalf("managed row: %v", err)
			}
			if err := scoped("managed:osn_1b", "osn_1"); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("duplicate canonical_sid = %v, want conflict", err)
			}
			// Nothing was mixed into the legacy timeline: still one legacy event per ref.
			if got := countRows(t, m, tenantA, timelineKind, eq(colTLSessionRef, "legacy-1")); got != 1 {
				t.Fatalf("legacy timeline rows = %d, want 1", got)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			// 3. A SECOND boot converges on the same schema and touches nothing.
			m3, _ := openProfileModule(t, be, nil)
			if idx2 := liveIndexNames(t, be); idx2["sessions_live_ref_uniq"] || !idx2["sessions_live_scope_ref_uniq"] || !idx2["sessions_live_canonical_sid_uniq"] {
				t.Fatalf("second boot indexes = %v", idx2)
			}
			if got := countRows(t, m3, tenantA, liveKind); got != 4 {
				t.Fatalf("live rows after second boot = %d, want 4", got)
			}
			if got := countRows(t, m3, tenantB, liveKind); got != 1 {
				t.Fatalf("tenant B rows after second boot = %d, want 1", got)
			}
		})
	}

	// And a FRESH database converges on the same index set.
	t.Run("fresh", func(t *testing.T) {
		be := profileBackends(t)[0]
		_, st := openProfileModule(t, be, nil)
		if idx := liveIndexNames(t, be); idx["sessions_live_ref_uniq"] || !idx["sessions_live_scope_ref_uniq"] || !idx["sessions_live_canonical_sid_uniq"] {
			t.Fatalf("fresh indexes = %v", idx)
		}
		_ = st.Close()
	})
}
