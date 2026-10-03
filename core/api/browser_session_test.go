// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestBrowserSessionLoginCookie(t *testing.T) {
	h := newHarness(t)
	h.adminLogin()
	r := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": strings.Repeat("supersecret", 1) + "1",
	}, map[string]string{"X-Olivares-Session": "cookie"})
	if r.code != http.StatusOK {
		t.Fatalf("login status = %d", r.code)
	}
	if _, exposed := r.body["token"]; exposed {
		t.Fatal("browser login exposed a bearer in JSON")
	}
	if csrf, _ := r.body["csrf_token"].(string); csrf == "" {
		t.Fatal("browser login has no CSRF token")
	}
	cookies := (&http.Response{Header: r.hdr}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookie count = %d", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" {
		t.Fatal("browser session cookie is not host-only, HttpOnly, Secure and Strict")
	}
	if c.Expires.IsZero() {
		t.Fatal("cookie has no server-session expiry")
	}
}

// Keep the original browser login wire contract while modules call the exported seam.
func TestBrowserSessionSharedEnvelopePreservesLoginResponse(t *testing.T) {
	h := newHarness(t)
	h.adminLogin()
	r := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "root@x.io", "password": "supersecret1",
	}, map[string]string{"X-Olivares-Session": "cookie"})
	if r.code != http.StatusOK {
		t.Fatalf("login status = %d", r.code)
	}
	cookies := (&http.Response{Header: r.hdr}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookie count = %d", len(cookies))
	}
	ctx := context.Background()
	principal, err := h.authr.Authenticate(ctx, cookies[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	var sess model.AuthSession
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		sess, err = as.Sessions().Get(ctx, principal.CredID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// This is the pre-export contract, independent of SessionEnvelope.
	sum := sha256.Sum256([]byte("olivares-browser-csrf-v1\x00" + cookies[0].Value))
	want, err := json.Marshal(map[string]any{
		"csrf_token": base64.RawURLEncoding.EncodeToString(sum[:]),
		"session_id": sess.ID.String(), "expires_at": sess.ExpiresAt.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCookie := (&http.Cookie{
		Name: "__Host-olivares-session", Value: cookies[0].Value, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteStrictMode, Expires: sess.ExpiresAt.Time(),
	}).String()
	if r.raw != string(want)+"\n" || r.hdr.Get("Set-Cookie") != wantCookie {
		t.Fatal("Community browser login changed its response or cookie bytes")
	}
	req := httptest.NewRequest(http.MethodPost, "https://console.example/v1/module-login", nil)
	req.Header.Set("X-Olivares-Session", "cookie")
	req.Header.Set("Origin", "https://console.example")
	if !api.BrowserSameOrigin(req) {
		t.Fatal("shared origin check refused same-origin module sign-in")
	}
	rec := httptest.NewRecorder()
	body, err := json.Marshal(api.SessionEnvelope(rec, req, cookies[0].Value, sess))
	if err != nil || string(body) != string(want) || rec.Header().Get("Set-Cookie") != wantCookie {
		t.Fatal("module seam differs from Community browser login")
	}
	req.Header.Set("Origin", "https://attacker.example")
	if api.BrowserSameOrigin(req) {
		t.Fatal("shared origin check admitted cross-origin module sign-in")
	}
}

func TestBrowserSessionAcceptInviteIgnoresStaleCookie(t *testing.T) {
	mail := &capturingInviteSender{}
	h := newHarnessOpts(t, func(o *api.Options) { o.InviteSender = mail })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "cookie-invite")
	h.elevate(admin)
	r := h.do("POST", "/v1/onboard", admin, map[string]any{
		"email": "cookie-invitee@example.test", "role": auth.RoleViewer, "mode": "invite",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated || len(mail.all()) != 1 {
		t.Fatalf("invite creation status = %d", r.code)
	}
	body := map[string]any{"token": mail.all()[0].token(), "password": "fixture-password"}
	headers := map[string]string{
		"X-Olivares-Session": "cookie", "Cookie": "__Host-olivares-session=revoked-credential",
		"Origin": "https://attacker.example",
	}
	if denied := h.do("POST", "/v1/invites/accept", "", body, headers); denied.code != http.StatusForbidden {
		t.Fatalf("cross-origin invite status = %d", denied.code)
	}
	delete(headers, "Origin")
	r = h.do("POST", "/v1/invites/accept", "", body, headers)
	if r.code != http.StatusOK {
		t.Fatalf("invite redemption with stale cookie status = %d", r.code)
	}
	if _, exposed := r.body["token"]; exposed || r.body["csrf_token"] == nil {
		t.Fatal("browser invite redemption did not return only CSRF session metadata")
	}
	cookies := (&http.Response{Header: r.hdr}).Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("browser invite redemption did not set a protected session cookie")
	}
	if live := h.do("GET", "/v1/auth/whoami", "", nil, map[string]string{"Cookie": cookies[0].String()}); live.code != http.StatusOK {
		t.Fatalf("invite cookie whoami status = %d", live.code)
	}
	if again := h.do("POST", "/v1/invites/accept", "", body, headers); again.code != http.StatusBadRequest {
		t.Fatalf("second invite redemption status = %d", again.code)
	}
}

func TestBrowserSessionSSORedirect(t *testing.T) {
	for _, destination := range []string{"//attacker.example", "/\\attacker.example", "https://attacker.example"} {
		t.Run(destination, func(t *testing.T) {
			h := newFedHarness(t, &fakeFed{proto: auth.ProtocolOIDC})
			h.adminLogin()
			start := h.raw("GET", "/v1/auth/federation/start?browser_session=1&return_to="+url.QueryEscape(destination), nil)
			location, err := url.Parse(start.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			flow := findCookie(start.Result().Cookies(), "olv_sso")
			if flow == nil {
				t.Fatal("missing SSO correlation cookie")
			}
			callback := h.raw("GET", "/v1/auth/federation/callback?code=good-code&state="+url.QueryEscape(location.Query().Get("state")), []*http.Cookie{flow})
			if callback.Code != http.StatusSeeOther || callback.Header().Get("Location") != "/" {
				t.Fatalf("SSO browser callback status = %d, destination = %s", callback.Code, callback.Header().Get("Location"))
			}
			cookie := findCookie(callback.Result().Cookies(), "__Host-olivares-session")
			if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatal("SSO browser callback did not set protected session cookie")
			}
			if strings.Contains(callback.Body.String(), cookie.Value) {
				t.Fatal("SSO bearer leaked in redirect body")
			}
		})
	}
}

func TestBrowserSessionCookieTokenExchange(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	principal, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	password := strings.Repeat("s", 12)
	if _, err := h.authr.CreateUser(context.Background(), principal, auth.NewUser{
		Email: "cookie-editor@example.test", Password: password, Tenant: principal.Tenants()[0], Role: auth.RoleEditor,
	}); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := h.authr.Login(context.Background(), "cookie-editor@example.test", password, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/v1/auth/browser-session", legacy, nil, map[string]string{"X-Olivares-Session": "cookie"})
	if r.code != 200 {
		t.Fatalf("migration status = %d", r.code)
	}
	cookie := (&http.Response{Header: r.hdr}).Cookies()[0]
	form := url.Values{
		"grant_type": {auth.GrantTypeTokenExchange}, "subject_token": {"browser-session"},
		"subject_token_type": {auth.TokenTypeAccessToken}, "scope": {"read"},
		"resource": {"https://mcp.example.com/github"},
	}
	request := httptest.NewRequest("POST", "/v1/auth/token-exchange", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRF-Token", r.body["csrf_token"].(string))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Fatalf("cookie exchange status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBrowserSessionMigrationCSRFRefreshLogout(t *testing.T) {
	h := newHarness(t)
	legacy := h.adminLogin()
	p, err := h.authr.Authenticate(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	var original model.AuthSession
	if err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		var err error
		original, err = as.Sessions().Get(context.Background(), p.CredID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.do("POST", "/v1/auth/browser-session", "", nil, nil); got.code != 401 {
		t.Fatalf("anonymous migration status = %d", got.code)
	}
	r := h.do("POST", "/v1/auth/browser-session", legacy, nil, map[string]string{"X-Olivares-Session": "cookie"})
	if r.code != 200 {
		t.Fatalf("migration status = %d", r.code)
	}
	if _, ok := r.body["token"]; ok {
		t.Fatal("migration exposed bearer")
	}
	if r.body["session_id"] != p.CredID.String() || r.body["expires_at"] != original.ExpiresAt.String() {
		t.Fatal("migration changed session identity or expiry")
	}
	if _, _, err := h.authr.MigrateBrowserSession(context.Background(), p); err == nil {
		t.Fatal("an in-flight stale principal migrated the retired bearer again")
	}
	cookie := (&http.Response{Header: r.hdr}).Cookies()[0]
	csrf := r.body["csrf_token"].(string)
	if got := h.do("GET", "/v1/auth/whoami", legacy, nil, nil); got.code != 401 {
		t.Fatalf("retired bearer status = %d", got.code)
	}
	hdr := map[string]string{"Cookie": cookie.String()}
	if got := h.do("GET", "/v1/auth/browser-session", "", nil, hdr); got.code != 200 || got.body["csrf_token"] != csrf {
		t.Fatalf("reload metadata status = %d", got.code)
	}
	for _, attack := range []map[string]string{
		{"Cookie": cookie.String()},
		{"Cookie": cookie.String(), "X-CSRF-Token": "wrong"},
		{"Cookie": cookie.String(), "X-CSRF-Token": csrf, "Origin": "https://attacker.example"},
		{"Cookie": cookie.String(), "X-CSRF-Token": csrf, "Sec-Fetch-Site": "same-site"},
	} {
		if got := h.do("POST", "/v1/auth/logout", "", nil, attack); got.code != 403 {
			t.Fatalf("CSRF logout status = %d", got.code)
		}
	}
	if got := h.do("GET", "/v1/auth/whoami", "", nil, hdr); got.code != 200 {
		t.Fatal("CSRF attack revoked session")
	}
	hdr["X-CSRF-Token"] = csrf
	r = h.do("POST", "/v1/auth/refresh", "", nil, hdr)
	if r.code != 200 {
		t.Fatalf("cookie renewal status = %d", r.code)
	}
	if r.body["csrf_token"] == csrf {
		t.Fatal("renewal did not rotate CSRF")
	}
	if got := h.do("GET", "/v1/auth/whoami", "", nil, hdr); got.code != 401 {
		t.Fatal("old cookie survived rotation")
	}
	cookie = (&http.Response{Header: r.hdr}).Cookies()[0]
	hdr["Cookie"], hdr["X-CSRF-Token"] = cookie.String(), r.body["csrf_token"].(string)
	r = h.do("POST", "/v1/auth/logout", "", nil, hdr)
	if r.code != 204 {
		t.Fatalf("logout status = %d", r.code)
	}
	if cookies := (&http.Response{Header: r.hdr}).Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal("logout did not delete cookie")
	}
	if got := h.do("GET", "/v1/auth/whoami", "", nil, hdr); got.code != 401 {
		t.Fatal("logged-out cookie authenticates")
	}
	var actions []string
	if err := h.st.AuthView(context.Background(), func(as store.AuthScope) error {
		return as.Audit().Walk(context.Background(), 0, func(row model.AuditEvent) error {
			actions = append(actions, row.Action)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"auth.session.cookie", "auth.refresh", "auth.logout"} {
		if !strings.Contains(strings.Join(actions, "\n"), action) {
			t.Fatalf("missing audit action %s", action)
		}
	}
}

type browserSessionClock struct{ now time.Time }

func (c *browserSessionClock) Now() model.Timestamp { return model.NewTimestamp(c.now) }

func TestBrowserSessionExpiryAndLoginRecovery(t *testing.T) {
	clock := &browserSessionClock{now: time.Now().UTC()}
	h := newHarnessOpts(t, func(o *api.Options) { o.Authenticator = auth.NewAuthenticator(o.Store, clock) })
	h.adminLogin()
	login := func(hdr map[string]string) resp {
		hdr["X-Olivares-Session"] = "cookie"
		return h.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": strings.Repeat("supersecret", 1) + "1"}, hdr)
	}
	r := login(map[string]string{})
	if r.code != 200 {
		t.Fatalf("login status = %d", r.code)
	}
	cookie := (&http.Response{Header: r.hdr}).Cookies()[0]
	clock.now = clock.now.Add(auth.DefaultSessionTTL + time.Second)
	hdr := map[string]string{"Cookie": cookie.String(), "X-CSRF-Token": r.body["csrf_token"].(string)}
	if got := h.do("POST", "/v1/auth/refresh", "", nil, hdr); got.code != 401 {
		t.Fatal("expiry resurrected session")
	}
	if got := login(hdr); got.code != 200 {
		t.Fatalf("expired cookie blocked login: %d", got.code)
	}
	if got := login(map[string]string{"Origin": "https://attacker.example"}); got.code != 403 {
		t.Fatalf("login CSRF status = %d", got.code)
	}
}

func TestBrowserSessionTOTPCompletion(t *testing.T) {
	h := newHarness(t)
	wireTOTPSealer(t, h)
	admin := h.adminLogin()
	migrated := h.do("POST", "/v1/auth/browser-session", admin, nil, map[string]string{"X-Olivares-Session": "cookie"})
	if migrated.code != 200 {
		t.Fatalf("migration status = %d", migrated.code)
	}
	cookie := (&http.Response{Header: migrated.hdr}).Cookies()[0]
	hdr := map[string]string{"Cookie": cookie.String(), "X-CSRF-Token": migrated.body["csrf_token"].(string)}
	enrol := h.do("POST", "/v1/auth/totp/enrol", "", map[string]any{}, hdr)
	if enrol.code != 200 {
		t.Fatalf("cookie enrolment status = %d", enrol.code)
	}
	secret := enrol.body["secret"].(string)
	activated := h.do("POST", "/v1/auth/totp/activate", "", map[string]any{"code": apiTOTPCode(t, secret, time.Now())}, hdr)
	if activated.code != 200 {
		t.Fatalf("cookie activation status = %d", activated.code)
	}
	browser := map[string]string{"X-Olivares-Session": "cookie", "Cookie": "__Host-olivares-session=expired"}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, browser)
	if login.code != 200 || login.body["mfa_required"] != true {
		t.Fatalf("gated browser login status = %d", login.code)
	}
	complete := h.do("POST", "/v1/auth/totp/challenge", "", map[string]any{"mfa_token": login.body["mfa_token"], "code": apiTOTPCode(t, secret, time.Now())}, browser)
	if complete.code != 200 || complete.body["csrf_token"] == nil || complete.body["token"] != nil {
		t.Fatalf("browser challenge completion status = %d", complete.code)
	}
	cookies := (&http.Response{Header: complete.hdr}).Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatal("TOTP completion did not protect the browser credential")
	}
	if who := h.do("GET", "/v1/auth/whoami", "", nil, map[string]string{"Cookie": cookies[0].String()}); who.code != 200 {
		t.Fatalf("completed cookie status = %d", who.code)
	}
}

func TestBrowserSessionRequiredTOTPEnrolment(t *testing.T) {
	h := newHarness(t)
	wireTOTPSealer(t, h)
	admin := h.adminLogin()
	h.elevate(admin)
	if policy := h.do("PUT", "/v1/auth/totp/policy", admin, map[string]any{"require_for_admins": true}, nil); policy.code != 200 {
		t.Fatalf("policy status = %d", policy.code)
	}
	browser := map[string]string{"X-Olivares-Session": "cookie", "Cookie": "__Host-olivares-session=expired"}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": harnessAdminEmail, "password": harnessAdminPass}, browser)
	if login.code != 200 || login.body["enrolment_required"] != true {
		t.Fatalf("pending browser enrolment status = %d", login.code)
	}
	pending := login.body["mfa_token"]
	enrol := h.do("POST", "/v1/auth/totp/enrol", "", map[string]any{"mfa_token": pending}, browser)
	if enrol.code != 200 {
		t.Fatalf("pending enrolment with expired cookie = %d", enrol.code)
	}
	activate := h.do("POST", "/v1/auth/totp/activate", "", map[string]any{"mfa_token": pending, "code": apiTOTPCode(t, enrol.body["secret"].(string), time.Now())}, browser)
	if activate.code != 200 || activate.body["csrf_token"] == nil || activate.body["token"] != nil {
		t.Fatalf("pending cookie activation status = %d", activate.code)
	}
	if len(activate.body["recovery_codes"].([]any)) != 10 {
		t.Fatal("pending activation lost recovery codes")
	}
	if cookies := (&http.Response{Header: activate.hdr}).Cookies(); len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatal("pending activation exposed browser credential")
	}
}
