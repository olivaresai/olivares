// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoginAbandonedDuringTheThrottleDelayIsCounted closes the hole the login
// delay opened in the abuse SLI. A login that a tripped client address holds is
// a real attempt with a real outcome; when the caller goes away before the
// credential is read, the attempt must land in the login counter under its own
// name rather than disappearing from it. An abuse query that silently stops
// counting the very attempts a throttle is holding reads as calm.
func TestLoginAbandonedDuringTheThrottleDelayIsCounted(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	if r := h.do("POST", "/v1/users", admin, map[string]any{"email": "waiting@x.io", "password": "waitingpass123"}, nil); r.code != http.StatusCreated {
		t.Fatalf("create the account = %d %s", r.code, r.raw)
	}

	// Trip the address every harness request arrives from.
	for i := 0; i < 5; i++ {
		if r := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "sprayed@x.io", "password": "wrong"}, nil); r.code != http.StatusUnauthorized {
			t.Fatalf("spray attempt %d = %d %s, want 401", i, r.code, r.raw)
		}
	}

	// The caller goes away while the tripped address is holding the attempt.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := bytes.NewReader([]byte(`{"email":"waiting@x.io","password":"waitingpass123"}`))
	req := httptest.NewRequest("POST", "/v1/auth/login", body).WithContext(ctx)
	req.RemoteAddr = "10.0.0.1:1234"
	h.srv.Handler().ServeHTTP(httptest.NewRecorder(), req)

	m := h.do("GET", "/metrics", "", nil, nil)
	const want = `olivares_auth_login_attempts_total{outcome="abandoned"} 1`
	if !strings.Contains(m.raw, want) {
		t.Fatalf("an abandoned login was counted in no outcome; /metrics has no %q\n--- body ---\n%s", want, m.raw)
	}
}
