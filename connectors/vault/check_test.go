// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// ACN-11: Open records the configuration and contacts nothing, so that the engine
// starts while Vault is away; Check is the call that contacts Vault, and only a probe
// makes it.
func TestCheckContactsVaultOnlyWhenAsked(t *testing.T) {
	var calls atomic.Int32
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/auth/token/lookup-self" || r.Header.Get("X-Vault-Token") != "fixture-token" {
			t.Errorf("unexpected call %s with token %q", r.URL.Path, r.Header.Get("X-Vault-Token"))
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()
	ctx := context.Background()
	open := func(baseURL, token string) *Source {
		s := New()
		if err := s.Open(ctx, sdk.Config{Settings: map[string]string{"base_url": baseURL, "token": token}}); err != nil {
			t.Fatalf("Open: %v", err)
		}
		return s
	}
	s := open(srv.URL, "fixture-token")
	if calls.Load() != 0 {
		t.Fatal("Open contacted Vault")
	}
	if err := s.Check(ctx); err != nil || calls.Load() != 1 {
		t.Fatalf("Check against an answering Vault = %v (%d calls)", err, calls.Load())
	}
	status = http.StatusForbidden
	if err := s.Check(ctx); err == nil {
		t.Fatal("a refused token passed the check")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + l.Addr().String()
	_ = l.Close()
	if err := open(closed, "fixture-token").Check(ctx); err == nil {
		t.Fatal("an address nothing listens on passed the check")
	}
	if err := open(srv.URL, "").Check(ctx); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("no token = %v, want the offline sentence", err)
	}
}
