// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcpaudit_test

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

	gcpaudit "github.com/olivaresai/olivares/connectors/gcp-audit"
	"github.com/olivaresai/olivares/sdk"
)

// TestServiceAccountTokenFlow proves the stdlib SA-JWT flow end to end: the
// connector signs a jwt-bearer assertion from a service-account key, exchanges it
// for an access token, presents that token as a Bearer credential on the API
// calls, and caches it across passes (the token endpoint is hit once, not twice).
func TestServiceAccountTokenFlow(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	saJSON, err := json.Marshal(map[string]string{
		"client_email":   "collector@proj.iam.gserviceaccount.com",
		"private_key":    string(pemBytes),
		"private_key_id": "kid-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	var tokenHits int32
	var sawAssertion atomic.Bool
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		_ = r.ParseForm()
		if r.FormValue("grant_type") == "urn:ietf:params:oauth:grant-type:jwt-bearer" && r.FormValue("assertion") != "" {
			sawAssertion.Store(true)
		}
		_, _ = w.Write([]byte(`{"access_token":"minted-xyz","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)

	var sawBearer atomic.Bool
	resSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer minted-xyz" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawBearer.Store(true)
		switch {
		case strings.HasSuffix(r.URL.Path, "/serviceAccounts"):
			_, _ = w.Write([]byte(`{"accounts":[]}`))
		case strings.Contains(r.URL.Path, "/folders"):
			_, _ = w.Write([]byte(`{"folders":[]}`))
		default:
			_, _ = w.Write([]byte(`{"projects":[]}`))
		}
	}))
	t.Cleanup(resSrv.Close)

	s := gcpaudit.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"credentials_json": string(saJSON),
		"token_uri":        tokenSrv.URL,
		"organization_id":  testOrg,
		"crm_endpoint":     resSrv.URL,
		"iam_endpoint":     resSrv.URL,
		"logging_endpoint": resSrv.URL,
		"enable_audit":     "false",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Two passes: the second must reuse the cached token.
	gather(t, s)
	sink := gather(t, s)

	if !sawAssertion.Load() {
		t.Error("token endpoint never received a jwt-bearer assertion")
	}
	if !sawBearer.Load() {
		t.Error("API calls never presented the minted Bearer token")
	}
	if n := atomic.LoadInt32(&tokenHits); n != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (token must be cached across passes)", n)
	}
	if len(sink.findingSnapshot()) != 0 {
		t.Errorf("token flow produced health findings (a 401 leaked through?): %+v", sink.findingSnapshot())
	}
}
