// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// 26.1001 documented this endpoint for SDK/curl callers and persisted session
// base URLs, so a config that omitted listen must keep the same address.
func TestInferenceProxyUpgrade26101(t *testing.T) {
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	for _, tc := range []struct {
		name, config, want string
		codex              bool
	}{
		{"omitted listen", `{"surface":"direct"}`, "127.0.0.1:8448", false},
		{"explicit old listen", `{"surface":"direct","listen":"127.0.0.1:8448"}`, "127.0.0.1:8448", false},
		{"custom listen", `{"surface":"direct","listen":"127.0.0.1:18448"}`, "127.0.0.1:18448", false},
		{"co-enabled with explicit Codex address", `{"surface":"direct"}`, "127.0.0.1:8448", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeInferenceProxyRawConfig(t, tc.config)
			proxy, err := buildClaudeMessagesProxyServer(eng, discardLog(), "")
			if err != nil || proxy == nil {
				t.Fatalf("build proxy: server=%v err=%v", proxy, err)
			}
			if proxy.Addr != tc.want {
				t.Fatalf("26.1001 endpoint moved: got %q, want %q", proxy.Addr, tc.want)
			}
			var codex *http.Server
			if tc.codex {
				writeCodexPEPConfig(t, `{"tenant":"`+model.NewTenantID().String()+`","listen":"127.0.0.1:8450"}`)
				codex, err = buildCodexHookPEPServer(eng, sessions.New(), discardLog())
				if err != nil || codex == nil || codex.Addr != "127.0.0.1:8450" {
					t.Fatalf("build Codex PEP with explicit address: server=%v err=%v", codex, err)
				}
			}
			specs := serveListenerRegistry(serveOptions{grpcListen: "127.0.0.1:0", insecure: true}, nil, nil, nil, nil, codex, nil, nil, proxy)
			if _, err := resolveServeListenerAddresses(context.Background(), specs); err != nil {
				t.Fatalf("upgraded listener configuration collides: %v", err)
			}
			// Reach the real proxy handler at the unchanged client URL. No upstream
			// key or request is needed: unauthenticated traffic must still be denied.
			ln, err := bindServeListener(context.Background(), proxy.Addr, false)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(proxy.Handler)
			_ = server.Listener.Close()
			server.Listener = ln
			server.Start()
			t.Cleanup(server.Close)
			client := server.Client()
			client.Timeout = 5 * time.Second
			resp, err := client.Post("http://"+tc.want+"/v1/messages", "application/json", strings.NewReader(`{"model":"test","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("proxy status = %d, want unauthenticated refusal", resp.StatusCode)
			}
		})
	}
}
