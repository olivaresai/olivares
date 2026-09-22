// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Provider-account acceptance, on BOTH engines (profileBackends adds the
// PostgreSQL leg whenever OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is set, and logs
// it as not exercised otherwise).
//
// Every test reaches the account plane through the module's OWN routes, mounted
// exactly as the module registers them, and names the account columns by their
// literal spelling. That keeps the package compiling against a tree without the
// account plane, so each test is red there by assertion rather than by a
// missing symbol.

// acctColumns are the eight nullable account columns of sessions_provider_profile.
var acctColumns = []string{
	"account_name", "home_mode", "home_generation", "owner_ref",
	"release_ref", "pending_release", "isolation_level", "os_user",
}

const acctNameIndex = "sessions_provider_account_name_uniq"

// acctRoutes mounts the module's routes on a bare router with one fixed caller
// and tenant, so a test drives the same handlers the engine mounts.
type acctRoutes struct {
	router chi.Router
	mc     api.ModuleContext
}

func (r acctRoutes) Handle(method, pattern string, _ auth.Permission, h api.ModuleHandler) {
	mc := r.mc
	r.router.MethodFunc(method, pattern, func(w http.ResponseWriter, req *http.Request) { h(w, req, mc) })
}

func (r acctRoutes) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, perm, h)
}

type acctAPI struct {
	router chi.Router
}

type acctResp struct {
	code int
	body map[string]any
	raw  string
}

func newAcctAPI(m *Module, st store.Store, tenant model.TenantID) *acctAPI {
	router := chi.NewRouter()
	m.APIRoutes(acctRoutes{router: router, mc: api.ModuleContext{
		Principal: testActor(), Tenant: tenant, Data: api.NewScopedData(st, tenant),
	}})
	return &acctAPI{router: router}
}

// call is safe from several goroutines: it reports and never stops the test.
func (a *acctAPI) call(method, target string, body any) acctResp {
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return acctResp{code: -1, raw: err.Error()}
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	out := acctResp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (a *acctAPI) adopt(ref, name string) acctResp {
	body := map[string]any{}
	if name != "" {
		body["name"] = name
	}
	return a.call(http.MethodPost, "/provider-accounts/"+ref+"/adopt", body)
}

func (a *acctAPI) list(query string) acctResp {
	target := "/provider-accounts"
	if query != "" {
		target += "?" + query
	}
	return a.call(http.MethodGet, target, nil)
}

func (r acctResp) items() []map[string]any {
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func acctNames(items []map[string]any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// acctProfiles registers n profiles of one driver, each on its own real homes.
func acctProfiles(t *testing.T, m *Module, tenant model.TenantID, driver string, n int) []ProviderProfile {
	t.Helper()
	root := t.TempDir()
	mk := func(p string) string {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return real
	}
	out := make([]ProviderProfile, 0, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("%s-%02d", driver, i))
		out = append(out, mustCreateProfile(t, m, tenant, CreateProfileInput{
			Driver: driver, ConfigHome: mk(filepath.Join(dir, "config")), UserHome: mk(filepath.Join(dir, "home")),
		}))
	}
	return out
}

// acctDigest is the K4 dispatch digest of a profiled launch of ref: the same
// canonical encoding and hash runtime_k4_digest_test.go pins as literal bytes.
func acctDigest(t *testing.T, m *Module, tenant model.TenantID, ref string) string {
	t.Helper()
	snap, _, err := m.resolveLaunchProfile(context.Background(), tenant, ref)
	if err != nil {
		t.Fatalf("resolve launch profile %s: %v", ref, err)
	}
	params := k4Params()
	params.ProviderProfileRef = ref
	params.ProviderHome = &snap
	raw, err := canonicalJSON(params)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x %s", sum, raw)
}

// acctRow reads one profile row as stored, account columns included.
func acctRow(t *testing.T, m *Module, tenant model.TenantID, ref string) model.Record {
	t.Helper()
	var out model.Record
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		rec, err := findProfileRec(context.Background(), sc, ref)
		out = rec
		return err
	}); err != nil {
		t.Fatalf("read profile %s: %v", ref, err)
	}
	return out
}

func acctCatalog(t *testing.T, be profileBackend, sqliteQuery, postgresQuery string) map[string]bool {
	t.Helper()
	db := be.rawOpen(t)
	defer db.Close() //nolint:errcheck
	q := sqliteQuery
	if be.engine == store.EnginePostgres {
		q = postgresQuery
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func acctProfileColumns(t *testing.T, be profileBackend) map[string]bool {
	t.Helper()
	return acctCatalog(t, be,
		"SELECT name FROM pragma_table_info('sessions_provider_profile')",
		"SELECT column_name FROM information_schema.columns WHERE table_name='sessions_provider_profile'")
}

func acctProfileIndexes(t *testing.T, be profileBackend) map[string]bool {
	t.Helper()
	return acctCatalog(t, be,
		"SELECT name FROM sqlite_schema WHERE type='index' AND tbl_name='sessions_provider_profile'",
		"SELECT indexname FROM pg_indexes WHERE tablename='sessions_provider_profile'")
}

// The snapshot's JSON names ARE the K4 launch digest, so no account field may
// reach it, and adopting a profile must leave the digest of its next launch
// byte-identical.
func TestProviderAccount_AdoptLeavesTheK4DigestUnchanged(t *testing.T) {
	wantTags := []string{
		"profile_id", "driver", "environment_ref", "config_home", "user_home",
		"auth_source,omitempty", "provider_record_ref,omitempty",
	}
	var gotTags []string
	snapType := reflect.TypeOf(ProviderHomeSnapshot{})
	for i := 0; i < snapType.NumField(); i++ {
		gotTags = append(gotTags, snapType.Field(i).Tag.Get("json"))
	}
	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Fatalf("ProviderHomeSnapshot JSON fields = %v, want exactly %v", gotTags, wantTags)
	}

	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "digest-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2)

			for i, name := range []string{"claude-1", ""} {
				ref := profiles[i].Ref
				before := acctDigest(t, m, tenant, ref)
				r := a.adopt(ref, name)
				if r.code != http.StatusOK {
					t.Fatalf("adopt %s = %d %s, want 200", ref, r.code, r.raw)
				}
				if got, _ := r.body["name"].(string); got == "" || (name != "" && got != name) {
					t.Fatalf("adopt %s answered name %q, want %q", ref, got, name)
				}
				if after := acctDigest(t, m, tenant, ref); after != before {
					t.Fatalf("adopting %s moved its K4 digest:\nbefore %s\nafter  %s", ref, before, after)
				}
			}
		})
	}
}

// A list of accounts is a list of NAMED rows: a legacy profile is a profile and
// not an account, and it is never listed as one. A human's list and get are
// decided by the engine's own route door, not refused by it.
func TestProviderAccount_ListsOnlyNamedRows(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "list-"+be.name)
			a := newAcctAPI(m, st, tenant)
			claude := acctProfiles(t, m, tenant, "claude", 3)
			codex := acctProfiles(t, m, tenant, "codex", 1)

			if r := a.adopt(claude[0].Ref, "claude-main"); r.code != http.StatusOK {
				t.Fatalf("adopt claude = %d %s", r.code, r.raw)
			}
			if r := a.adopt(codex[0].Ref, ""); r.code != http.StatusOK || r.body["name"] != "codex" {
				t.Fatalf("adopt codex = %d %s, want 200 named codex", r.code, r.raw)
			}

			r := a.list("")
			if r.code != http.StatusOK {
				t.Fatalf("list = %d %s", r.code, r.raw)
			}
			if got := acctNames(r.items()); !reflect.DeepEqual(got, []string{"claude-main", "codex"}) {
				t.Fatalf("listed accounts = %v, want only the two named rows: %s", got, r.raw)
			}
			for _, item := range r.items() {
				if item["account_ref"] == claude[1].Ref || item["account_ref"] == claude[2].Ref {
					t.Fatalf("an unnamed profile was listed as an account: %s", r.raw)
				}
			}
			if got := acctNames(a.list("driver=codex").items()); !reflect.DeepEqual(got, []string{"codex"}) {
				t.Fatalf("driver filter = %v, want [codex]", got)
			}
			if got := a.list("environment=another-node").items(); len(got) != 0 {
				t.Fatalf("environment filter = %v, want none", got)
			}
			if r := a.call(http.MethodGet, "/provider-accounts/"+claude[1].Ref, nil); r.code != http.StatusNotFound {
				t.Fatalf("get of an unnamed profile = %d %s, want 404", r.code, r.raw)
			}
			if r := a.call(http.MethodGet, "/provider-accounts/"+claude[0].Ref, nil); r.code != http.StatusOK || r.body["account_ref"] != claude[0].Ref {
				t.Fatalf("get of an account = %d %s", r.code, r.raw)
			}
		})
	}

	// A HUMAN through the engine's real door: a viewer's list and get are
	// answered, and the viewer's adopt is refused by tier, not by the reader.
	t.Run("human", func(t *testing.T) {
		m := New()
		m.UseExecutionEnvironmentRef(testEnvRef)
		h := newHarness(t, m)
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "acme")
		viewer := h.viewerToken(admin, tenant, "v@acme.com")
		profiles := acctProfiles(t, m, tenant, "claude", 2)

		base := "/v1/m/sessions/provider-accounts"
		if r := h.doJSON("POST", base+"/"+profiles[0].Ref+"/adopt", viewer, map[string]any{}, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Fatalf("viewer adopt = %d %s, want 403", r.code, r.raw)
		}
		if r := h.doJSON("POST", base+"/"+profiles[0].Ref+"/adopt", admin, map[string]any{"name": "claude-1"}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("admin adopt = %d %s, want 200", r.code, r.raw)
		}
		r := h.do("GET", base, viewer, tenantHdr(tenant))
		items, _ := r.body["items"].([]any)
		if r.code != http.StatusOK || len(items) != 1 {
			t.Fatalf("viewer list = %d %s, want 200 with one account", r.code, r.raw)
		}
		if r := h.do("GET", base+"/"+profiles[0].Ref, viewer, tenantHdr(tenant)); r.code != http.StatusOK || r.body["name"] != "claude-1" {
			t.Fatalf("viewer get = %d %s", r.code, r.raw)
		}
		if r := h.do("GET", base+"/"+profiles[1].Ref, viewer, tenantHdr(tenant)); r.code != http.StatusNotFound {
			t.Fatalf("viewer get of an unnamed profile = %d %s, want 404", r.code, r.raw)
		}
	})
}

// Two operators race the SAME explicit name onto two profiles. The database
// decides: one is 200, the other is 409 naming that name, and the loser is never
// handed another name.
func TestProviderAccount_OperatorNameLosingTheRaceIs409NotRenamed(t *testing.T) {
	const rounds = 8
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "race-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2*rounds)

			var want []string
			for i := 0; i < rounds; i++ {
				name := fmt.Sprintf("claude-%d", i+1)
				want = append(want, name)
				pair := []ProviderProfile{profiles[2*i], profiles[2*i+1]}
				results := make([]acctResp, len(pair))
				start := make(chan struct{})
				var wg sync.WaitGroup
				for j := range pair {
					wg.Add(1)
					go func(j int) {
						defer wg.Done()
						<-start
						results[j] = a.adopt(pair[j].Ref, name)
					}(j)
				}
				close(start)
				wg.Wait()

				var won, lost int
				for j, r := range results {
					switch r.code {
					case http.StatusOK:
						won++
						if r.body["name"] != name {
							t.Fatalf("round %d: the winner answered name %v, want %q", i, r.body["name"], name)
						}
					case http.StatusConflict:
						lost++
						if !strings.Contains(r.raw, name) {
							t.Fatalf("round %d: the 409 does not name %q: %s", i, name, r.raw)
						}
						if got := acctRow(t, m, tenant, pair[j].Ref); !got.IsNull("account_name") {
							t.Fatalf("round %d: the loser was named %q; a lost name is never replaced", i, got.String("account_name"))
						}
					default:
						t.Fatalf("round %d: adopt = %d %s, want 200 or 409", i, r.code, r.raw)
					}
				}
				if won != 1 || lost != 1 {
					t.Fatalf("round %d: won=%d lost=%d, want exactly one of each", i, won, lost)
				}
			}
			sort.Strings(want)
			if got := acctNames(a.list("limit=100").items()); !reflect.DeepEqual(got, want) {
				t.Fatalf("accounts after the races = %v, want exactly %v (no renamed loser)", got, want)
			}
		})
	}
}

// acctStaleNames hides named rows from what an adopt transaction reads. That is
// exactly how a concurrent writer that commits after the read looks to the
// writer, so the unique index refuses every name the generator proposes.
type acctStaleNames struct {
	inner   api.ModuleData
	mu      sync.Mutex
	hidden  map[string]bool
	mutates int
}

func (d *acctStaleNames) hide(names ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hidden = map[string]bool{}
	for _, n := range names {
		d.hidden[n] = true
	}
	d.mutates = 0
}

func (d *acctStaleNames) isHidden(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hidden[name]
}

func (d *acctStaleNames) writes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mutates
}

func (d *acctStaleNames) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, func(sc store.Scope) error { return fn(acctStaleScope{Scope: sc, owner: d}) })
}

func (d *acctStaleNames) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mu.Lock()
	d.mutates++
	d.mu.Unlock()
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(acctStaleScope{Scope: sc, owner: d}) })
}

type acctStaleScope struct {
	store.Scope
	owner *acctStaleNames
}

func (s acctStaleScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != providerProfileKind {
		return repo, err
	}
	return acctStaleRepo{GenericRepo: repo, owner: s.owner}, nil
}

type acctStaleRepo struct {
	store.GenericRepo
	owner *acctStaleNames
}

func (r acctStaleRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	recs, page, err := r.GenericRepo.List(ctx, q)
	if err != nil {
		return recs, page, err
	}
	out := make([]model.Record, 0, len(recs))
	for _, rec := range recs {
		if !r.owner.isHidden(rec.String("account_name")) {
			out = append(out, rec)
		}
	}
	return out, page, nil
}

// acctGenerated is the generator's sequence for driver: the bare name, then -b,
// -c, … (enough for these tests, which stay below -z).
func acctGenerated(driver string, n int) []string {
	out := []string{driver}
	for i := 1; i < n; i++ {
		out = append(out, driver+"-"+string(rune('a'+i)))
	}
	return out
}

// A GENERATED name that loses the race retries with the next suffix, at most
// sixteen times, then answers 409 naming the exhaustion — and a profile whose
// every attempt lost stays unnamed.
func TestProviderAccount_GeneratedNameLosingTheRaceRetriesThenExhausts(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "gen-"+be.name)
			a := newAcctAPI(m, st, tenant)

			// Real concurrency: six generated adopts converge on six distinct names.
			racers := acctProfiles(t, m, tenant, "codex", 6)
			results := make([]acctResp, len(racers))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range racers {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					results[i] = a.adopt(racers[i].Ref, "")
				}(i)
			}
			close(start)
			wg.Wait()
			for i, r := range results {
				if r.code != http.StatusOK {
					t.Fatalf("concurrent generated adopt %d = %d %s, want 200", i, r.code, r.raw)
				}
			}
			want := acctGenerated("codex", 6)
			sort.Strings(want)
			if got := acctNames(a.list("driver=codex").items()); !reflect.DeepEqual(got, want) {
				t.Fatalf("generated names = %v, want %v", got, want)
			}

			// Deterministic losses: twenty names are taken but invisible to the
			// adopt's read, so every proposal loses at the index.
			named := acctProfiles(t, m, tenant, "claude", 20)
			taken := acctGenerated("claude", 20)
			for i, p := range named {
				if r := a.adopt(p.Ref, taken[i]); r.code != http.StatusOK {
					t.Fatalf("seed %s = %d %s", taken[i], r.code, r.raw)
				}
			}
			target := acctProfiles(t, m, tenant, "claude", 1)[0]
			stale := &acctStaleNames{inner: m.data}
			m.data = stale

			stale.hide(taken...)
			r := a.adopt(target.Ref, "")
			if r.code != http.StatusConflict || !strings.Contains(r.raw, "exhausted") || !strings.Contains(r.raw, "16") {
				t.Fatalf("adopt with every proposal lost = %d %s, want 409 naming the exhaustion after 16 attempts", r.code, r.raw)
			}
			if got := stale.writes(); got != 16 {
				t.Fatalf("write attempts = %d, want exactly 16", got)
			}
			if got := acctRow(t, m, tenant, target.Ref); !got.IsNull("account_name") {
				t.Fatalf("an exhausted adopt left the profile named %q", got.String("account_name"))
			}

			// The bound is sixteen, not fifteen: with fifteen invisible names the
			// sixteenth attempt proposes the first free name and wins.
			stale.hide(taken[:15]...)
			r = a.adopt(target.Ref, "")
			if r.code != http.StatusOK || r.body["name"] != "claude-u" {
				t.Fatalf("adopt with fifteen proposals lost = %d %s, want 200 named claude-u", r.code, r.raw)
			}
			if got := stale.writes(); got != 16 {
				t.Fatalf("write attempts = %d, want 16 (fifteen lost, one won)", got)
			}
		})
	}
}

// Archiving never frees a name: a retired account keeps it, the generator skips
// it, an explicit request for it is 409, and the check covers every driver.
func TestProviderAccount_ArchivedNameIsStillTaken(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "archived-"+be.name)
			a := newAcctAPI(m, st, tenant)
			claude := acctProfiles(t, m, tenant, "claude", 3)
			codex := acctProfiles(t, m, tenant, "codex", 1)

			if r := a.adopt(claude[0].Ref, ""); r.code != http.StatusOK || r.body["name"] != "claude" {
				t.Fatalf("first adopt = %d %s, want 200 named claude", r.code, r.raw)
			}
			retired := ProfileRetired
			if _, err := m.PatchProfile(ctx, tenant, claude[0].Ref, ProfilePatch{State: &retired}); err != nil {
				t.Fatalf("retire: %v", err)
			}
			if r := a.call(http.MethodGet, "/provider-accounts/"+claude[0].Ref, nil); r.code != http.StatusOK ||
				r.body["name"] != "claude" || r.body["state"] != ProfileRetired {
				t.Fatalf("the archived account = %d %s, want it still named claude", r.code, r.raw)
			}

			if r := a.adopt(claude[1].Ref, "claude"); r.code != http.StatusConflict || !strings.Contains(r.raw, "claude") {
				t.Fatalf("explicit archived name = %d %s, want 409 naming it", r.code, r.raw)
			}
			if r := a.adopt(claude[1].Ref, ""); r.code != http.StatusOK || r.body["name"] != "claude-b" {
				t.Fatalf("generated beside an archived name = %d %s, want 200 named claude-b", r.code, r.raw)
			}
			// Across drivers: a Codex profile cannot take a Claude account's name.
			if r := a.adopt(codex[0].Ref, "claude-b"); r.code != http.StatusConflict || !strings.Contains(r.raw, "claude-b") {
				t.Fatalf("cross-driver name = %d %s, want 409 naming it", r.code, r.raw)
			}
			// A retired profile is not adopted at all.
			if _, err := m.PatchProfile(ctx, tenant, claude[2].Ref, ProfilePatch{State: &retired}); err != nil {
				t.Fatalf("retire: %v", err)
			}
			if r := a.adopt(claude[2].Ref, "claude-c"); r.code != http.StatusConflict {
				t.Fatalf("adopt of a retired profile = %d %s, want 409", r.code, r.raw)
			}
			if got := acctNames(a.list("").items()); !reflect.DeepEqual(got, []string{"claude", "claude-b"}) {
				t.Fatalf("accounts = %v, want [claude claude-b]", got)
			}
		})
	}
}

// acctProfileOnlyRegistry is the registration of a binary that predates the
// account plane: the profile descriptor without the eight account columns and
// none of the account migrations. Opening a database through it produces the
// schema an existing deployment upgrades FROM.
type acctProfileOnlyRegistry struct{ store.ExtensionRegistry }

func (r acctProfileOnlyRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == providerProfileKind {
		account := map[string]bool{}
		for _, c := range acctColumns {
			account[c] = true
		}
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if !account[f.Name] {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func (r acctProfileOnlyRegistry) Migrations(namespace string, fsys fs.FS) error {
	filtered := fstest.MapFS{}
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		version, verr := strconv.Atoi(strings.SplitN(path.Base(p), "_", 2)[0])
		if verr != nil {
			return verr
		}
		if (strings.HasPrefix(p, "sqlite/") && version >= 98) || (strings.HasPrefix(p, "postgres/") && version >= 25) {
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

// A database written by a profile-only binary gains the columns and the index at
// the next boot. Its rows stay unnamed profiles, key on their own profile_ref,
// keep their home slot and keep their exact K4 digest — and a second boot
// changes nothing.
func TestProviderAccount_UpgradeFromProfileOnlySchema(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			old, oldStore := openProfileModule(t, be, func(reg store.ExtensionRegistry) error {
				return New().RegisterSchema(acctProfileOnlyRegistry{reg})
			})
			tenant := ensureTenant(t, oldStore, "upgrade-"+be.name)
			legacy := acctProfiles(t, old, tenant, "claude", 2)
			digests := map[string]string{}
			slots := map[string]string{}
			for _, p := range legacy {
				digests[p.Ref] = acctDigest(t, old, tenant, p.Ref)
				slots[p.Ref] = acctRow(t, old, tenant, p.Ref).String(colPPHomeSlot)
			}
			if cols := acctProfileColumns(t, be); cols["account_name"] {
				t.Fatalf("the profile-only schema already has account columns: %v", cols)
			}
			if err := oldStore.Close(); err != nil {
				t.Fatal(err)
			}

			for boot := 1; boot <= 2; boot++ {
				m, st := openProfileModule(t, be, nil)
				cols := acctProfileColumns(t, be)
				for _, c := range acctColumns {
					if !cols[c] {
						t.Fatalf("boot %d: column %s is missing after the upgrade: %v", boot, c, cols)
					}
				}
				if idx := acctProfileIndexes(t, be); !idx[acctNameIndex] || !idx["sessions_provider_profile_home_uniq"] {
					t.Fatalf("boot %d: indexes = %v, want %s beside the home slot index", boot, idx, acctNameIndex)
				}
				for _, p := range legacy {
					if got := acctDigest(t, m, tenant, p.Ref); got != digests[p.Ref] {
						t.Fatalf("boot %d: %s changed its K4 digest across the upgrade:\nbefore %s\nafter  %s", boot, p.Ref, digests[p.Ref], got)
					}
					if got := acctRow(t, m, tenant, p.Ref).String(colPPHomeSlot); got != slots[p.Ref] {
						t.Fatalf("boot %d: %s home slot %q, want %q", boot, p.Ref, got, slots[p.Ref])
					}
				}
				if boot == 2 {
					// The adopt of the first boot survived the restart, still under
					// its name and still with the digest it had as a legacy profile.
					if got := acctRow(t, m, tenant, legacy[0].Ref).String("account_name"); got != "claude" {
						t.Fatalf("after the second boot the adopted profile is named %q, want claude", got)
					}
					_ = st.Close()
					continue
				}

				a := newAcctAPI(m, st, tenant)
				for _, p := range legacy {
					row := acctRow(t, m, tenant, p.Ref)
					for _, c := range acctColumns {
						if !row.IsNull(c) {
							t.Fatalf("legacy row %s gained a value in %s: %v", p.Ref, c, row[c])
						}
					}
				}
				if got := a.list("").items(); len(got) != 0 {
					t.Fatalf("legacy profiles were listed as accounts: %v", got)
				}
				// The home slot still decides: the same home is not registered twice.
				if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{
					Driver: "claude", ConfigHome: legacy[0].ConfigHome, UserHome: legacy[0].UserHome,
				}); !errors.Is(err, ErrProfileHomeTaken) {
					t.Fatalf("second profile on a legacy home = %v, want ErrProfileHomeTaken", err)
				}
				// A legacy row keys on its own profile_ref: no other row may claim that
				// key, which is what keeps every existing row unique at upgrade time.
				err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(providerProfileKind)
					if err != nil {
						return err
					}
					rec, err := findProfileRec(ctx, sc, legacy[1].Ref)
					if err != nil {
						return err
					}
					rec["account_name"] = legacy[0].Ref
					_, err = repo.Update(ctx, rec)
					return err
				})
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("a row named after a legacy row's profile_ref = %v, want store.ErrConflict", err)
				}
				if r := a.adopt(legacy[0].Ref, ""); r.code != http.StatusOK || r.body["name"] != "claude" {
					t.Fatalf("adopt of an upgraded legacy profile = %d %s, want 200 named claude", r.code, r.raw)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// An adopted home is the operator's existing directory under the engine's own
// service user, so its isolation is SHARED. Adopt never labels it dedicated,
// never accepts a request to, and never returns the absolute path.
func TestProviderAccount_AdoptedHomeIsSharedIsolationNeverDedicated(t *testing.T) {
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "isolation-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2)

			r := a.call(http.MethodPost, "/provider-accounts/"+profiles[1].Ref+"/adopt",
				map[string]any{"name": "claude-x", "isolation": "dedicated"})
			if r.code != http.StatusBadRequest {
				t.Fatalf("adopt asking for dedicated isolation = %d %s, want 400", r.code, r.raw)
			}
			if got := acctRow(t, m, tenant, profiles[1].Ref); !got.IsNull("account_name") || !got.IsNull("isolation_level") {
				t.Fatalf("a refused adopt wrote the row: %v", got)
			}

			r = a.adopt(profiles[0].Ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", r.code, r.raw)
			}
			if r.body["isolation_level"] != "shared" || r.body["home_mode"] != "adopted" {
				t.Fatalf("adopt answered isolation %v and home mode %v, want shared and adopted: %s",
					r.body["isolation_level"], r.body["home_mode"], r.raw)
			}
			row := acctRow(t, m, tenant, profiles[0].Ref)
			if row.String("isolation_level") != "shared" || row.String("home_mode") != "adopted" || !row.IsNull("os_user") {
				t.Fatalf("stored isolation %q, home mode %q, os user %v; want shared, adopted, none",
					row.String("isolation_level"), row.String("home_mode"), row["os_user"])
			}
			got := a.call(http.MethodGet, "/provider-accounts/"+profiles[0].Ref, nil)
			if got.code != http.StatusOK || got.body["isolation_level"] != "shared" {
				t.Fatalf("get = %d %s, want isolation shared", got.code, got.raw)
			}
			for _, raw := range []string{r.raw, got.raw, a.list("").raw} {
				if strings.Contains(raw, profiles[0].ConfigHome) || strings.Contains(raw, profiles[0].UserHome) {
					t.Fatalf("an account read carries the absolute home path: %s", raw)
				}
			}
		})
	}
}

// Adopt names a profile ONCE. A second adopt of an account is refused with 409
// "already an account", with or without a name, and nothing a reader sees moves:
// not the name, not a second generated name, not a byte of the account.
func TestProviderAccount_AdoptOfANamedProfileIs409AlreadyAnAccount(t *testing.T) {
	rows := []struct {
		label string
		name  string // what the second adopt asks for; "" asks for a generated name
	}{
		{label: "without a name", name: ""},
		{label: "with a free name", name: "x"},
		{label: "with its own name", name: "claude-b"},
	}
	for _, be := range profileBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "named-"+be.name)
			a := newAcctAPI(m, st, tenant)
			p := acctProfiles(t, m, tenant, "claude", 1)[0]
			if r := a.adopt(p.Ref, "claude-b"); r.code != http.StatusOK || r.body["name"] != "claude-b" {
				t.Fatalf("first adopt = %d %s, want 200 named claude-b", r.code, r.raw)
			}
			before := a.call(http.MethodGet, "/provider-accounts/"+p.Ref, nil)
			if before.code != http.StatusOK {
				t.Fatalf("get after the first adopt = %d %s", before.code, before.raw)
			}
			for _, row := range rows {
				t.Run(row.label, func(t *testing.T) {
					r := a.adopt(p.Ref, row.name)
					if r.code != http.StatusConflict || !strings.Contains(r.raw, "already an account") {
						t.Fatalf("second adopt = %d %s, want 409 already an account", r.code, r.raw)
					}
					if got := acctRow(t, m, tenant, p.Ref).String("account_name"); got != "claude-b" {
						t.Fatalf("the stored name is %q after a refused adopt, want claude-b", got)
					}
					if after := a.call(http.MethodGet, "/provider-accounts/"+p.Ref, nil); after.raw != before.raw {
						t.Fatalf("a refused adopt changed the account:\nbefore %s\nafter  %s", before.raw, after.raw)
					}
					if got := acctNames(a.list("").items()); !reflect.DeepEqual(got, []string{"claude-b"}) {
						t.Fatalf("accounts after a refused adopt = %v, want [claude-b]", got)
					}
				})
			}
		})
	}
}

// acctConfinedBackend is one engine for the confined-reader case, built the way
// the module's confinement fixture is (stream_confinement_test.go).
type acctConfinedBackend struct {
	name   string
	config func(*testing.T) store.Config
}

func acctConfinedBackends(t *testing.T) []acctConfinedBackend {
	t.Helper()
	out := []acctConfinedBackend{{"sqlite", func(*testing.T) store.Config {
		return store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}
	}}}
	if enginetest.PostgresAvailable(t) {
		out = append(out, acctConfinedBackend{"postgres", func(t *testing.T) store.Config {
			pg := enginetest.IsolatedPostgres(t)
			return store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin}
		}})
	} else {
		t.Logf("%s unset: Postgres NOT exercised (SQLite-only this run)", enginetest.EnvSuperuserDSN)
	}
	return out
}

// An account belongs to its tenant and an execution environment, never to a
// workspace: the profile row declares no workspace lineage. A member confined to
// one workspace therefore reads NO account — the list and a get answer 403
// "workspace confined" before any row is read, the same for a reference that
// exists and one that does not — and cannot adopt one either, while a
// tenant-wide reader of the same tenant reads the account. Real memberships,
// real route door, real scoped grants.
func TestProviderAccount_WorkspaceConfinedReaderIsRefusedNeverShown(t *testing.T) {
	for _, be := range acctConfinedBackends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			f := newStreamConfinementFixture(t, be.config(t))
			f.m.UseExecutionEnvironmentRef(testEnvRef)
			admin := f.adminLogin()
			tenant := f.createOrg(admin, "accounts-confined")
			var workspace model.ID
			if err := f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
				ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Confined", Slug: "confined", Status: model.StatusActive})
				workspace = ws.ID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			confined := f.member(t, admin, tenant, "confined@accounts.test", workspace)
			principal, err := f.authr.Authenticate(ctx, confined)
			if err != nil {
				t.Fatal(err)
			}
			if ws, ok := principal.ConfinedWorkspaceIn(tenant); !ok || ws != workspace || principal.Superadmin {
				t.Fatalf("the member is not workspace-confined: workspace=%s confined=%t superadmin=%t", ws, ok, principal.Superadmin)
			}
			wide := f.viewerToken(admin, tenant, "wide@accounts.test")
			profiles := acctProfiles(t, f.m, tenant, "claude", 2)

			base := "/v1/m/sessions/provider-accounts"
			if r := f.doJSON("POST", base+"/"+profiles[0].Ref+"/adopt", admin, map[string]any{"name": "claude-1"}, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("admin adopt = %d %s, want 200", r.code, r.raw)
			}
			// The positive control: a tenant-wide reader reads the account, so the
			// refusals below are the confinement and not an absence.
			if r := f.do("GET", base+"/"+profiles[0].Ref, wide, tenantHdr(tenant)); r.code != http.StatusOK || r.body["name"] != "claude-1" {
				t.Fatalf("tenant-wide get = %d %s, want 200 named claude-1", r.code, r.raw)
			}

			reads := []struct{ label, path string }{
				{"get of an account", base + "/" + profiles[0].Ref},
				{"get of an unknown reference", base + "/" + newProfileRef()},
				{"list", base},
			}
			for _, row := range reads {
				r := f.do("GET", row.path, confined, tenantHdr(tenant))
				if r.code != http.StatusForbidden || !strings.Contains(r.raw, "workspace confined") {
					t.Fatalf("confined %s = %d %s, want 403 workspace confined", row.label, r.code, r.raw)
				}
				if strings.Contains(r.raw, "claude-1") || strings.Contains(r.raw, profiles[0].Ref) {
					t.Fatalf("confined %s disclosed the account: %s", row.label, r.raw)
				}
			}
			// A malformed reference is answered 404 before the data handle, for the
			// confined reader as for the tenant-wide one: the answer depends only on the
			// reference's spelling, so it discloses nothing.
			for _, who := range []struct{ label, token string }{{"confined", confined}, {"tenant-wide", wide}} {
				r := f.do("GET", base+"/not-a-profile-reference", who.token, tenantHdr(tenant))
				if r.code != http.StatusNotFound || strings.Contains(r.raw, "claude-1") || strings.Contains(r.raw, profiles[0].Ref) {
					t.Fatalf("%s get of a malformed reference = %d %s, want 404 disclosing nothing", who.label, r.code, r.raw)
				}
			}
			r := f.doJSON("POST", base+"/"+profiles[1].Ref+"/adopt", confined, map[string]any{"name": "claude-2"}, tenantHdr(tenant))
			if r.code != http.StatusForbidden {
				t.Fatalf("confined adopt = %d %s, want 403", r.code, r.raw)
			}
			if got := acctRow(t, f.m, tenant, profiles[1].Ref); !got.IsNull("account_name") {
				t.Fatalf("a confined adopt named the profile %q", got.String("account_name"))
			}
		})
	}
}
