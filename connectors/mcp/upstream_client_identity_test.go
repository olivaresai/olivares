// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"net/http/httptest"
	"testing"
)

func TestRSUpstreamCarriesValidatedClientIdentity(t *testing.T) {
	rs := &ResourceServer{}
	req := httptest.NewRequest("POST", "https://plane.test/mcp", nil)
	req.Header.Set("Authorization", "Bearer fixture-not-forwarded")
	up := rs.upstreamReq(req, "tools/list", nil, validatedToken{Subject: "agent:fixture", ClientID: "client:fixture", Scopes: map[string]struct{}{"tools:read": {}}})
	if up.Subject != "agent:fixture" || up.ClientID != "client:fixture" || len(up.Scopes) != 1 || up.Scopes[0] != "tools:read" {
		t.Fatal("validated attribution lost")
	}
}
