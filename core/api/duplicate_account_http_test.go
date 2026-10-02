// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"github.com/olivaresai/olivares/core/auth"
	"net/http"
	"strings"
	"testing"
)

func TestDuplicateAccountCreationHTTP(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "duplicate-account")
	const originalPassword = "original-fixture-password-123"
	const replacementPassword = "replacement-fixture-password-123"
	body := map[string]any{"email": "person@example.test", "password": originalPassword, "tenant": tenant.String(), "role": auth.RoleViewer}
	if r := h.do("POST", "/v1/users", admin, body, nil); r.code != http.StatusCreated {
		t.Fatalf("first create HTTP%d", r.code)
	}
	usersBefore := h.do("GET", "/v1/users", admin, nil, nil).raw
	membersBefore := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "person@example.test", "password": originalPassword}, nil)
	if login.code != http.StatusOK {
		t.Fatal("member fixture cannot sign in")
	}
	viewer := login.body["token"].(string)
	for _, c := range []struct {
		bearer string
		status int
	}{{"", http.StatusUnauthorized}, {viewer, http.StatusForbidden}} {
		if r := h.do("POST", "/v1/users", c.bearer, body, nil); r.code != c.status {
			t.Errorf("unauthorized create HTTP%d, want%d", r.code, c.status)
		}
	}
	for _, email := range []string{"person@example.test", " Person@Example.test "} {
		body["email"], body["password"], body["role"] = email, replacementPassword, auth.RoleAdmin
		r := h.do("POST", "/v1/users", admin, body, nil)
		if r.code != http.StatusConflict {
			t.Fatalf("duplicate HTTP%d", r.code)
		}
		errBody := r.body["error"].(map[string]any)
		if errBody["code"] != "conflict" {
			t.Error("duplicate changed the public conflict code")
		}
		if errBody["message"] != "An account with that email already exists in this organization." {
			t.Errorf("duplicate does not show the plain sentence")
		}
		for _, internal := range []string{"constraint", "users.tenant_id", "2067", "UNIQUE", "SQLITE"} {
			if strings.Contains(r.raw, internal) {
				t.Errorf("duplicate discloses storage details")
			}
		}
	}
	if got := h.do("GET", "/v1/users", admin, nil, nil).raw; got != usersBefore {
		t.Error("duplicate changed the original account")
	}
	if got := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenant)).raw; got != membersBefore {
		t.Error("duplicate changed membership")
	}
	for _, c := range []struct {
		password string
		status   int
	}{{originalPassword, http.StatusOK}, {replacementPassword, http.StatusUnauthorized}} {
		if r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "person@example.test", "password": c.password}, nil); r.code != c.status {
			t.Errorf("original credential preservation HTTP%d, want%d", r.code, c.status)
		}
	}
}
