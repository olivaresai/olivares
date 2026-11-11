// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// A role change names its account by id: a missing or malformed id is the caller's
// mistake (400), never an internal error. An owner signed in with a password and no
// passkey, on an install with no step-up, changes a real member's role.
func TestGrantMembershipRefusesAMalformedAccountID(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "role-id")
		_, ownerTok := h.guardMember(root, tenant, "owner@role-id.test", auth.RoleOwner)
		member, _ := h.guardMember(root, tenant, "member@role-id.test", auth.RoleViewer)
		h.requireStepUp(auth.StepUpNone)

		for name, id := range map[string]any{"missing": nil, "empty": "", "malformed": "not-an-id"} {
			body := map[string]any{"tenant": tenant.String(), "role": auth.RoleEditor}
			if id != nil {
				body["user_id"] = id
			}
			r := h.do("POST", "/v1/memberships", ownerTok, body, nil)
			if r.code != http.StatusBadRequest || errorCode(r) != "bad_request" {
				t.Errorf("%s user_id = %d %s, want 400 bad_request", name, r.code, r.raw)
			}
		}
		r := h.do("POST", "/v1/memberships", ownerTok, map[string]any{
			"user_id": member.String(), "tenant": tenant.String(), "role": auth.RoleEditor,
		}, nil)
		if r.code != http.StatusCreated || r.body["role"] != auth.RoleEditor {
			t.Errorf("owner without a passkey changing a member's role = %d %s, want 201 editor", r.code, r.raw)
		}
	})
}
