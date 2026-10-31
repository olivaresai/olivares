// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
)

func TestSessionPairingRouteAndBrowserEnvelope(t *testing.T) {
	h := newHarness(t)
	owner := h.adminLogin()
	if response := h.do("POST", "/v1/auth/sessions/pairing", "", map[string]any{"name": "Phone"}, nil); response.code != http.StatusUnauthorized {
		t.Fatalf("anonymous creates pairing: %d %s", response.code, response.raw)
	}
	if response := h.do("POST", "/v1/auth/sessions/pairing", owner, map[string]any{"name": "\n"}, nil); response.code != http.StatusBadRequest {
		t.Fatalf("bad label = %d %s", response.code, response.raw)
	}
	created := h.do("POST", "/v1/auth/sessions/pairing", owner, map[string]any{"name": "Phone"}, nil)
	if created.code != http.StatusOK {
		t.Fatalf("create link=%d %s", created.code, created.raw)
	}
	link := created.body["token"].(string)
	if created.hdr.Get("Cache-Control") != "no-store" {
		t.Fatal("pairing response may be cached")
	}
	if response := h.do("POST", "/v1/auth/sessions/pairing/accept", "", map[string]any{"token": link}, map[string]string{"Origin": "https://evil.test", "X-Olivares-Session": "cookie"}); response.code != http.StatusForbidden {
		t.Fatalf("cross-origin accept=%d %s", response.code, response.raw)
	}
	accepted := h.do("POST", "/v1/auth/sessions/pairing/accept", "", map[string]any{"token": link}, map[string]string{"X-Olivares-Session": "cookie", "Cookie": "__Host-olivares-session=expired"})
	if accepted.code != http.StatusOK {
		t.Fatalf("accept link=%d %s", accepted.code, accepted.raw)
	}
	if _, ok := accepted.body["token"]; ok {
		t.Fatal("browser pairing leaked bearer")
	}
	cookies := (&http.Response{Header: accepted.hdr}).Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("paired browser cookie=%v", cookies)
	}
	if accepted.body["csrf_token"] == nil {
		t.Fatal("paired browser lacks CSRF")
	}
	who := h.do("GET", "/v1/auth/whoami", "", nil, map[string]string{"Cookie": cookies[0].String()})
	if who.code != http.StatusOK {
		t.Fatalf("paired browser failed=%d %s", who.code, who.raw)
	}
	if again := h.do("POST", "/v1/auth/sessions/pairing/accept", "", map[string]any{"token": link}, nil); again.code != http.StatusUnauthorized {
		t.Fatalf("link reused=%d %s", again.code, again.raw)
	}
	list := h.do("GET", "/v1/auth/sessions", owner, nil, nil)
	if list.code != http.StatusOK {
		t.Fatal(list.raw)
	}
	found := false
	for _, item := range list.body["items"].([]any) {
		row := item.(map[string]any)
		if row["id"] == accepted.body["session_id"] {
			found = row["name"] == "Phone"
		}
	}
	if !found {
		t.Fatalf("named device absent: %s", list.raw)
	}
	if ended := h.do("DELETE", "/v1/auth/sessions/"+accepted.body["session_id"].(string), owner, nil, nil); ended.code != http.StatusNoContent {
		t.Fatalf("revoke device=%d %s", ended.code, ended.raw)
	}
	if who := h.do("GET", "/v1/auth/whoami", "", nil, map[string]string{"Cookie": cookies[0].String()}); who.code != http.StatusUnauthorized {
		t.Fatalf("revoked device retained access: %d", who.code)
	}
}
