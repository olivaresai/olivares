// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	systemTestVia        = "engine:orchestration-cron"
	systemTestReason     = "due cron schedule"
	systemTestPermission = Permission("provider:write")
	systemOriginalURL    = "https://original.example.test"
)

// systemAuthorityFixture is the engine-system counterpart of
// newMutationAuthorityFixture: one real tenant, one seeded product row, and a
// system principal resolved through the native producer for provider:write.
type systemAuthorityFixture struct {
	*principalEvidenceFixture
	op     Principal
	p      Principal
	target model.ID
}

func newSystemAuthorityFixture(t *testing.T) *systemAuthorityFixture {
	return newSystemAuthorityFixtureConfig(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

func newSystemAuthorityFixtureConfig(t *testing.T, cfg store.Config) *systemAuthorityFixture {
	t.Helper()
	f := newPrincipalEvidenceFixtureConfig(t, cfg)
	op, err := NewSystemOperator(systemTestVia, systemTestReason)
	if err != nil {
		t.Fatal(err)
	}
	var target model.ID
	if err := f.raw.Mutate(f.ctx, f.tenant, func(sc store.Scope) error {
		row, err := sc.Providers().Create(f.ctx, model.Provider{
			Name: "system-target", Kind: "anthropic", BaseURL: systemOriginalURL, Status: model.StatusActive,
		})
		target = row.ID
		return err
	}); err != nil {
		t.Fatalf("seed the authorization target: %v", err)
	}
	// Resolve at an engine-observed instant: the fixture's default is an
	// application nanosecond that can lie after the millisecond database time
	// every later authorization and barrier reads.
	f.hooks.now = model.NewTimestamp(engineInstant(t, f))
	p, err := f.a.ResolveSystemScope(f.deadline(time.Minute), op, f.tenant, systemTestPermission)
	if err != nil {
		t.Fatalf("ResolveSystemScope: %v", err)
	}
	return &systemAuthorityFixture{principalEvidenceFixture: f, op: op, p: p, target: target}
}

// authorize issues the mutation authority for q at an engine-observed instant.
func (s *systemAuthorityFixture) authorize(q Request) (RouteMutationAuthorization, time.Time, error) {
	s.t.Helper()
	now := engineInstant(s.t, s.principalEvidenceFixture)
	az := NewAuthorizer(nil, WithScopedGrants(&principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(s.p.evidence.observedAt, s.p.evidence.freshUntil),
	}), WithClock(func() time.Time { return now }))
	ctx, cancel := context.WithDeadline(s.ctx, s.p.evidence.freshUntil)
	s.t.Cleanup(cancel)
	a, err := az.AuthorizeRouteMutation(ctx, q)
	return a, now, err
}

// protectedWrite runs the consumer's barrier on ONE transaction: AuthorityFor at
// database time, the complete bundle lock, the product write, and the recheck.
// recheckAt, when set, replaces the database time of the post-write recheck.
func (s *systemAuthorityFixture) protectedWrite(
	a RouteMutationAuthorization, q Request, url string, recheckAt time.Time,
) error {
	return s.raw.Mutate(s.ctx, s.tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return errors.New("the engine scope exposes no TransactionClock")
		}
		locker, ok := sc.(store.AuthoritySnapshotBundleLocker)
		if !ok {
			return errors.New("the engine scope exposes no complete bundle locker")
		}
		at, err := clock.TransactionNow(s.ctx)
		if err != nil {
			return err
		}
		bundle, err := a.AuthorityFor(at.Time(), q)
		if err != nil {
			return err
		}
		if err := locker.LockAuthoritySnapshotBundle(s.ctx, bundle); err != nil {
			return err
		}
		row, err := sc.Providers().Get(s.ctx, s.target)
		if err != nil {
			return err
		}
		row.BaseURL = url
		if _, err := sc.Providers().Update(s.ctx, row); err != nil {
			return err
		}
		if at, err = clock.TransactionNow(s.ctx); err != nil {
			return err
		}
		if !recheckAt.IsZero() {
			at = model.NewTimestamp(recheckAt)
		}
		_, err = a.AuthorityFor(at.Time(), q)
		return err
	})
}

func TestSystemScopeProducesNativeMutationAuthority(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
			if engine == store.EnginePostgres {
				if !pgtest.Available(t) {
					t.Skip("no Postgres configured: this engine's coverage is UNAVAILABLE, not passing")
				}
				dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SingleRole)
				cfg = store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}
			}
			assertSystemMutationJourney(t, newSystemAuthorityFixtureConfig(t, cfg))
		})
	}
}

func assertSystemMutationJourney(t *testing.T, s *systemAuthorityFixture) {
	t.Helper()
	p := s.p
	// System attribution survives: the engine, not a human, and no borrowed credential.
	if !p.IsSystemOperator() || p.ActorKind() != model.ActorSystem || p.Actor() != s.op.Actor() {
		t.Fatalf("attribution = %q/%q, want the system operator %q", p.ActorKind(), p.Actor(), s.op.Actor())
	}
	if meta := p.AuditMeta(); meta["actor_reason"] != systemTestReason || meta["actor_via"] != systemTestVia {
		t.Fatalf("audit provenance lost: %+v", meta)
	}
	if _, ok := p.Ref(); ok || !p.UserID.IsZero() || !p.CredID.IsZero() {
		t.Fatal("the system principal carries a credential or a User")
	}
	if _, ok := p.AuthenticationEvidence(); ok {
		t.Fatal("the system principal claims an authentication event")
	}
	// Bound to exactly this tenant and exactly its ceiling, never system-wide.
	role, ok := p.RoleIn(s.tenant)
	ceiling, restricted := p.PurposePermissionsIn(s.tenant)
	if !ok || role != systemAuthorityRole || IsRole(role) || p.Superadmin || p.SessionScope() != s.tenant ||
		len(p.Tenants()) != 1 || !restricted || !slices.Equal(ceiling, []Permission{systemTestPermission}) {
		t.Fatalf("authority = %v/%q ceiling=%v superadmin=%v scope=%s, want only %s in %s",
			ok, role, ceiling, p.Superadmin, p.SessionScope(), systemTestPermission, s.tenant)
	}
	if !validPrincipalAuthoritySeal(p) {
		t.Fatal("the system principal is not sealed")
	}
	// Finite: never beyond the caller's deadline.
	if !p.evidence.freshUntil.After(p.evidence.observedAt) || p.evidence.freshUntil.After(s.now.Add(time.Minute)) {
		t.Fatalf("window [%s, %s) is not finite and caller-bounded", p.evidence.observedAt, p.evidence.freshUntil)
	}

	q := providerWriteRequest(p, s.tenant, s.target)
	a, now, err := s.authorize(q)
	if err != nil {
		t.Fatalf("AuthorizeRouteMutation for the system principal: %v", err)
	}
	bundle, err := a.AuthorityFor(now, q)
	if err != nil {
		t.Fatalf("AuthorityFor: %v", err)
	}
	if len(bundle.UserAuthorities) != 0 || !slices.Contains(bundle.Facts, s.directoryEpoch(s.tenant)) {
		t.Fatalf("bundle = %+v, want the tenant directory fence and no User fence", bundle)
	}

	// The bundle is locked and rechecked on the product transaction, and the write lands.
	if err := s.protectedWrite(a, q, "https://system.example.test", time.Time{}); err != nil {
		t.Fatalf("protected system write: %v", err)
	}
	assertProviderURL(t, s.principalEvidenceFixture, s.target, "https://system.example.test")

	// A recheck after the window refuses AFTER the write and rolls it back.
	if err := s.protectedWrite(a, q, "https://late.example.test", p.evidence.freshUntil); !errors.Is(err, ErrRouteUndecided) {
		t.Fatalf("expired recheck = %v, want ErrRouteUndecided", err)
	}
	assertProviderURL(t, s.principalEvidenceFixture, s.target, "https://system.example.test")

	// Changed authority: a tenant directory write moves the fence and the barrier refuses.
	if err := s.raw.Mutate(s.ctx, s.tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(s.ctx, model.Identity{
			Name: "epoch-mover", Kind: "db_role", ExternalID: "epoch-mover", Provider: "test",
		})
		return err
	}); err != nil {
		t.Fatalf("advance the tenant directory fence: %v", err)
	}
	if err := s.protectedWrite(a, q, "https://stale.example.test", time.Time{}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale directory fence = %v, want store.ErrConflict at the barrier", err)
	}
	assertProviderURL(t, s.principalEvidenceFixture, s.target, "https://system.example.test")
}

func TestSystemScopeWindowIsCappedByTheEngine(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	op, err := NewSystemOperator(systemTestVia, systemTestReason)
	if err != nil {
		t.Fatal(err)
	}
	// A generous caller gets the engine ceiling, not its own deadline.
	p, err := f.a.ResolveSystemScope(f.deadline(time.Hour), op, f.tenant, systemTestPermission)
	if err != nil {
		t.Fatal(err)
	}
	if want := p.evidence.observedAt.Add(systemAuthorityLifetime); !p.evidence.freshUntil.Equal(want) {
		t.Fatalf("freshUntil = %s, want the engine ceiling %s", p.evidence.freshUntil, want)
	}
	// A live caller deadline that is already behind database time refuses.
	f.hooks.now = model.NewTimestamp(f.now.Add(10 * time.Minute))
	got, err := f.a.ResolveSystemScope(f.deadline(time.Minute), op, f.tenant, systemTestPermission)
	if !errors.Is(err, ErrPrincipalEvidenceUnavailable) || !reflect.DeepEqual(got, Principal{}) {
		t.Fatalf("deadline behind database time = %v, want ErrPrincipalEvidenceUnavailable", err)
	}
}

func TestSystemScopeRefusesWhatItCannotEstablish(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	op, err := NewSystemOperator(systemTestVia, systemTestReason)
	if err != nil {
		t.Fatal(err)
	}
	human, err := NewLocalOperator(LocalOperator{Subject: "ops", Via: "cli:test", Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.a.ResolvePrincipalScope(f.deadline(30*time.Minute), f.sessionRef(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := f.a.ResolveSystemScope(f.deadline(time.Minute), op, f.tenant, systemTestPermission)
	if err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithDeadline(f.ctx, f.now.Add(-time.Second))
	defer cancel()
	resolve := func(ctx context.Context, operator Principal, tenant model.TenantID, permissions ...Permission) func() (Principal, error) {
		return func() (Principal, error) { return f.a.ResolveSystemScope(ctx, operator, tenant, permissions...) }
	}
	live := f.deadline(time.Minute)

	for name, call := range map[string]func() (Principal, error){
		"human-local-operator": resolve(live, human, f.tenant, systemTestPermission),
		"human-session":        resolve(live, session, f.tenant, systemTestPermission),
		"zero-operator":        resolve(live, Principal{}, f.tenant, systemTestPermission),
		"already-resolved":     resolve(live, resolved, f.tenant, systemTestPermission),
		"system-tenant":        resolve(live, op, model.SystemTenantID, systemTestPermission),
		"unknown-tenant":       resolve(live, op, model.NewTenantID(), systemTestPermission),
		"no-deadline":          resolve(f.ctx, op, f.tenant, systemTestPermission),
		"expired-deadline":     resolve(expired, op, f.tenant, systemTestPermission),
		"no-ceiling":           resolve(live, op, f.tenant),
		"system-permission":    resolve(live, op, f.tenant, systemTestPermission, PermSystemAdmin),
		"duplicate-permission": resolve(live, op, f.tenant, systemTestPermission, systemTestPermission),
		"verbless-permission":  resolve(live, op, f.tenant, Permission("provider")),
		"padded-permission":    resolve(live, op, f.tenant, Permission(" provider:write")),
		"epoch-unavailable": func() (Principal, error) {
			f.hooks.epochErr = errors.New("epoch unavailable")
			defer func() { f.hooks.epochErr = nil }()
			return f.a.ResolveSystemScope(live, op, f.tenant, systemTestPermission)
		},
		"clock-unavailable": func() (Principal, error) {
			f.hooks.clockErr = errors.New("clock unavailable")
			defer func() { f.hooks.clockErr = nil }()
			return f.a.ResolveSystemScope(live, op, f.tenant, systemTestPermission)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := call()
			if !errors.Is(err, ErrPrincipalEvidenceUnavailable) || !reflect.DeepEqual(p, Principal{}) {
				t.Fatalf("err = %v principal=%+v, want ErrPrincipalEvidenceUnavailable and a zero principal", err, p.evidence)
			}
		})
	}
}

func TestSystemMutationAuthorityRefusesEverythingElse(t *testing.T) {
	s := newSystemAuthorityFixture(t)
	q := providerWriteRequest(s.p, s.tenant, s.target)
	a, now, err := s.authorize(q)
	if err != nil {
		t.Fatal(err)
	}
	other := s.provisionTenant("system-other", "system-other")
	human, err := s.a.ResolvePrincipalScope(s.deadline(30*time.Minute), s.sessionRef(), s.tenant)
	if err != nil {
		t.Fatal(err)
	}

	// Forged or unestablished provenance never issues, never matches the issued
	// value, and never writes. A structural forgery is refused by the closed
	// shape even when it is resealed.
	for name, c := range map[string]struct {
		mutate     func(*Principal)
		structural bool
	}{
		"unsealed-operator": {func(p *Principal) { *p = s.op }, true},
		"forged-seal":       {func(p *Principal) { p.evidence.seal[0] ^= 1 }, false},
		"forged-window":     {func(p *Principal) { p.evidence.freshUntil = p.evidence.freshUntil.Add(time.Hour) }, false},
		"forged-epoch":      {func(p *Principal) { p.evidence.directoryEpoch.Version++ }, false},
		"forged-tenant":     {func(p *Principal) { p.evidence.tenant = other }, true},
		"forged-grants": {func(p *Principal) {
			p.grants = map[model.TenantID]string{s.tenant: systemAuthorityRole, other: systemAuthorityRole}
		}, true},
		"forged-owner-role": {func(p *Principal) { p.grants = map[model.TenantID]string{s.tenant: RoleOwner} }, true},
		"widened-ceiling": {func(p *Principal) {
			*p = p.withRestrictedPermissions(s.tenant, systemTestPermission, Permission("provider:delete"))
		}, false},
		"system-ceiling": {func(p *Principal) {
			*p = p.withRestrictedPermissions(s.tenant, systemTestPermission, PermSystemAdmin)
		}, true},
		"dropped-ceiling":   {func(p *Principal) { p.restricted = nil }, true},
		"forged-superadmin": {func(p *Principal) { p.Superadmin = true }, true},
		"forged-scope":      {func(p *Principal) { p.sessionScope = "" }, true},
		"forged-meta": {func(p *Principal) {
			p.localMeta = map[string]any{"actor_via": "cli:other", "actor_subject": p.localSubject}
		}, true},
		"human-mode":    {func(p *Principal) { p.evidence.authorityMode = principalHumanAuthority }, true},
		"token-mode":    {func(p *Principal) { p.evidence.authorityMode = principalTokenDirectoryOnly }, true},
		"borrowed-user": {func(p *Principal) { p.UserID = s.user.ID }, true},
		"borrowed-credential": {func(p *Principal) {
			p.Kind, p.CredID = KindToken, s.token.ID
			p.credentialRef = PrincipalRef{kind: KindToken, credentialID: s.token.ID, version: s.token.Version}
		}, true},
		"borrowed-user-fence": {func(p *Principal) {
			p.evidence.userAuthority = store.UserAuthorityFactRef{UserID: s.user.ID, Version: 1}
		}, true},
		"not-system": {func(p *Principal) { p.localSystem = false }, true},
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneEvidencePrincipal(s.p)
			c.mutate(&bad)
			if validPrincipalAuthoritySeal(bad) {
				t.Fatal("the mutated system principal still has a valid seal")
			}
			if _, err := computePrincipalAuthoritySeal(bad); c.structural && err == nil {
				t.Fatal("the closed system shape accepted a structural forgery for resealing")
			}
			badQ := providerWriteRequest(bad, s.tenant, s.target)
			got, _, err := s.authorize(badQ)
			if got.issued || !(errors.Is(err, ErrRouteUndecided) || errors.Is(err, ErrRouteDenied)) {
				t.Fatalf("err = %v (issued=%v), want a refusal and no issuance", err, got.issued)
			}
			if err := s.protectedWrite(a, badQ, "https://forged.example.test", time.Time{}); !errors.Is(err, ErrRouteUndecided) {
				t.Fatalf("the issued authority admitted a forged principal's write: %v", err)
			}
			assertProviderURL(t, s.principalEvidenceFixture, s.target, systemOriginalURL)
		})
	}

	// Outside the ceiling, the system permission, and another tenant never issue.
	for name, mutate := range map[string]func(*Request){
		"outside-ceiling":   func(r *Request) { r.Permission = Permission("provider:delete") },
		"system-permission": func(r *Request) { r.Permission, r.Resource = PermSystemAdmin, ResourceFor(PermSystemAdmin) },
		"other-tenant":      func(r *Request) { r.Tenant = other },
	} {
		t.Run("issue/"+name, func(t *testing.T) {
			bad := q
			mutate(&bad)
			if got, _, err := s.authorize(bad); err == nil || got.issued {
				t.Fatalf("issued for %s", name)
			}
		})
	}

	// Another tenant, question, principal or instant refuses the issued value, and
	// no write happens through it.
	otherVia, err := NewSystemOperator("engine:other", systemTestReason)
	if err != nil {
		t.Fatal(err)
	}
	otherSystem, err := s.a.ResolveSystemScope(s.deadline(time.Minute), otherVia, s.tenant, systemTestPermission)
	if err != nil {
		t.Fatal(err)
	}
	s.hooks.now = model.NewTimestamp(s.hooks.now.Time().Add(time.Millisecond))
	reconstructed, err := s.a.ResolveSystemScope(s.deadline(time.Minute), s.op, s.tenant, systemTestPermission)
	if err != nil {
		t.Fatal(err)
	}
	if reconstructed.evidence.seal == s.p.evidence.seal {
		t.Fatal("two reconstructions share one seal")
	}
	otherTenant := q
	otherTenant.Tenant = other
	otherPermission := q
	otherPermission.Permission = Permission("provider:delete")
	for name, wrong := range map[string]Request{
		"other-tenant":         otherTenant,
		"other-resource":       providerWriteRequest(s.p, s.tenant, model.NewID()),
		"other-permission":     otherPermission,
		"other-reconstruction": providerWriteRequest(reconstructed, s.tenant, s.target),
		"other-system-path":    providerWriteRequest(otherSystem, s.tenant, s.target),
		"human-principal":      providerWriteRequest(human, s.tenant, s.target),
	} {
		t.Run(name, func(t *testing.T) {
			if bundle, err := a.AuthorityFor(now, wrong); !errors.Is(err, ErrRouteUndecided) ||
				len(bundle.Facts) != 0 || len(bundle.UserAuthorities) != 0 {
				t.Fatalf("AuthorityFor = %+v, %v; want an empty bundle and ErrRouteUndecided", bundle, err)
			}
			if err := s.protectedWrite(a, wrong, "https://wrong.example.test", time.Time{}); err == nil {
				t.Fatal("the issued authority admitted another question's write")
			}
			assertProviderURL(t, s.principalEvidenceFixture, s.target, systemOriginalURL)
		})
	}
	for name, at := range map[string]time.Time{
		"expired":            s.p.evidence.freshUntil,
		"before-observation": s.p.evidence.observedAt.Add(-time.Nanosecond),
	} {
		t.Run(name, func(t *testing.T) {
			if bundle, err := a.AuthorityFor(at, q); !errors.Is(err, ErrRouteUndecided) || len(bundle.Facts) != 0 {
				t.Fatalf("AuthorityFor = %+v, %v; want an empty bundle and ErrRouteUndecided", bundle, err)
			}
		})
	}
}

// The system principal is sealed for mutations only: a read decision would say
// Allowed with nothing a page barrier could verify, so none is issued.
func TestSystemPrincipalHasNoReadDecision(t *testing.T) {
	s := newSystemAuthorityFixture(t)
	az := NewAuthorizer(nil, WithScopedGrants(&principalAuthorityScopedProducer{
		decision: principalAuthorityCleanScoped(s.p.evidence.observedAt, s.p.evidence.freshUntil),
	}), WithClock(func() time.Time { return s.p.evidence.observedAt }))
	read := providerWriteRequest(s.p, s.tenant, s.target)
	if d, err := az.DecideRouteRead(s.ctx, read); !errors.Is(err, ErrRouteUndecided) || d.Allowed() {
		t.Fatalf("DecideRouteRead = %v (allowed=%v), want ErrRouteUndecided", err, d.Allowed())
	}
}

// The engine-system seal preimage has its own domain and a fixed order. A change
// to either must be a new version, never a silent one.
func TestSystemAuthoritySealGolden(t *testing.T) {
	tenant := model.TenantID("01890a5d-ac96-774b-bcce-b302099a8057")
	observed := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
	p := Principal{
		Kind:         KindUser,
		DisplayName:  systemTestVia,
		grants:       map[model.TenantID]string{tenant: systemAuthorityRole},
		sessionScope: tenant,
		localVia:     systemTestVia,
		localSubject: "host-a",
		localMeta:    map[string]any{"actor_via": systemTestVia, "actor_subject": "host-a"},
		localSystem:  true,
		evidence: principalEvidenceProvenance{
			tenant:         tenant,
			directoryEpoch: store.AuthorizationFactRef{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: 7},
			authorityMode:  principalEngineSystemAuthority,
			observedAt:     observed,
			freshUntil:     observed.Add(systemAuthorityLifetime),
		},
	}.withRestrictedPermissions(tenant, systemTestPermission)
	seal, err := computePrincipalAuthoritySeal(p)
	if err != nil {
		t.Fatal(err)
	}
	const want = "87784b47b007fac15ceb72bd63e69551505d42c3ea4bd49d941da7f9049e93f2"
	if got := hex.EncodeToString(seal[:]); got != want {
		t.Fatalf("engine-system seal = %s, want %s", got, want)
	}
}
