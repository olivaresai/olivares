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
)

// fixtureSecret is the throwaway password every account these tests create has.
const fixtureSecret = "synthetic-fixture-value"

// A tenant administrator removes a member from the organization in every edition:
// the People tab does it, and it does not ride the SCIM server (a Business
// capability).
func TestRemoveMemberTakesTheAccountOutOfTheTenant(t *testing.T) {
	h := newHarness(t)
	admin := h.elevatedAdmin()
	tenant := h.createOrg(admin, "member-remove")
	other := h.createOrg(admin, "member-remove-other")
	leaver := h.onboardPassword(admin, tenant, "leaver@remove.test", auth.RoleViewer, fixtureSecret)
	h.seedMembership(leaver, other, auth.RoleViewer)
	bystander := h.onboardPassword(admin, tenant, "stays@remove.test", auth.RoleViewer, fixtureSecret)
	foreign := h.onboardPassword(admin, other, "foreign@remove.test", auth.RoleViewer, fixtureSecret)
	path := func(id string) string { return "/v1/members/" + id }

	// Refusals first: nothing is removed by a request the caller may not make.
	if r := h.do("DELETE", path(leaver.String()), "", nil, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated DELETE = %d %s, want 401", r.code, r.raw)
	}
	viewer := h.login("leaver@remove.test", fixtureSecret)
	if r := h.do("DELETE", path(bystander.String()), viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer DELETE = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("DELETE", path(foreign.String()), admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("DELETE of another organization's member = %d %s, want 404", r.code, r.raw)
	}
	if !h.memberOf(leaver, tenant) || !h.memberOf(bystander, tenant) || !h.memberOf(foreign, other) {
		t.Fatalf("a refused request removed someone: leaver=%v bystander=%v foreign=%v",
			h.memberOf(leaver, tenant), h.memberOf(bystander, tenant), h.memberOf(foreign, other))
	}

	r := h.do("DELETE", path(leaver.String()), admin, nil, tenantHdr(tenant))
	if r.code != http.StatusNoContent {
		t.Fatalf("admin DELETE = %d %s, want 204", r.code, r.raw)
	}
	if h.memberOf(leaver, tenant) {
		t.Error("the member is still in the organization after DELETE")
	}
	if !h.memberOf(leaver, other) || !h.memberOf(bystander, tenant) {
		t.Error("DELETE touched another membership of the account or another member")
	}
	if got := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw; strings.Contains(got, leaver.String()) {
		t.Errorf("the roster still lists the removed member: %s", got)
	}
	if again := h.do("DELETE", path(leaver.String()), admin, nil, tenantHdr(tenant)); again.code != http.StatusNotFound {
		t.Errorf("second DELETE = %d %s, want 404", again.code, again.raw)
	}
}

// TestRemoveMemberWritesNoGlobalStatusAndCutsNoOtherTenant walks the member kinds a
// removal meets: it removes the membership of the acting tenant only and never writes
// the account's status, whether the account is shared, belongs to that tenant alone,
// or was suspended by the deployment.
func TestRemoveMemberWritesNoGlobalStatusAndCutsNoOtherTenant(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		admin := h.adminLogin()
		super := h.principalOf(admin)
		tT := h.createOrg(admin, "remove-t")
		tB := h.createOrg(admin, "remove-b")
		remove := func(t *testing.T, v model.ID) {
			t.Helper()
			if r := h.do("DELETE", "/v1/members/"+v.String(), admin, nil, tenantHdr(tT)); r.code != http.StatusNoContent {
				t.Fatalf("DELETE member = %d %s, want 204", r.code, r.raw)
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

		t.Run("shared member", func(t *testing.T) {
			v, sess := h.sharedAccount(admin, "shared@remove.example", tT, tB)
			remove(t, v)
			if u := h.userByID(v); u.Status != model.StatusActive {
				t.Errorf("the removal wrote the account's global status: %q", u.Status)
			}
			if code := h.actsIn(sess, tB); code != http.StatusOK {
				t.Errorf("the account's session in the other tenant = %d, want 200", code)
			}
			offboarded(t, v, sess)
		})

		t.Run("sole member", func(t *testing.T) {
			w, sess := h.sharedAccount(admin, "sole@remove.example", tT)
			remove(t, w)
			if u := h.userByID(w); u.Status != model.StatusActive {
				t.Errorf("removing the tenant's only membership disabled the account globally: %q", u.Status)
			}
			if h.liveSessionCount(w) == 0 {
				t.Errorf("removing the tenant's only membership revoked the account's account-scope sessions")
			}
			offboarded(t, w, sess)
		})

		t.Run("member the deployment suspended", func(t *testing.T) {
			u, created, err := h.authr.SCIMProvisionUser(ctx, super, tT, auth.SCIMUserInput{UserName: "suspended@remove.example", Active: true})
			if err != nil || !created {
				t.Fatalf("provision = %v created=%v", err, created)
			}
			h.mutateUser(u.ID, func(u *model.User) { u.Status = model.StatusInactive })
			before := h.userByID(u.ID)
			remove(t, u.ID)
			if h.memberOf(u.ID, tT) {
				t.Errorf("the removal left the suspended member in the tenant")
			}
			if after := h.userByID(u.ID); after.DisplayName != before.DisplayName || after.Status != model.StatusInactive {
				t.Errorf("the removal wrote the account's global record: %+v", after)
			}
		})
	})
}
