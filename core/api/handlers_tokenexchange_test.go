// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// formResp captures the status, JSON body and headers of a form POST.
type formResp struct {
	code int
	body map[string]any
	raw  string
	hdr  http.Header
}

func (h *harness) postForm(path, token string, form url.Values) formResp {
	h.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := formResp{code: rec.Code, raw: rec.Body.String(), hdr: rec.Header()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// issueEditorToken provisions an editor user in tenant and returns a bound
// editor API token string.
func (h *harness) issueEditorToken(super auth.Principal, tenant model.TenantID, email string) string {
	h.t.Helper()
	ctx := context.Background()
	u, err := h.authr.CreateUser(ctx, super, auth.NewUser{Email: email, Password: "editor-pass-1"})
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.authr.GrantMembership(ctx, super, u.ID, tenant, auth.RoleEditor, model.ID("")); err != nil {
		h.t.Fatal(err)
	}
	tok, _, err := h.authr.IssueToken(ctx, super, auth.TokenSpec{Name: "editor", BoundTenant: tenant, Role: auth.RoleEditor})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}

func TestTokenExchangeEndpoint(t *testing.T) {
	h := newHarness(t)
	adminTok := h.adminLogin()
	tenant := h.createOrg(adminTok, "acme")
	super, err := h.authr.Authenticate(context.Background(), adminTok)
	if err != nil {
		t.Fatal(err)
	}
	editorTok := h.issueEditorToken(super, tenant, "editor@acme.com")

	t.Run("success", func(t *testing.T) {
		form := url.Values{}
		form.Set("grant_type", auth.GrantTypeTokenExchange)
		form.Set("subject_token", editorTok)
		form.Set("subject_token_type", auth.TokenTypeAccessToken)
		form.Set("scope", "read")
		form.Add("resource", "https://mcp.example.com/github")
		r := h.postForm("/v1/auth/token-exchange", editorTok, form)
		if r.code != http.StatusOK {
			t.Fatalf("exchange = %d %s", r.code, r.raw)
		}
		if at, _ := r.body["access_token"].(string); !strings.HasPrefix(at, auth.PrefixToken+"_") {
			t.Errorf("access_token = %q, want an opaque olvk_ token", at)
		}
		if r.body["token_type"] != "Bearer" {
			t.Errorf("token_type = %v, want Bearer", r.body["token_type"])
		}
		if r.body["issued_token_type"] != auth.TokenTypeAccessToken {
			t.Errorf("issued_token_type = %v", r.body["issued_token_type"])
		}
		if r.body["scope"] != "read" {
			t.Errorf("scope = %v, want read", r.body["scope"])
		}
		if cc := r.hdr.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", cc)
		}
	})

	t.Run("anonymous caller rejected", func(t *testing.T) {
		form := url.Values{}
		form.Set("grant_type", auth.GrantTypeTokenExchange)
		form.Set("subject_token", editorTok)
		form.Set("subject_token_type", auth.TokenTypeAccessToken)
		r := h.postForm("/v1/auth/token-exchange", "", form)
		if r.code != http.StatusUnauthorized || r.body["error"] != "invalid_client" {
			t.Errorf("anonymous = %d %v, want 401 invalid_client", r.code, r.body["error"])
		}
	})

	t.Run("wrong grant_type", func(t *testing.T) {
		form := url.Values{}
		form.Set("grant_type", "authorization_code")
		form.Set("subject_token", editorTok)
		form.Set("subject_token_type", auth.TokenTypeAccessToken)
		r := h.postForm("/v1/auth/token-exchange", editorTok, form)
		if r.code != http.StatusBadRequest || r.body["error"] != "unsupported_grant_type" {
			t.Errorf("wrong grant = %d %v", r.code, r.body["error"])
		}
	})

	t.Run("invalid_target on fragment resource", func(t *testing.T) {
		form := url.Values{}
		form.Set("grant_type", auth.GrantTypeTokenExchange)
		form.Set("subject_token", editorTok)
		form.Set("subject_token_type", auth.TokenTypeAccessToken)
		form.Add("resource", "https://mcp.example.com/x#frag")
		r := h.postForm("/v1/auth/token-exchange", editorTok, form)
		if r.code != http.StatusBadRequest || r.body["error"] != "invalid_target" {
			t.Errorf("fragment resource = %d %v, want 400 invalid_target", r.code, r.body["error"])
		}
	})

	t.Run("missing subject_token", func(t *testing.T) {
		form := url.Values{}
		form.Set("grant_type", auth.GrantTypeTokenExchange)
		r := h.postForm("/v1/auth/token-exchange", editorTok, form)
		if r.code != http.StatusBadRequest || r.body["error"] != "invalid_request" {
			t.Errorf("missing subject = %d %v", r.code, r.body["error"])
		}
	})
}
