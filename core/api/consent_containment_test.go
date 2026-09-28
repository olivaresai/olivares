// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/caep"
	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// These cases pin the containment rule: a tenant never joins an existing account
// on its own, never receives a redemption token, never writes an account's
// global status or cuts it anywhere but in itself, and never creates or renames
// an address into a domain another organization's identity provider claims.

const consentPassword = "longpassword1"

// configureSCIMSet enables tenant's SCIM event receiver with a fresh publisher
// key and returns the key that signs its events.
func (h *harness) configureSCIMSet(super auth.Principal, tenant model.TenantID) *ecdsa.PrivateKey {
	h.t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		h.t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		h.t.Fatal(err)
	}
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if err := h.authr.ConfigureSCIMSet(context.Background(), super, tenant, auth.SCIMSetConfig{
		SETPublisher: auth.SETPublisher{
			Enabled: true, Issuer: "https://idp.consent.test", Audiences: []string{"https://cp.consent.test"},
			Keys: []auth.SETVerificationKey{{Kid: "k1", Alg: "ES256", PEM: pubPEM}},
		},
	}); err != nil {
		h.t.Fatal(err)
	}
	return priv
}

// scimEvent signs one SCIM lifecycle event for user.
func scimEvent(t *testing.T, priv *ecdsa.PrivateKey, jti string, user model.ID, event string) string {
	t.Helper()
	return signSETForTest(t, priv, map[string]any{
		"iss": "https://idp.consent.test", "aud": []string{"https://cp.consent.test"},
		"iat": time.Now().Unix(), "jti": jti,
		"sub_id": map[string]any{"format": "scim", "uri": "Users/" + user.String()},
		"events": map[string]any{event: map[string]any{}},
	})
}

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

// TestSETActivateAfterALocalOffboardRestoresNothing: a tenant's SCIM
// disable removes the person from that tenant only, and a later activate from
// the same tenant finds no member and writes nothing.
func TestSETActivateAfterALocalOffboardRestoresNothing(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		admin := h.adminLogin()
		super := h.principalOf(admin)
		tT := h.createOrg(admin, "walk-g-t")
		tB := h.createOrg(admin, "walk-g-b")
		v, sess := h.sharedAccount(admin, "v@walk-g.example", tT, tB)
		priv := h.configureSCIMSet(super, tT)
		tok := h.scimToken(super, tT)

		if r := h.scim("POST", "/v1/scim/v2/Events", tok, scimEvent(t, priv, "g-1", v, scim.EventProvDeactivate)); r.code != http.StatusAccepted {
			t.Fatalf("SET disable = %d %s, want 202", r.code, r.raw)
		}
		if u := h.userByID(v); u.Status != model.StatusActive {
			t.Errorf("a tenant's SET disable wrote the account's global status: %q", u.Status)
		}
		if h.memberOf(v, tT) {
			t.Errorf("the SET disable left the membership in the disabling tenant")
		}
		if !h.memberOf(v, tB) {
			t.Errorf("the SET disable removed the membership in another tenant")
		}
		if code := h.actsIn(sess, tB); code != http.StatusOK {
			t.Errorf("the session in the other tenant after the disable = %d, want 200", code)
		}
		if code := h.actsIn(sess, tT); code != http.StatusForbidden {
			t.Errorf("the session in the disabling tenant after the disable = %d, want 403", code)
		}

		before := h.userByID(v)
		r := h.scim("POST", "/v1/scim/v2/Events", tok, scimEvent(t, priv, "g-2", v, scim.EventProvActivate))
		if r.code != http.StatusBadRequest || r.body["err"] != "invalid_request" {
			t.Errorf("SET activate after the offboard = %d %s, want 400 invalid_request", r.code, r.raw)
		}
		if after := h.userByID(v); after.Version != before.Version {
			t.Errorf("SET activate wrote the account (version %d -> %d)", before.Version, after.Version)
		}
		if h.memberOf(v, tT) {
			t.Errorf("SET activate restored the membership")
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

// TestNoTenantActionWritesGlobalStatusOrCutsAnotherTenant walks the action
// table: every tenant-authorized action on a member removes, revokes or
// excludes only within that tenant and never writes the account's status.
func TestNoTenantActionWritesGlobalStatusOrCutsAnotherTenant(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := h.adminLogin()
		super := h.principalOf(admin)
		tT := h.createOrg(admin, "table-t")
		tB := h.createOrg(admin, "table-b")
		scimTok := h.scimToken(super, tT)
		const users = "/v1/scim/v2/Users/"
		globalUnchanged := func(t *testing.T, v model.ID, sess string) {
			t.Helper()
			if u := h.userByID(v); u.Status != model.StatusActive {
				t.Errorf("the tenant's action wrote the account's global status: %q", u.Status)
			}
			if code := h.actsIn(sess, tB); code != http.StatusOK {
				t.Errorf("the account's session in the other tenant = %d, want 200", code)
			}
		}
		offboarded := func(t *testing.T, v model.ID, sess string) {
			t.Helper()
			if h.memberOf(v, tT) {
				t.Errorf("the account is still a member of the acting tenant")
			}
			if code := h.actsIn(sess, tT); code != http.StatusForbidden {
				t.Errorf("the account's session in the acting tenant = %d, want 403", code)
			}
		}

		t.Run("scim deactivate", func(t *testing.T) {
			v, sess := h.sharedAccount(admin, "deactivate@table.example", tT, tB)
			r := h.scim("PATCH", users+v.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
			if r.code != http.StatusOK || r.body["active"] != false {
				t.Errorf("PATCH active=false = %d %s, want 200 active:false", r.code, r.raw)
			}
			globalUnchanged(t, v, sess)
			offboarded(t, v, sess)
			if g := h.scim("GET", users+v.String(), scimTok, ""); g.code != http.StatusNotFound {
				t.Errorf("GET after the deactivate = %d, want 404", g.code)
			}
		})

		t.Run("scim activate writes nothing", func(t *testing.T) {
			v, _ := h.sharedAccount(admin, "activate@table.example", tT, tB)
			before := h.userByID(v)
			r := h.scim("PATCH", users+v.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`)
			if r.code != http.StatusOK {
				t.Errorf("PATCH active=true = %d %s, want 200", r.code, r.raw)
			}
			if after := h.userByID(v); after.Version != before.Version {
				t.Errorf("PATCH active=true wrote the account")
			}
		})

		t.Run("scim attribute change on a shared account", func(t *testing.T) {
			v, _ := h.sharedAccount(admin, "attrs@table.example", tT, tB)
			before := h.userByID(v)
			r := h.scim("PATCH", users+v.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"Renamed"}]}`)
			if r.code != http.StatusForbidden {
				t.Errorf("attribute change on an account another tenant shares = %d %s, want 403", r.code, r.raw)
			}
			if after := h.userByID(v); after.DisplayName != before.DisplayName || after.Version != before.Version {
				t.Errorf("the refused attribute change was written")
			}
		})

		t.Run("scim attribute change on a tenant's own account", func(t *testing.T) {
			u, created, err := h.authr.SCIMProvisionUser(ctx, super, tT, auth.SCIMUserInput{UserName: "provisioned@table.example", Active: true})
			if err != nil || !created {
				t.Fatalf("provision = %v created=%v", err, created)
			}
			r := h.scim("PATCH", users+u.ID.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"Renamed"}]}`)
			if r.code != http.StatusOK {
				t.Errorf("attribute change on the tenant's own account = %d %s, want 200", r.code, r.raw)
			}
		})

		t.Run("scim delete of a sole member", func(t *testing.T) {
			w, sess := h.sharedAccount(admin, "sole@table.example", tT)
			if r := h.scim("DELETE", users+w.String(), scimTok, ""); r.code != http.StatusNoContent {
				t.Fatalf("DELETE = %d %s, want 204", r.code, r.raw)
			}
			if u := h.userByID(w); u.Status != model.StatusActive {
				t.Errorf("deleting the tenant's only membership disabled the account globally: %q", u.Status)
			}
			if h.liveSessionCount(w) == 0 {
				t.Errorf("deleting the tenant's only membership revoked the account's account-scope sessions")
			}
			offboarded(t, w, sess)
		})

		t.Run("scim delete of a shared member", func(t *testing.T) {
			v, sess := h.sharedAccount(admin, "delete@table.example", tT, tB)
			if r := h.scim("DELETE", users+v.String(), scimTok, ""); r.code != http.StatusNoContent {
				t.Fatalf("DELETE = %d %s, want 204", r.code, r.raw)
			}
			globalUnchanged(t, v, sess)
			offboarded(t, v, sess)
		})

		t.Run("set disable and activate of a non-member", func(t *testing.T) {
			v, sess := h.sharedAccount(admin, "set@table.example", tT, tB)
			priv := h.configureSCIMSet(super, tT)
			if r := h.scim("POST", "/v1/scim/v2/Events", scimTok, scimEvent(t, priv, "t-1", v, scim.EventProvDeactivate)); r.code != http.StatusAccepted {
				t.Fatalf("SET disable = %d %s", r.code, r.raw)
			}
			globalUnchanged(t, v, sess)
			offboarded(t, v, sess)
			r := h.scim("POST", "/v1/scim/v2/Events", scimTok, scimEvent(t, priv, "t-2", v, scim.EventProvActivate))
			if r.code != http.StatusBadRequest {
				t.Errorf("SET activate of a non-member = %d %s, want 400", r.code, r.raw)
			}
		})

		t.Run("caep events", func(t *testing.T) {
			priv, bearer := setupCAEP(t, ctx, h, super, tT)
			send := func(event string, v model.ID, jti string) {
				t.Helper()
				if r := h.scim("POST", caepEndpoint, bearer, signCAEP(t, priv, caepSET(event, v.String(), jti))); r.code != http.StatusAccepted {
					t.Fatalf("CAEP %s = %d %s, want 202", event, r.code, r.raw)
				}
			}

			v, sess := h.sharedAccount(admin, "caep-session@table.example", tT, tB)
			send(caep.EventSessionRevoked, v, "c-1")
			globalUnchanged(t, v, sess)
			if code := h.actsIn(sess, tT); code != http.StatusForbidden {
				t.Errorf("an account-scope session after the tenant's session-revoked = %d in that tenant, want 403", code)
			}

			v, sess = h.sharedAccount(admin, "caep-credential@table.example", tT, tB)
			send(caep.EventCredentialChange, v, "c-2")
			globalUnchanged(t, v, sess)
			if code := h.actsIn(sess, tT); code != http.StatusForbidden {
				t.Errorf("an account-scope session after the tenant's credential-change = %d in that tenant, want 403", code)
			}

			v, sess = h.sharedAccount(admin, "caep-disabled@table.example", tT, tB)
			send(caep.EventAccountDisabled, v, "c-3")
			globalUnchanged(t, v, sess)
			offboarded(t, v, sess)

			v, sess = h.sharedAccount(admin, "caep-compromise@table.example", tT, tB)
			send(caep.EventCredentialCompromise, v, "c-4")
			globalUnchanged(t, v, sess)
			offboarded(t, v, sess)
		})
	})
}

// TestASuspendedMembersAttributesAreRefusedAndNeverOffboardedByDerivation walks
// the action table's column for a member the deployment suspended: an attribute
// change answers 403 and writes nothing, whether it comes as a PATCH or a PUT,
// and never turns into a removal because the account is inactive globally; an
// explicit active=true writes nothing; an explicit active=false removes the
// member from the tenant only.
func TestASuspendedMembersAttributesAreRefusedAndNeverOffboardedByDerivation(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := h.adminLogin()
		super := h.principalOf(admin)
		tT := h.createOrg(admin, "suspended-t")
		scimTok := h.scimToken(super, tT)
		const users = "/v1/scim/v2/Users/"
		// suspended provisions a tenant's own account, which the tenant alone
		// governs, and suspends it globally as the deployment would.
		suspended := func(t *testing.T, email string) model.User {
			t.Helper()
			u, created, err := h.authr.SCIMProvisionUser(ctx, super, tT, auth.SCIMUserInput{UserName: email, Active: true})
			if err != nil || !created {
				t.Fatalf("provision = %v created=%v", err, created)
			}
			h.mutateUser(u.ID, func(u *model.User) { u.Status = model.StatusInactive })
			return h.userByID(u.ID)
		}
		unchanged := func(t *testing.T, before model.User) {
			t.Helper()
			if after := h.userByID(before.ID); after.Version != before.Version || after.DisplayName != before.DisplayName {
				t.Errorf("the suspended account was written: %+v, want %+v", after, before)
			}
			if !h.memberOf(before.ID, tT) {
				t.Errorf("the suspended member was removed from the tenant")
			}
		}

		t.Run("patch of an attribute", func(t *testing.T) {
			before := suspended(t, "patch@suspended.example")
			r := h.scim("PATCH", users+before.ID.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"Renamed"}]}`)
			if r.code != http.StatusForbidden {
				t.Errorf("PATCH displayName of a suspended member = %d %s, want 403", r.code, r.raw)
			}
			unchanged(t, before)
		})

		t.Run("put of an attribute", func(t *testing.T) {
			before := suspended(t, "put@suspended.example")
			r := h.scim("PUT", users+before.ID.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"put@suspended.example","displayName":"Renamed","active":true}`)
			if r.code != http.StatusForbidden {
				t.Errorf("PUT of a suspended member's attributes = %d %s, want 403", r.code, r.raw)
			}
			unchanged(t, before)
		})

		t.Run("patch active=true", func(t *testing.T) {
			before := suspended(t, "activate@suspended.example")
			r := h.scim("PATCH", users+before.ID.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":true}]}`)
			if r.code != http.StatusOK {
				t.Errorf("PATCH active=true of a suspended member = %d %s, want 200", r.code, r.raw)
			}
			unchanged(t, before)
			if u := h.userByID(before.ID); u.Status != model.StatusInactive {
				t.Errorf("a tenant's active=true reactivated the suspended account: %q", u.Status)
			}
		})

		t.Run("patch active=false", func(t *testing.T) {
			before := suspended(t, "deactivate@suspended.example")
			r := h.scim("PATCH", users+before.ID.String(), scimTok,
				`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`)
			if r.code != http.StatusOK {
				t.Errorf("PATCH active=false of a suspended member = %d %s, want 200", r.code, r.raw)
			}
			if h.memberOf(before.ID, tT) {
				t.Errorf("active=false left the suspended member in the tenant")
			}
			if u := h.userByID(before.ID); u.DisplayName != before.DisplayName || u.Status != model.StatusInactive {
				t.Errorf("the removal wrote the account's global record: %+v", u)
			}
		})
	})
}

// TestTenantCreateOrRenameIntoAnotherTenantsClaimedDomainIsRefused: before any
// write, a tenant's create, invitation or rename refuses an address in a domain
// another organization's identity provider claims.
func TestTenantCreateOrRenameIntoAnotherTenantsClaimedDomainIsRefused(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := h.elevatedAdmin()
		super := h.principalOf(admin)
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

		scimTok := h.scimToken(super, tA)
		c := h.scim("POST", "/v1/scim/v2/Users", scimTok,
			`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"q@claimed.example","active":true}`)
		if c.code != http.StatusForbidden {
			t.Errorf("SCIM create into another tenant's claimed domain = %d %s, want 403", c.code, c.raw)
		}
		if _, found := h.userByEmail("q@claimed.example"); found {
			t.Errorf("the refused SCIM create created the account")
		}

		own, created, err := h.authr.SCIMProvisionUser(ctx, super, tA, auth.SCIMUserInput{UserName: "own@domain-a.example", Active: true})
		if err != nil || !created {
			t.Fatalf("provision = %v created=%v", err, created)
		}
		rn := h.scim("PATCH", "/v1/scim/v2/Users/"+own.ID.String(), scimTok,
			`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"userName","value":"own@claimed.example"}]}`)
		if rn.code != http.StatusForbidden {
			t.Errorf("SCIM rename into another tenant's claimed domain = %d %s, want 403", rn.code, rn.raw)
		}
		if got := h.userByID(own.ID); got.Email != "own@domain-a.example" {
			t.Errorf("the refused rename was written: %q", got.Email)
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
