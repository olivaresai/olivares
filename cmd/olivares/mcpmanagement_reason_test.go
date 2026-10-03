// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// TestManagedMCPProbeReason: a failed connection test names its cause, measured through the
// managed client against real endpoints, so the console can say what to fix.
func TestManagedMCPProbeReason(t *testing.T) {
	post := func(endpoint string, cidrs ...string) (error, int) {
		client, err := newManagedMCPClient(auth.MCPGatewayServerInput{URL: endpoint, EgressCIDRs: cidrs}, 5*time.Second)
		if err != nil {
			t.Fatalf("client for %s: %v", endpoint, err)
		}
		status := &mcpProbeStatus{inner: client.Transport}
		client.Transport = status
		req, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{}`))
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err, status.code
	}

	// A self-signed certificate is a TLS failure, not "unreachable".
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsSrv.Close()
	if reason, _ := mcpProbeReason(post(tlsSrv.URL+"/mcp", "127.0.0.1/32")); reason != "tls" {
		t.Errorf("self-signed: reason %q, want tls", reason)
	}

	// Nothing listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()
	if reason, _ := mcpProbeReason(post("https://"+closed+"/mcp", "127.0.0.1/32")); reason != "connection_refused" {
		t.Errorf("closed port: reason %q, want connection_refused", reason)
	}

	// A name that never resolves is DNS, and no longer an egress refusal.
	err, code := post("https://olivares-probe.invalid/mcp")
	if reason, _ := mcpProbeReason(err, code); reason != "dns" {
		t.Errorf("unresolvable name: reason %q (%v), want dns", reason, err)
	}

	for _, c := range []struct {
		err    error
		status int
		reason string
	}{
		{fmt.Errorf("mcp: %w", context.DeadlineExceeded), 0, "timeout"},
		{fmt.Errorf("mcp: status"), 404, "http_status"},
		{fmt.Errorf("mcp: status"), 502, "http_status"},
		{fmt.Errorf("something else"), 0, ""},
	} {
		if reason, status := mcpProbeReason(c.err, c.status); reason != c.reason || (reason == "http_status" && status != c.status) {
			t.Errorf("%v/%d: reason %q status %d, want %q", c.err, c.status, reason, status, c.reason)
		}
	}
}
