// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	azureactivity "github.com/olivaresai/olivares/connectors/azure-activity"
	"github.com/olivaresai/olivares/sdk"
)

// TestClientCredentialsFlow proves the client-credentials flow end to end: the
// connector POSTs {grant_type=client_credentials, client_id, client_secret,
// scope} to the token endpoint, presents the returned token as a Bearer
// credential on the API calls, and caches it across passes (one token hit, not
// two). The client secret must never appear on a resource request.
func TestClientCredentialsFlow(t *testing.T) {
	var tokenHits int32
	var sawCC atomic.Bool
	var leakedSecret atomic.Bool
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenHits, 1)
		_ = r.ParseForm()
		if r.FormValue("grant_type") == "client_credentials" && r.FormValue("client_secret") == "top-secret" {
			sawCC.Store(true)
		}
		_, _ = w.Write([]byte(`{"access_token":"minted-azure","expires_in":3600}`))
	}))
	t.Cleanup(tokenSrv.Close)

	var sawBearer atomic.Bool
	resSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer minted-azure" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawBearer.Store(true)
		if strings.Contains(r.URL.RawQuery, "top-secret") {
			leakedSecret.Store(true)
		}
		switch {
		case strings.Contains(r.URL.Path, "Microsoft.ResourceGraph"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			_, _ = w.Write([]byte(`{"value":[]}`))
		}
	}))
	t.Cleanup(resSrv.Close)

	s := azureactivity.New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"tenant_id":           testTenant,
		"client_id":           "client-abc",
		"client_secret":       "top-secret",
		"oauth_token_url":     tokenSrv.URL,
		"subscriptions":       "sub-1",
		"management_endpoint": resSrv.URL,
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	gather(t, s)
	sink := gather(t, s)

	if !sawCC.Load() {
		t.Error("token endpoint never received a client_credentials grant")
	}
	if !sawBearer.Load() {
		t.Error("API calls never presented the minted Bearer token")
	}
	if leakedSecret.Load() {
		t.Error("the client secret leaked onto a resource request")
	}
	if n := atomic.LoadInt32(&tokenHits); n != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (token must be cached across passes)", n)
	}
	if len(sink.findingSnapshot()) != 0 {
		t.Errorf("token flow produced health findings: %+v", sink.findingSnapshot())
	}
}
