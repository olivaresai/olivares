// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func TestManagedMCPEnableDoesNotRequireNativeToolsSwitch(t *testing.T) {
	snapshot := auth.MCPGatewaySnapshot{}
	if sessionMCPAvailable(snapshot) {
		t.Fatal("empty registry enabled session tools")
	}
	snapshot.Servers = []auth.MCPGatewayServer{{MCPGatewayServerInput: auth.MCPGatewayServerInput{Enabled: true}}}
	if !sessionMCPAvailable(snapshot) {
		t.Fatal("enabled server requires another global toggle")
	}
	snapshot.Servers[0].Enabled = false
	if sessionMCPAvailable(snapshot) {
		t.Fatal("disabled server remained available")
	}
}

func TestManagedMCPLaunchReusesPEPEdgeAndEnvironment(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	if _, err := m.store.SetSessionTools(t.Context(), p, f.tenant, 0, true); err != nil {
		t.Fatal(err)
	}
	m.eng.dataDir = t.TempDir()
	m.UseSessionCredentials(auth.NewSessionCredentials(nil, nil))
	spec := sessions.LaunchSpec{Env: []sessions.EnvVar{{Name: envHookPEPURL, Value: "http://127.0.0.1:19710/"}, {Name: envHookPEPToken, Value: "fixture-session-secret"}}, Confinement: &confine.Policy{ReadWrite: []string{t.TempDir()}}}
	cleanup, err := m.ConfigureSessionMCP(t.Context(), f.tenant, "fixture-run", "claude", &spec)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	raw, err := os.ReadFile(spec.Confinement.ReadOnly[0])
	if err != nil || !strings.Contains(string(raw), "http://127.0.0.1:19710/session/mcp") || strings.Contains(string(raw), "fixture-session-secret") {
		t.Fatalf("generated connection settings: %v", err)
	}
	// A configured integration must not silently launch without authentication.
	spec.Env = nil
	if _, err := m.ConfigureSessionMCP(t.Context(), f.tenant, "other-run", "claude", &spec); err == nil {
		t.Fatal("missing shared credential did not refuse configuration")
	}
}

func TestManagedMCPLocalPolicyKeepsSessionBoundary(t *testing.T) {
	folder, home, protected, foreign := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	m := &mcpManagement{eng: &engine{sessionsMod: sessions.New(sessions.WithConfinement([]string{protected}, false))}}
	spec := sessions.LaunchSpec{Program: os.Args[0], Dir: folder, Env: []sessions.EnvVar{{Name: "HOME", Value: foreign}}, Confinement: &confine.Policy{ReadWrite: []string{folder, home}}}
	if err := m.confineLocalServer(&spec); err != nil {
		t.Fatal(err)
	}
	for _, item := range spec.Env {
		if item.Name == "HOME" || item.Name == "TMPDIR" {
			t.Fatal("MCP launch reused an explicit home or temp path")
		}
	}
	if spec.Confinement == nil || len(spec.Confinement.Protect) != 1 || spec.Confinement.Protect[0] != protected {
		t.Fatal("stdio child lost the node's protected paths")
	}
	for _, path := range spec.Confinement.ReadWrite {
		if path != folder && path != home {
			t.Fatal("server configuration widened the session's writable paths")
		}
	}
	if len(spec.Confinement.ReadOnly) != 1 {
		t.Fatal("stdio executable install was not readable")
	}
}

func TestManagedMCPBootSharesCredentialAndLocalEdge(t *testing.T) {
	t.Setenv("OLIVARES_HOOK_PEP_CONFIG", "")
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Logger: discardLog(), Version: "test", NoIngest: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.sessionMCP == nil || eng.sessionMCP.sessionAuthenticator != eng.sessionHooks.SessionCredentials {
		t.Fatal("MCP is not bound to the one shared issuer")
	}
	server, err := buildClaudeHookPEPServer(eng, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)))
	if w.Code != 401 {
		t.Fatalf("MCP local edge did not authenticate: %d", w.Code)
	}
}
