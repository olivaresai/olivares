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
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const totpBoundaryPassword = "supersecret1"

// A tenant administrator must not enumerate or remove another tenant's factor.
func TestTOTPAdminCannotReachForeignAccount(t *testing.T) {
	h := newHarness(t)
	do := totpHTTPClient(t, h)
	wireTOTPSealer(t, h)
	root := h.adminLogin()
	own := h.createOrg(root, "totp-own")
	foreign := h.createOrg(root, "totp-foreign")
	create := func(email string, tenant model.TenantID, role string) string {
		t.Helper()
		r := do("POST", "/v1/users", root, map[string]any{"email": email, "password": totpBoundaryPassword, "tenant": tenant.String(), "role": role}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create: %d", r.code)
		}
		r = do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": totpBoundaryPassword}, nil)
		if r.code != http.StatusOK {
			t.Fatalf("login: %d", r.code)
		}
		return r.body["token"].(string)
	}
	boss := create("boss@boundary.test", own, auth.RoleAdmin)
	victim := create("victim@boundary.test", foreign, auth.RoleEditor)
	enrol := do("POST", "/v1/auth/totp/enrol", victim, map[string]any{}, nil)
	if enrol.code != http.StatusOK {
		t.Fatalf("enrol: %d", enrol.code)
	}
	code := apiTOTPCode(t, enrol.body["secret"].(string), time.Now())
	if r := do("POST", "/v1/auth/totp/activate", victim, map[string]any{"code": code}, nil); r.code != http.StatusOK {
		t.Fatalf("activate: %d", r.code)
	}
	id := userID(t, h, victim)
	h.elevate(boss)
	t.Run("status", func(t *testing.T) {
		r := do("GET", "/v1/users/"+id+"/totp", boss, nil, tenantHdr(own))
		if r.code != http.StatusNotFound {
			t.Fatalf("foreign factor disclosed: got %d, want 404", r.code)
		}
	})
	t.Run("reset", func(t *testing.T) {
		r := do("POST", "/v1/users/"+id+"/totp/reset", boss, map[string]any{}, tenantHdr(own))
		if r.code != http.StatusNotFound {
			t.Errorf("foreign factor reset admitted: got %d, want 404", r.code)
		}
		r = do("GET", "/v1/auth/totp/status", victim, nil, nil)
		if r.code != http.StatusOK || r.body["enrolled"] != true {
			t.Fatal("foreign factor was removed")
		}
	})
}

func TestTOTPAdminCannotResetSharedOrHolderAccount(t *testing.T) {
	for _, mode := range []string{"shared", "holder"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			do := totpHTTPClient(t, h)
			wireTOTPSealer(t, h)
			root := h.adminLogin()
			own := h.createOrg(root, "protected-own")
			other := h.createOrg(root, "protected-other")
			create := func(email, role string) string {
				t.Helper()
				r := do("POST", "/v1/users", root, map[string]any{"email": email, "password": totpBoundaryPassword, "tenant": own.String(), "role": role}, nil)
				if r.code != http.StatusCreated {
					t.Fatalf("create: %d", r.code)
				}
				r = do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": totpBoundaryPassword}, nil)
				if r.code != http.StatusOK {
					t.Fatalf("login: %d", r.code)
				}
				return r.body["token"].(string)
			}
			boss := create("boss@protected.test", auth.RoleAdmin)
			victim := create("victim@protected.test", auth.RoleEditor)
			id := model.ID(userID(t, h, victim))
			enrol := do("POST", "/v1/auth/totp/enrol", victim, map[string]any{}, nil)
			if enrol.code != http.StatusOK {
				t.Fatalf("enrol: %d", enrol.code)
			}
			r := do("POST", "/v1/auth/totp/activate", victim, map[string]any{"code": apiTOTPCode(t, enrol.body["secret"].(string), time.Now())}, nil)
			if r.code != http.StatusOK {
				t.Fatalf("activate: %d", r.code)
			}
			if err := h.st.AuthMutate(context.Background(), func(as store.AuthScope) error {
				if mode == "shared" {
					_, err := as.Memberships().Create(context.Background(), model.Membership{UserID: id, TargetTenantID: other, Role: auth.RoleViewer})
					return err
				}
				u, err := as.Users().Get(context.Background(), id)
				if err != nil {
					return err
				}
				u.CredentialCustody = model.CustodyHolder
				u.CustodyTenantID = ""
				_, err = as.Users().Update(context.Background(), u)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			h.elevate(boss)
			r = do("POST", "/v1/users/"+id.String()+"/totp/reset", boss, map[string]any{}, tenantHdr(own))
			if r.code != http.StatusForbidden {
				t.Errorf("%s reset admitted: got %d, want403", mode, r.code)
			}
			r = do("GET", "/v1/auth/totp/status", victim, nil, nil)
			if r.code != http.StatusOK || r.body["enrolled"] != true {
				t.Fatal("protected factor removed")
			}

			// A deployment superadmin retains recovery authority for a member.
			h.elevate(root)
			r = do("POST", "/v1/users/"+id.String()+"/totp/reset", root, map[string]any{}, tenantHdr(own))
			if r.code != http.StatusOK {
				t.Fatalf("deployment recovery refused: %d", r.code)
			}
			r = do("GET", "/v1/auth/totp/status", victim, nil, nil)
			if r.code != http.StatusOK || r.body["enrolled"] != false {
				t.Fatal("deployment recovery did not remove factor")
			}
		})
	}
}

// Exercise the native router, bearer authentication and authorization over HTTP.
func totpHTTPClient(t *testing.T, h *harness) func(string, string, string, any, map[string]string) resp {
	t.Helper()
	srv := httptest.NewServer(h.srv.Handler())
	t.Cleanup(srv.Close)
	return func(method, path, token string, body any, headers map[string]string) resp {
		t.Helper()
		var payload []byte
		if body != nil {
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal("encode fixture body")
			}
		}
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(payload))
		if err != nil {
			t.Fatal("create fixture request")
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		out, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal("fixture HTTP request failed")
		}
		defer out.Body.Close()
		data, err := io.ReadAll(out.Body)
		if err != nil {
			t.Fatal("read fixture response")
		}
		r := resp{code: out.StatusCode, hdr: out.Header}
		_ = json.Unmarshal(data, &r.body)
		return r
	}
}
