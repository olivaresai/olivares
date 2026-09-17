// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// Acceptance for the launch-readiness read, over REAL HTTP on REAL engines.
//
// Every table here runs on SQLite AND on a split-owner PostgreSQL, and the
// Postgres leg is LOGGED AS NOT EXERCISED when no server is configured rather
// than silently dropped: a run that skipped it did not prove it. The filesystem
// and the Runner are exercised as themselves — real disposable directories, real
// disposable executables — because a fixture that returns a constant JSON proves
// only that the fixture is consistent with itself.

// --- engines ------------------------------------------------------------------

// readinessEngine is one real backend plus a raw handle the effect census reads.
type readinessEngine struct {
	name string
	cfg  store.Config
	// rawOpen is a plain database/sql handle used ONLY to census row counts.
	rawOpen func(t *testing.T) *sql.DB
	// tables lists the physical tables of this engine.
	tables func(t *testing.T, db *sql.DB) []string
}

// readinessEngines returns SQLite (file-backed, so it can be reopened and
// inspected) and, when a server is configured, a private SPLIT-OWNER Postgres:
// the topology where the app role holds DML only and a separate least-privilege
// owner runs DDL, which is the one this plane ships against.
func readinessEngines(t *testing.T) []readinessEngine {
	t.Helper()
	out := []readinessEngine{sqliteReadinessEngine(t)}
	if enginetest.PostgresAvailable(t) {
		out = append(out, postgresReadinessEngine(t))
	} else {
		t.Logf("%s unset: PostgreSQL split-owner NOT exercised on this run (SQLite only)", enginetest.EnvSuperuserDSN)
	}
	return out
}

// freshEngine provisions ANOTHER private database of the same kind. A table that
// compares two compositions needs two clean estates, and reusing one would let
// the first composition's rows explain the second's answers.
func freshEngine(t *testing.T, be readinessEngine) readinessEngine {
	t.Helper()
	if be.cfg.Engine == store.EngineSQLite {
		return sqliteReadinessEngine(t)
	}
	return postgresReadinessEngine(t)
}

func sqliteReadinessEngine(t *testing.T) readinessEngine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "readiness.db")
	return readinessEngine{
		name: "sqlite",
		cfg:  store.Config{Engine: store.EngineSQLite, DSN: path, Debug: true},
		rawOpen: func(t *testing.T) *sql.DB {
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			return db
		},
		tables: func(t *testing.T, db *sql.DB) []string {
			return scanNames(t, db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
		},
	}
}

func postgresReadinessEngine(t *testing.T) readinessEngine {
	t.Helper()
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	return readinessEngine{
		name: "postgres-split-owner",
		cfg: store.Config{
			Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
			AdminDSN: pg.Admin, Debug: true,
		},
		// The census reads through the maintenance role so row-level security
		// cannot make an effect invisible to the very check that looks for it.
		rawOpen: func(t *testing.T) *sql.DB {
			db, err := sql.Open("pgx", pg.Superuser)
			if err != nil {
				t.Fatal(err)
			}
			return db
		},
		tables: func(t *testing.T, db *sql.DB) []string {
			return scanNames(t, db, `SELECT tablename FROM pg_tables WHERE schemaname='public'`)
		},
	}
}

func scanNames(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	sort.Strings(out)
	return out
}

// --- HTTP fixture -------------------------------------------------------------

// readinessFixture is a real api.Server with the sessions module mounted on a
// real engine, plus the two principals the permission table needs.
type readinessFixture struct {
	t      *testing.T
	m      *Module
	h      *harness
	be     readinessEngine
	admin  string
	viewer string
	tenant model.TenantID
	other  model.TenantID
}

// newReadinessFixture opens the given engine, mounts the module and bootstraps
// auth. It deliberately re-implements the small in-memory harness instead of
// widening it: this one must be able to point at a file/Postgres DSN so the
// effect census can read the same database the server writes.
func newReadinessFixture(t *testing.T, be readinessEngine, m *Module) *readinessFixture {
	t.Helper()
	ctx := context.Background()
	st, err := engine.Open(ctx, be.cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("open %s: %v", be.name, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, e := sys.EnsureSystemTenant(ctx); return e }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	tok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := tok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authorizer := auth.NewAuthorizer(nil)
	m.UseWorkAuthorizer(authorizer)
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: authorizer,
		Signer: signer, SetupToken: tok, Version: "test", Modules: []api.Module{m},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, m: m, srv: srv, st: st, setupTok: plaintext}
	f := &readinessFixture{t: t, m: m, h: h, be: be}
	f.admin = h.adminLogin()
	f.tenant = h.createOrg(f.admin, "acme")
	f.other = h.createOrg(f.admin, "globex")
	// A VIEWER holds sessions:profile:read and NOT sessions:run:write: exactly the
	// principal that must be able to read the requirements and unable to launch.
	f.viewer = h.viewerToken(f.admin, f.tenant, "reader@acme.com")
	return f
}

// createProfile registers a profile over HTTP, the way the console does.
func (f *readinessFixture) createProfile(driver, configHome, userHome, authSource string) string {
	f.t.Helper()
	body := map[string]any{
		"driver": driver, "config_home": configHome, "user_home": userHome,
		"display_name": driver + " fixture",
	}
	if authSource != "" {
		body["auth_source"] = authSource
	}
	r := f.h.doJSON("POST", "/v1/m/sessions/provider-profiles", f.admin, body, tenantHdr(f.tenant))
	if r.code != http.StatusCreated {
		f.t.Fatalf("create %s profile = %d %s", driver, r.code, r.raw)
	}
	ref, _ := r.body["profile_ref"].(string)
	if ref == "" {
		f.t.Fatalf("create %s profile returned no ref: %s", driver, r.raw)
	}
	return ref
}

// operable reads the LEGACY flag of the profile DTO, unchanged by this work.
func (f *readinessFixture) operable(ref string) bool {
	f.t.Helper()
	r := f.h.do("GET", "/v1/m/sessions/provider-profiles/"+ref, f.viewer, tenantHdr(f.tenant))
	if r.code != http.StatusOK {
		f.t.Fatalf("get profile = %d %s", r.code, r.raw)
	}
	got, _ := r.body["operable"].(bool)
	return got
}

// readiness performs the GET as the VIEWER — the least-privileged principal that
// may see it — and decodes the typed document.
func (f *readinessFixture) readiness(ref, query string) (SessionLaunchReadiness, resp) {
	f.t.Helper()
	return f.readinessAs(f.viewer, f.tenant, ref, query)
}

func (f *readinessFixture) readinessAs(token string, tenant model.TenantID, ref, query string) (SessionLaunchReadiness, resp) {
	f.t.Helper()
	path := "/v1/m/sessions/provider-profiles/" + ref + "/launch-readiness"
	if query != "" {
		path += "?" + query
	}
	r := f.h.do("GET", path, token, tenantHdr(tenant))
	var out SessionLaunchReadiness
	if r.code == http.StatusOK {
		if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
			f.t.Fatalf("decode readiness: %v (%s)", err, r.raw)
		}
	}
	return out, r
}

// check returns one dimension's verdict, failing if the panel omitted it: the
// panel is COMPLETE by contract, and a missing dimension is a defect, not a
// reason for a test to pass vacuously.
func (d SessionLaunchReadiness) check(t *testing.T, kind ReadinessCheck) LaunchReadinessCheck {
	t.Helper()
	for _, c := range d.Checks {
		if c.Check == kind {
			return c
		}
	}
	t.Fatalf("readiness panel has no %q check: %+v", kind, d.Checks)
	return LaunchReadinessCheck{}
}

// wantCheck asserts one dimension's state AND cause together: a right state for
// the wrong reason is not the assertion this contract needs.
func (d SessionLaunchReadiness) wantCheck(t *testing.T, kind ReadinessCheck, state ReadinessState, code string) {
	t.Helper()
	got := d.check(t, kind)
	if got.State != state || got.Code != code {
		t.Fatalf("%s = (%s, %s), want (%s, %s)", kind, got.State, got.Code, state, code)
	}
}

// --- filesystem fixtures ------------------------------------------------------

// readinessHomes makes one real, canonical pair of homes.
func readinessHomes(t *testing.T) (configHome, userHome string) {
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
	return mk("config"), mk("home")
}

// readinessProgramFixtures builds the four real states an operator's pinned
// binary can be in. The executable one is a SCRIPT THAT LEAVES A MARK: if
// anything ever runs it — a stray `--version` probe, a shell-out, a spawn — the
// sentinel appears and every test that shares this fixture fails.
type readinessProgramFixtures struct {
	present       string
	missing       string
	notExecutable string
	unexaminable  string
	sentinel      string
	dir           string
}

func newReadinessProgramFixtures(t *testing.T) readinessProgramFixtures {
	t.Helper()
	dir := t.TempDir()
	out := readinessProgramFixtures{
		dir:           dir,
		present:       filepath.Join(dir, "provider-cli"),
		missing:       filepath.Join(dir, "absent-cli"),
		notExecutable: filepath.Join(dir, "plain-cli"),
		unexaminable:  filepath.Join(dir, "linked-cli"),
		sentinel:      filepath.Join(dir, "EXECUTED"),
	}
	script := "#!/bin/sh\ntouch " + out.sentinel + "\nexit 0\n"
	if err := os.WriteFile(out.present, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out.notExecutable, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	// A link whose target cannot be EXAMINED: the target lives inside a directory
	// with no search permission, so stat answers EACCES. That is an inspection
	// that could not look, and it must not be reported as "not installed".
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(locked, "hidden-cli")
	if err := os.WriteFile(target, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, out.unexaminable); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	t.Cleanup(func() {
		if _, err := os.Stat(out.sentinel); err == nil {
			t.Error("a provider program was EXECUTED during a readiness read: the sentinel exists")
		}
	})
	return out
}

// --- purity spies -------------------------------------------------------------

// inspectingRunner delegates the inspection to the NATIVE runner and fails the
// test the moment anything asks it to launch a process.
type inspectingRunner struct {
	t      *testing.T
	native Runner
	mu     sync.Mutex
	calls  int
}

func newInspectingRunner(t *testing.T) *inspectingRunner {
	return &inspectingRunner{t: t, native: NewProcRunner()}
}

func (r *inspectingRunner) Launch(context.Context, LaunchSpec) (Process, error) {
	r.t.Error("launch readiness called Runner.Launch; the read must start no process")
	return nil, errors.New("readiness must not launch")
}

func (r *inspectingRunner) InspectLaunch(ctx context.Context, in RunnerInspection) (RunnerObservation, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return r.native.(RunnerInspector).InspectLaunch(ctx, in)
}

func (r *inspectingRunner) inspections() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// mintRefusingCredentialSource is WIRED — so the credential-source check reports
// ready — and fails the test if the read ever tries to mint through it.
type mintRefusingCredentialSource struct{ t *testing.T }

func (c mintRefusingCredentialSource) Mint(context.Context, CredentialRequest) (Credential, error) {
	c.t.Error("launch readiness minted an inference credential; the read must not mint")
	return Credential{}, errors.New("readiness must not mint")
}

type mintRefusingProviderSource struct{ t *testing.T }

func (c mintRefusingProviderSource) Mint(context.Context, ProviderCredentialRequest) (ProviderCredential, error) {
	c.t.Error("launch readiness called a governed provider credential adapter")
	return ProviderCredential{}, errors.New("readiness must not mint")
}

// refusingLaunchGate / refusingStopGate are wired to prove the read never asks
// them: consulting either can open a HITL approval or provision a PEP bearer.
type refusingLaunchGate struct{ t *testing.T }

func (g refusingLaunchGate) Authorize(context.Context, model.TenantID, LaunchIntent) (LaunchDecision, error) {
	g.t.Error("launch readiness consulted the LaunchGate; authorization is decided on the POST")
	return LaunchDecision{}, errors.New("readiness must not authorize")
}

type refusingStopGate struct{ t *testing.T }

func (g refusingStopGate) Check(context.Context, model.TenantID, StopDims) (StopDecision, error) {
	g.t.Error("launch readiness consulted the StopGate")
	return StopDecision{}, errors.New("readiness must not check the kill switch")
}

// --- row census ---------------------------------------------------------------

// tableCounts is every physical table's row count. Comparing the WHOLE database
// before and after is the only census that cannot be short by the one table
// somebody forgot to list.
func tableCounts(t *testing.T, be readinessEngine) map[string]int64 {
	t.Helper()
	db := be.rawOpen(t)
	defer func() { _ = db.Close() }()
	out := map[string]int64{}
	for _, name := range be.tables(t, db) {
		var n int64
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM "`+name+`"`).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

// diffCounts names every table whose row count moved, so a failure says WHICH
// relation grew rather than only that something did.
func diffCounts(before, after map[string]int64) []string {
	var out []string
	for name, want := range before {
		if got := after[name]; got != want {
			out = append(out, name+": "+strconv.FormatInt(want, 10)+"→"+strconv.FormatInt(got, 10))
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			out = append(out, name+": table appeared")
		}
	}
	sort.Strings(out)
	return out
}
