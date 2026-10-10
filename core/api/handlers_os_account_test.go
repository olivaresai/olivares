// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/nativepam"
)

type apiOSAccountNative struct{ calls int }

func (*apiOSAccountNative) UID(context.Context, string) (uint32, error) { return 1234, nil }
func (*apiOSAccountNative) Name(context.Context, uint32) (string, error) {
	return "native-subject", nil
}
func (n *apiOSAccountNative) Verify(_ context.Context, login string, password []byte) (nativepam.Result, error) {
	n.calls++
	defer clear(password)
	if string(password) != "native-proof" {
		return nativepam.Result{}, nativepam.ErrRefused
	}
	return nativepam.Result{Authenticated: true, AccountAllowed: true, Login: login, UID: 1234}, nil
}

func TestOSAccountBindingHTTPRequiresOwnSubjectAndTLS(t *testing.T) {
	n := &apiOSAccountNative{}
	h := newHarnessOpts(t, func(o *api.Options) { o.OSAccounts = n })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "native-binding")
	h.requirePasskeyStepUp()
	h.elevate(admin)
	user := h.do("POST", "/v1/users", admin, map[string]any{"email": "subject@binding.test", "password": "subject-pass1", "tenant": tenant.String(), "role": auth.RoleViewer}, nil)
	if user.code != http.StatusCreated {
		t.Fatalf("subject creation: %d", user.code)
	}
	login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "subject@binding.test", "password": "subject-pass1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("subject login: %d", login.code)
	}
	subject := login.body["token"].(string)
	srv := httptest.NewTLSServer(h.srv.Handler())
	defer srv.Close()
	request := func(method, path, token string, body any) (int, map[string]any, http.Header) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		defer clear(encoded)
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Olivares-Tenant", tenant.String())
		response, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if strings.Contains(string(raw), "native-proof") || strings.Contains(string(raw), "credential_id") || strings.Contains(string(raw), "binding_id") {
			t.Fatal("private credential or secret escaped")
		}
		if response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("binding response cacheable")
		}
		return response.StatusCode, out, response.Header
	}
	begin := map[string]any{"tenant": tenant.String(), "user_id": user.body["id"], "account": "native-subject"}
	if r := h.do("POST", "/v1/auth/os-account-bindings", admin, begin, nil); r.code != http.StatusForbidden {
		t.Fatalf("cleartext: %d", r.code)
	}
	code, out, _ := request("POST", "/v1/auth/os-account-bindings", admin, begin)
	if code != http.StatusOK {
		t.Fatalf("begin: %d %v", code, out)
	}
	complete := map[string]any{"ceremony_id": out["ceremony_id"], "password": []byte("native-proof")}
	if code, _, _ := request("POST", "/v1/auth/os-account-bindings/complete", admin, complete); code != http.StatusForbidden || n.calls != 0 {
		t.Fatalf("admin as subject: %d calls=%d", code, n.calls)
	}
	code, out, _ = request("POST", "/v1/auth/os-account-bindings", admin, begin)
	if code != http.StatusOK {
		t.Fatalf("begin2: %d", code)
	}
	complete["ceremony_id"] = out["ceremony_id"]
	complete["principal"] = map[string]any{"user_id": user.body["id"]}
	if code, _, _ := request("POST", "/v1/auth/os-account-bindings/complete", subject, complete); code != http.StatusBadRequest || n.calls != 0 {
		t.Fatalf("supplied principal: %d", code)
	}
	delete(complete, "principal")
	code, out, _ = request("POST", "/v1/auth/os-account-bindings/complete", subject, complete)
	if code != http.StatusOK || n.calls != 1 {
		t.Fatalf("own proof: %d %v calls=%d", code, out, n.calls)
	}
	if out["uid"] != float64(1234) || out["user_id"] != user.body["id"] || out["digest"] == "" {
		t.Fatalf("public mapping: %v", out)
	}
	path := "/v1/auth/os-account-bindings/" + user.body["id"].(string)
	if code, _, _ := request("GET", path, subject, nil); code != http.StatusForbidden {
		t.Fatalf("subject cannot administer: %d", code)
	}
	if code, _, _ := request("GET", path, admin, nil); code != http.StatusOK {
		t.Fatalf("read: %d", code)
	}
	if code, _, _ := request("DELETE", path, admin, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if code, _, _ := request("GET", path, admin, nil); code != http.StatusNotFound {
		t.Fatalf("read revoked: %d", code)
	}
}
