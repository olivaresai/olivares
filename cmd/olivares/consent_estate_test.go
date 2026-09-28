// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/webaddr"
)

// The consent estate is the real Community composition, booted the way the
// binary boots it, on SQLite and on PostgreSQL. Its tests drive the HTTP routes
// the product exposes and read the store only to observe.

// consentEngines are the engines every composition case runs on.
var consentEngines = []string{"sqlite", "postgres"}

// consentEstate is one booted composition with a step-up-verified superadmin and
// two business tenants, T and B.
type consentEstate struct {
	t      *testing.T
	engine string
	eng    *engine
	h      http.Handler
	admin  string
	super  auth.Principal
	tT     model.TenantID
	tB     model.TenantID
	// comm is the messaging graph of an estate booted with the communication
	// kernel activated (bootMessagingEstate); nil otherwise.
	comm *messagingGraph
}

// consentResp is one HTTP answer.
type consentResp struct {
	code int
	raw  string
	body map[string]any
}

// errorCode returns the machine-readable error code of an API error answer, or
// the error type of an apps gateway answer, whose envelope names it so.
func (r consentResp) errorCode() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
		if c, ok := e["type"].(string); ok {
			return c
		}
	}
	return ""
}

// bootConsentEstate boots the composition on engineName.
func bootConsentEstate(t *testing.T, engineName string) *consentEstate {
	t.Helper()
	return bootConsentEstateAt(t, engineName, "")
}

// bootConsentEstateAt boots the composition on engineName with publicURL as the
// console address the operator declared, or none when it is empty.
func bootConsentEstateAt(t *testing.T, engineName, publicURL string) *consentEstate {
	t.Helper()
	backing := consentBacking(t, engineName)
	var eng *engine
	if publicURL == "" {
		eng = bootCommunicationHTTPTestEngine(t, backing)
	} else {
		addr, err := webaddr.Parse("--public-url", publicURL)
		if err != nil {
			t.Fatalf("parse the console address: %v", err)
		}
		prepareCompositionTestBoot(t)
		cfg := backing.bootConfig()
		cfg.PublicAddr, cfg.PublicAddrSource = addr, publicAddrFlag
		if eng, err = boot(context.Background(), cfg); err != nil {
			t.Fatalf("boot the composition: %v", err)
		}
	}
	t.Cleanup(func() { _ = eng.Close() })
	e := &consentEstate{t: t, engine: engineName, eng: eng, h: eng.api.Handler()}
	e.setupRoot()
	e.tT = e.createOrg("consent-t")
	e.tB = e.createOrg("consent-b")
	return e
}

// consentBacking returns a fresh store on engineName.
func consentBacking(t *testing.T, engineName string) communicationHTTPTestStore {
	t.Helper()
	if engineName == "postgres" {
		return communicationHTTPTestPostgresStore(t)
	}
	return communicationHTTPTestSQLiteStore(t)
}

// setupRoot claims the fresh estate for the superadmin and signs it in. Every
// estate signs its accounts in by password, so it first installs the test
// argon2id parameters, as the module harnesses do, and restores the production
// ones when the test ends.
func (e *consentEstate) setupRoot() {
	e.t.Helper()
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	e.t.Cleanup(func() {
		auth.SetTestHashParams(auth.DefaultArgonMemKiB, auth.DefaultArgonTime, auth.DefaultArgonThreads)
	})
	setup, _, err := e.eng.setupTok.Ensure()
	if err != nil {
		e.t.Fatalf("setup token: %v", err)
	}
	if r := e.do("POST", "/v1/setup", "", "", map[string]any{
		"token": setup, "email": "root@consent.test", "password": "consent-root-pass",
	}); r.code != http.StatusCreated {
		e.t.Fatalf("setup = %d %s", r.code, r.raw)
	}
	e.signInRoot()
}

// signInRoot signs the superadmin in and steps its session up.
func (e *consentEstate) signInRoot() {
	e.t.Helper()
	e.admin = e.login("root@consent.test", "consent-root-pass")
	ctx := context.Background()
	p, err := e.eng.authr.Authenticate(ctx, e.admin)
	if err != nil {
		e.t.Fatalf("authenticate the superadmin: %v", err)
	}
	if _, err := e.eng.authr.ElevateSession(ctx, p, "webauthn", auth.AAL3); err != nil {
		e.t.Fatalf("elevate the superadmin: %v", err)
	}
	e.super = p
}

// forSubtest returns the estate reporting to t, for a subtest that drives it.
func (e *consentEstate) forSubtest(t *testing.T) *consentEstate {
	sub := *e
	sub.t = t
	return &sub
}

// onConsentEngines runs body on a fresh estate per engine.
func onConsentEngines(t *testing.T, body func(t *testing.T, e *consentEstate)) {
	t.Helper()
	for _, name := range consentEngines {
		t.Run(name, func(t *testing.T) { body(t, bootConsentEstate(t, name)) })
	}
}

// do issues one request against the composed handler.
func (e *consentEstate) do(method, path, token string, tenant model.TenantID, body any) consentResp {
	e.t.Helper()
	return e.doWith(method, path, token, tenant, body, nil)
}

// doWith is do with extra request headers; "Host" sets the request's host.
func (e *consentEstate) doWith(method, path, token string, tenant model.TenantID, body any, headers map[string]string) consentResp {
	e.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "10.0.0.2:4321"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant.String())
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := consentResp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// scim issues one SCIM request with the SCIM media type.
func (e *consentEstate) scim(method, path, token, body string) consentResp {
	e.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/scim+json")
	req.RemoteAddr = "10.0.0.2:4321"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := consentResp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (e *consentEstate) login(email, password string) string {
	e.t.Helper()
	r := e.do("POST", "/v1/auth/login", "", "", map[string]any{"email": email, "password": password})
	if r.code != http.StatusOK {
		e.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	tok, _ := r.body["token"].(string)
	return tok
}

func (e *consentEstate) createOrg(slug string) model.TenantID {
	e.t.Helper()
	r := e.do("POST", "/v1/system/orgs", e.admin, "", map[string]any{"name": slug, "slug": slug})
	if r.code != http.StatusCreated {
		e.t.Fatalf("create org %s = %d %s", slug, r.code, r.raw)
	}
	id, _ := r.body["tenant_id"].(string)
	return model.TenantID(id)
}

// onboard onboards a new person into tenant with a password and returns the id.
func (e *consentEstate) onboard(tenant model.TenantID, email, role string) model.ID {
	e.t.Helper()
	r := e.do("POST", "/v1/onboard", e.admin, tenant, map[string]any{
		"email": email, "role": role, "mode": "password", "password": consentMemberPassword,
	})
	if r.code != http.StatusCreated {
		e.t.Fatalf("onboard %s = %d %s", email, r.code, r.raw)
	}
	u, _ := r.body["user"].(map[string]any)
	id, _ := u["id"].(string)
	return model.ID(id)
}

// createUser creates an account through the deployment's own user route, with
// no membership, and returns its id.
func (e *consentEstate) createUser(email string) model.ID {
	e.t.Helper()
	r := e.do("POST", "/v1/users", e.admin, "", map[string]any{"email": email, "password": consentMemberPassword})
	if r.code != http.StatusCreated {
		e.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	return model.ID(id)
}

// consentMemberPassword is every consent-test member's password.
const consentMemberPassword = "consent-member-pass"

// scimToken issues a SCIM connection token bound to tenant.
func (e *consentEstate) scimToken(tenant model.TenantID) string {
	e.t.Helper()
	tok, _, err := e.eng.authr.IssueToken(context.Background(), e.super, auth.TokenSpec{
		Name: "scim", BoundTenant: tenant, Role: auth.RoleAdmin,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// scimDelete removes a member through the tenant's SCIM connection.
func (e *consentEstate) scimDelete(tenant model.TenantID, user model.ID) {
	e.t.Helper()
	if r := e.scim("DELETE", "/v1/scim/v2/Users/"+user.String(), e.scimToken(tenant), ""); r.code != http.StatusNoContent {
		e.t.Fatalf("SCIM DELETE = %d %s", r.code, r.raw)
	}
}

// seedMembership writes a membership through the store; it models one that
// predates the consent rule.
func (e *consentEstate) seedMembership(user model.ID, tenant model.TenantID, role string) {
	e.t.Helper()
	ctx := context.Background()
	if err := e.eng.store.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Memberships().Create(ctx, model.Membership{UserID: user, TargetTenantID: tenant, Role: role})
		return err
	}); err != nil {
		e.t.Fatalf("seed membership: %v", err)
	}
}

func (e *consentEstate) memberOf(user model.ID, tenant model.TenantID) bool {
	e.t.Helper()
	ctx := context.Background()
	var found bool
	if err := e.eng.store.AuthView(ctx, func(as store.AuthScope) error {
		ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
			{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
		}, Limit: 1})
		found = len(ms) > 0
		return err
	}); err != nil {
		e.t.Fatalf("read membership: %v", err)
	}
	return found
}

func (e *consentEstate) user(id model.ID) model.User {
	e.t.Helper()
	ctx := context.Background()
	var out model.User
	if err := e.eng.store.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, id)
		out = u
		return err
	}); err != nil {
		e.t.Fatalf("read user: %v", err)
	}
	return out
}

// userByEmail returns the account holding email, and whether one exists.
func (e *consentEstate) userByEmail(email string) (model.User, bool) {
	e.t.Helper()
	ctx := context.Background()
	var (
		u     model.User
		found bool
	)
	if err := e.eng.store.AuthView(ctx, func(as store.AuthScope) error {
		users, _, err := as.Users().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "email", Op: model.OpEq, Value: strings.ToLower(strings.TrimSpace(email))},
		}, Limit: 1})
		if err != nil {
			return err
		}
		if len(users) > 0 {
			u, found = users[0], true
		}
		return nil
	}); err != nil {
		e.t.Fatalf("read the account of %s: %v", email, err)
	}
	return u, found
}

// record returns the account's retirement record in tenant.
func (e *consentEstate) record(user model.ID, tenant model.TenantID) (model.TenantExclusion, bool) {
	e.t.Helper()
	rec, found, err := e.eng.authr.RetirementRecord(context.Background(), user, tenant)
	if err != nil {
		e.t.Fatalf("read retirement record: %v", err)
	}
	return rec, found
}

// pump returns the composition's retirement pump. A composition without one
// fails the test: an absent pump never finishes a retirement.
func (e *consentEstate) pump() *retirementPump {
	e.t.Helper()
	if e.eng.retirementPump == nil {
		e.t.Fatal("the composition has no retirement pump")
	}
	return e.eng.retirementPump
}

// runPump advances every due retirement until none advances, bounded. It first
// moves the pump's clock past the longest backoff, as the running pump would
// have waited, so a blocker the test resolved since the last call is seen.
func (e *consentEstate) runPump() {
	e.t.Helper()
	p := e.pump()
	advancePumpClock(p)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		n, err := p.runOnce(ctx)
		if err != nil {
			e.t.Fatalf("retirement pump: %v", err)
		}
		if n == 0 {
			return
		}
	}
}

// advancePumpClock moves p's clock past the longest retirement backoff.
func advancePumpClock(p *retirementPump) {
	next := p.clock().Add(time.Hour + time.Minute)
	p.now = func() time.Time { return next }
}

// directoryEpoch reads tenant's directory epoch version.
func (e *consentEstate) directoryEpoch(tenant model.TenantID) int64 {
	e.t.Helper()
	ctx := context.Background()
	var version int64
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		fact, err := directoryEpochFact(ctx, sc)
		version = fact.Version
		return err
	}); err != nil {
		e.t.Fatalf("read the directory epoch: %v", err)
	}
	return version
}

// actsIn returns the status of a read the caller's role permits in tenant.
func (e *consentEstate) actsIn(token string, tenant model.TenantID) int {
	e.t.Helper()
	return e.do("GET", "/v1/agents", token, tenant, nil).code
}

// directoryEpochFact reads tenant's directory epoch as a fact reference.
func directoryEpochFact(ctx context.Context, sc store.Scope) (store.AuthorizationFactRef, error) {
	reader, ok := sc.(store.DirectorySnapshotReader)
	if !ok {
		return store.AuthorizationFactRef{}, errors.New("the scope exposes no directory snapshot reader")
	}
	epoch, err := reader.ReadDirectoryEpoch(ctx)
	if err != nil {
		return store.AuthorizationFactRef{}, err
	}
	return store.AuthorizationFactRef{Kind: model.DirectoryEpochKind, ID: epoch.ID, Version: epoch.Version}, nil
}

// authorityRefs reads the authority version of every account.
func (e *consentEstate) authorityRefs(users ...model.ID) []store.UserAuthorityFactRef {
	e.t.Helper()
	ctx := context.Background()
	out := make([]store.UserAuthorityFactRef, 0, len(users))
	if err := e.eng.store.AuthView(ctx, func(as store.AuthScope) error {
		reader, ok := as.(store.AuthUserAuthorityEvidenceScope)
		if !ok {
			return errors.New("the auth scope exposes no authority reader")
		}
		for _, u := range users {
			ref, err := reader.ReadUserAuthorityFact(ctx, u)
			if err != nil {
				return err
			}
			out = append(out, ref)
		}
		return nil
	}); err != nil {
		e.t.Fatalf("read authority versions: %v", err)
	}
	return out
}

// seedFenced writes one module row naming users, inside a transaction opened
// with the directory authority barrier over the tenant's directory epoch and
// those accounts. It stands in for a product writer where the test's point is
// what later happens to the row.
func (e *consentEstate) seedFenced(tenant model.TenantID, kind model.Kind, rec model.Record, users ...model.ID) model.ID {
	e.t.Helper()
	ctx := context.Background()
	refs := e.authorityRefs(users...)
	var id model.ID
	if err := e.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		fact, err := directoryEpochFact(ctx, sc)
		if err != nil {
			return err
		}
		locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return errors.New("the scope exposes no directory authority barrier")
		}
		if err := locker.LockDirectoryAuthoritySnapshot(ctx, store.AuthoritySnapshotBundle{
			Facts: []store.AuthorizationFactRef{fact}, UserAuthorities: refs,
		}); err != nil {
			return err
		}
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return err
		}
		id = model.ID(created.String(model.ColID))
		return nil
	}); err != nil {
		e.t.Fatalf("seed %s: %v", kind, err)
	}
	return id
}

// rowExists reports whether the module row id of kind is still stored in tenant.
func (e *consentEstate) rowExists(tenant model.TenantID, kind model.Kind, id model.ID) bool {
	e.t.Helper()
	ctx := context.Background()
	found := false
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		_, err = repo.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		found = err == nil
		return err
	}); err != nil {
		e.t.Fatalf("read %s: %v", kind, err)
	}
	return found
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
