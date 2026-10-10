// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestCLILanesCleartextOptIn(t *testing.T) {
	prepareDatalaneCLITest(t)
	for _, lane := range []struct {
		name, method, path string
		args               []string
	}{
		{"observe", http.MethodGet, "/v1/m/health/status", []string{"health", "status"}},
		{"agent-exec", http.MethodGet, "/v1/m/orchestration/schedules", []string{"orchestration", "schedules", "ls"}},
		{"agent-exec-stream", http.MethodGet, "/v1/m/voice/sessions/session-1/stream", []string{"voice", "sessions", "stream", "session-1"}},
		{"datalane", http.MethodGet, "/v1/m/knowledge/kbs", []string{"knowledge", "kbs", "ls"}},
		{"datalane-raw", http.MethodPost, "/v1/m/knowledge/memory/import", []string{"knowledge", "memory", "import", "--bundle-file", "-"}},
	} {
		t.Run(lane.name, func(t *testing.T) {
			for _, opt := range []struct {
				name, env string
				args      []string
				allow     bool
			}{
				{name: "default"},
				{name: "false-flag", args: []string{"--allow-cleartext=false"}},
				{name: "insecure-is-not-opt-in", args: []string{"--insecure"}},
				{name: "flag", args: []string{"--allow-cleartext"}, allow: true},
				{name: "environment", env: "1", allow: true},
			} {
				t.Run(opt.name, func(t *testing.T) {
					t.Setenv(cliCleartextOptInEnv, opt.env)
					spy := newObserveSpy(t, http.StatusOK, "{}")
					// Keep the URL non-loopback for the real policy check, but
					// route every dial to our local server without DNS or proxies.
					original := http.DefaultTransport
					transport := original.(*http.Transport).Clone()
					transport.Proxy = nil
					transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, network, spy.srv.Listener.Addr().String())
					}
					http.DefaultTransport = transport
					t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
					args := append([]string{}, lane.args...)
					args = append(args, "--server", "http://plane.invalid", "--token", "test-token", "--tenant", "tenant-a", "-o", "json")
					args = append(args, opt.args...)
					const bundle = "{\"kind\":\"test-bundle\"}\n"
					_, _, err := execDatalane(t, bundle, args...)
					if !opt.allow {
						if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "plain HTTP") {
							t.Fatalf("want cleartext usage refusal, got %v", err)
						}
						if spy.count() != 0 {
							t.Fatal("request sent without cleartext opt-in")
						}
						return
					}
					if err != nil {
						t.Fatalf("explicit cleartext opt-in refused: %v", err)
					}
					if spy.count() != 1 {
						t.Fatalf("requests = %d, want 1", spy.count())
					}
					req := spy.last(t)
					if req.method != lane.method || req.path != lane.path || req.auth != "Bearer test-token" || req.tenant != "tenant-a" {
						t.Fatalf("unexpected request: %+v", req)
					}
					if lane.name == "datalane-raw" && (req.body != bundle || req.ctype != "application/x-ndjson") {
						t.Fatalf("raw bundle changed: body=%q content-type=%q", req.body, req.ctype)
					}
				})
			}
		})
	}
}
