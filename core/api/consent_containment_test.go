// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These cases pin the containment rule: a tenant never joins an existing account
// on its own, never receives a redemption token, never writes an account's
// global status or cuts it anywhere but in itself, and never creates or renames
// an address into a domain another organization's identity provider claims.

const consentPassword = "longpassword1"

// sharedAccount creates an account the deployment owns and makes it a member of
// every tenant given, through the store. It returns the account and a password
// session of it.
func (h *harness) sharedAccount(admin, email string, tenants ...model.TenantID) (model.ID, string) {
	h.t.Helper()
	v := h.createDeploymentUser(admin, email, consentPassword)
	for _, tenant := range tenants {
		h.seedMembership(v, tenant, auth.RoleViewer)
	}
	return v, h.login(email, consentPassword)
}

// TestAnInvitationTokenIsNeverReturnedToTheInviter: the invitation's
// redemption token reaches the invitee's mailbox and no API response.
func TestAnInvitationTokenIsNeverReturnedToTheInviter(t *testing.T) {
	onEachEngineWithMailer(t, func(t *testing.T, h *harness, mail *capturingInviteSender) {
		admin := h.elevatedAdmin()
		tenant := h.createOrg(admin, "walk-c")

		r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "invitee@walk-c.example", "role": auth.RoleViewer, "mode": "invite",
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("invite = %d %s, want 201", r.code, r.raw)
		}
		sent := mail.all()
		if len(sent) != 1 || sent[0].email != "invitee@walk-c.example" || sent[0].token() == "" {
			t.Fatalf("mailed invitations = %+v, want one to the invitee carrying a token", sent)
		}
		token := sent[0].token()
		if strings.Contains(r.raw, token) {
			t.Errorf("the onboarding response carries the redemption token: %s", r.raw)
		}
		inv, _ := r.body["invite"].(map[string]any)
		if _, has := inv["token"]; has {
			t.Errorf("the onboarding response has an invite token field: %s", r.raw)
		}
		if _, has := inv["accept_url"]; has {
			t.Errorf("the onboarding response has an accept link: %s", r.raw)
		}
		if inv["id"] == nil || inv["expires_at"] == nil {
			t.Errorf("the onboarding response lost the invitation's id or expiry: %s", r.raw)
		}

		list := h.do("GET", "/v1/invites", admin, nil, tenantHdr(tenant))
		if list.code != http.StatusOK {
			t.Fatalf("list invites = %d %s", list.code, list.raw)
		}
		if strings.Contains(list.raw, token) {
			t.Errorf("the invitation list carries the redemption token: %s", list.raw)
		}

		// Positive: the mailed token redeems.
		acc := h.do("POST", "/v1/invites/accept", "", map[string]any{"token": token, "password": consentPassword}, nil)
		if acc.code != http.StatusOK {
			t.Fatalf("accept the mailed token = %d %s, want 200", acc.code, acc.raw)
		}
		h.login("invitee@walk-c.example", consentPassword)
	})

	// Without an invitation mailer, invite mode refuses and writes nothing.
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		admin := h.elevatedAdmin()
		tenant := h.createOrg(admin, "walk-c-nomail")
		r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "nomail@walk-c.example", "role": auth.RoleViewer, "mode": "invite",
		}, tenantHdr(tenant))
		if r.code != http.StatusConflict || errCode(r.body) != "invite_delivery_unavailable" {
			t.Errorf("invite without a mailer = %d %s, want 409 invite_delivery_unavailable", r.code, r.raw)
		}
		if _, found := h.userByEmail("nomail@walk-c.example"); found {
			t.Errorf("a refused invitation created the account")
		}
	})
}

// TestOnboardAndGrantNeverJoinAnExistingAccount: an existing account that is not
// a member answers 202 consent_required to onboarding, to invitation and to a
// membership grant, whoever asks, and nothing joins.
func TestOnboardAndGrantNeverJoinAnExistingAccount(t *testing.T) {
	onEachEngineWithMailer(t, func(t *testing.T, h *harness, mail *capturingInviteSender) {
		admin := h.elevatedAdmin()
		tA := h.createOrg(admin, "join-a")
		tB := h.createOrg(admin, "join-b")
		v := h.onboardPassword(admin, tA, "v@join.example", auth.RoleViewer, consentPassword)

		var bodies []string
		for name, body := range map[string]map[string]any{
			"onboard password": {"email": "V@Join.Example", "role": auth.RoleEditor, "mode": "password", "password": "anotherpass1"},
			"onboard invite":   {"email": "v@join.example", "role": auth.RoleEditor, "mode": "invite"},
		} {
			r := h.do("POST", "/v1/onboard", admin, body, tenantHdr(tB))
			if r.code != http.StatusAccepted || r.body["status"] != "consent_required" {
				t.Errorf("%s of an existing account = %d %s, want 202 consent_required", name, r.code, r.raw)
			}
			bodies = append(bodies, r.raw)
		}
		r := h.do("POST", "/v1/memberships", admin, map[string]any{
			"user_id": v.String(), "tenant": tB.String(), "role": auth.RoleViewer,
		}, nil)
		if r.code != http.StatusAccepted || r.body["status"] != "consent_required" {
			t.Errorf("grant of an existing account = %d %s, want 202 consent_required", r.code, r.raw)
		}
		bodies = append(bodies, r.raw)
		for _, b := range bodies[1:] {
			if b != bodies[0] {
				t.Errorf("the consent answers differ: %q vs %q", bodies[0], b)
			}
		}
		if h.memberOf(v, tB) {
			t.Fatalf("the existing account joined the other tenant")
		}
		if len(mail.all()) != 0 {
			t.Errorf("an invitation was mailed for an existing account: %+v", mail.all())
		}
		// The account's credentials are untouched: its own password still signs in.
		h.login("v@join.example", consentPassword)
		if lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "v@join.example", "password": "anotherpass1"}, nil); lr.code == http.StatusOK {
			t.Errorf("the refused onboarding reset the account's password")
		}
		// A member keeps the unchanged re-grant path.
		if rg := h.do("POST", "/v1/memberships", admin, map[string]any{
			"user_id": v.String(), "tenant": tA.String(), "role": auth.RoleEditor,
		}, nil); rg.code != http.StatusCreated {
			t.Errorf("re-grant of a member = %d %s, want 201", rg.code, rg.raw)
		}
	})
}

// TestCreateUserWithATenantGrantsItInOneTransaction: the deployment's user route
// creates the account and its first membership together, or neither.
func TestCreateUserWithATenantGrantsItInOneTransaction(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		tenant := h.createOrg(admin, "create-with-tenant")

		r := h.do("POST", "/v1/users", admin, map[string]any{
			"email": "made@create.example", "password": consentPassword,
			"tenant": tenant.String(), "role": auth.RoleEditor,
		}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create with a tenant = %d %s, want 201", r.code, r.raw)
		}
		id := model.ID(r.body["id"].(string))
		if !h.memberOf(id, tenant) {
			t.Fatalf("the create did not grant the membership it named")
		}
		m, _ := r.body["membership"].(map[string]any)
		if m["tenant"] != tenant.String() || m["role"] != auth.RoleEditor {
			t.Errorf("the create response's membership = %v, want %s/%s", m, tenant, auth.RoleEditor)
		}
		sess := h.login("made@create.example", consentPassword)
		if code := h.actsIn(sess, tenant); code != http.StatusOK {
			t.Errorf("the created member acting in its tenant = %d, want 200", code)
		}

		// A membership that cannot be granted leaves no account behind.
		bad := h.do("POST", "/v1/users", admin, map[string]any{
			"email": "unmade@create.example", "password": consentPassword,
			"tenant": tenant.String(), "role": "emperor",
		}, nil)
		if bad.code != http.StatusBadRequest {
			t.Errorf("create with an invalid role = %d %s, want 400", bad.code, bad.raw)
		}
		if _, found := h.userByEmail("unmade@create.example"); found {
			t.Errorf("a refused membership left its account behind")
		}
	})
}

// TestAcceptInviteWritesOnlyTheAccountItCreated: an invitation redeems only
// while its account is exactly what the invitation created, and redeeming it
// never writes the account's status.
func TestAcceptInviteWritesOnlyTheAccountItCreated(t *testing.T) {
	onEachEngineWithMailer(t, func(t *testing.T, h *harness, mail *capturingInviteSender) {
		admin := h.elevatedAdmin()
		tA := h.createOrg(admin, "accept-a")
		tB := h.createOrg(admin, "accept-b")
		invite := func(email string) string {
			r := h.do("POST", "/v1/onboard", admin, map[string]any{
				"email": email, "role": auth.RoleViewer, "mode": "invite",
			}, tenantHdr(tA))
			if r.code != http.StatusCreated {
				t.Fatalf("invite %s = %d %s", email, r.code, r.raw)
			}
			sent := mail.all()
			return sent[len(sent)-1].token()
		}
		accept := func(token string) resp {
			return h.do("POST", "/v1/invites/accept", "", map[string]any{"token": token, "password": consentPassword}, nil)
		}

		// The account gained another tenant's membership before acceptance.
		tok := invite("joined-elsewhere@accept.example")
		u, _ := h.userByEmail("joined-elsewhere@accept.example")
		h.seedMembership(u.ID, tB, auth.RoleViewer)
		if r := accept(tok); r.code != http.StatusBadRequest || errCode(r.body) != "invite_invalid" {
			t.Errorf("accept for an account that joined another tenant = %d %s, want 400 invite_invalid", r.code, r.raw)
		}
		if got := h.userByID(u.ID); got.PasswordHash != "" {
			t.Errorf("the refused acceptance set the account's password")
		}

		// The account was disabled before acceptance: the acceptance refuses and
		// never writes the status.
		tok = invite("disabled@accept.example")
		u, _ = h.userByEmail("disabled@accept.example")
		h.mutateUser(u.ID, func(x *model.User) { x.Status = model.StatusInactive })
		if r := accept(tok); r.code != http.StatusBadRequest || errCode(r.body) != "invite_invalid" {
			t.Errorf("accept for a disabled account = %d %s, want 400 invite_invalid", r.code, r.raw)
		}
		if got := h.userByID(u.ID); got.Status != model.StatusInactive {
			t.Errorf("the acceptance wrote the account's status: %q", got.Status)
		}

		// The account was made a superadmin before acceptance.
		tok = invite("elevated@accept.example")
		u, _ = h.userByEmail("elevated@accept.example")
		h.mutateUser(u.ID, func(x *model.User) { x.IsSuperadmin = true })
		if r := accept(tok); r.code != http.StatusBadRequest || errCode(r.body) != "invite_invalid" {
			t.Errorf("accept for a superadmin = %d %s, want 400 invite_invalid", r.code, r.raw)
		}

		// Positive: an untouched invitation redeems.
		tok = invite("pristine@accept.example")
		if r := accept(tok); r.code != http.StatusOK {
			t.Errorf("accept of an untouched invitation = %d %s, want 200", r.code, r.raw)
		}
	})
}

// TestTenantCreateOrRenameIntoAnotherTenantsClaimedDomainIsRefused: before any
// write, a tenant's create, invitation or rename refuses an address in a domain
// another organization's identity provider claims.
func TestTenantCreateOrRenameIntoAnotherTenantsClaimedDomainIsRefused(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := h.elevatedAdmin()
		tA := h.createOrg(admin, "domain-a")
		tB := h.createOrg(admin, "domain-b")
		if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if _, err := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{
				TargetTenantID: tB, ConfigID: model.NewID(), Domain: "claimed.example",
			}); err != nil {
				return err
			}
			_, err := as.FederationDomainClaims().Create(ctx, model.FederationDomainClaim{
				TargetTenantID: model.SystemTenantID, ConfigID: model.NewID(), Domain: "deployment.example",
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}

		r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "p@claimed.example", "role": auth.RoleViewer, "mode": "password", "password": consentPassword,
		}, tenantHdr(tA))
		if r.code != http.StatusConflict || errCode(r.body) != "domain_claimed_elsewhere" {
			t.Errorf("onboard into another tenant's claimed domain = %d %s, want 409 domain_claimed_elsewhere", r.code, r.raw)
		}
		if _, found := h.userByEmail("p@claimed.example"); found {
			t.Errorf("the refused onboarding created the account")
		}

		// The claiming tenant itself, and a domain only the deployment's provider
		// claims, are not refused.
		if r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "b@claimed.example", "role": auth.RoleViewer, "mode": "password", "password": consentPassword,
		}, tenantHdr(tB)); r.code != http.StatusCreated {
			t.Errorf("onboard into the tenant's own claimed domain = %d %s, want 201", r.code, r.raw)
		}
		if r := h.do("POST", "/v1/onboard", admin, map[string]any{
			"email": "d@deployment.example", "role": auth.RoleViewer, "mode": "password", "password": consentPassword,
		}, tenantHdr(tA)); r.code != http.StatusCreated {
			t.Errorf("onboard into a domain only the deployment's provider claims = %d %s, want 201", r.code, r.raw)
		}
	})
}
