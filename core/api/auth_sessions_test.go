// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuthSessionInventoryIsOwnOnlyAndRevokesOtherSessions(t *testing.T) {
	h := newHarness(t)
	current := h.adminLogin()
	actor, err := h.authr.Authenticate(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	second := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	}, nil)
	if second.code != http.StatusOK {
		t.Fatalf("second login = %d %s", second.code, second.raw)
	}
	secondID := second.body["session_id"].(string)
	browser := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	}, map[string]string{"X-Olivares-Session": "cookie"})
	if browser.code != http.StatusOK {
		t.Fatalf("browser login = %d %s", browser.code, browser.raw)
	}
	browserID := browser.body["session_id"].(string)
	cookie := (&http.Response{Header: browser.hdr}).Cookies()[0]
	if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		row, err := as.Sessions().Get(context.Background(), model.ID(secondID))
		if err != nil {
			return err
		}
		row.ClientKind = ""
		row.LastUsedAt = nil
		_, err = as.Sessions().Update(context.Background(), row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tenant := h.createOrg(current, "session-inventory")
	other := h.tenantTokenRole(current, tenant, "other@example.test", auth.RoleViewer)

	list := h.do("GET", "/v1/auth/sessions", "", nil, map[string]string{"Cookie": cookie.String()})
	if list.code != http.StatusOK {
		t.Fatalf("list sessions = %d %s", list.code, list.raw)
	}
	items, _ := list.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("own session count = %d, want 3: %s", len(items), list.raw)
	}
	byID := map[string]map[string]any{}
	for _, item := range items {
		row, _ := item.(map[string]any)
		byID[row["id"].(string)] = row
		for _, field := range []string{"created_at", "last_used_at", "kind", "current"} {
			if _, ok := row[field]; !ok {
				t.Errorf("session %v lacks %s", row["id"], field)
			}
		}
	}
	if byID[browserID]["current"] != true || byID[browserID]["kind"] != "browser" {
		t.Errorf("current browser session = %#v", byID[browserID])
	}
	if _, err := time.Parse(time.RFC3339Nano, byID[browserID]["last_used_at"].(string)); err != nil {
		t.Errorf("browser last_used_at is not RFC3339: %v", err)
	}
	if byID[actor.CredID.String()]["current"] != false || byID[actor.CredID.String()]["kind"] != "token" {
		t.Errorf("bearer session = %#v", byID[actor.CredID.String()])
	}
	if byID[secondID] == nil {
		t.Error("second own session is missing")
	}
	if byID[secondID]["kind"] != "unknown" || byID[secondID]["last_used_at"] != nil {
		t.Errorf("legacy session invented use metadata: %#v", byID[secondID])
	}

	// Both personal and People mutation routes reject browser cookies without CSRF.
	for _, path := range []string{"/v1/auth/sessions/" + secondID, "/v1/users/" + actor.UserID.String() + "/sessions"} {
		if r := h.do("DELETE", path, "", nil, map[string]string{"Cookie": cookie.String(), "X-Olivares-Tenant": tenant.String()}); r.code != http.StatusForbidden {
			t.Fatalf("cookie DELETE without CSRF %s = %d %s", path, r.code, r.raw)
		}
	}
	apiCredential, err := auth.NewCredential(auth.PrefixToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		_, err := as.Tokens().Create(context.Background(), model.APIToken{Name: "session-inventory-test", UserID: actor.UserID, Selector: apiCredential.Selector, SecretHash: apiCredential.SecretHash, BoundTenantID: tenant, Role: auth.RoleViewer})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := h.do("GET", "/v1/auth/sessions", apiCredential.Token, nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("API token inventory = %d %s, want 401", r.code, r.raw)
	}
	otherList := h.do("GET", "/v1/auth/sessions", other, nil, nil)
	otherItems, _ := otherList.body["items"].([]any)
	if otherList.code != http.StatusOK || len(otherItems) != 1 {
		t.Fatalf("other user's list = %d %s", otherList.code, otherList.raw)
	}
	otherRow, _ := otherItems[0].(map[string]any)
	if otherRow["current"] != true || otherRow["id"] == actor.CredID.String() {
		t.Errorf("other user's list exposes the wrong session: %#v", otherRow)
	}

	if r := h.do("DELETE", "/v1/auth/sessions/"+secondID, other, nil, nil); r.code != http.StatusNotFound {
		t.Fatalf("cross-user delete = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", second.body["token"].(string), nil, nil); r.code != http.StatusOK {
		t.Fatalf("cross-user delete revoked the victim: %d", r.code)
	}
	if r := h.do("DELETE", "/v1/auth/sessions/"+actor.CredID.String(), current, nil, nil); r.code != http.StatusBadRequest {
		t.Fatalf("delete current = %d %s, want 400", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", current, nil, nil); r.code != http.StatusOK {
		t.Fatalf("delete current revoked the caller: %d", r.code)
	}
	if r := h.do("DELETE", "/v1/auth/sessions/"+secondID, current, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("delete other own session = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", second.body["token"].(string), nil, nil); r.code != http.StatusUnauthorized {
		t.Fatalf("revoked session still authenticates: %d", r.code)
	}
	assertSessionAudit(t, h, "auth.session.end", actor.UserID.String(), secondID)
}

func TestAuthSessionBearerToBrowserUpdatesClientKind(t *testing.T) {
	h := newHarness(t)
	current := h.adminLogin()
	actor, err := h.authr.Authenticate(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	migrated := h.do("POST", "/v1/auth/browser-session", current, nil, map[string]string{"X-Olivares-Session": "cookie"})
	if migrated.code != http.StatusOK {
		t.Fatalf("browser migration = %d %s", migrated.code, migrated.raw)
	}
	cookie := (&http.Response{Header: migrated.hdr}).Cookies()[0]
	list := h.do("GET", "/v1/auth/sessions", "", nil, map[string]string{"Cookie": cookie.String()})
	if list.code != http.StatusOK {
		t.Fatalf("list after migration = %d %s", list.code, list.raw)
	}
	items, _ := list.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("migration duplicated the session: %s", list.raw)
	}
	row, _ := items[0].(map[string]any)
	if row["id"] != actor.CredID.String() || row["current"] != true || row["kind"] != "browser" {
		t.Errorf("migrated session = %#v, want current browser", row)
	}
	if _, err := time.Parse(time.RFC3339Nano, row["last_used_at"].(string)); err != nil {
		t.Errorf("migrated last_used_at is not RFC3339: %v", err)
	}
}

func TestAdministratorEndsMemberSessionsWithTenantPermissionAndAudit(t *testing.T) {
	h := newHarness(t)
	super := h.adminLogin()
	tenant := h.createOrg(super, "session-admin")
	otherTenant := h.createOrg(super, "session-admin-other")
	admin := h.tenantTokenRole(super, tenant, "admin@example.test", auth.RoleAdmin)
	adminPrincipal, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	viewer := h.tenantTokenRole(super, tenant, "viewer@example.test", auth.RoleViewer)
	member := h.tenantTokenRole(super, tenant, "member@example.test", auth.RoleEditor)
	memberPrincipal, err := h.authr.Authenticate(context.Background(), member)
	if err != nil {
		t.Fatal(err)
	}
	memberSecond := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "member@example.test", "password": "memberpass1",
	}, nil)
	if memberSecond.code != http.StatusOK {
		t.Fatalf("second member login = %d %s", memberSecond.code, memberSecond.raw)
	}
	var accountScope, otherScope, selectedScope model.AuthSession
	for _, fixture := range []struct {
		name  string
		scope model.TenantID
	}{
		{name: "account", scope: ""},
		{name: "other", scope: otherTenant},
		{name: "selected", scope: tenant},
	} {
		credential, err := auth.NewCredential(auth.PrefixSession)
		if fixture.scope != "" {
			credential, err = auth.NewCredential(auth.PrefixScopedSession)
		}
		if err != nil {
			t.Fatal(err)
		}
		row, err := func() (model.AuthSession, error) {
			var created model.AuthSession
			err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
				var err error
				created, err = as.Sessions().Create(context.Background(), model.AuthSession{
					UserID: memberPrincipal.UserID, Selector: credential.Selector, SecretHash: credential.SecretHash,
					ExpiresAt: model.NewTimestamp(time.Now().Add(time.Hour)), TenantScope: fixture.scope,
				})
				return err
			})
			return created, err
		}()
		if err != nil {
			t.Fatal(err)
		}
		switch fixture.name {
		case "account":
			accountScope = row
		case "other":
			otherScope = row
		default:
			selectedScope = row
		}
	}
	outsider := h.tenantTokenRole(super, otherTenant, "outsider@example.test", auth.RoleEditor)
	outsiderPrincipal, err := h.authr.Authenticate(context.Background(), outsider)
	if err != nil {
		t.Fatal(err)
	}
	owner := h.tenantTokenRole(super, tenant, "owner@example.test", auth.RoleOwner)
	ownerPrincipal, err := h.authr.Authenticate(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/users/" + memberPrincipal.UserID.String() + "/sessions"
	if r := h.do("DELETE", path, viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer admin delete = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", member, nil, nil); r.code != http.StatusOK {
		t.Fatalf("viewer revoked the member: %d", r.code)
	}
	if r := h.do("DELETE", "/v1/users/"+outsiderPrincipal.UserID.String()+"/sessions", admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("delete outside tenant = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", outsider, nil, nil); r.code != http.StatusOK {
		t.Fatalf("cross-tenant delete revoked the outsider: %d", r.code)
	}
	h.requirePasskeyStepUp()
	if r := h.do("DELETE", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
		t.Fatalf("unelevated admin delete = %d %s, want 403 step_up_required", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", member, nil, nil); r.code != http.StatusOK {
		t.Fatalf("unelevated admin revoked the member: %d", r.code)
	}
	// The service refuses on its own, not only behind the route's check.
	if err := h.authr.RevokeSessionsInTenant(context.Background(), adminPrincipal, tenant, memberPrincipal.UserID); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("unelevated service call = %v, want ErrStepUpRequired", err)
	}
	h.elevate(admin)
	if r := h.do("DELETE", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("admin delete = %d %s", r.code, r.raw)
	}
	// The member's account sessions stay signed in for the organizations that
	// still admit them; the selected tenant no longer grants them anything.
	for name, token := range map[string]string{"first": member, "second": memberSecond.body["token"].(string)} {
		r := h.do("GET", "/v1/auth/whoami", token, nil, nil)
		if r.code != http.StatusOK {
			t.Fatalf("%s member session = %d %s, want 200 without the tenant", name, r.code, r.raw)
		}
		if grants, _ := r.body["grants"].([]any); len(grants) != 0 {
			t.Fatalf("%s member session still holds %v after administrator revocation", name, grants)
		}
	}
	if r := h.do("DELETE", "/v1/users/"+ownerPrincipal.UserID.String()+"/sessions", admin, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("admin ending an owner's sessions = %d %s, want 403", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", owner, nil, nil); r.code != http.StatusOK {
		t.Fatalf("a refused admin revoked the owner: %d", r.code)
	}
	if r := h.do("DELETE", "/v1/users/"+adminPrincipal.UserID.String()+"/sessions", admin, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("admin ending their own tenant sessions = %d %s, want 400", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/auth/whoami", admin, nil, nil); r.code != http.StatusOK {
		t.Fatalf("a refused self-revoke ended the admin's own session: %d", r.code)
	}
	if err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		if got, err := as.Sessions().Get(context.Background(), selectedScope.ID); err != nil || !got.Revoked {
			t.Errorf("a session scoped to the selected tenant survived (revoked=%v, err=%v)", got.Revoked, err)
		}
		for _, row := range []model.AuthSession{accountScope, otherScope} {
			got, err := as.Sessions().Get(context.Background(), row.ID)
			if err != nil {
				return err
			}
			if got.Revoked {
				t.Errorf("tenant admin revoked out-of-scope session %s", row.ID)
			}
		}
		exclusions, _, err := as.TenantExclusions().List(context.Background(), model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: memberPrincipal.UserID.String()}}, Limit: 100})
		if err != nil {
			return err
		}
		found := false
		for _, exclusion := range exclusions {
			if exclusion.SessionID == accountScope.ID && exclusion.TargetTenantID == tenant && exclusion.Kind == model.ExclusionSession {
				found = true
			}
		}
		if !found {
			t.Error("account-scope session was not excluded from the selected tenant")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertSessionAudit(t, h, "auth.sessions.admin_revoke", adminPrincipal.UserID.String(), memberPrincipal.UserID.String())
}

func assertSessionAudit(t *testing.T, h *harness, action, actorID, targetID string) {
	t.Helper()
	found := false
	if err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		return as.Audit().Walk(context.Background(), 0, func(event model.AuditEvent) error {
			if event.Action == action && event.Actor == "user:"+actorID && (targetID == "" || event.TargetID.String() == targetID) {
				found = true
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Errorf("audit lacks %s by user:%s targeting %s", action, actorID, targetID)
	}
}
