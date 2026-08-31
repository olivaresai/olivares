// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestSATokenMintAndCache verifies the JWT-bearer mint exchanges a signed assertion for an
// access token and caches it (a second token() does not re-mint).
func TestSATokenMintAndCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))

	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		_ = r.ParseForm()
		if r.FormValue("grant_type") != jwtBearerGrant {
			t.Errorf("grant_type = %q, want %q", r.FormValue("grant_type"), jwtBearerGrant)
		}
		if r.FormValue("assertion") == "" {
			t.Error("missing assertion")
		}
		_, _ = w.Write([]byte(`{"access_token":"minted-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	saJSON, _ := json.Marshal(map[string]string{
		"client_email":   "svc@test.iam.gserviceaccount.com",
		"private_key":    pemKey,
		"private_key_id": "kid-1",
		"token_uri":      srv.URL,
	})
	ts, err := newSATokenSource(saJSON, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("newSATokenSource: %v", err)
	}
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

// TestSATokenBadCredsNoLeak verifies a malformed key is rejected with a static message that
// does not echo the credential bytes.
func TestSATokenBadCredsNoLeak(t *testing.T) {
	_, err := newSATokenSource([]byte(`{"client_email":"x","private_key":"-----BEGIN PRIVATE KEY-----\nnotbase64\n-----END PRIVATE KEY-----"}`), http.DefaultClient, "")
	if err == nil {
		t.Fatal("want error for malformed key")
	}
	if strings.Contains(err.Error(), "notbase64") {
		t.Errorf("error leaked key bytes: %v", err)
	}
}
