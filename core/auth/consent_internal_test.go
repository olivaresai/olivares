// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// openConsentStore opens a fresh store on engine; the PostgreSQL leg skips
// without a configured server. The consent tests sign accounts in by password,
// so the store's test takes the test argon2id parameters and restores the
// production ones, which TestProductionHashParamsUnchanged pins, when it ends.
func openConsentStore(t *testing.T, engine store.Engine) store.Store {
	t.Helper()
	SetTestHashParams(TestArgonMemKiB, TestArgonTime, TestArgonThreads)
	t.Cleanup(func() { SetTestHashParams(DefaultArgonMemKiB, DefaultArgonTime, DefaultArgonThreads) })
	cfg := store.Config{Engine: engine, DSN: ":memory:", Debug: true}
	if engine == store.EnginePostgres {
		dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
		cfg = store.Config{Engine: engine, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, Debug: true, MaxConns: 8}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := sqlstore.Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open %s store: %v", engine, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// forEachConsentEngine runs body on SQLite and on PostgreSQL.
func forEachConsentEngine(t *testing.T, body func(t *testing.T, st store.Store)) {
	t.Helper()
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) { body(t, openConsentStore(t, engine)) })
	}
}

// consentTenant provisions a business tenant the way boot does.
func consentTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	ctx := context.Background()
	var id model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		o, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		id = o.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision %s: %v", slug, err)
	}
	return id
}

// consentSuperadmin bootstraps the deployment's superadmin and returns its
// authenticated principal.
func consentSuperadmin(t *testing.T, ctx context.Context, a *Authenticator) Principal {
	t.Helper()
	if _, err := a.BootstrapSuperadmin(ctx, "root@consent.example", "bootstrap-pass-123"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	tok, _, err := a.Login(ctx, "root@consent.example", "bootstrap-pass-123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	p, err := a.Authenticate(ctx, tok)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return p
}

// consentMember creates an active account with a viewer membership in every
// tenant given, through the store.
func consentMember(t *testing.T, ctx context.Context, st store.Store, email string, tenants ...model.TenantID) model.ID {
	t.Helper()
	var id model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Create(ctx, model.User{Email: email, Status: model.StatusActive})
		if err != nil {
			return err
		}
		id = u.ID
		for _, tenant := range tenants {
			if _, err := as.Memberships().Create(ctx, model.Membership{UserID: u.ID, TargetTenantID: tenant, Role: RoleViewer}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("create member %s: %v", email, err)
	}
	return id
}

// authorityVersion reads the account's authority version.
func authorityVersion(t *testing.T, ctx context.Context, st store.Store, user model.ID) int64 {
	t.Helper()
	var v int64
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		reader, ok := as.(store.AuthUserAuthorityEvidenceScope)
		if !ok {
			t.Fatal("the auth scope exposes no authority reader")
		}
		ref, err := reader.ReadUserAuthorityFact(ctx, user)
		v = ref.Version
		return err
	}); err != nil {
		t.Fatalf("read authority version: %v", err)
	}
	return v
}

// setRetirementState rewrites a retirement record's state through the store.
func setRetirementState(t *testing.T, ctx context.Context, st store.Store, id model.ID, state model.RetirementState) {
	t.Helper()
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rec, err := as.TenantExclusions().Get(ctx, id)
		if err != nil {
			return err
		}
		rec.RetirementState = state
		_, err = as.TenantExclusions().Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("set retirement state %s: %v", state, err)
	}
}

// sessionFor writes a live session for user and returns its token.
func sessionFor(t *testing.T, ctx context.Context, st store.Store, user model.ID, scope model.TenantID) string {
	t.Helper()
	prefix := PrefixSession
	if !scope.IsZero() {
		prefix = PrefixScopedSession
	}
	cred, err := NewCredential(prefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Create(ctx, model.AuthSession{
			UserID: user, Selector: cred.Selector, SecretHash: cred.SecretHash,
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), AAL: AAL1, AMR: []string{"pwd"},
			TenantScope: scope,
		})
		return err
	}); err != nil {
		t.Fatalf("write session: %v", err)
	}
	return cred.Token
}

// TestARepeatedOffboardOfARemovedMemberWritesNothing: the offboard of an account
// already removed from the tenant, whose retirement has not been lifted, is the
// same retirement. It writes no new generation and moves no authority version; it
// wakes the pump and returns the record. A re-admitted account is a new
// incarnation, and its offboard starts the next generation.
func TestARepeatedOffboardOfARemovedMemberWritesNothing(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		a := NewAuthenticator(st, nil)
		woken := 0
		a.SetRetirementWaker(func() { woken++ })
		tenant := consentTenant(t, st, "offboard-t")
		actor := consentSuperadmin(t, ctx, a)
		v := consentMember(t, ctx, st, "v@offboard.example", tenant)
		offboard := func() model.TenantExclusion {
			t.Helper()
			var rec model.TenantExclusion
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				rec, err = a.scopedOffboard(ctx, as, actor, v, tenant, "test")
				return err
			}); err != nil {
				t.Fatalf("offboard: %v", err)
			}
			return rec
		}

		h0 := authorityVersion(t, ctx, st, v)
		first := offboard()
		if first.ID.IsZero() || first.RetirementState != model.RetirementRetiring || first.RetirementGeneration != 1 {
			t.Fatalf("the first offboard's record = %+v, want a retiring record at generation 1", first)
		}
		h1 := authorityVersion(t, ctx, st, v)
		if h1 != h0+1 {
			t.Errorf("the first offboard moved the authority version %d -> %d, want exactly one bump", h0, h1)
		}
		if woken != 1 {
			t.Errorf("the first offboard woke the pump %d times, want 1", woken)
		}

		for i, state := range []model.RetirementState{model.RetirementRetiring, model.RetirementBlocked, model.RetirementRetired} {
			setRetirementState(t, ctx, st, first.ID, state)
			again := offboard()
			if again.ID != first.ID || again.RetirementGeneration != 1 || again.RetirementState != state {
				t.Errorf("a repeated offboard while %s = %+v, want the same record unchanged", state, again)
			}
			if h := authorityVersion(t, ctx, st, v); h != h1 {
				t.Errorf("a repeated offboard while %s moved the authority version %d -> %d", state, h1, h)
			}
			if woken != i+2 {
				t.Errorf("a repeated offboard while %s did not wake the pump (%d wakes)", state, woken)
			}
		}

		// After a lift and a new membership the account is a new incarnation.
		setRetirementState(t, ctx, st, first.ID, model.RetirementLifted)
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			_, err := as.Memberships().Create(ctx, model.Membership{UserID: v, TargetTenantID: tenant, Role: RoleViewer})
			return err
		}); err != nil {
			t.Fatalf("re-admit: %v", err)
		}
		h2 := authorityVersion(t, ctx, st, v)
		next := offboard()
		if next.RetirementGeneration != 2 || next.RetirementState != model.RetirementRetiring {
			t.Errorf("the offboard of a re-admitted account = %+v, want retiring at generation 2", next)
		}
		if h := authorityVersion(t, ctx, st, v); h != h2+1 {
			t.Errorf("the offboard of a re-admitted account moved the authority version %d -> %d, want one bump", h2, h)
		}
	})
}

// principalBuilders classifies every production caller of newPrincipal by the
// function that holds the call. A new caller fails the tripwire below until it
// is classified here, with what it carries: an account's grants (and so its
// exclusions, session scope and floors), a token's single bound tenant (and so
// that tenant's exclusion), or no account at all.
var principalBuilders = map[string]string{
	"authenticator.go:authSession":                          "account session: exclusion, floor, session scope",
	"authenticator.go:authToken":                            "account token: exclusion of its bound tenant",
	"principal_lookup.go:principalForUserInScope":           "account standing entitlement: exclusion, floor",
	"principal_lookup.go:TenantPrincipals":                  "account standing entitlement: exclusion, floor",
	"principal_lookup.go:principalFromToken":                "account token: exclusion of its bound tenant",
	"principal_evidence.go:resolveSessionEvidenceMaterial":  "strict session path: exclusion, floor, session scope",
	"principal_evidence.go:resolveTokenEvidenceMaterial":    "strict token path: exclusion of its bound tenant",
	"delegation.go:PrincipalForDelegation":                  "delegation subject: membership, exclusion of the account and its source session, scope and floor re-read at verify",
	"pepservice.go:AuthenticatePEP":                         "service credential: no account authority",
	"worksession.go:workSessionPrincipal":                   "runtime credential: no account subject",
	"communicationsession.go:communicationSessionPrincipal": "runtime credential: no account subject",
	"orchestrationsession.go:orchestrationSessionPrincipal": "runtime credential: no account subject; exact tenant/workspace and restricted orchestration capabilities",
	"scoped.go:ScopedPrincipal":                             "in-process subject: no account",
	"ema.go:PrincipalForExternalID":                         "unwired",
	"ema.go:PrincipalForSSOSubject":                         "unwired",
}

// TestNoPrincipalBuilderCarriesAuthorityOutsideItsScopeOrThroughAnExclusion:
// every wired builder of an account's principal carries the account's exclusion
// from a tenant, its session scope and its retirement floor, and every builder
// is classified.
func TestNoPrincipalBuilderCarriesAuthorityOutsideItsScopeOrThroughAnExclusion(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		a := NewAuthenticator(st, nil)
		tT := consentTenant(t, st, "builders-t")
		tB := consentTenant(t, st, "builders-b")
		actor := consentSuperadmin(t, ctx, a)

		t.Run("an offboard exclusion", func(t *testing.T) {
			v := consentMember(t, ctx, st, "offboarded@builders.example", tT, tB)
			sess := sessionFor(t, ctx, st, v, "")
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				_, err := a.scopedOffboard(ctx, as, actor, v, tT, "test")
				return err
			}); err != nil {
				t.Fatalf("offboard: %v", err)
			}
			p, err := a.Authenticate(ctx, sess)
			if err != nil {
				t.Fatalf("authenticate the account-scope session: %v", err)
			}
			if !p.ExcludedFrom(tT) || p.IsMember(tT) {
				t.Errorf("the session principal of an offboarded account is not excluded from the tenant")
			}
			if !p.IsMember(tB) || p.ExcludedFrom(tB) {
				t.Errorf("the offboard reached the account's authority in another tenant")
			}
			if byUser, found, err := a.PrincipalForUser(ctx, v.String(), AAL1); err != nil || !found || !byUser.ExcludedFrom(tT) {
				t.Errorf("the standing principal of an offboarded account is not excluded (found=%t err=%v)", found, err)
			}
			// A caller that already holds an auth view builds the same principal in it.
			if err := st.AuthView(ctx, func(as store.AuthScope) error {
				inView, found, err := principalForUserInScope(ctx, as, v.String(), AAL1)
				if err != nil || !found || !inView.ExcludedFrom(tT) {
					t.Errorf("the standing principal built in a caller's view is not excluded (found=%t err=%v)", found, err)
				}
				return nil
			}); err != nil {
				t.Fatalf("read the standing principal in a caller's view: %v", err)
			}
			roster, err := a.TenantPrincipals(ctx, tT, AAL1)
			if err != nil {
				t.Fatal(err)
			}
			for _, rp := range roster {
				if rp.UserID == v && !rp.ExcludedFrom(tT) {
					t.Errorf("the tenant roster carries the offboarded account unexcluded")
				}
			}
			ref, ok := p.Ref()
			if !ok {
				t.Fatal("the session principal has no credential reference")
			}
			deadline, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if strict, err := a.ResolvePrincipalScope(deadline, ref, tT); err == nil && !strict.ExcludedFrom(tT) {
				t.Errorf("the strict path resolved an offboarded account unexcluded in the tenant")
			}
		})

		t.Run("a token bound to an excluded tenant", func(t *testing.T) {
			v := consentMember(t, ctx, st, "token@builders.example", tT, tB)
			cred, err := NewCredential(PrefixToken)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				if _, err := as.Tokens().Create(ctx, model.APIToken{
					Name: "bound", UserID: v, Selector: cred.Selector, SecretHash: cred.SecretHash,
					BoundTenantID: tT, Role: RoleViewer,
				}); err != nil {
					return err
				}
				_, err := as.TenantExclusions().Create(ctx, model.TenantExclusion{
					UserID: v, TargetTenantID: tT, Kind: model.ExclusionOffboard, CreatedBy: "test",
					RetirementGeneration: 1, RetirementState: model.RetirementRetiring,
				})
				return err
			}); err != nil {
				t.Fatalf("seed a live bound token and an exclusion: %v", err)
			}
			p, err := a.Authenticate(ctx, cred.Token)
			if err == nil && (!p.ExcludedFrom(tT) || p.IsMember(tT)) {
				t.Errorf("a token bound to a tenant that excludes its account carries that tenant")
			}
		})

		t.Run("a scoped session", func(t *testing.T) {
			v := consentMember(t, ctx, st, "scoped@builders.example", tT, tB)
			sess := sessionFor(t, ctx, st, v, tT)
			p, err := a.Authenticate(ctx, sess)
			if err != nil {
				t.Fatalf("authenticate the scoped session: %v", err)
			}
			if p.SessionScope() != tT || !p.IsMember(tT) || p.IsMember(tB) {
				t.Errorf("a session scoped to one tenant carries scope %q and tenants %v", p.SessionScope(), p.Tenants())
			}
		})

		t.Run("a retirement floor", func(t *testing.T) {
			v := consentMember(t, ctx, st, "lifted@builders.example", tT)
			epoch := int64(7)
			if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
				_, err := as.TenantExclusions().Create(ctx, model.TenantExclusion{
					UserID: v, TargetTenantID: tT, Kind: model.ExclusionOffboard, CreatedBy: "test",
					RetirementGeneration: 1, RetirementState: model.RetirementLifted, RetiredEpoch: &epoch,
				})
				return err
			}); err != nil {
				t.Fatalf("seed a lifted record: %v", err)
			}
			p, found, err := a.PrincipalForUser(ctx, v.String(), AAL1)
			if err != nil || !found {
				t.Fatalf("principal for the re-admitted account: found=%t err=%v", found, err)
			}
			if floor, ok := p.RetirementFloor(tT); !ok || floor != epoch {
				t.Errorf("the re-admitted account's floor = %d (%t), want %d", floor, ok, epoch)
			}
			if p.ExcludedFrom(tT) {
				t.Errorf("a lifted retirement still excludes the account")
			}
			if err := st.AuthView(ctx, func(as store.AuthScope) error {
				inView, found, err := principalForUserInScope(ctx, as, v.String(), AAL1)
				if err != nil || !found {
					t.Errorf("the standing principal in a caller's view: found=%t err=%v", found, err)
					return nil
				}
				if floor, ok := inView.RetirementFloor(tT); !ok || floor != epoch || inView.ExcludedFrom(tT) {
					t.Errorf("in a caller's view the re-admitted account's floor = %d (%t), excluded %t; want %d, not excluded",
						floor, ok, inView.ExcludedFrom(tT), epoch)
				}
				return nil
			}); err != nil {
				t.Fatalf("read the standing principal in a caller's view: %v", err)
			}
		})
	})

	t.Run("every builder is classified", func(t *testing.T) {
		found := principalBuilderCallers(t)
		for _, site := range found {
			if _, ok := principalBuilders[site]; !ok {
				t.Errorf("newPrincipal is called from %s, which is not classified", site)
			}
		}
		for site := range principalBuilders {
			if !containsString(found, site) {
				t.Errorf("classified builder %s no longer calls newPrincipal", site)
			}
		}
	})
}

// principalBuilderCallers lists, as "file:function", every production function in
// this package that calls newPrincipal.
func principalBuilderCallers(t *testing.T) []string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Dir(self)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "newPrincipal" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "newPrincipal" {
					seen[name+":"+fn.Name.Name] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// unwiredBuilders are the principal builders no production composition may
// reference: each would mint an account's authority outside the exclusion,
// scope and floor the wired builders carry.
var unwiredBuilders = map[string]bool{"NewEMAGrant": true, "NewIDJAGIssuer": true, "BindPEPCredential": true}

// TestUnwiredPrincipalBuildersStayUnwired is a tripwire: no production source in
// the tree references an unwired builder outside its own declaration.
func TestUnwiredPrincipalBuildersStayUnwired(t *testing.T) {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(self), "..", ".."))
	fset := token.NewFileSet()
	var refs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "web", "testdata", "docs-site":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		declared := map[*ast.Ident]bool{}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && unwiredBuilders[fn.Name.Name] {
				declared[fn.Name] = true
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if ok && unwiredBuilders[id.Name] && !declared[id] {
				refs = append(refs, fset.Position(id.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) > 0 {
		t.Errorf("an unwired principal builder is referenced by production source: %v", refs)
	}
}

// accountsOnly is a standing reader under which exactly the ids it holds are
// accounts, none of them being removed.
type accountsOnly map[model.ID]bool

// Standing implements StandingReader.
func (a accountsOnly) Standing(_ context.Context, _ model.TenantID, users []model.ID) (map[model.ID]Standing, error) {
	out := make(map[model.ID]Standing, len(users))
	for _, u := range users {
		out[u] = Standing{User: u, Exists: a[u], AuthorityVersion: 1}
	}
	return out, nil
}

// TestTheFenceBoundCountsAccountsNotValues: the bound on one fenced write counts
// the accounts it names, once each, after resolution; repeated values and values
// that name no account never count against it.
func TestTheFenceBoundCountsAccountsNotValues(t *testing.T) {
	ctx := context.Background()
	tenant := model.NewTenantID()
	accounts := accountsOnly{}
	var named []model.ID
	for i := 0; i < MaxFencedSubjects; i++ {
		id := model.NewID()
		accounts[id] = true
		named = append(named, id, id)
	}
	for i := 0; i < 3*MaxFencedSubjects; i++ {
		named = append(named, model.NewID())
	}
	refs, err := FenceSubjects(ctx, accounts, tenant, named)
	if err != nil || len(refs) != MaxFencedSubjects {
		t.Fatalf("a write naming %d accounts among %d values = %d refs, %v; want %d, nil",
			MaxFencedSubjects, len(named), len(refs), err, MaxFencedSubjects)
	}
	extra := model.NewID()
	accounts[extra] = true
	if _, err := FenceSubjects(ctx, accounts, tenant, append(named, extra)); !errors.Is(err, ErrTooManySubjects) {
		t.Errorf("a write naming %d accounts = %v, want the bound's refusal", MaxFencedSubjects+1, err)
	}
}

// TestContentAliasesFindEveryFormAWriterResolves: untyped content names an
// account by a canonical id in any literal (User, Principal, user:), and by an
// email address in any case.
func TestContentAliasesFindEveryFormAWriterResolves(t *testing.T) {
	user, cred := model.NewID(), model.NewID()
	content := `permit(principal == User::"` + user.String() + `", action, resource);` +
		` permit(principal == Principal::"` + cred.String() + `", action, resource);` +
		` // owner user:` + user.String() + ` Contact: Someone.Else@Example.COM`
	got := ContentAliases(content)
	ids := map[model.ID]bool{}
	for _, id := range got.IDs {
		ids[id] = true
	}
	if !ids[user] || !ids[cred] || len(got.IDs) != 2 {
		t.Errorf("content aliases ids = %v, want the account and the credential once each", got.IDs)
	}
	if len(got.Emails) != 1 || got.Emails[0] != "someone.else@example.com" {
		t.Errorf("content aliases emails = %v, want the normalized address", got.Emails)
	}
}

// TestADelegationHandleFailsOnceItsSessionIsExcluded: a delegation handle minted
// from an account-scope session stops revalidating in a tenant as soon as a
// session revocation there excludes that session, keeps revalidating in another
// tenant that did not, and carries a tenant's retirement floor into the
// principal it builds.
func TestADelegationHandleFailsOnceItsSessionIsExcluded(t *testing.T) {
	forEachConsentEngine(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		a := NewAuthenticator(st, nil)
		tT := consentTenant(t, st, "deleg-t")
		tB := consentTenant(t, st, "deleg-b")
		actor := consentSuperadmin(t, ctx, a)
		v := consentMember(t, ctx, st, "v@deleg.example", tT, tB)
		p, err := a.Authenticate(ctx, sessionFor(t, ctx, st, v, ""))
		if err != nil {
			t.Fatalf("authenticate the account-scope session: %v", err)
		}
		handle := model.DelegationHandle{
			SubjectUserID: v, SourceCredKind: "user", SourceCredID: p.CredID, MintRole: RoleViewer,
		}
		revalidate := func(tenant model.TenantID) (*int64, error) {
			var floor *int64
			err := st.AuthView(ctx, func(as store.AuthScope) error {
				var err error
				_, _, floor, err = a.revalidateSubject(ctx, as, handle, tenant)
				return err
			})
			return floor, err
		}
		for _, tenant := range []model.TenantID{tT, tB} {
			if _, err := revalidate(tenant); err != nil {
				t.Fatalf("the handle before any exclusion: %v", err)
			}
		}

		if err := a.dispatchCAEPAction(ctx, actor, tT, CAEPSetConfig{}, CAEPEventEnvelope{
			Action: CAEPSessionRevoke, SubjectUserID: p.CredID.String(),
		}, v); err != nil {
			t.Fatalf("the tenant's session revocation: %v", err)
		}
		if _, err := revalidate(tT); !errors.Is(err, ErrDelegationInvalid) {
			t.Errorf("the handle in the tenant that excluded its session = %v, want ErrDelegationInvalid", err)
		}
		if _, err := revalidate(tB); err != nil {
			t.Errorf("the handle in a tenant that excluded nothing = %v, want it still valid", err)
		}

		// A tenant that retired the account and admitted it again carries its
		// floor through the handle.
		var rec model.TenantExclusion
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			var err error
			rec, err = a.scopedOffboard(ctx, as, actor, v, tB, "test")
			return err
		}); err != nil {
			t.Fatalf("offboard: %v", err)
		}
		const epoch = int64(7)
		if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
			x, err := as.TenantExclusions().Get(ctx, rec.ID)
			if err != nil {
				return err
			}
			retired := epoch
			x.RetirementState, x.RetiredEpoch = model.RetirementLifted, &retired
			if _, err := as.TenantExclusions().Update(ctx, x); err != nil {
				return err
			}
			_, err = as.Memberships().Create(ctx, model.Membership{UserID: v, TargetTenantID: tB, Role: RoleViewer})
			return err
		}); err != nil {
			t.Fatalf("lift and re-admit: %v", err)
		}
		floor, err := revalidate(tB)
		if err != nil || floor == nil || *floor != epoch {
			t.Fatalf("the re-admitted handle = floor %v, %v; want floor %d", floor, err, epoch)
		}
		got, ok := PrincipalForDelegation(VerifiedDelegation{
			tenant: tB, subjectUserID: v, subjectCredKind: "user", subjectCredID: p.CredID,
			effectiveRole: RoleViewer, floor: floor,
		}).RetirementFloor(tB)
		if !ok || got != epoch {
			t.Errorf("the delegated principal's floor = %d (%t), want %d", got, ok, epoch)
		}
	})
}
