// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func proxyLogin(t *testing.T, h *harness, peer, email, password string, forwarded []string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewReader(body))
	req.RemoteAddr = net.JoinHostPort(peer, "1234")
	for _, value := range forwarded {
		req.Header.Add("X-Forwarded-For", value)
	}
	req.Header.Set("X-Real-IP", "192.0.2.200")
	req.Header.Set("Forwarded", "for=192.0.2.200")
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func newProxyLoginHarness(t *testing.T, cidrs string) *harness {
	t.Helper()
	trust, err := auth.ParseTrustedLoginProxies(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	return newHarnessOpts(t, func(opts *api.Options) {
		opts.Authenticator.SetTrustedLoginProxies(trust)
	})
}

func TestLoginTrustedProxiesHTTPAddressSelection(t *testing.T) {
	const client = "198.51.100.8"
	const peer = "10.0.0.1"
	tests := []struct {
		name      string
		trust     string
		peer      string
		forwarded []string
		bucket    string
	}{
		{"unconfigured", "", peer, []string{client}, peer},
		{"untrusted_peer", "10.0.0.0/8", "192.0.2.1", []string{client}, "192.0.2.1"},
		{"trusted_client", "10.0.0.0/8", peer, []string{client}, client},
		{"leftmost_spoof", "10.0.0.0/8", peer, []string{"192.0.2.99, " + client}, client},
		{"untrusted_left_ignored", "10.0.0.0/8", peer, []string{"not-an-ip, " + client}, client},
		{"trusted_chain", "10.0.0.0/8", peer, []string{client + ", 10.1.1.1,10.2.2.2"}, client},
		{"repeated_lines", "10.0.0.0/8", peer, []string{"192.0.2.99", client + ",10.1.1.1"}, client},
		{"ipv4_port", "10.0.0.0/8", peer, []string{client + ":4321"}, client},
		{"ipv6_literal", "10.0.0.0/8", peer, []string{"2001:0db8:1::0008"}, "2001:db8:1::8"},
		{"ipv6_brackets", "10.0.0.0/8", peer, []string{"[2001:db8:1::8]"}, "2001:db8:1::8"},
		{"ipv6_port", "10.0.0.0/8", peer, []string{"[2001:db8:1::8]:4321"}, "2001:db8:1::8"},
		{"ipv6_proxy", "2001:db8:2::/64", "2001:db8:2::1", []string{client}, client},
		{"mapped_ipv4", "10.0.0.0/8", peer, []string{"::ffff:" + client}, client},
		{"missing_header", "10.0.0.0/8", peer, nil, peer},
		{"malformed_suffix", "10.0.0.0/8", peer, []string{client + ",not-an-ip"}, peer},
		{"empty_suffix", "10.0.0.0/8", peer, []string{client + ","}, peer},
		{"empty_middle", "10.0.0.0/8", peer, []string{client + ",,10.1.1.1"}, peer},
		{"empty_repeated_line", "10.0.0.0/8", peer, []string{client, ""}, peer},
		{"all_trusted", "10.0.0.0/8", peer, []string{"10.1.1.1,10.2.2.2"}, peer},
		{"malformed_brackets", "10.0.0.0/8", peer, []string{"[[2001:db8::8]]"}, peer},
		{"scoped_ipv6", "10.0.0.0/8", peer, []string{"[fe80::8%eth0]:1234"}, peer},
		{"invalid_port", "10.0.0.0/8", peer, []string{client + ":65536"}, peer},
		{"unparseable_peer", "10.0.0.0/8", "not-an-ip", []string{client}, "not-an-ip"},
		{"byte_limit", "10.0.0.0/8", peer, []string{strings.Repeat(" ", 8192-len(client)) + client}, client},
		{"bytes_over_limit", "10.0.0.0/8", peer, []string{strings.Repeat(" ", 8193-len(client)) + client}, peer},
		{"repeated_bytes_over_limit", "10.0.0.0/8", peer, []string{strings.Repeat(" ", 8192), client}, peer},
		{"entry_limit", "10.0.0.0/8", peer, []string{client + strings.Repeat(",10.1.1.1", 63)}, client},
		{"entries_over_limit", "10.0.0.0/8", peer, []string{client + strings.Repeat(",10.1.1.1", 64)}, peer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newProxyLoginHarness(t, tt.trust)
			h.adminLogin()
			// The account has one typo; only this address, not the account, is tripped.
			if r := proxyLogin(t, h, tt.bucket, "root@x.io", "wrong", nil); r.Code != http.StatusUnauthorized {
				t.Fatalf("account typo = %d, want 401", r.Code)
			}
			for i := 0; i < 4; i++ {
				if r := proxyLogin(t, h, tt.bucket, "sprayed@x.io", "wrong", nil); r.Code != http.StatusUnauthorized {
					t.Fatalf("trip attempt %d = %d, want 401", i, r.Code)
				}
			}
			if r := proxyLogin(t, h, tt.peer, "root@x.io", "supersecret1", tt.forwarded); r.Code != http.StatusTooManyRequests {
				t.Errorf("selected address did not retain its throttle: status = %d, want 429", r.Code)
			}
			if r := proxyLogin(t, h, "203.0.113.250", "root@x.io", "supersecret1", nil); r.Code != http.StatusOK {
				t.Fatalf("independent address = %d, want 200: the account itself must remain usable", r.Code)
			}
		})
	}
}

type proxyPeerPolicy struct {
	allow string
	seen  string
}

func (p *proxyPeerPolicy) AllowNetwork(_ context.Context, ip string) error {
	p.seen = ip
	if ip != p.allow {
		return auth.ErrNetworkNotAllowed
	}
	return nil
}

func (*proxyPeerPolicy) RequireSSO(context.Context, model.User) error { return nil }

func TestLoginTrustedProxiesHTTPPreservesPeerAuthority(t *testing.T) {
	h := newProxyLoginHarness(t, "10.0.0.0/8")
	h.adminLogin()
	const peer = "10.2.3.4"
	const client = "198.51.100.8"
	policy := &proxyPeerPolicy{allow: peer}
	h.authr.WithLoginPolicy(policy)
	if r := proxyLogin(t, h, peer, "root@x.io", "wrong", []string{client}); r.Code != http.StatusUnauthorized {
		t.Fatalf("failed credential = %d, want 401", r.Code)
	}
	if policy.seen != peer {
		t.Fatalf("network policy saw %q, want transport peer", policy.seen)
	}
	ctx := context.Background()
	failed := 0
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit port lacks canonical reads")
		}
		return walker.WalkCanonical(ctx, 0, func(event model.AuditEvent, text string, _ []byte) error {
			if event.Action != "auth.login.failed" {
				return nil
			}
			failed++
			var meta map[string]any
			if err := json.Unmarshal([]byte(text), &meta); err != nil {
				return err
			}
			if meta["ip"] != peer {
				t.Errorf("failed-login audit IP = %v, want transport peer", meta["ip"])
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if failed != 1 {
		t.Fatalf("failed-login audit events = %d, want 1", failed)
	}
	r := proxyLogin(t, h, peer, "root@x.io", "supersecret1", []string{client})
	if r.Code != http.StatusOK {
		t.Fatalf("login from allowed peer = %d, want 200", r.Code)
	}
	var login map[string]string
	if err := json.Unmarshal(r.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
		session, err := as.Sessions().Get(ctx, model.ID(login["session_id"]))
		if err == nil && session.CreatedIP != peer {
			t.Errorf("session IP = %q, want transport peer", session.CreatedIP)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	policy.allow = client
	if r := proxyLogin(t, h, peer, "root@x.io", "supersecret1", []string{client}); r.Code != http.StatusForbidden {
		t.Fatalf("forwarded address bypassed peer allow-list: status = %d, want 403", r.Code)
	}
}
