// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAccountPasswordCookieAndRefusals(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	in := map[string]any{"current_password": "supersecret1", "new_password": "replacement-secret1"}
	if got := h.do("POST", "/v1/account/password", "", in, nil); got.code != 401 {
		t.Fatalf("anonymous = %d %s", got.code, got.raw)
	}
	token := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "api", "superadmin": true}, nil)
	if token.code != 201 {
		t.Fatalf("issue token = %d %s", token.code, token.raw)
	}
	apiToken := token.body["token"].(string)
	if got := h.do("POST", "/v1/account/password", apiToken, in, nil); got.code != 403 || got.body["error"].(map[string]any)["message"] != "Sign in with your account to change your password." {
		t.Fatalf("API token = %d %s", got.code, got.raw)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, map[string]string{"X-Olivares-Session": "cookie"})
	if login.code != 200 {
		t.Fatalf("cookie login = %d %s", login.code, login.raw)
	}
	cookie := (&http.Response{Header: login.hdr}).Cookies()[0]
	headers := map[string]string{"Cookie": cookie.String()}
	if got := h.do("POST", "/v1/account/password", "", in, headers); got.code != 403 {
		t.Fatalf("missing CSRF = %d %s", got.code, got.raw)
	}
	headers["X-CSRF-Token"] = login.body["csrf_token"].(string)
	headers["Origin"] = "https://attacker.example"
	if got := h.do("POST", "/v1/account/password", "", in, headers); got.code != 403 {
		t.Fatalf("cross-origin = %d %s", got.code, got.raw)
	}
	delete(headers, "Origin")
	if got := h.do("POST", "/v1/account/password", "", map[string]any{"current_password": "wrong", "new_password": "replacement-secret1"}, headers); got.code != 400 {
		t.Fatalf("wrong current password = %d %s", got.code, got.raw)
	}
	if got := h.do("GET", "/v1/auth/browser-session", "", nil, headers); got.code != 200 {
		t.Fatalf("wrong password logged the person out = %d %s", got.code, got.raw)
	}
	if got := h.do("POST", "/v1/account/password", "", in, headers); got.code != 204 || got.hdr.Get("Set-Cookie") != "" {
		t.Fatalf("cookie password change = %d %s", got.code, got.raw)
	}
	if got := h.do("GET", "/v1/auth/browser-session", "", nil, headers); got.code != 200 || got.body["session_id"] != login.body["session_id"] || got.body["expires_at"] != login.body["expires_at"] {
		t.Fatal("current cookie session or expiry changed")
	}
	if _, err := h.authr.Authenticate(context.Background(), apiToken); err != nil {
		t.Fatalf("password change revoked a separate API token: %v", err)
	}
}

func TestAccountPasswordThrottle(t *testing.T) {
	h := newHarness(t)
	current := h.adminLogin()
	for i := 0; i < 5; i++ {
		got := h.do("POST", "/v1/account/password", current, map[string]any{"current_password": "wrong", "new_password": "replacement-secret1"}, nil)
		if got.code != 400 {
			t.Fatalf("failed attempt %d = %d %s", i, got.code, got.raw)
		}
	}
	got := h.do("POST", "/v1/account/password", current, map[string]any{"current_password": "supersecret1", "new_password": "replacement-secret1"}, nil)
	if got.code != 429 || !strings.Contains(got.raw, "Too many attempts. Try again later.") {
		t.Fatalf("password throttle = %d %s", got.code, got.raw)
	}
	if _, _, err := h.authr.Login(context.Background(), "root@x.io", "supersecret1", "another-peer"); err != auth.ErrLockedOut {
		t.Fatalf("password change did not share sign-in's account throttle: %v", err)
	}
	if _, err := h.authr.Authenticate(context.Background(), current); err != nil {
		t.Fatal("failed password changes invalidated the current session")
	}
}

func TestAccountPasswordSQLite(t *testing.T) { testAccountPassword(t, newHarness(t)) }

func TestAccountPasswordPostgres(t *testing.T) {
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var privileged bool
	if err := db.QueryRow(`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged); err != nil {
		t.Fatal(err)
	}
	if privileged {
		t.Fatal("password change must run through the unprivileged application role")
	}
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(context.Background()); return err }); err != nil {
		t.Fatal(err)
	}
	testAccountPassword(t, newHarnessOptsFromStoreSource(t, harnessStoreSource{borrowed: st}, nil))
}

func testAccountPassword(t *testing.T, h *harness) {
	ctx := context.Background()
	current := h.adminLogin()
	p, err := h.authr.Authenticate(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	var before model.AuthSession
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		before, err = as.Sessions().Get(ctx, p.CredID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sibling, _, err := h.authr.Login(ctx, "root@x.io", "supersecret1", "other-peer")
	if err != nil {
		t.Fatal(err)
	}
	u, _, err := h.authr.CreateUserWithMembership(ctx, p, auth.NewUser{Email: "other@example.test", Password: "other-secret1"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := h.authr.Login(ctx, u.Email, "other-secret1", "other-peer")
	if err != nil {
		t.Fatal(err)
	}
	change := func(old, next string) resp {
		return h.do("POST", "/v1/account/password", current, map[string]any{"current_password": old, "new_password": next}, nil)
	}
	if got := change("wrong-password", "replacement-secret1"); got.code != 400 || got.body["error"].(map[string]any)["message"] != "The current password is incorrect." {
		t.Fatalf("wrong current password = %d %s", got.code, got.raw)
	}
	if got := change("supersecret1", "short"); got.code != 400 {
		t.Fatalf("weak password = %d %s", got.code, got.raw)
	}
	if got := change("supersecret1", "replacement-secret1"); got.code != http.StatusNoContent || got.raw != "" {
		t.Fatalf("password change = %d %s", got.code, got.raw)
	}
	for name, token := range map[string]string{"current": current, "other user": other} {
		if _, err := h.authr.Authenticate(ctx, token); err != nil {
			t.Fatalf("%s session stopped: %v", name, err)
		}
	}
	if _, err := h.authr.Authenticate(ctx, sibling); err == nil {
		t.Fatal("sibling session survived password change")
	}
	if _, _, err := h.authr.Login(ctx, "root@x.io", "supersecret1", "fresh-peer"); err == nil {
		t.Fatal("old password still signs in")
	}
	if _, _, err := h.authr.Login(ctx, "root@x.io", "replacement-secret1", "fresh-peer"); err != nil {
		t.Fatalf("new password refused: %v", err)
	}
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		after, err := as.Sessions().Get(ctx, p.CredID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("password change altered the current session")
		}
		var events []model.AuditEvent
		if err := as.Audit().Walk(ctx, 0, func(event model.AuditEvent) error {
			if event.Action == "account.password.changed" {
				events = append(events, event)
			}
			return nil
		}); err != nil {
			return err
		}
		if len(events) != 1 || events[0].Actor != p.Actor() || events[0].TargetID != p.UserID {
			t.Fatalf("password-change audit does not name the acting user: %+v", events)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Self-service is available without an administrator role or membership.
	if got := h.do("POST", "/v1/account/password", other, map[string]any{"current_password": "other-secret1", "new_password": "other-replacement1"}, nil); got.code != 204 {
		t.Fatalf("non-administrator's own password change = %d %s", got.code, got.raw)
	}
	if _, _, err := h.authr.Login(ctx, u.Email, "other-replacement1", "other-peer"); err != nil {
		t.Fatalf("non-administrator's new password refused: %v", err)
	}
	if _, err := h.authr.Authenticate(ctx, current); err != nil {
		t.Fatal("another account's password change ended the administrator's session")
	}
}
