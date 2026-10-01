// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRSCredentialRefusalAuditsDiscoveryAndNotifications(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, http.StatusBadGateway},
		{`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, http.StatusAccepted},
	} {
		token, jwks := mintAccessToken(t, "k1", rsResource, "", validExp())
		upstream := &fakeUpstream{err: ErrUpstreamCredentialDisclosure}
		rs := newRS(t, jwks, fakeToolGate{StatusApproved}, upstream)
		auditor := &capturingAuditor{}
		rs.auditor = auditor
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, rsPost(token, tc.body))
		if w.Code != tc.status || !upstream.called {
			t.Fatalf("credential refusal status=%d upstream=%v", w.Code, upstream.called)
		}
		if len(auditor.decisions) != 1 || auditor.decisions[0].Allowed || auditor.decisions[0].RequiredScope != "" || auditor.decisions[0].Reason != "upstream credential disclosure refused" {
			t.Fatal("scope-free credential refusal must produce one value-free denial audit")
		}
	}
}
