// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	testPDPAudience      = "urn:olivares:pdp:inference"
	testOtherPDPAudience = "urn:olivares:pdp:other"
)

type pepFixture struct {
	ctx       context.Context
	st        store.Store
	a         *auth.Authenticator
	super     auth.Principal
	admin     auth.Principal
	editor    auth.Principal
	tenant    model.TenantID
	other     model.TenantID
	subject   string
	subjectID model.ID
}

func newPEPFixture(t *testing.T) pepFixture {
	return newPEPFixtureFromStore(t, testStore(t))
}

// newPEPFixtureFromStore is newPEPFixture over a caller-supplied store, so a test
// can provision the same fixture on a store with a non-default audit-spool policy
// (e.g. a tiny DEGRADE budget for evidence-drop tests).
func newPEPFixtureFromStore(t *testing.T, st store.Store) pepFixture {
	t.Helper()
	ctx := context.Background()
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "pep-acme")
	other := provisionTenant(t, st, "pep-other")

	admin := pepMemberPrincipal(t, ctx, a, super, tenant, "pep-admin@acme.test", auth.RoleAdmin)
	editor := pepMemberPrincipal(t, ctx, a, super, tenant, "pep-editor@acme.test", auth.RoleEditor)
	subject, stored, err := a.IssueToken(ctx, super, auth.TokenSpec{
		Name: "pep-provisioning-subject", BoundTenant: tenant, Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("issue PEP provisioning subject: %v", err)
	}
	return pepFixture{
		ctx: ctx, st: st, a: a, super: super, admin: admin, editor: editor,
		tenant: tenant, other: other, subject: subject, subjectID: stored.ID,
	}
}

func pepMemberPrincipal(
	t *testing.T,
	ctx context.Context,
	a *auth.Authenticator,
	super auth.Principal,
	tenant model.TenantID,
	email, role string,
) auth.Principal {
	t.Helper()
	u, err := a.CreateUser(ctx, super, auth.NewUser{
		Email: email, DisplayName: role, Password: "pep-member-password-1",
	})
	if err != nil {
		t.Fatalf("create %s member: %v", role, err)
	}
	if _, err := a.GrantMembership(ctx, super, u.ID, tenant, role, model.ID("")); err != nil {
		t.Fatalf("grant %s member: %v", role, err)
	}
	session, _, err := a.Login(ctx, email, "pep-member-password-1", "127.0.0.1")
	if err != nil {
		t.Fatalf("login %s member: %v", role, err)
	}
	p, err := a.Authenticate(ctx, session)
	if err != nil {
		t.Fatalf("authenticate %s member: %v", role, err)
	}
	return p
}

func (f pepFixture) register(
	t *testing.T,
	name string,
	audience string,
	caps map[string]bool,
) model.PEPService {
	t.Helper()
	service, err := f.a.RegisterPEPService(f.ctx, f.admin, auth.PEPServiceSpec{
		Tenant: f.tenant, Name: name, PDPAudience: audience, Capabilities: caps,
	})
	if err != nil {
		t.Fatalf("register PEP service %q: %v", name, err)
	}
	return service
}

func (f pepFixture) exchangedToken(
	t *testing.T,
	audience string,
) (string, model.APIToken) {
	t.Helper()
	caller, err := f.a.Authenticate(f.ctx, f.subject)
	if err != nil {
		t.Fatalf("authenticate token-exchange subject: %v", err)
	}
	req := accessReq(f.subject)
	req.Audiences = []string{audience}
	req.Name = "pep-transport"
	res, err := f.a.ExchangeToken(f.ctx, caller, req)
	if err != nil {
		t.Fatalf("exchange PEP transport token: %v", err)
	}
	return res.AccessToken, res.Stored
}

func (f pepFixture) bind(
	t *testing.T,
	service model.PEPService,
	audience string,
) (string, model.APIToken) {
	t.Helper()
	bearer, token := f.exchangedToken(t, audience)
	if err := f.a.BindPEPCredential(f.ctx, f.admin, service.ID, token.ID); err != nil {
		t.Fatalf("bind PEP credential: %v", err)
	}
	return bearer, token
}

func TestRegisterPEPServiceRequiresTenantAdminAndAudits(t *testing.T) {
	f := newPEPFixture(t)
	spec := auth.PEPServiceSpec{
		Tenant: f.tenant, Name: "litellm", PDPAudience: testPDPAudience,
		Capabilities: map[string]bool{"buffer_request": true, "streaming": true},
	}

	if _, err := f.a.RegisterPEPService(f.ctx, f.editor, spec); !errors.Is(err, auth.ErrRoleCeiling) {
		t.Fatalf("editor registration err = %v, want ErrRoleCeiling", err)
	}
	service, err := f.a.RegisterPEPService(f.ctx, f.admin, spec)
	if err != nil {
		t.Fatalf("admin registration: %v", err)
	}
	if service.CapabilityVersion != 1 {
		t.Errorf("CapabilityVersion = %d, want 1", service.CapabilityVersion)
	}
	if service.TargetTenantID != f.tenant {
		t.Errorf("TargetTenantID = %q, want %q", service.TargetTenantID, f.tenant)
	}
	if !service.Capabilities["buffer_request"] || !service.Capabilities["streaming"] {
		t.Errorf("registered capabilities = %v", service.Capabilities)
	}

	otherTenantSpec := spec
	otherTenantSpec.Tenant = f.other
	if _, err := f.a.RegisterPEPService(f.ctx, f.super, otherTenantSpec); err != nil {
		t.Fatalf("same service name in another tenant: %v", err)
	}
	if _, err := f.a.RegisterPEPService(f.ctx, f.admin, spec); !errors.Is(err, auth.ErrPEPServiceExists) {
		t.Fatalf("duplicate registration err = %v, want ErrPEPServiceExists", err)
	}
	assertPEPAuditActions(t, f, "pep_service.register")
}

func TestBindPEPCredentialRestrictsPurposeAndTenant(t *testing.T) {
	f := newPEPFixture(t)
	serviceA := f.register(t, "pep-a", testPDPAudience, nil)
	serviceB := f.register(t, "pep-b", testPDPAudience, nil)

	bearer, token := f.exchangedToken(t, testPDPAudience)
	if _, err := f.a.Authenticate(f.ctx, bearer); err != nil {
		t.Fatalf("ordinary token must authenticate before binding: %v", err)
	}
	if err := f.a.BindPEPCredential(f.ctx, f.admin, serviceA.ID, token.ID); err != nil {
		t.Fatalf("bind existing ordinary token: %v", err)
	}
	if _, err := f.a.Authenticate(f.ctx, bearer); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("ordinary auth after PEP bind err = %v, want ErrUnauthenticated", err)
	}
	if err := f.a.BindPEPCredential(f.ctx, f.admin, serviceB.ID, token.ID); !errors.Is(err, auth.ErrPEPCredentialBound) {
		t.Fatalf("second service bind err = %v, want ErrPEPCredentialBound", err)
	}

	foreignBearer, foreignToken, err := f.a.IssueToken(f.ctx, f.super, auth.TokenSpec{
		Name: "foreign", BoundTenant: f.other, Role: auth.RoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = foreignBearer
	if err := f.a.BindPEPCredential(f.ctx, f.admin, serviceA.ID, foreignToken.ID); !errors.Is(err, auth.ErrPEPCredentialTenant) {
		t.Fatalf("cross-tenant bind err = %v, want ErrPEPCredentialTenant", err)
	}

	_, superToken, err := f.a.IssueToken(f.ctx, f.super, auth.TokenSpec{Name: "system", Superadmin: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.BindPEPCredential(f.ctx, f.admin, serviceA.ID, superToken.ID); !errors.Is(err, auth.ErrInvalidPEPCredential) {
		t.Fatalf("superadmin bind err = %v, want ErrInvalidPEPCredential", err)
	}

	_, reserved := f.exchangedToken(t, testPDPAudience)
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		got, err := as.Tokens().Get(f.ctx, reserved.ID)
		if err != nil {
			return err
		}
		got.Purpose = "other"
		_, err = as.Tokens().Update(f.ctx, got)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.BindPEPCredential(f.ctx, f.admin, serviceA.ID, reserved.ID); !errors.Is(err, auth.ErrInvalidPEPCredential) {
		t.Fatalf("already-purpose-restricted bind err = %v, want ErrInvalidPEPCredential", err)
	}
	assertPEPAuditActions(t, f, "pep_service.bind_credential")
}

func TestAuthenticatePEPRejectsInvalidCredentialStates(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(*testing.T, pepFixture, model.PEPService) string
	}{
		{
			name: "ordinary token",
			arrange: func(t *testing.T, f pepFixture, _ model.PEPService) string {
				bearer, _ := f.exchangedToken(t, testPDPAudience)
				return bearer
			},
		},
		{
			name: "revoked PEP token",
			arrange: func(t *testing.T, f pepFixture, service model.PEPService) string {
				bearer, token := f.bind(t, service, testPDPAudience)
				if err := f.a.RevokeToken(f.ctx, f.admin, token.ID); err != nil {
					t.Fatal(err)
				}
				return bearer
			},
		},
		{
			name: "expired PEP token",
			arrange: func(t *testing.T, f pepFixture, service model.PEPService) string {
				bearer, token := f.bind(t, service, testPDPAudience)
				expired := model.NewTimestamp(time.Now().Add(-time.Minute))
				if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
					got, err := as.Tokens().Get(f.ctx, token.ID)
					if err != nil {
						return err
					}
					got.ExpiresAt = &expired
					_, err = as.Tokens().Update(f.ctx, got)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				return bearer
			},
		},
		{
			name: "disabled mapping",
			arrange: func(t *testing.T, f pepFixture, service model.PEPService) string {
				bearer, token := f.bind(t, service, testPDPAudience)
				if err := f.a.UnbindPEPCredential(f.ctx, f.admin, service.ID, token.ID); err != nil {
					t.Fatal(err)
				}
				return bearer
			},
		},
		{
			name: "disabled service",
			arrange: func(t *testing.T, f pepFixture, service model.PEPService) string {
				bearer, _ := f.bind(t, service, testPDPAudience)
				if err := f.a.DisablePEPService(f.ctx, f.admin, service.ID); err != nil {
					t.Fatal(err)
				}
				return bearer
			},
		},
		{
			name: "wrong PDP audience",
			arrange: func(t *testing.T, f pepFixture, service model.PEPService) string {
				bearer, _ := f.bind(t, service, testOtherPDPAudience)
				return bearer
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPEPFixture(t)
			service := f.register(t, "reject-"+tt.name, testPDPAudience, nil)
			bearer := tt.arrange(t, f, service)
			if _, err := f.a.AuthenticatePEP(f.ctx, bearer); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("AuthenticatePEP err = %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestAuthenticatePEPAcceptsAudienceBoundRotationAndUnbindsOld(t *testing.T) {
	f := newPEPFixture(t)
	service := f.register(t, "rotating-pep", testPDPAudience, map[string]bool{
		"buffer_request": true, "streaming": true,
	})
	oldBearer, oldToken := f.bind(t, service, testPDPAudience)
	newBearer, newToken := f.bind(t, service, testPDPAudience)

	for label, tc := range map[string]struct {
		bearer string
		token  model.APIToken
	}{
		"old": {oldBearer, oldToken},
		"new": {newBearer, newToken},
	} {
		t.Run(label, func(t *testing.T) {
			identity, err := f.a.AuthenticatePEP(f.ctx, tc.bearer)
			if err != nil {
				t.Fatalf("AuthenticatePEP: %v", err)
			}
			if identity.ServiceID() != service.ID || identity.Tenant() != f.tenant {
				t.Errorf("identity service/tenant = %q/%q", identity.ServiceID(), identity.Tenant())
			}
			if identity.CredentialID() != tc.token.ID || identity.Name() != service.Name {
				t.Errorf("identity credential/name = %q/%q", identity.CredentialID(), identity.Name())
			}
			if identity.CapabilityVersion() != 1 || !identity.RegisteredCapabilities()["streaming"] {
				t.Errorf("identity capability snapshot = v%d %v",
					identity.CapabilityVersion(), identity.RegisteredCapabilities())
			}
			caps := identity.RegisteredCapabilities()
			caps["streaming"] = false
			if !identity.RegisteredCapabilities()["streaming"] {
				t.Error("RegisteredCapabilities exposed mutable identity state")
			}
		})
	}

	third := f.register(t, "third-pep", testPDPAudience, nil)
	thirdBearer, _ := f.bind(t, third, testPDPAudience)
	thirdIdentity, err := f.a.AuthenticatePEP(f.ctx, thirdBearer)
	if err != nil {
		t.Fatalf("authenticate third service: %v", err)
	}
	if thirdIdentity.ServiceID() == service.ID {
		t.Fatal("a third service credential masqueraded as the rotating service")
	}

	if err := f.a.UnbindPEPCredential(f.ctx, f.admin, service.ID, oldToken.ID); err != nil {
		t.Fatalf("unbind old credential: %v", err)
	}
	if _, err := f.a.AuthenticatePEP(f.ctx, oldBearer); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old credential after unbind err = %v, want ErrUnauthenticated", err)
	}
	if _, err := f.a.AuthenticatePEP(f.ctx, newBearer); err != nil {
		t.Fatalf("new overlapping credential after old unbind: %v", err)
	}
	if _, err := f.a.Authenticate(f.ctx, oldBearer); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("unbound PEP credential ordinary auth err = %v, want ErrUnauthenticated", err)
	}
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		got, err := as.Tokens().Get(f.ctx, oldToken.ID)
		if err != nil {
			return err
		}
		if got.Purpose != "pep" {
			t.Errorf("unbound token Purpose = %q, want pep", got.Purpose)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPEPAuditActions(t, f, "pep_service.unbind_credential")
}

func TestPEPServiceCapabilityUpdateAndDisableAreAudited(t *testing.T) {
	f := newPEPFixture(t)
	service := f.register(t, "managed-pep", testPDPAudience, map[string]bool{
		"buffer_request": true,
	})
	caps := map[string]bool{"buffer_response": true, "batch": true}
	if err := f.a.UpdatePEPServiceCapabilities(f.ctx, f.admin, service.ID, caps); err != nil {
		t.Fatalf("update capabilities: %v", err)
	}
	caps["batch"] = false

	var updated model.PEPService
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		var err error
		updated, err = as.PEPServices().Get(f.ctx, service.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if updated.CapabilityVersion != 2 {
		t.Errorf("CapabilityVersion = %d, want 2", updated.CapabilityVersion)
	}
	if !updated.Capabilities["batch"] || updated.Capabilities["buffer_request"] {
		t.Errorf("replaced capabilities = %v", updated.Capabilities)
	}

	if err := f.a.DisablePEPService(f.ctx, f.admin, service.ID); err != nil {
		t.Fatalf("disable service: %v", err)
	}
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		got, err := as.PEPServices().Get(f.ctx, service.ID)
		if err != nil {
			return err
		}
		if got.DisabledAt == nil {
			t.Error("DisabledAt is nil")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPEPAuditActions(
		t,
		f,
		"pep_service.update_capabilities",
		"pep_service.disable",
	)
}

// TestAuthenticatePEPRejectsSuperadminCredential pins NOTE1: even if a superadmin
// token somehow acquired Purpose="pep", a tenant binding, an active mapping, and
// the PDP audience, it must NOT authenticate as a PEP (defense-in-depth: a PEP
// governs one business tenant, never the system role).
func TestAuthenticatePEPRejectsSuperadminCredential(t *testing.T) {
	f := newPEPFixture(t)
	service := f.register(t, "super-guard", testPDPAudience, nil)

	cred, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	// Build the disqualifying state DIRECTLY: a superadmin token carrying (against
	// the model's normal invariants) a tenant binding, the pep purpose, the PDP
	// audience, and an active mapping — everything AuthenticatePEP checks EXCEPT the
	// superadmin flag itself.
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		tok, err := as.Tokens().Create(f.ctx, model.APIToken{
			Name:          "rogue-super",
			Selector:      cred.Selector,
			SecretHash:    cred.SecretHash,
			BoundTenantID: f.tenant,
			Role:          auth.RoleAdmin,
			IsSuperadmin:  true,
			Purpose:       "pep",
			Audience:      testPDPAudience,
		})
		if err != nil {
			return err
		}
		_, err = as.PEPServiceCredentials().Create(f.ctx, model.PEPServiceCredential{
			ServiceID: service.ID, TokenID: tok.ID,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.a.AuthenticatePEP(f.ctx, cred.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("superadmin PEP credential auth err = %v, want ErrUnauthenticated", err)
	}
}

// TestBindPEPCredentialConcurrentDoubleBindTyped pins L3: a concurrent double-bind
// of the same token yields exactly one winner; the loser receives the typed
// ErrPEPCredentialBound (a 409-equivalent), never a raw store conflict.
func TestBindPEPCredentialConcurrentDoubleBindTyped(t *testing.T) {
	f := newPEPFixture(t)
	service := f.register(t, "concurrent-bind", testPDPAudience, nil)
	_, token := f.exchangedToken(t, testPDPAudience)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			errs <- f.a.BindPEPCredential(f.ctx, f.admin, service.ID, token.ID)
		}()
	}
	ready.Wait()
	close(start)

	e1, e2 := <-errs, <-errs
	winners := 0
	if e1 == nil {
		winners++
	}
	if e2 == nil {
		winners++
	}
	if winners != 1 {
		t.Fatalf("bind winners = %d, want exactly 1 (errs: %v / %v)", winners, e1, e2)
	}
	loser := e1
	if e1 == nil {
		loser = e2
	}
	if !errors.Is(loser, auth.ErrPEPCredentialBound) {
		t.Fatalf("concurrent double-bind loser err = %v, want ErrPEPCredentialBound", loser)
	}
}

func assertPEPAuditActions(t *testing.T, f pepFixture, wants ...string) {
	t.Helper()
	var actions []string
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(f.ctx, 1, func(ev model.AuditEvent) error {
			actions = append(actions, ev.Action)
			return nil
		})
	}); err != nil {
		t.Fatalf("walk auth audit: %v", err)
	}
	for _, want := range wants {
		if !slices.Contains(actions, want) {
			t.Errorf("audit actions %v do not contain %q", actions, want)
		}
	}
}
