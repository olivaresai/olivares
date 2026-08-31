// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/sessions"
)

type appsGatewayClock struct{ t time.Time }

func (c *appsGatewayClock) Now() model.Timestamp { return model.NewTimestamp(c.t) }

type fakeSessionCredentialSource struct {
	token string
	now   func() time.Time
	reqs  []sessions.CredentialRequest
}

func (f *fakeSessionCredentialSource) Mint(_ context.Context, req sessions.CredentialRequest) (sessions.Credential, error) {
	f.reqs = append(f.reqs, req)
	return sessions.Credential{
		ID: "cred-device", Token: f.token, Scheme: "test", NotAfter: f.now().Add(time.Hour),
	}, nil
}

type appsGatewayHarness struct {
	proxy      *httptest.Server
	apiHandler http.Handler
	ipx        *inferenceproxy.Module
	tenant     model.TenantID
	adminToken string
	clock      *appsGatewayClock
	creds      *fakeSessionCredentialSource
	fin        *finops.Module
	store      store.Store
}

func newAppsGatewayHarness(t *testing.T) *appsGatewayHarness {
	t.Helper()
	clk := &appsGatewayClock{t: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)}
	ipx := inferenceproxy.New(inferenceproxy.WithClock(clk))
	fin := finops.New()
	st, tenant := provisionAppsGatewayTenant(t, ipx, fin)
	settingsPath := filepath.Join(t.TempDir(), "managed-settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"policy":"managed","tools":{"allow":["Read"]}}`), 0o600); err != nil {
		t.Fatalf("write managed settings: %v", err)
	}
	creds := &fakeSessionCredentialSource{token: "minted-device-token", now: func() time.Time { return clk.t }}
	principal := auth.ScopedPrincipal(model.ID("u1"), "user one", tenant, auth.RoleAdmin)
	handler := newAppsGatewayHandler(
		inferenceProxyConfig{PublicURL: "https://gateway.example.test", ManagedSettingsPath: settingsPath},
		tenant,
		fakeProxyAuthr{p: principal},
		ipx,
		fin,
		creds,
		func() time.Time { return clk.t },
		"test-version",
	)
	mux := http.NewServeMux()
	mountAppsGatewayHandlers(mux, handler)
	mux.Handle("/", appsGatewayRootHandler(http.NotFoundHandler()))
	proxy := httptest.NewServer(mux)
	t.Cleanup(proxy.Close)
	apiHandler, adminToken := newApprovalAPIServer(t, st, ipx)
	return &appsGatewayHarness{
		proxy: proxy, apiHandler: apiHandler, ipx: ipx, tenant: tenant,
		adminToken: adminToken, clock: clk, creds: creds, fin: fin, store: st,
	}
}

func provisionAppsGatewayTenant(t *testing.T, ipx *inferenceproxy.Module, fin *finops.Module) (store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, func(reg store.ExtensionRegistry) error {
		if err := ipx.RegisterSchema(reg); err != nil {
			return err
		}
		return fin.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatalf("open apps-gateway store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		if err == nil {
			tenant = org.TenantID
		}
		return err
	}); err != nil {
		t.Fatalf("provision apps-gateway tenant: %v", err)
	}
	ipx.UseData(api.NewModuleData(st))
	fin.UseData(finopsData{ModuleData: api.NewModuleData(st), st: st})
	return st, tenant
}

func newApprovalAPIServer(t *testing.T, st store.Store, ipx *inferenceproxy.Module) (http.Handler, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("ed25519 key: %v", err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatalf("audit signer: %v", err)
	}
	setupTok := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	authr := auth.NewAuthenticator(st, nil)
	apiSrv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: setupTok, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", Modules: []api.Module{ipx},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	tok, _, err := setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	handler := apiSrv.Handler()
	if code, _ := doJSON(t, handler, "POST", "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "root@example.test", "password": "supersecret1",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	var login struct {
		Token string `json:"token"`
	}
	if code, body := doJSONInto(t, handler, "POST", "/v1/auth/login", "", "", map[string]any{
		"email": "root@example.test", "password": "supersecret1",
	}, &login); code != http.StatusOK || login.Token == "" {
		t.Fatalf("login = %d body=%s", code, string(body))
	}
	principal, err := authr.Authenticate(context.Background(), login.Token)
	if err != nil {
		t.Fatalf("authenticate approval operator: %v", err)
	}
	if _, err := authr.ElevateSession(context.Background(), principal, "webauthn", auth.AAL3); err != nil {
		t.Fatalf("elevate approval operator: %v", err)
	}
	return handler, login.Token
}

func TestAppsGatewaySupersetConformance(t *testing.T) {
	h := newAppsGatewayHarness(t)
	if res, body := doProxy(t, h.proxy.URL, "HEAD", "/", "", nil); res.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD / = %d body=%q", res.StatusCode, string(body))
	}
	var descriptor appsGatewayDescriptor
	getProxyJSON(t, h.proxy.URL, "/protocol", &descriptor)
	if descriptor.Snapshot != "2026-07-10" || len(descriptor.Divergences) < 7 {
		t.Fatalf("descriptor snapshot/divergences = %q / %#v", descriptor.Snapshot, descriptor.Divergences)
	}
	wantEndpoints := []string{
		"/", "/protocol", "/v1/messages", "/v1/messages/batches",
		appsGatewaySpendLimitPath, appsGatewaySpendLimitPath + "/{id}",
		appsGatewaySpendLimitPath + "/effective", appsGatewaySpendLimitPath + "/audit",
		"/.well-known/oauth-authorization-server", "/oauth/device_authorization", "/oauth/token",
		"/managed/settings",
	}
	if !reflect.DeepEqual(descriptor.Endpoints, wantEndpoints) {
		t.Fatalf("descriptor endpoints = %#v, want %#v", descriptor.Endpoints, wantEndpoints)
	}
	var discovery map[string]any
	getProxyJSON(t, h.proxy.URL, "/.well-known/oauth-authorization-server", &discovery)
	if discovery["issuer"] != "https://gateway.example.test" {
		t.Fatalf("issuer = %v", discovery["issuer"])
	}
	var device struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		ExpiresIn  int    `json:"expires_in"`
		Interval   int    `json:"interval"`
	}
	postFormProxy(t, h.proxy.URL, "/oauth/device_authorization", nil, &device)
	if device.DeviceCode == "" || len(device.UserCode) != len("XXXX-XXXX") || device.ExpiresIn != 600 || device.Interval != 5 {
		t.Fatalf("device response = %+v", device)
	}
	assertOAuthError(t, h.proxy.URL, device.DeviceCode, "authorization_pending")
	assertOAuthError(t, h.proxy.URL, device.DeviceCode, "slow_down")
	if code, body := doJSON(t, h.apiHandler, "POST", "/v1/m/inferenceproxy/device/approve", h.adminToken, h.tenant.String(), map[string]any{
		"user_code": device.UserCode,
	}); code != http.StatusOK {
		t.Fatalf("approve = %d body=%s", code, string(body))
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	postToken(t, h.proxy.URL, device.DeviceCode, &token)
	if token.AccessToken != "minted-device-token" || token.TokenType != "Bearer" || token.ExpiresIn <= 0 {
		t.Fatalf("token response = %+v", token)
	}
	if len(h.creds.reqs) != 1 || h.creds.reqs[0].Tenant != h.tenant || !strings.HasPrefix(h.creds.reqs[0].RunRef, "device:") {
		t.Fatalf("mint request = %+v", h.creds.reqs)
	}
	assertOAuthError(t, h.proxy.URL, device.DeviceCode, "invalid_grant")
	headers := map[string]string{"Authorization": "Bearer " + token.AccessToken}
	res, body := doProxy(t, h.proxy.URL, "GET", "/managed/settings", "", headers)
	if res.StatusCode != http.StatusOK || res.Header.Get("ETag") == "" || res.Header.Get(headerOlivaresVersion) != "test-version" {
		t.Fatalf("managed settings status=%d headers=%v body=%s", res.StatusCode, res.Header, string(body))
	}
	etag := res.Header.Get("ETag")
	res, _ = doProxy(t, h.proxy.URL, "GET", "/managed/settings", "", map[string]string{
		"Authorization": "Bearer " + token.AccessToken,
		"If-None-Match": etag,
	})
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("managed settings If-None-Match = %d", res.StatusCode)
	}
	var spend struct {
		Data    []any `json:"data"`
		HasMore bool  `json:"has_more"`
	}
	res, body = doProxy(t, h.proxy.URL, "GET", appsGatewaySpendLimitPath, "", headers)
	if res.StatusCode != http.StatusOK || res.Header.Get(headerSpendRequestID) == "" {
		t.Fatalf("spend list status=%d headers=%v body=%s", res.StatusCode, res.Header, string(body))
	}
	if err := json.Unmarshal(body, &spend); err != nil || len(spend.Data) != 0 || spend.HasMore {
		t.Fatalf("spend list = %+v err=%v body=%s", spend, err, string(body))
	}
}

func TestAppsGatewayExpiredGrant(t *testing.T) {
	h := newAppsGatewayHarness(t)
	past := h.clock.t.Add(-20 * time.Minute)
	if _, err := h.ipx.CreateDeviceGrant(context.Background(), h.tenant, "expired-device", "BCDF-GHJK", past, time.Minute); err != nil {
		t.Fatalf("create expired grant: %v", err)
	}
	assertOAuthError(t, h.proxy.URL, "expired-device", "expired_token")
}

func TestAppsGatewayFeatureOffAndMessagesRegression(t *testing.T) {
	ipx := inferenceproxy.New()
	_, tenant := provisionTenant(t, ipx, "")
	principal := auth.ScopedPrincipal(model.ID("u1"), "user one", tenant, auth.RoleEditor)
	proxy := claudeapi.NewMessagesProxy(nil, nil, nil, nil)
	baseline := http.NewServeMux()
	baseline.Handle("/", proxy)
	withGateway := http.NewServeMux()
	handler := newAppsGatewayHandler(inferenceProxyConfig{}, tenant, fakeProxyAuthr{p: principal}, ipx, nil, nil, time.Now, "test")
	mountAppsGatewayHandlers(withGateway, handler)
	withGateway.Handle("/", appsGatewayRootHandler(proxy))
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/managed/settings"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		withGateway.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s feature-off status = %d, want 404", path, rec.Code)
		}
	}
	for _, path := range []string{"/oauth/device_authorization", "/oauth/token"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		withGateway.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s feature-off status = %d, want 404", path, rec.Code)
		}
	}
	body, _ := json.Marshal(userReq("hello", false))
	baseRec := httptest.NewRecorder()
	gateRec := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	reqB := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	baseline.ServeHTTP(baseRec, reqA)
	withGateway.ServeHTTP(gateRec, reqB)
	if baseRec.Code != gateRec.Code || baseRec.Body.String() != gateRec.Body.String() {
		t.Fatalf("/v1/messages changed: base=%d %s gateway=%d %s", baseRec.Code, baseRec.Body.String(), gateRec.Code, gateRec.Body.String())
	}
}

func TestBudgetDeniesCarryNoRetryHeader(t *testing.T) {
	for _, tc := range []struct {
		action string
		status int
		errTyp string
	}{
		{"block", http.StatusPaymentRequired, "billing_error"},
		{"throttle", http.StatusTooManyRequests, "rate_limit_error"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			a, mg, bg, kg, pol := allowAll()
			bg.bc = finops.BudgetCheck{Allowed: false, Action: tc.action, BudgetID: "b1"}
			proxy := claudeapi.NewMessagesProxy(nil, newTestDecider(a, mg, bg, kg, pol), nil, nil)
			body, _ := json.Marshal(userReq("hello", false))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer caller")
			proxy.ServeHTTP(rec, req)
			if rec.Code != tc.status || rec.Header().Get("x-should-retry") != "false" || !strings.Contains(rec.Body.String(), tc.errTyp) {
				t.Fatalf("deny status=%d retry=%q body=%s", rec.Code, rec.Header().Get("x-should-retry"), rec.Body.String())
			}
		})
	}
}

func TestAppsGatewaySpendLimitsPhase2(t *testing.T) {
	h := newAppsGatewayHarness(t)
	headers := map[string]string{"Authorization": "Bearer admin"}

	post := func(body string) (*http.Response, []byte, finops.SpendLimit) {
		t.Helper()
		res, raw := doProxy(t, h.proxy.URL, http.MethodPost, appsGatewaySpendLimitPath, body, headers)
		var row finops.SpendLimit
		if res.StatusCode == http.StatusOK {
			if err := json.Unmarshal(raw, &row); err != nil {
				t.Fatalf("decode POST: %v body=%s", err, raw)
			}
		}
		return res, raw, row
	}

	res, raw, created := post(`{"scope":{"type":"user","user_id":"user:u1"},"amount":"75000","period":"monthly"}`)
	if res.StatusCode != http.StatusOK || res.Header.Get(headerSpendRequestID) == "" || created.Type != "spend_limit" || !strings.HasPrefix(created.ID, "spl_") || created.Amount == nil || *created.Amount != "75000" || created.Currency != "USD" {
		t.Fatalf("POST status=%d headers=%v row=%+v body=%s", res.StatusCode, res.Header, created, raw)
	}
	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"/"+created.ID, "", headers)
	if res.StatusCode != http.StatusOK || res.Header.Get(headerSpendRequestID) == "" {
		t.Fatalf("GET id status=%d headers=%v body=%s", res.StatusCode, res.Header, raw)
	}
	var got finops.SpendLimit
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("GET id=%+v err=%v want=%+v", got, err, created)
	}

	_, _, org := post(`{"scope":{"type":"organization"},"amount":"100000","period":"monthly"}`)
	if org.ID == "" {
		t.Fatal("organization limit was not created")
	}
	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"?limit=1", "", headers)
	var list struct {
		Data    []finops.SpendLimit `json:"data"`
		HasMore bool                `json:"has_more"`
		FirstID *string             `json:"first_id"`
		LastID  *string             `json:"last_id"`
	}
	if err := json.Unmarshal(raw, &list); res.StatusCode != http.StatusOK || err != nil || len(list.Data) != 1 || !list.HasMore || list.FirstID == nil || list.LastID == nil {
		t.Fatalf("list status=%d value=%+v err=%v body=%s", res.StatusCode, list, err, raw)
	}

	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"/effective?user_ids%5B%5D=user%3Au1&period%5B%5D=monthly", "", headers)
	var effective struct {
		Data     []finops.SpendLimitEffectiveRow `json:"data"`
		NextPage *string                         `json:"next_page"`
	}
	if err := json.Unmarshal(raw, &effective); res.StatusCode != http.StatusOK || err != nil || len(effective.Data) != 1 || effective.NextPage != nil || effective.Data[0].Amount == nil || *effective.Data[0].Amount != "75000" || effective.Data[0].Actor.UserID != "user:u1" {
		t.Fatalf("effective status=%d value=%+v err=%v body=%s", res.StatusCode, effective, err, raw)
	}
	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"/effective?sort=spend_desc", "", headers)
	if res.StatusCode != http.StatusBadRequest || res.Header.Get(headerSpendRequestID) == "" || !strings.Contains(string(raw), `"invalid_request_error"`) {
		t.Fatalf("invalid sort status=%d headers=%v body=%s", res.StatusCode, res.Header, raw)
	}

	_, _, updated := post(`{"scope":{"type":"user","user_id":"user:u1"},"amount":null,"currency":"USD","period":"monthly"}`)
	if updated.ID != created.ID || updated.Amount != nil {
		t.Fatalf("upsert did not replace in place: created=%+v updated=%+v", created, updated)
	}
	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"/audit?limit=2", "", headers)
	var audit struct {
		Data    []finops.SpendLimitAuditEvent `json:"data"`
		HasMore bool                          `json:"has_more"`
	}
	if err := json.Unmarshal(raw, &audit); res.StatusCode != http.StatusOK || err != nil || len(audit.Data) != 2 || !audit.HasMore || audit.Data[0].Action != "update" || audit.Data[0].Before == nil || audit.Data[0].After == nil {
		t.Fatalf("audit status=%d value=%+v err=%v body=%s", res.StatusCode, audit, err, raw)
	}

	res, raw = doProxy(t, h.proxy.URL, http.MethodDelete, appsGatewaySpendLimitPath+"/"+created.ID, "", headers)
	var deleted map[string]string
	_ = json.Unmarshal(raw, &deleted)
	if res.StatusCode != http.StatusOK || deleted["type"] != "spend_limit_deleted" || deleted["id"] != created.ID {
		t.Fatalf("DELETE status=%d value=%v body=%s", res.StatusCode, deleted, raw)
	}
	res, raw = doProxy(t, h.proxy.URL, http.MethodGet, appsGatewaySpendLimitPath+"/"+created.ID, "", headers)
	if res.StatusCode != http.StatusNotFound || !strings.Contains(string(raw), `"not_found_error"`) {
		t.Fatalf("GET deleted status=%d body=%s", res.StatusCode, raw)
	}

	res, raw, _ = post(`{"scope":{"type":"organization"},"amount":"1","currency":"EUR","period":"daily"}`)
	if res.StatusCode != http.StatusBadRequest || res.Header.Get(headerSpendRequestID) == "" || !strings.Contains(string(raw), `"invalid_request_error"`) {
		t.Fatalf("EUR status=%d headers=%v body=%s", res.StatusCode, res.Header, raw)
	}
}

func TestAppsGatewaySpendLimitAuthenticationAndAuthorization(t *testing.T) {
	h := newAppsGatewayHarness(t)
	request := func(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer test")
		handler.ServeHTTP(rec, req)
		return rec
	}
	build := func(authr principalAuthenticator, spend spendLimitAdmin) http.Handler {
		handler := newAppsGatewayHandler(inferenceProxyConfig{}, h.tenant, authr, h.ipx, spend, nil, time.Now, "test")
		mux := http.NewServeMux()
		mountAppsGatewayHandlers(mux, handler)
		return mux
	}
	body := `{"scope":{"type":"organization"},"amount":"1","period":"daily"}`
	editor := auth.ScopedPrincipal(model.ID("editor"), "editor", h.tenant, auth.RoleEditor)
	rec := request(build(fakeProxyAuthr{p: editor}, h.fin), http.MethodPost, appsGatewaySpendLimitPath, body)
	if rec.Code != http.StatusForbidden || rec.Header().Get(headerSpendRequestID) == "" || !strings.Contains(rec.Body.String(), `"permission_error"`) {
		t.Fatalf("editor POST status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	rec = request(build(fakeProxyAuthr{err: errors.New("invalid bearer")}, h.fin), http.MethodGet, appsGatewaySpendLimitPath, "")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get(headerSpendRequestID) == "" || !strings.Contains(rec.Body.String(), `"authentication_error"`) {
		t.Fatalf("invalid bearer status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	admin := auth.ScopedPrincipal(model.ID("admin"), "admin", h.tenant, auth.RoleAdmin)
	rec = request(build(fakeProxyAuthr{p: admin}, nil), http.MethodGet, appsGatewaySpendLimitPath, "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get(headerSpendRequestID) == "" || !strings.Contains(rec.Body.String(), `"api_error"`) {
		t.Fatalf("nil store status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func getProxyJSON(t *testing.T, baseURL, path string, out any) {
	t.Helper()
	res, body := doProxy(t, baseURL, "GET", path, "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d body=%s", path, res.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, string(body))
	}
}

func postFormProxy(t *testing.T, baseURL, path string, form map[string]string, out any) {
	t.Helper()
	values := urlValues(form)
	res, body := doProxy(t, baseURL, "POST", path, values, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST %s = %d body=%s", path, res.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		t.Fatalf("decode %s: %v body=%s", path, err, string(body))
	}
}

func postToken(t *testing.T, baseURL, deviceCode string, out any) {
	t.Helper()
	postFormProxy(t, baseURL, "/oauth/token", map[string]string{
		"grant_type":  deviceGrantType,
		"device_code": deviceCode,
	}, out)
}

func assertOAuthError(t *testing.T, baseURL, deviceCode, want string) {
	t.Helper()
	values := urlValues(map[string]string{"grant_type": deviceGrantType, "device_code": deviceCode})
	res, body := doProxy(t, baseURL, "POST", "/oauth/token", values, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("token error status=%d body=%s", res.StatusCode, string(body))
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode token error: %v body=%s", err, string(body))
	}
	if got["error"] != want {
		t.Fatalf("token error = %q, want %q body=%s", got["error"], want, string(body))
	}
}

func doProxy(t *testing.T, baseURL, method, path, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, baseURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return res, out
}

func doJSON(t *testing.T, h http.Handler, method, path, token, tenant string, body any) (int, []byte) {
	t.Helper()
	code, raw := doJSONInto(t, h, method, path, token, tenant, body, nil)
	return code, raw
}

func doJSONInto(t *testing.T, h http.Handler, method, path, token, tenant string, body any, out any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant != "" {
		req.Header.Set("X-Olivares-Tenant", tenant)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw := rec.Body.Bytes()
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode response: %v body=%s", err, string(raw))
		}
	}
	return rec.Code, raw
}

func urlValues(in map[string]string) string {
	values := url.Values{}
	for k, v := range in {
		values.Set(k, v)
	}
	return values.Encode()
}
