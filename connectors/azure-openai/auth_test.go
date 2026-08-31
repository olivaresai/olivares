// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestCCTokenMintAndCache verifies the client-credentials mint exchanges the secret for an
// access token, sends the ARM scope, and caches it (a second token() does not re-mint).
func TestCCTokenMintAndCache(t *testing.T) {
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q, want client_credentials", r.FormValue("grant_type"))
		}
		if r.FormValue("scope") != managementScope {
			t.Errorf("scope = %q, want %q", r.FormValue("scope"), managementScope)
		}
		_, _ = w.Write([]byte(`{"token_type":"Bearer","expires_in":3600,"access_token":"minted-token"}`))
	}))
	defer srv.Close()

	ts := newCCTokenSource("tenant-1", "client-1", "secret-1", srv.URL, srv.Client())
	ts.now = func() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }

	for i := 0; i < 2; i++ {
		tok, err := ts.token(context.Background())
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if tok != "minted-token" {
			t.Fatalf("token = %q, want minted-token", tok)
		}
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Errorf("mints = %d, want 1 (cached after first)", got)
	}
}

// TestCCTokenStatusNoLeak verifies a non-2xx token response surfaces only the status, never
// the secret.
func TestCCTokenStatusNoLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied", http.StatusUnauthorized)
	}))
	defer srv.Close()

	ts := newCCTokenSource("tenant-1", "client-1", "super-secret", srv.URL, srv.Client())
	ts.now = func() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }
	_, err := ts.token(context.Background())
	if err == nil {
		t.Fatal("want error on 401")
	}
	if got := err.Error(); got == "" || strings.Contains(got, "super-secret") {
		t.Errorf("error leaked the secret: %v", err)
	}
}
