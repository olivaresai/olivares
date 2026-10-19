// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const guardPassword = "guardpass-123"

// guardMember onboards a person into tenant through the deployment's user route and
// returns the account id and a password session of it.
func (h *harness) guardMember(root string, tenant model.TenantID, email, role string) (model.ID, string) {
	h.t.Helper()
	id, tok := h.authzMember(root, email, guardPassword, role, tenant)
	return model.ID(id), tok
}

func removeMemberPath(id model.ID) string { return "/v1/members/" + id.String() }

// A member cannot remove itself, whatever its role and however many owners remain: the
// People tab hides the button (HU-25), and the server refuses the request the button
// would have sent.
func TestRemoveMemberRefusesRemovingYourself(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "remove-self")
		owner, ownerTok := h.guardMember(root, tenant, "owner@remove-self.test", auth.RoleOwner)
		h.guardMember(root, tenant, "second@remove-self.test", auth.RoleOwner)
		admin, adminTok := h.guardMember(root, tenant, "admin@remove-self.test", auth.RoleAdmin)

		for name, c := range map[string]struct {
			id  model.ID
			tok string
		}{"owner with a second owner": {owner, ownerTok}, "admin": {admin, adminTok}} {
			r := h.do("DELETE", removeMemberPath(c.id), c.tok, nil, tenantHdr(tenant))
			if r.code != http.StatusConflict || errorCode(r) != "cannot_remove_self" {
				t.Errorf("%s removing itself = %d %s, want 409 cannot_remove_self", name, r.code, r.raw)
			}
			if !h.memberOf(c.id, tenant) {
				t.Errorf("%s: the refused request removed the member", name)
			}
		}
	})
}

// The organization always keeps an active owner: the last one cannot be removed, by
// anyone. An owner whose account the deployment suspended cannot act, so it does not
// count as one.
func TestRemoveMemberKeepsTheLastActiveOwner(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()

		t.Run("sole owner", func(t *testing.T) {
			tenant := h.createOrg(root, "last-owner")
			first, _ := h.guardMember(root, tenant, "first@last-owner.test", auth.RoleOwner)
			second, _ := h.guardMember(root, tenant, "second@last-owner.test", auth.RoleOwner)

			if r := h.do("DELETE", removeMemberPath(first), root, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
				t.Fatalf("removing one of two owners = %d %s, want 204", r.code, r.raw)
			}
			if h.memberOf(first, tenant) {
				t.Error("the removed owner is still a member")
			}
			r := h.do("DELETE", removeMemberPath(second), root, nil, tenantHdr(tenant))
			if r.code != http.StatusConflict || errorCode(r) != "last_owner" {
				t.Errorf("removing the last owner = %d %s, want 409 last_owner", r.code, r.raw)
			}
			if !h.memberOf(second, tenant) {
				t.Error("the refused request removed the last owner")
			}
		})

		t.Run("suspended owner does not count", func(t *testing.T) {
			tenant := h.createOrg(root, "suspended-owner")
			active, _ := h.guardMember(root, tenant, "active@suspended-owner.test", auth.RoleOwner)
			suspended, _ := h.guardMember(root, tenant, "suspended@suspended-owner.test", auth.RoleOwner)
			h.mutateUser(suspended, func(u *model.User) { u.Status = model.StatusInactive })

			r := h.do("DELETE", removeMemberPath(active), root, nil, tenantHdr(tenant))
			if r.code != http.StatusConflict || errorCode(r) != "last_owner" {
				t.Errorf("removing the only active owner = %d %s, want 409 last_owner", r.code, r.raw)
			}
			if r := h.do("DELETE", removeMemberPath(suspended), root, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
				t.Errorf("removing the suspended owner while an active one remains = %d %s, want 204", r.code, r.raw)
			}
			if h.memberOf(suspended, tenant) {
				t.Error("the removed suspended owner is still a member")
			}
		})
	})
}

// Removal obeys the role ceiling demotion already has: a caller cannot remove a member
// above its own role, whether that role is the membership's or a directory group's
// mapped role. Equal rank and below are allowed.
func TestRemoveMemberAppliesTheRoleCeiling(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "ceiling")
		owner, ownerTok := h.guardMember(root, tenant, "owner@ceiling.test", auth.RoleOwner)
		_, adminTok := h.guardMember(root, tenant, "admin@ceiling.test", auth.RoleAdmin)
		peer, _ := h.guardMember(root, tenant, "peer@ceiling.test", auth.RoleAdmin)
		editor, _ := h.guardMember(root, tenant, "editor@ceiling.test", auth.RoleEditor)
		viaGroup, _ := h.guardMember(root, tenant, "group-owner@ceiling.test", auth.RoleViewer)
		if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
			group, err := as.Groups().Create(ctx, model.UserGroup{
				TargetTenantID: tenant, DisplayName: "owners", MappedRole: auth.RoleOwner,
			})
			if err != nil {
				return err
			}
			_, err = as.GroupMembers().Create(ctx, model.UserGroupMember{GroupID: group.ID, UserID: viaGroup})
			return err
		}); err != nil {
			t.Fatalf("seed the owner-mapped group: %v", err)
		}

		for name, id := range map[string]model.ID{"owner": owner, "member elevated to owner by a group": viaGroup} {
			r := h.do("DELETE", removeMemberPath(id), adminTok, nil, tenantHdr(tenant))
			if r.code != http.StatusForbidden || errorCode(r) != "role_ceiling" {
				t.Errorf("admin removing the %s = %d %s, want 403 role_ceiling", name, r.code, r.raw)
			}
			if !h.memberOf(id, tenant) {
				t.Errorf("the refused request removed the %s", name)
			}
		}
		if code := h.actsIn(ownerTok, tenant); code != http.StatusOK {
			t.Errorf("the owner's session after the refused removals = %d, want 200", code)
		}
		for name, id := range map[string]model.ID{"admin of equal rank": peer, "editor": editor} {
			if r := h.do("DELETE", removeMemberPath(id), adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
				t.Errorf("admin removing the %s = %d %s, want 204", name, r.code, r.raw)
			}
			if h.memberOf(id, tenant) {
				t.Errorf("the removed %s is still a member", name)
			}
		}
	})
}

// A workspace-confined caller is refused on the tenant-wide removal route, as it is on
// onboarding and the TOTP reset: removing a member is not a workspace action.
func TestRemoveMemberRefusesAWorkspaceConfinedCaller(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "confined")
		editor, _ := h.guardMember(root, tenant, "editor@confined.test", auth.RoleEditor)
		var defaultWorkspace string
		for _, it := range h.do("GET", "/v1/workspaces", root, nil, tenantHdr(tenant)).body["items"].([]any) {
			if ws := it.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
				defaultWorkspace = ws["id"].(string)
			}
		}
		if r := h.do("POST", "/v1/users", root, map[string]any{
			"email": "confined@confined.test", "password": guardPassword, "tenant": tenant.String(),
			"role": auth.RoleAdmin, "workspace_id": defaultWorkspace,
		}, nil); r.code != http.StatusCreated {
			t.Fatalf("create the confined admin = %d %s", r.code, r.raw)
		}
		confinedTok := h.login("confined@confined.test", guardPassword)

		r := h.do("DELETE", removeMemberPath(editor), confinedTok, nil, tenantHdr(tenant))
		if r.code != http.StatusForbidden {
			t.Errorf("confined admin removing a member = %d %s, want 403", r.code, r.raw)
		}
		if !h.memberOf(editor, tenant) {
			t.Error("the refused request removed the member")
		}
	})
}

// Removal asks a human session for the deployment's administrative step-up, as the
// TOTP reset does. An API token never carries one, and its authority was fixed when
// it was issued, so a token stays governed by its role alone.
func TestRemoveMemberAsksAHumanSessionForStepUp(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "step-up")
		_, ownerTok := h.guardMember(root, tenant, "owner@step-up.test", auth.RoleOwner)
		first, _ := h.guardMember(root, tenant, "first@step-up.test", auth.RoleViewer)
		second, _ := h.guardMember(root, tenant, "second@step-up.test", auth.RoleViewer)
		if err := h.authr.SetAdminStepUp(ctx, h.principalOf(root), auth.StepUpPasskey); err != nil {
			t.Fatalf("require passkeys: %v", err)
		}
		issued := h.do("POST", "/v1/tokens", root, map[string]any{
			"name": "automation", "tenant": tenant.String(), "role": auth.RoleAdmin,
		}, nil)
		if issued.code != http.StatusCreated {
			t.Fatalf("issue a token = %d %s", issued.code, issued.raw)
		}
		apiToken, _ := issued.body["token"].(string)

		r := h.do("DELETE", removeMemberPath(first), ownerTok, nil, tenantHdr(tenant))
		if r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
			t.Errorf("owner session without step-up = %d %s, want 403 step_up_required", r.code, r.raw)
		}
		if !h.memberOf(first, tenant) {
			t.Error("the refused request removed the member")
		}
		h.elevate(ownerTok)
		if r := h.do("DELETE", removeMemberPath(first), ownerTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
			t.Errorf("owner session after step-up = %d %s, want 204", r.code, r.raw)
		}
		if r := h.do("DELETE", removeMemberPath(second), apiToken, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
			t.Errorf("admin API token = %d %s, want 204 (a token is not asked for a human step-up)", r.code, r.raw)
		}
		if h.memberOf(first, tenant) || h.memberOf(second, tenant) {
			t.Errorf("a removed member is still in the organization: first=%v second=%v", h.memberOf(first, tenant), h.memberOf(second, tenant))
		}
	})
}

// A confined owner cannot run the organization, so it does not keep the organization
// from losing its last usable owner.
func TestRemoveMemberDoesNotCountAConfinedOwner(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "confined-owner")
		wide, _ := h.guardMember(root, tenant, "wide@confined-owner.test", auth.RoleOwner)
		var defaultWorkspace string
		for _, it := range h.do("GET", "/v1/workspaces", root, nil, tenantHdr(tenant)).body["items"].([]any) {
			if ws := it.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
				defaultWorkspace = ws["id"].(string)
			}
		}
		confined := h.do("POST", "/v1/users", root, map[string]any{
			"email": "confined@confined-owner.test", "password": guardPassword, "tenant": tenant.String(),
			"role": auth.RoleOwner, "workspace_id": defaultWorkspace,
		}, nil)
		if confined.code != http.StatusCreated {
			t.Fatalf("create the confined owner = %d %s", confined.code, confined.raw)
		}

		r := h.do("DELETE", removeMemberPath(wide), root, nil, tenantHdr(tenant))
		if r.code != http.StatusConflict || errorCode(r) != "last_owner" {
			t.Errorf("removing the only tenant-wide owner = %d %s, want 409 last_owner", r.code, r.raw)
		}
		if !h.memberOf(wide, tenant) {
			t.Error("the refused request removed the owner")
		}
	})
}

// An API token goes through the same guards as the person who issued it: its role is the
// ceiling, and it cannot remove the account that owns it.
func TestRemoveMemberHoldsAnAPITokenToTheSameGuards(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "token-guards")
		owner, ownerTok := h.guardMember(root, tenant, "owner@token-guards.test", auth.RoleOwner)
		h.guardMember(root, tenant, "second@token-guards.test", auth.RoleOwner)
		issue := func(session, role string) string {
			t.Helper()
			r := h.do("POST", "/v1/tokens", session, map[string]any{"name": role, "tenant": tenant.String(), "role": role}, nil)
			if r.code != http.StatusCreated {
				t.Fatalf("issue a %s token = %d %s", role, r.code, r.raw)
			}
			tok, _ := r.body["token"].(string)
			return tok
		}

		r := h.do("DELETE", removeMemberPath(owner), issue(root, auth.RoleAdmin), nil, tenantHdr(tenant))
		if r.code != http.StatusForbidden || errorCode(r) != "role_ceiling" {
			t.Errorf("admin token removing an owner = %d %s, want 403 role_ceiling", r.code, r.raw)
		}
		r = h.do("DELETE", removeMemberPath(owner), issue(ownerTok, auth.RoleOwner), nil, tenantHdr(tenant))
		if r.code != http.StatusConflict || errorCode(r) != "cannot_remove_self" {
			t.Errorf("a token removing the account that owns it = %d %s, want 409 cannot_remove_self", r.code, r.raw)
		}
		if !h.memberOf(owner, tenant) {
			t.Error("a refused request removed the owner")
		}
	})
}

// A superadmin outranks every tenant role: a tenant admin cannot remove its membership.
func TestRemoveMemberKeepsASuperadminAboveTheTenantAdmin(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "superadmin-target")
		_, adminTok := h.guardMember(root, tenant, "admin@superadmin-target.test", auth.RoleAdmin)
		super, found := h.userByEmail("root@x.io")
		if !found {
			t.Fatal("the setup superadmin is missing")
		}
		h.seedMembership(super.ID, tenant, auth.RoleViewer)

		r := h.do("DELETE", removeMemberPath(super.ID), adminTok, nil, tenantHdr(tenant))
		if r.code != http.StatusForbidden || errorCode(r) != "role_ceiling" {
			t.Errorf("admin removing a superadmin's membership = %d %s, want 403 role_ceiling", r.code, r.raw)
		}
		if !h.memberOf(super.ID, tenant) {
			t.Error("the refused request removed the superadmin's membership")
		}
	})
}

// Two removals racing for the last two owners cannot each see the other owner still
// there: the directory writer lock serializes them, one wins and one is refused.
func TestRemoveMemberKeepsAnOwnerWhenTwoRemovalsRace(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "race")
		ids := []model.ID{}
		for _, email := range []string{"a@race.test", "b@race.test"} {
			id, _ := h.guardMember(root, tenant, email, auth.RoleOwner)
			ids = append(ids, id)
		}

		codes := make([]int, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = h.do("DELETE", removeMemberPath(id), root, nil, tenantHdr(tenant)).code
			}()
		}
		wg.Wait()

		removed, refused := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusNoContent:
				removed++
			case http.StatusConflict:
				refused++
			}
		}
		if removed != 1 || refused != 1 {
			t.Errorf("racing removals answered %v, want one 204 and one 409", codes)
		}
		if h.memberOf(ids[0], tenant) == h.memberOf(ids[1], tenant) {
			t.Errorf("owners left after the race: first=%v second=%v, want exactly one", h.memberOf(ids[0], tenant), h.memberOf(ids[1], tenant))
		}
	})
}

// The removal is on the tenant's own ledger with its own cause, under the leaver action
// the Identity card reads: the HTTP route reaches the removal, not a directory event.
func TestRemoveMemberRecordsItsOwnCauseOnTheTenantLedger(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "cause")
		leaver, _ := h.guardMember(root, tenant, "leaver@cause.test", auth.RoleViewer)

		if r := h.do("DELETE", removeMemberPath(leaver), root, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
			t.Fatalf("DELETE = %d %s, want 204", r.code, r.raw)
		}

		var causes []string
		if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
			walker, ok := sc.Audit().(store.CanonicalWalker)
			if !ok {
				return errors.New("audit lacks canonical metadata")
			}
			return walker.WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, _ []byte) error {
				if ev.Action != "scim.user.deprovision" || ev.TargetID != leaver {
					return nil
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(meta), &m); err != nil {
					return err
				}
				cause, _ := m["cause"].(string)
				causes = append(causes, cause)
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		if len(causes) != 1 || causes[0] != "member.remove" {
			t.Errorf("leaver events for the removed member carry causes %v, want [member.remove]", causes)
		}
	})
}
