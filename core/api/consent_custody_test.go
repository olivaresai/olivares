// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These cases pin credential custody and session scope: the engine records who
// created an account, a sign-in under one tenant's authority acts only there, and
// a session confined to a tenant cannot enroll a credential for the whole account.

// scopedSession writes a session confined to tenant for user, as a sign-in
// through that tenant's identity provider mints one, and returns its token.
func (h *harness) scopedSession(user model.ID, tenant model.TenantID) string {
	h.t.Helper()
	ctx := context.Background()
	cred, err := auth.NewCredential(auth.PrefixScopedSession)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Create(ctx, model.AuthSession{
			UserID: user, Selector: cred.Selector, SecretHash: cred.SecretHash,
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
			AAL:       auth.AAL1, AMR: []string{"sso"}, TenantScope: tenant,
		})
		return err
	}); err != nil {
		h.t.Fatalf("write scoped session: %v", err)
	}
	return cred.Token
}

// passkeyCount counts the account's registered authenticators.
func (h *harness) passkeyCount(user model.ID) int {
	h.t.Helper()
	ctx := context.Background()
	var n int
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		cs, _, err := as.WebAuthnCredentials().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: user.String()},
		}, Limit: 100})
		n = len(cs)
		return err
	}); err != nil {
		h.t.Fatalf("read passkeys: %v", err)
	}
	return n
}

// sessionScopeOf reads the tenant scope recorded on the session behind token.
func (h *harness) sessionScopeOf(token string) model.TenantID {
	h.t.Helper()
	ctx := context.Background()
	_, selector, _, ok := auth.ParseToken(token)
	if !ok {
		h.t.Fatalf("malformed session token")
	}
	var scope model.TenantID
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		ss, _, err := as.Sessions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "selector", Op: model.OpEq, Value: selector},
		}, Limit: 1})
		if len(ss) == 1 {
			scope = ss[0].TenantScope
		}
		return err
	}); err != nil {
		h.t.Fatalf("read session: %v", err)
	}
	return scope
}

// TestATenantCustodyLoginCarriesOnlyItsTenant: an account a tenant created signs
// in with a session scoped to that tenant, which carries no authority in any
// other tenant the account belongs to.
func TestATenantCustodyLoginCarriesOnlyItsTenant(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		admin := h.elevatedAdmin()
		tT := h.createOrg(admin, "custody-t")
		tB := h.createOrg(admin, "custody-b")
		v := h.onboardPassword(admin, tT, "v@custody.example", auth.RoleViewer, consentPassword)
		h.seedMembership(v, tB, auth.RoleViewer)

		sess := h.login("v@custody.example", consentPassword)
		if code := h.actsIn(sess, tT); code != http.StatusOK {
			t.Errorf("the custodian tenant's session acting in that tenant = %d, want 200", code)
		}
		if code := h.actsIn(sess, tB); code != http.StatusForbidden {
			t.Errorf("a tenant-custody session acting in another tenant = %d, want 403", code)
		}
		if !strings.HasPrefix(sess, auth.PrefixScopedSession+"_") {
			t.Errorf("a tenant-custody session token lacks the scoped prefix")
		}
		if scope := h.sessionScopeOf(sess); scope != tT {
			t.Errorf("the session's recorded scope = %q, want %q", scope, tT)
		}

		// Control: an account the deployment created keeps account scope.
		_, deploymentSess := h.sharedAccount(admin, "d@custody.example", tT, tB)
		if code := h.actsIn(deploymentSess, tB); code != http.StatusOK {
			t.Errorf("an account-scope session acting in its second tenant = %d, want 200", code)
		}
	})
}

// TestAScopedSessionCannotEnrollAnAccountWidePasskey: a session whose scope is
// not its account's custody scope cannot register an authenticator.
func TestAScopedSessionCannotEnrollAnAccountWidePasskey(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tT := h.createOrg(admin, "passkey-t")
		v, accountSess := h.sharedAccount(admin, "passkey@scope.example", tT)
		scoped := h.scopedSession(v, tT)
		if code := h.actsIn(scoped, tT); code != http.StatusOK {
			t.Fatalf("the tenant-scoped session acting in its tenant = %d, want 200", code)
		}

		if opts := h.do("POST", "/v1/auth/webauthn/register/options", scoped, nil, nil); opts.code != http.StatusForbidden {
			t.Errorf("registration options for a scoped session = %d %s, want 403", opts.code, opts.raw)
		}
		aopts := h.do("POST", "/v1/auth/webauthn/register/options", accountSess, nil, nil)
		if aopts.code != http.StatusOK {
			t.Fatalf("registration options for the account-scope session = %d %s", aopts.code, aopts.raw)
		}
		soft := newSoftAuthenticator(t)
		r := h.do("POST", "/v1/auth/webauthn/register", scoped,
			map[string]any{"credential": soft.register(t, aopts, flagUP|flagUV|flagAT, testOrigin)}, nil)
		if r.code == http.StatusOK {
			t.Errorf("a tenant-scoped session completed a passkey registration")
		}
		if n := h.passkeyCount(v); n != 0 {
			t.Errorf("the account holds %d passkeys after the scoped attempts, want 0", n)
		}

		// Control: a session whose scope equals the account's custody scope enrolls.
		registerOK(t, h, accountSess, newSoftAuthenticator(t))
		if n := h.passkeyCount(v); n != 1 {
			t.Errorf("the account holds %d passkeys after the account-scope enrollment, want 1", n)
		}
	})
}

// TestCreationStampsCustodyOnce: every creation path records who established the
// account, and no later tenant or account write changes it.
func TestCreationStampsCustodyOnce(t *testing.T) {
	onEachEngineWithMailer(t, func(t *testing.T, h *harness, _ *capturingInviteSender) {
		ctx := context.Background()
		admin := h.elevatedAdmin()
		super := h.principalOf(admin)
		tT := h.createOrg(admin, "stamp-t")

		want := func(t *testing.T, id model.ID, custody model.CredentialCustody, tenant model.TenantID) {
			t.Helper()
			u := h.userByID(id)
			if u.CredentialCustody != custody || u.CustodyTenantID != tenant {
				t.Errorf("%s custody = %q/%q, want %q/%q", u.Email, u.CredentialCustody, u.CustodyTenantID, custody, tenant)
			}
		}

		root, _ := h.userByEmail("root@x.io")
		want(t, root.ID, model.CustodyDeployment, "")

		onboarded := h.onboardPassword(admin, tT, "onboarded@stamp.example", auth.RoleViewer, consentPassword)
		want(t, onboarded, model.CustodyTenant, tT)

		if r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "invited@stamp.example", "role": auth.RoleViewer, "mode": "invite",
		}, tenantHdr(tT)); r.code != http.StatusCreated {
			t.Fatalf("invite = %d %s", r.code, r.raw)
		}
		invited, _ := h.userByEmail("invited@stamp.example")
		want(t, invited.ID, model.CustodyTenant, tT)

		provisioned, _, err := h.authr.SCIMProvisionUser(ctx, super, tT, auth.SCIMUserInput{UserName: "scim@stamp.example", Active: true})
		if err != nil {
			t.Fatal(err)
		}
		want(t, provisioned.ID, model.CustodyTenant, tT)

		deployment := h.createDeploymentUser(admin, "deployment@stamp.example", consentPassword)
		want(t, deployment, model.CustodyDeployment, "")

		// Later writes leave custody as it was stamped.
		scimTok := h.scimToken(super, tT)
		if r := h.scim("PATCH", "/v1/scim/v2/Users/"+provisioned.ID.String(), scimTok,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"Renamed"}]}`); r.code != http.StatusOK {
			t.Fatalf("SCIM attribute change = %d %s", r.code, r.raw)
		}
		want(t, provisioned.ID, model.CustodyTenant, tT)
		if err := h.authr.SetPassword(ctx, super, deployment, "yetanotherpass1"); err != nil {
			t.Fatal(err)
		}
		want(t, deployment, model.CustodyDeployment, "")
	})
}
