// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type openCodeMCPLaunchFixture struct{ dir, endpoint string }

func (f openCodeMCPLaunchFixture) ConfigureSessionMCP(_ context.Context, _ model.TenantID, runRef, driver string, spec *LaunchSpec) (func(), error) {
	spec.Env = append(spec.Env, EnvVar{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "fixture-session-mcp-token"})
	return ConfigureSessionMCP(spec, driver, f.dir, runRef, f.endpoint, "OLIVARES_HOOK_PEP_TOKEN")
}

func TestOpenCodeRuntimeSessionMCPUsesACPOnNewResumeAndLoad(t *testing.T) {
	for _, load := range []bool{false, true} {
		method := acpMethodSessionResume
		if load {
			method = acpMethodSessionLoad
		}
		t.Run(method, func(t *testing.T) {
			m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
			const endpoint = "http://127.0.0.1:19710/session/mcp"
			m.SessionMCP = openCodeMCPLaunchFixture{t.TempDir(), endpoint}
			record := setOpenCodeFixture(t, prof, openCodeFixture{SessionID: "ses-mcp", RequireMCPURL: endpoint})
			first, err := openCodeLaunch(t, m, tenant, prof)
			if err != nil {
				t.Fatalf("new session lost its MCP connection: %v", err)
			}
			peer := readOpenCodeFixtureRecord(t, record)
			if len(peer.MCPMethods) != 1 || peer.MCPMethods[0] != acpMethodSessionNew {
				t.Fatal("new session did not receive its authenticated MCP endpoint over ACP")
			}
			if _, err := m.stopRun(t.Context(), tenant, first.RunRef, "user:u1", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			record = setOpenCodeFixture(t, prof, openCodeFixture{RequireMCPURL: endpoint, NoResumeCapability: load})
			resumed, err := m.resumeRun(t.Context(), tenant, first.RunRef, "user:u1", model.ActorUser, "")
			if err != nil {
				t.Fatalf("resumed session lost its MCP connection: %v", err)
			}
			t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, resumed.RunRef, "user:u1", model.ActorUser) })
			peer = readOpenCodeFixtureRecord(t, record)
			if len(peer.MCPMethods) != 1 || peer.MCPMethods[0] != method || resumed.ProviderConversationID != "ses-mcp" {
				t.Fatal("resume did not keep its conversation and authenticated MCP endpoint")
			}
		})
	}
}

func TestOpenCodeRuntimeSessionMCPRequiresHTTPTransportCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
			if enabled {
				m.SessionMCP = openCodeMCPLaunchFixture{t.TempDir(), "http://127.0.0.1:19710/session/mcp"}
			}
			record := setOpenCodeFixture(t, prof, openCodeFixture{NoMCPHTTP: true})
			dto, err := openCodeLaunch(t, m, tenant, prof)
			if !enabled {
				if err != nil {
					t.Fatal(err)
				}
				_, _ = m.stopRun(t.Context(), tenant, dto.RunRef, "user:u1", model.ActorUser)
				return
			}
			if err == nil || !strings.Contains(err.Error(), "MCP") {
				t.Fatalf("missing HTTP capability did not refuse configured MCP: %v", err)
			}
			peer := readOpenCodeFixtureRecord(t, record)
			if len(peer.Methods) != 1 || peer.Methods[0] != acpMethodInitialize {
				t.Fatal("session was opened without its required MCP transport")
			}
		})
	}
}
