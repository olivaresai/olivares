// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// mintSession creates a live session for userID directly through the store and
// returns its wire token, so a test can prove whether an account's existing
// access survives a request made by a tenant the account does not belong to.
func (h *harness) mintSession(userID model.ID) string {
	h.t.Helper()
	ctx := context.Background()
	sc, err := auth.NewCredential(auth.PrefixSession)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Sessions().Create(ctx, model.AuthSession{
			UserID: userID, Selector: sc.Selector, SecretHash: sc.SecretHash,
			ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)),
		})
		return err
	}); err != nil {
		h.t.Fatal(err)
	}
	return sc.Token
}

// TestSCIMCreateUserCannotAdoptAnAccountOutsideTheBoundTenant pins the boundary a
// SCIM connection is built on: the connection is bound to ONE tenant and manages
// accounts as members of that tenant. A create whose userName belongs to an
// account outside that tenant must not read as absent, must not write that
// account and must not enroll it — otherwise one tenant's provisioning credential
// is a lever over every account in the deployment.
//
// Both administrative shapes are covered, because they fail differently and both
// matter: active:false is a deactivation (the account's sessions stop
// authenticating everywhere, in every tenant it belongs to), and active:true is a
// silent roster adoption (the account appears in a tenant that never onboarded
// it, with the requesting tenant's attributes written over its own).
func TestSCIMCreateUserCannotAdoptAnAccountOutsideTheBoundTenant(t *testing.T) {
	const base = "/v1/scim/v2"
	const victimName, victimExternalID = "Blair Okonjo", "idp-owner-7"
	for _, tc := range []struct {
		name   string
		active string
		victim string
	}{
		{name: "deactivation", active: "false", victim: "ceo@globex.example"},
		{name: "silent adoption", active: "true", victim: "cfo@globex.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			adminTok := h.adminLogin()
			claiming := h.createOrg(adminTok, "acme")
			owning := h.createOrg(adminTok, "globex")
			super, err := h.authr.Authenticate(context.Background(), adminTok)
			if err != nil {
				t.Fatal(err)
			}
			claimingTok := h.scimToken(super, claiming)
			owningTok := h.scimToken(super, owning)

			// The account is a member of the owning tenant and of nothing else.
			seed := h.scim("POST", base+"/Users", owningTok, `{
				"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
				"userName":"`+tc.victim+`","externalId":"`+victimExternalID+`",
				"displayName":"`+victimName+`","active":true}`)
			if seed.code != http.StatusCreated {
				t.Fatalf("seed create in the owning tenant = %d %s", seed.code, seed.raw)
			}
			victimID, _ := seed.body["id"].(string)
			if victimID == "" {
				t.Fatal("seed create returned no id")
			}
			sessTok := h.mintSession(model.ID(victimID))

			// The other tenant's provisioning credential claims that same address.
			claim := h.scim("POST", base+"/Users", claimingTok, `{
				"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
				"userName":"`+tc.victim+`","externalId":"idp-claimant-1",
				"displayName":"Claimed","active":`+tc.active+`}`)
			if claim.code == http.StatusCreated {
				t.Errorf("POST /Users userName=%s active=%s returned 201: a connection bound to one tenant made a resource out of another tenant's account",
					tc.victim, tc.active)
			}
			// A taken address is a uniqueness conflict, and it is the SAME conflict a
			// duplicate INSIDE the bound tenant gets. The two must be word-for-word
			// identical: a response that differs tells the caller whether the address
			// exists elsewhere in the deployment, which is an existence oracle over
			// every account the caller has no authority over.
			if claim.code != http.StatusConflict || claim.body["scimType"] != scim.TypeUniqueness {
				t.Errorf("foreign userName = %d scimType=%v, want 409 uniqueness", claim.code, claim.body["scimType"])
			}
			const ownAddress = "newcomer@acme.example"
			if first := h.scim("POST", base+"/Users", claimingTok, `{"userName":"`+ownAddress+`","active":true}`); first.code != http.StatusCreated {
				t.Fatalf("create of an unused address = %d %s, want 201 (unchanged behavior)", first.code, first.raw)
			}
			dup := h.scim("POST", base+"/Users", claimingTok, `{"userName":"`+ownAddress+`","active":true}`)
			if dup.code != claim.code || dup.body["scimType"] != claim.body["scimType"] || dup.body["detail"] != claim.body["detail"] {
				t.Errorf("a taken address answers %d/%v/%v inside the bound tenant and %d/%v/%v outside it; the difference is an existence oracle",
					dup.code, dup.body["scimType"], dup.body["detail"], claim.code, claim.body["scimType"], claim.body["detail"])
			}

			// Read back through the owning tenant, the only connection with authority
			// over the account: nothing of it moved.
			after := h.scim("GET", base+"/Users/"+victimID, owningTok, "")
			if after.code != http.StatusOK {
				t.Fatalf("owner GET after the foreign create = %d %s", after.code, after.raw)
			}
			if after.body["active"] != true {
				t.Errorf("active = %v, want true: another tenant's create set the account to inactive, which stops its sessions in EVERY tenant", after.body["active"])
			}
			if after.body["displayName"] != victimName {
				t.Errorf("displayName = %v, want %q: another tenant's create overwrote it", after.body["displayName"], victimName)
			}
			if after.body["externalId"] != victimExternalID {
				t.Errorf("externalId = %v, want %q: another tenant's create overwrote it", after.body["externalId"], victimExternalID)
			}

			// And it was not enrolled: the claiming tenant's resource set does not
			// hold it, by id or by userName.
			if probe := h.scim("GET", base+"/Users/"+victimID, claimingTok, ""); probe.code != http.StatusNotFound {
				t.Errorf("GET by id with the claiming tenant's credential = %d, want 404 (not a member of it)", probe.code)
			}
			listed := h.scim("GET", base+"/Users?filter="+url.QueryEscape(`userName eq "`+tc.victim+`"`), claimingTok, "")
			if listed.code != http.StatusOK || total(listed) != 0 {
				t.Errorf("the claiming tenant's roster reports the account: %d total=%v, want 200 / 0", listed.code, listed.body["totalResults"])
			}

			// The account's access in its own tenant is untouched.
			if _, err := h.authr.Authenticate(context.Background(), sessTok); err != nil {
				t.Errorf("the account's session = %v, want still valid: another tenant's create cut its access", err)
			}
		})
	}
}
