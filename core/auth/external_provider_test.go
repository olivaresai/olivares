// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestExternalProviderStageKeepsOpaqueMetadata(t *testing.T) {
	svc, st := u8Svc(t)
	var in auth.FederationConfigInput
	if err := json.Unmarshal([]byte(`{"Protocol":"external","ExternalConnectorRef":"connector-revision-owner","ExternalConnectorGeneration":7,"ExternalIssuer":"urn:provider:immutable-owner","ClaimedDomains":["corp.example"]}`), &in); err != nil {
		t.Fatal(err)
	}
	tenant := model.NewTenantID()
	view, err := svc.PutConfigIdP(context.Background(), fedTestActor(), tenant, "corp", in)
	if err != nil {
		t.Fatalf("stage opaque provider: %v", err)
	}
	if view.Status != string(model.StatusInactive) || view.ProviderAvailable {
		t.Fatalf("staged provider must remain inactive and unavailable without an installed builder: %+v", view)
	}
	if err := st.AuthView(context.Background(), func(as store.AuthScope) error {
		configs, _, err := as.FederationConfigs().List(context.Background(), byTenantAlias(tenant, "corp"))
		if err != nil {
			return err
		}
		if len(configs) != 1 {
			t.Fatalf("stored configs = %d", len(configs))
		}
		encoded, err := json.Marshal(configs[0])
		if err != nil {
			return err
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return err
		}
		if fields["ExternalConnectorRef"] != "connector-revision-owner" || fields["ExternalConnectorGeneration"] != float64(7) || fields["ExternalIssuer"] != "urn:provider:immutable-owner" {
			t.Fatalf("opaque revision metadata did not round-trip: %s", encoded)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExternalProviderReadinessUsesOnlyInstalledBuilderWithoutEffects(t *testing.T) {
	f := newExternalFixture(t)
	without := auth.NewFederationService(f.st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{})
	if err := without.TestConfigIdP(context.Background(), f.tenant, "corp", externalInput(false)); !errors.Is(err, auth.ErrExternalProviderUnavailable) {
		t.Fatalf("browser builder substituted for missing external readiness: %v", err)
	}
	if err := f.service.TestConfigIdP(context.Background(), f.tenant, "corp", externalInput(false)); err != nil {
		t.Fatalf("installed external readiness: %v", err)
	}
	if f.builder.builds != 1 || f.builder.verifies != 0 {
		t.Fatalf("readiness performed credential verification or bypassed actual revision: builds %d, verifies %d", f.builder.builds, f.builder.verifies)
	}
	if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
		configs, _, err := as.FederationConfigs().List(context.Background(), byTenantAlias(f.tenant, "corp"))
		if err == nil && len(configs) != 0 {
			t.Fatal("readiness persisted a provider slot")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if users, sessions, memberships := externalAccountCounts(t, f); users != 1 || sessions != 0 || memberships != 0 {
		t.Fatalf("readiness persisted account effects: %d/%d/%d", users, sessions, memberships)
	}
}

func byTenantAlias(tenant model.TenantID, alias string) model.Query {
	return model.Query{Filters: []model.Filter{{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()}, {Column: "alias", Op: model.OpEq, Value: alias}}, Limit: 2}
}

type externalTestBuilder struct {
	revision                  int64
	identity                  auth.FederatedIdentity
	builds, verifies          int
	afterVerify               func()
	admissionErr              error
	verificationErr           error
	expiresAt                 time.Time
	zeroHorizon, ignoreExpiry bool
	extendedHorizon           time.Time
	horizonCalls              int
}

func (b *externalTestBuilder) Build(_ context.Context, slot auth.ExternalProviderSlot) (auth.ExternalProvider, error) {
	b.builds++
	if slot.ConnectorRef != "connector-revision-owner" || slot.ConnectorGeneration != b.revision || slot.Issuer != "urn:provider:immutable-owner" {
		return nil, auth.ErrExternalProviderUnavailable
	}
	until := b.expiresAt
	if until.IsZero() {
		until = time.Now().Add(time.Minute)
	}
	return &externalTestProvider{builder: b, identity: b.identity, expiresAt: until}, nil
}

type externalTestProvider struct {
	builder   *externalTestBuilder
	identity  auth.FederatedIdentity
	expiresAt time.Time
}

func (p *externalTestProvider) Verify(_ context.Context, credentials auth.ExternalLoginCredentials) (auth.FederatedIdentity, error) {
	p.builder.verifies++
	if credentials.Username != "account" || string(credentials.Password) != "verified-password" {
		return auth.FederatedIdentity{}, auth.ErrInvalidCredentials
	}
	if p.builder.afterVerify != nil {
		p.builder.afterVerify()
	}
	if p.builder.verificationErr != nil {
		return auth.FederatedIdentity{}, p.builder.verificationErr
	}
	return p.identity, nil
}

func (p *externalTestProvider) ProofHorizon() time.Time {
	p.builder.horizonCalls++
	if p.builder.zeroHorizon {
		return time.Time{}
	}
	if !p.builder.extendedHorizon.IsZero() {
		return p.builder.extendedHorizon
	}
	return p.expiresAt
}

func (p *externalTestProvider) ValidateAdmission(context.Context) error {
	if p.builder.admissionErr != nil {
		return p.builder.admissionErr
	}
	if !p.builder.ignoreExpiry && !time.Now().Before(p.expiresAt) {
		return auth.ErrUnauthenticated
	}
	return nil
}

type externalFixture struct {
	st      store.Store
	a       *auth.Authenticator
	service *auth.FederationService
	tenant  model.TenantID
	super   auth.Principal
	builder *externalTestBuilder
}

func newExternalFixture(t *testing.T) *externalFixture {
	t.Helper()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, context.Background(), a)
	tenant := provisionTenant(t, st, "external-provider")
	builder := &externalTestBuilder{revision: 7, identity: auth.FederatedIdentity{Issuer: "urn:provider:immutable-owner", Subject: "immutable-object-42", Email: "account@corp.example", DisplayName: "Account"}}
	service := auth.NewFederationService(st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{}).WithExternalProviderBuilder(builder)
	return &externalFixture{st: st, a: a, service: service, tenant: tenant, super: super, builder: builder}
}

func externalInput(enabled bool) auth.FederationConfigInput {
	return auth.FederationConfigInput{Protocol: auth.ProtocolExternal, Enabled: enabled,
		ExternalConnectorRef: "connector-revision-owner", ExternalConnectorGeneration: 7,
		ExternalIssuer: "urn:provider:immutable-owner", ClaimedDomains: []string{"corp.example"}}
}

func (f *externalFixture) activate(t *testing.T) {
	t.Helper()
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", externalInput(true)); err != nil {
		t.Fatal(err)
	}
}

func (f *externalFixture) prepare() (model.User, *auth.ExternalLoginAdmission, error) {
	return f.a.PrepareExternalLogin(context.Background(), f.service, f.tenant, "corp",
		auth.ExternalLoginCredentials{Username: "account", Password: []byte("verified-password")}, "10.0.0.4")
}

func externalAccountCounts(t *testing.T, f *externalFixture) (users, sessions, memberships int) {
	t.Helper()
	if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
		us, _, err := as.Users().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		users = len(us)
		ss, _, err := as.Sessions().List(context.Background(), model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, session := range ss {
			if session.UserID != f.super.UserID {
				sessions++
			}
		}
		ms, _, err := as.Memberships().List(context.Background(), model.Query{Limit: 100})
		memberships = len(ms)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestExternalProviderPreparesStrictAccountWithoutSessionAndPreservesSubject(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	callerSecret := []byte("verified-password")
	user, admission, err := f.a.PrepareExternalLogin(context.Background(), f.service, f.tenant, "corp", auth.ExternalLoginCredentials{Username: "account", Password: callerSecret}, "10.0.0.4")
	if err != nil {
		t.Fatal(err)
	}
	if string(callerSecret) != "verified-password" {
		t.Fatal("verification modified the caller's secret")
	}
	if admission == nil || admission.SessionScope() != f.tenant || admission.ProofHorizon().IsZero() || user.ID.IsZero() {
		t.Fatal("preparation did not retain exact account/tenant/original producer horizon")
	}
	if users, sessions, memberships := externalAccountCounts(t, f); users != 2 || sessions != 0 || memberships != 1 {
		t.Fatalf("preparation effects: accounts %d, sessions %d, memberships %d", users, sessions, memberships)
	}
	originalUser := user.ID
	f.builder.identity.Email = "renamed@corp.example"
	renamed, _, err := f.prepare()
	if err != nil || renamed.ID != originalUser {
		t.Fatalf("immutable subject rename = %s / %v", renamed.ID, err)
	}
	if users, sessions, _ := externalAccountCounts(t, f); users != 2 || sessions != 0 {
		t.Fatalf("subject rename forked an account or minted a credential: %d/%d", users, sessions)
	}
}

func TestExternalProviderVerifiedSubjectRefusesAnotherEmailOwner(t *testing.T) {
	for _, owner := range []string{"unbound-account", "other-provider"} {
		t.Run(owner, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			original, _, err := f.prepare()
			if err != nil {
				t.Fatal(err)
			}
			other, err := f.a.CreateUser(context.Background(), f.super, auth.NewUser{Email: "other@corp.example", Password: "password-123"})
			if err != nil {
				t.Fatal(err)
			}
			if owner == "other-provider" {
				if _, session, err := f.a.CompleteSSO(context.Background(), auth.FederatedIdentity{Issuer: "urn:provider:another-owner", Subject: "another-object", Email: other.Email}, "10.0.0.5", "", false); err != nil || session.UserID != other.ID {
					t.Fatalf("real other-provider binding = %s / %v", session.UserID, err)
				}
			}
			beforeUsers, beforeSessions, beforeMembers := externalAccountCounts(t, f)
			f.builder.identity.Email = other.Email
			if user, session, err := f.prepare(); !errors.Is(err, auth.ErrUnauthenticated) || !user.ID.IsZero() || session != nil {
				t.Fatalf("subject %s claimed another account's email: account-present %t / %v", original.ID, !user.ID.IsZero(), err)
			}
			users, sessions, members := externalAccountCounts(t, f)
			if users != beforeUsers || sessions != beforeSessions || members != beforeMembers {
				t.Fatalf("email collision persisted effects: users %d/%d, sessions %d/%d, memberships %d/%d", users, beforeUsers, sessions, beforeSessions, members, beforeMembers)
			}
		})
	}
}

func TestExternalProviderUnavailableAndImmutableReadiness(t *testing.T) {
	f := newExternalFixture(t)
	f.service.WithExternalProviderBuilder(nil)
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", externalInput(true)); !errors.Is(err, auth.ErrExternalProviderUnavailable) {
		t.Fatalf("absent activation = %v", err)
	}
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", externalInput(false)); err != nil {
		t.Fatal(err)
	}
	f.service.WithExternalProviderBuilder(f.builder)
	wrong := externalInput(true)
	wrong.ExternalConnectorGeneration++
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", wrong); !errors.Is(err, auth.ErrExternalProviderUnavailable) {
		t.Fatalf("wrong immutable revision = %v", err)
	}
	view, err := f.service.GetConfigIdP(context.Background(), f.tenant, "corp")
	if err != nil || view.Status != string(model.StatusInactive) || view.ExternalConnectorGeneration != 7 {
		t.Fatalf("failed activation changed slot: %+v / %v", view, err)
	}
}

func TestExternalProviderNetworkPolicyPrecedesVerifier(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	f.builder.builds = 0
	policy := &fakeLoginPolicy{networkErr: auth.ErrNetworkNotAllowed}
	f.a.WithLoginPolicy(policy)
	if _, _, err := f.prepare(); !errors.Is(err, auth.ErrNetworkNotAllowed) {
		t.Fatalf("network refusal = %v", err)
	}
	if f.builder.builds != 0 || f.builder.verifies != 0 || policy.sawNetwork != 1 {
		t.Fatal("network-denied request reached provider")
	}
	if users, sessions, memberships := externalAccountCounts(t, f); users != 1 || sessions != 0 || memberships != 0 {
		t.Fatalf("refused effects = %d/%d/%d", users, sessions, memberships)
	}
}

func TestExternalProviderNativeAttemptLimitPrecedesVerifier(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	for range 5 {
		if user, _, err := f.a.PrepareExternalLogin(context.Background(), f.service, f.tenant, "corp", auth.ExternalLoginCredentials{Username: "account", Password: []byte("incorrect")}, "10.0.0.4"); !errors.Is(err, auth.ErrInvalidCredentials) || !user.ID.IsZero() {
			t.Fatalf("incorrect credentials = account-present %t / %v", !user.ID.IsZero(), err)
		}
	}
	builds, verifies := f.builder.builds, f.builder.verifies
	if user, _, err := f.prepare(); !errors.Is(err, auth.ErrLockedOut) || !user.ID.IsZero() {
		t.Fatalf("native locked attempt = account-present %t / %v", !user.ID.IsZero(), err)
	}
	if f.builder.builds != builds || f.builder.verifies != verifies || verifies != 5 {
		t.Fatalf("native refused attempt reached provider: builds %d/%d, verifies %d/%d", f.builder.builds, builds, f.builder.verifies, verifies)
	}
	if users, sessions, members := externalAccountCounts(t, f); users != 1 || sessions != 0 || members != 0 {
		t.Fatalf("invalid or refused credential persisted account effects: %d/%d/%d", users, sessions, members)
	}
}

func TestExternalProviderTypedVerificationRefusalDoesNotPrepare(t *testing.T) {
	f := newExternalFixture(t)
	f.activate(t)
	producerRefusal := errors.New("installed producer admission withdrawn")
	f.builder.verificationErr = producerRefusal
	if user, session, err := f.prepare(); !errors.Is(err, producerRefusal) || !user.ID.IsZero() || session != nil {
		t.Fatalf("typed producer refusal = account-present %t / %v", !user.ID.IsZero(), err)
	}
	if users, sessions, members := externalAccountCounts(t, f); users != 1 || sessions != 0 || members != 0 {
		t.Fatalf("typed refusal persisted account effects: %d/%d/%d", users, sessions, members)
	}
}

func TestExternalProviderSlotChangeDuringVerificationRefusesWithoutEffects(t *testing.T) {
	for _, change := range []string{"generation", "disable", "delete", "domains"} {
		t.Run(change, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			f.builder.afterVerify = func() {
				in := externalInput(false)
				switch change {
				case "generation":
					in.ExternalConnectorGeneration = 8
				case "domains":
					in.ClaimedDomains = []string{"other.example"}
				case "delete":
					if err := f.service.DeleteConfigIdP(context.Background(), f.super, f.tenant, "corp"); err != nil {
						t.Fatal(err)
					}
					return
				}
				if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
					t.Fatal(err)
				}
			}
			if user, _, err := f.prepare(); err == nil || !user.ID.IsZero() {
				t.Fatalf("changed slot admitted: account-present?%v err=%v", !user.ID.IsZero(), err)
			}
			if users, sessions, memberships := externalAccountCounts(t, f); users != 1 || sessions != 0 || memberships != 0 {
				t.Fatalf("changed slot effects = %d/%d/%d", users, sessions, memberships)
			}
		})
	}
}

func TestExternalProviderStrictIssuerSubjectAndEmailConfinement(t *testing.T) {
	for _, bad := range []string{"issuer", "subject", "domain", "existing-other-binding", "existing-other-tenant", "superadmin", "disabled"} {
		t.Run(bad, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			switch bad {
			case "issuer":
				f.builder.identity.Issuer = "urn:provider:another-owner"
			case "subject":
				f.builder.identity.Subject = " ambiguous "
			case "domain":
				f.builder.identity.Email = "account@outside.example"
			case "existing-other-binding":
				mustMember(t, context.Background(), f.a, f.super, f.tenant, f.builder.identity.Email, auth.RoleViewer)
				if _, _, err := f.a.CompleteSSO(context.Background(), auth.FederatedIdentity{Issuer: "urn:other:owner", Subject: "other-object", Email: f.builder.identity.Email}, "10.0.0.3", "", false); err != nil {
					t.Fatal(err)
				}
			case "existing-other-tenant":
				other := provisionTenant(t, f.st, "other-tenant")
				mustMember(t, context.Background(), f.a, f.super, other, f.builder.identity.Email, auth.RoleViewer)
			case "superadmin":
				f.builder.identity.Email = "root@corp.example"
				if _, err := f.a.CreateUser(context.Background(), f.super, auth.NewUser{Email: f.builder.identity.Email, Password: "strong-password-123", Superadmin: true}); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				uid, _ := mustMember(t, context.Background(), f.a, f.super, f.tenant, f.builder.identity.Email, auth.RoleViewer)
				if err := f.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
					if err := as.(store.AuthUserAuthorityWriter).PrepareUserAuthorityWrite(context.Background(), []model.ID{uid}); err != nil {
						return err
					}
					u, err := as.Users().Get(context.Background(), uid)
					if err != nil {
						return err
					}
					u.Status = model.StatusInactive
					_, err = as.Users().Update(context.Background(), u)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			beforeU, beforeS, beforeM := externalAccountCounts(t, f)
			if user, _, err := f.prepare(); err == nil || !user.ID.IsZero() {
				t.Fatalf("outside identity admitted: account-present?%v err=%v", !user.ID.IsZero(), err)
			}
			if users, sessions, memberships := externalAccountCounts(t, f); users != beforeU || sessions != beforeS || memberships != beforeM {
				t.Fatalf("refusal mutated account authority = %d/%d/%d", users, sessions, memberships)
			}
		})
	}
}

func TestExternalProviderRefusesUnboundLocalEmailWithoutAdoption(t *testing.T) {
	for _, allowJIT := range []bool{false, true} {
		t.Run(map[bool]string{false: "jit-disabled", true: "jit-enabled"}[allowJIT], func(t *testing.T) {
			f := newExternalFixture(t)
			in := externalInput(true)
			in.SCIMAuthoritative = !allowJIT
			if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
				t.Fatal(err)
			}
			uid, _ := mustMember(t, context.Background(), f.a, f.super, f.tenant, f.builder.identity.Email, auth.RoleViewer)
			beforeUsers, beforeSessions, beforeMembers := externalAccountCounts(t, f)
			var before model.User
			if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
				var err error
				before, err = as.Users().Get(context.Background(), uid)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if user, session, err := f.prepare(); !errors.Is(err, auth.ErrUnauthenticated) || !user.ID.IsZero() || session != nil {
				t.Fatalf("unbound local email adopted: account-present %t / %v", !user.ID.IsZero(), err)
			}
			users, sessions, members := externalAccountCounts(t, f)
			if users != beforeUsers || sessions != beforeSessions || members != beforeMembers {
				t.Fatalf("email adoption refusal persisted effects: users %d/%d, sessions %d/%d, memberships %d/%d", users, beforeUsers, sessions, beforeSessions, members, beforeMembers)
			}
			if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
				u, err := as.Users().Get(context.Background(), uid)
				if err == nil && (u.SsoSubject != "" || u.Version != before.Version || u.PasswordHash != before.PasswordHash) {
					t.Fatal("refused external assertion changed local account or credential")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalProviderJITDisabledAdmitsOnlyExactExistingSubject(t *testing.T) {
	f := newExternalFixture(t)
	in := externalInput(true)
	in.SCIMAuthoritative = true
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
		t.Fatal(err)
	}
	if user, _, err := f.prepare(); !errors.Is(err, auth.ErrUnauthenticated) || !user.ID.IsZero() {
		t.Fatalf("no-JIT policy created an account: %v", err)
	}
	f.activate(t)
	original, _, err := f.prepare()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, f.tenant, "corp", in); err != nil {
		t.Fatal(err)
	}
	existing, _, err := f.prepare()
	if err != nil || existing.ID != original.ID {
		t.Fatalf("exact existing subject with JIT disabled = %s / %v", existing.ID, err)
	}
}

func TestExternalProviderAdmissionExpiryAndRefusalRollBackJIT(t *testing.T) {
	for _, failure := range []string{"expired", "withdrawn"} {
		t.Run(failure, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			if failure == "expired" {
				f.builder.expiresAt = time.Now().Add(15 * time.Millisecond)
				f.builder.afterVerify = func() { time.Sleep(20 * time.Millisecond) }
			} else {
				f.builder.afterVerify = func() { f.builder.admissionErr = auth.ErrUnauthenticated }
			}
			if user, _, err := f.prepare(); err == nil || !user.ID.IsZero() {
				t.Fatalf("unavailable retained authority admitted: %v", err)
			}
			if users, sessions, memberships := externalAccountCounts(t, f); users != 1 || sessions != 0 || memberships != 0 {
				t.Fatalf("unavailable proof effects = %d/%d/%d", users, sessions, memberships)
			}
		})
	}
}

func TestExternalProviderSharesNativeCapsAndDomainOwnership(t *testing.T) {
	f := newExternalFixture(t)
	f.service = auth.NewFederationService(f.st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, nil).WithExternalProviderBuilder(f.builder)
	f.activate(t)
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, model.NewTenantID(), "default", oidcDomains("other-provider", true, "other.example")); err == nil {
		t.Fatal("native second-provider cap ignored external slot")
	}
	f.service = auth.NewFederationService(f.st, fedTestSealer{}, fedTestBuilder, auth.NoFederation{}, fedTestMultiIDP{}).WithExternalProviderBuilder(f.builder)
	if _, err := f.service.PutConfigIdP(context.Background(), f.super, model.NewTenantID(), "default", oidcDomains("other-provider", false, "corp.example")); !errors.Is(err, auth.ErrDomainClaimed) {
		t.Fatalf("native domain ownership ignored external slot: %v", err)
	}
	if fed, err := f.service.Resolve(context.Background(), f.tenant); err != nil || fed.Protocol() != "" {
		t.Fatalf("external slot invented browser federation: %v / %v", fed, err)
	}
}

func TestExternalProviderRejectsMalformedAndMixedConfiguration(t *testing.T) {
	f := newExternalFixture(t)
	for _, mutation := range []string{"zero-generation", "bad-reference", "bad-issuer", "no-domains", "browser-settings", "wrong-kind", "global"} {
		t.Run(mutation, func(t *testing.T) {
			in, tenant := externalInput(false), f.tenant
			switch mutation {
			case "zero-generation":
				in.ExternalConnectorGeneration = 0
			case "bad-reference":
				in.ExternalConnectorRef = " connection with whitespace "
			case "bad-issuer":
				in.ExternalIssuer = "urn:owner\x1finjected"
			case "no-domains":
				in.ClaimedDomains = nil
			case "browser-settings":
				in.OIDCIssuer = "https://browser.example"
			case "wrong-kind":
				in.Protocol = auth.ProtocolOIDC
			case "global":
				tenant = auth.GlobalFederationScope
			}
			if _, err := f.service.PutConfigIdP(context.Background(), f.super, tenant, "corp", in); !errors.Is(err, auth.ErrBadFederationConfig) {
				t.Fatalf("invalid metadata = %v", err)
			}
		})
	}
}

type externalIssuanceStore struct {
	store.Store
	beforeLoginAudit func() error
	loginAudits      int
	loginAuditAction string
}

func (s *externalIssuanceStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		return fn(externalIssuanceScope{AuthScope: as, owner: s})
	})
}

type externalIssuanceScope struct {
	store.AuthScope
	owner *externalIssuanceStore
}

func (s externalIssuanceScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.AuthScope.(store.TransactionClock).TransactionNow(ctx)
}

func (s externalIssuanceScope) PrepareUserAuthorityWrite(ctx context.Context, ids []model.ID) error {
	writer, ok := s.AuthScope.(store.AuthUserAuthorityWriter)
	if !ok {
		return errors.New("native User authority writer unavailable")
	}
	return writer.PrepareUserAuthorityWrite(ctx, ids)
}

func (s externalIssuanceScope) Audit() store.AuditLog {
	return externalIssuanceAudit{AuditLog: s.AuthScope.Audit(), owner: s.owner}
}

type externalIssuanceAudit struct {
	store.AuditLog
	owner *externalIssuanceStore
}

func (a externalIssuanceAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	action := a.owner.loginAuditAction
	if action == "" {
		action = "sso.user.join"
	}
	if draft.Action == action {
		a.owner.loginAudits++
		if err := a.owner.beforeLoginAudit(); err != nil {
			return model.AuditEvent{}, err
		}
	}
	return a.AuditLog.Append(ctx, draft)
}

func TestExternalProviderFinalAdmissionRollsBackNativePreparation(t *testing.T) {
	for _, name := range []string{"expired-after-provisioning", "audit-unavailable"} {
		t.Run(name, func(t *testing.T) {
			f := newExternalFixture(t)
			f.activate(t)
			beforeUsers, beforeSessions, beforeMembers := externalAccountCounts(t, f)
			var beforeHead store.HeadRef
			if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
				var err error
				beforeHead, _, err = as.Audit().Head(context.Background())
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// The installed producer supplies one immutable expiry before Build.
			// Wait at the native join audit after JIT membership creation, to
			// prove the final retained-proof check rolls preparation back.
			f.builder.expiresAt = time.Now().Add(200 * time.Millisecond)
			fault := errors.New("native login audit unavailable")
			wrapped := &externalIssuanceStore{Store: f.st, beforeLoginAudit: func() error {
				if name == "audit-unavailable" {
					return fault
				}
				time.Sleep(time.Until(f.builder.expiresAt) + time.Millisecond)
				return nil
			}}
			f.a = auth.NewAuthenticator(wrapped, nil)
			user, admission, err := f.prepare()
			want := error(auth.ErrUnauthenticated)
			if name == "audit-unavailable" {
				want = fault
			}
			if !errors.Is(err, want) || !user.ID.IsZero() || admission != nil {
				t.Fatalf("native preparation refusal = account-present %t, admission-present %t, error %v", !user.ID.IsZero(), admission != nil, err)
			}
			if wrapped.loginAudits != 1 {
				t.Fatalf("native issuance reached login audit %d times, want once", wrapped.loginAudits)
			}
			users, sessions, members := externalAccountCounts(t, f)
			if users != beforeUsers || sessions != beforeSessions || members != beforeMembers {
				t.Fatalf("refused issuance persisted effects: users %d/%d, sessions %d/%d, memberships %d/%d", users, beforeUsers, sessions, beforeSessions, members, beforeMembers)
			}
			if err := f.st.AuthView(context.Background(), func(as store.AuthScope) error {
				head, _, err := as.Audit().Head(context.Background())
				if err == nil && (head.Seq != beforeHead.Seq || !bytes.Equal(head.Hash, beforeHead.Hash)) {
					t.Fatalf("refused issuance persisted provisional audit: %v -> %v", beforeHead, head)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
