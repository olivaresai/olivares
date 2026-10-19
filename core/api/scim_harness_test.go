// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// The SCIM request helpers the edition tests share: the Community refusal of the
// SCIM server and of the SSF receiver, both reached with a tenant-bound SCIM token.

type scimResp struct {
	code int
	body map[string]any
	hdr  http.Header
	raw  string
}

func (h *harness) scim(method, path, token, jsonBody string) scimResp {
	h.t.Helper()
	var rdr io.Reader = http.NoBody
	if jsonBody != "" {
		rdr = strings.NewReader(jsonBody)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", scim.ContentType)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	out := scimResp{code: rec.Code, hdr: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// scimToken provisions a tenant-bound admin token to authenticate SCIM requests.
func (h *harness) scimToken(super auth.Principal, tenant model.TenantID) string {
	h.t.Helper()
	tok, _, err := h.authr.IssueToken(context.Background(), super, auth.TokenSpec{
		Name: "scim", BoundTenant: tenant, Role: auth.RoleAdmin,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return tok
}
