// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginTrustedProxiesBootSeparatesHTTPClients(t *testing.T) {
	if isolateBootCaller(t) {
		return
	}
	t.Setenv("OLIVARES_LOGIN_TRUSTED_PROXIES", "10.0.0.0/8")
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NoIngest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	request := func(path string, body map[string]any, client string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		eng.api.Handler().ServeHTTP(rec, req)
		return rec
	}
	token, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if r := request("/v1/setup", map[string]any{
		"token": token, "email": "root@x.io", "password": "supersecret1",
	}, "198.51.100.2"); r.Code != http.StatusCreated {
		t.Fatalf("setup status = %d", r.Code)
	}
	login := func(email, password, client string) int {
		return request("/v1/auth/login", map[string]any{
			"email": email, "password": password,
		}, client).Code
	}
	if got := login("root@x.io", "wrong", "198.51.100.2"); got != http.StatusUnauthorized {
		t.Fatalf("first client's typo = %d, want 401", got)
	}
	for i := 0; i < 5; i++ {
		if got := login("sprayed@x.io", "wrong", "198.51.100.1"); got != http.StatusUnauthorized {
			t.Fatalf("second client's spray attempt %d = %d, want 401", i, got)
		}
	}
	if got := login("root@x.io", "supersecret1", "198.51.100.2"); got != http.StatusOK {
		t.Fatalf("a different client behind the configured proxy shares the spray bucket: status = %d, want 200", got)
	}
}
